package handler

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	// gif 编解码支持
	_ "image/gif"
	// jpeg 编解码支持
	_ "image/jpeg"
	// png 编解码支持
	_ "image/png"

	"github.com/disintegration/imaging"
	"github.com/onaonbir/Cloodsy-S3/config"
	"github.com/onaonbir/Cloodsy-S3/db"
	"github.com/onaonbir/Cloodsy-S3/storage"
)

// AutoResizeTask 自动缩放任务
type AutoResizeTask struct {
	BucketName  string    // bucket 名称
	Key         string    // 对象 key
	ContentType string    // 图片内容类型
	Size        int64     // 原始文件大小
	Quality     int       // 目标 JPEG 质量
	TargetW     int       // 目标宽度（0=不限制）
	TargetH     int       // 目标高度（0=不限制）
	SubmittedAt time.Time // 任务提交时间
}

// ImageProcessor 异步图片缩放处理器
// 负责在上传大图后自动异步缩放替换原图，防止存储空间浪费
type ImageProcessor struct {
	cfg     *config.ImageResizeConfig // 图片缩放配置
	db      *db.DB                    // 数据库访问
	storage storage.Backend           // 存储后端
	logger  *slog.Logger              // 日志记录器

	taskCh chan AutoResizeTask // 任务队列（带缓冲）
	stopCh chan struct{}       // 停止信号通道
}

// NewImageProcessor 创建异步图片缩放处理器
func NewImageProcessor(cfg *config.ImageResizeConfig, database *db.DB, store storage.Backend, logger *slog.Logger) *ImageProcessor {
	// 任务队列缓冲大小为并发数的 2 倍，避免频繁阻塞
	bufferSize := cfg.MaxConcurrent * 2
	if bufferSize < 8 {
		bufferSize = 8 // 最小缓冲 8 个任务
	}
	return &ImageProcessor{
		cfg:     cfg,
		db:      database,
		storage: store,
		logger:  logger,
		taskCh:  make(chan AutoResizeTask, bufferSize),
		stopCh:  make(chan struct{}),
	}
}

// Start 启动异步缩放处理器的工作协程
func (p *ImageProcessor) Start() {
	// 启动 maxConcurrent 个工作协程，并发处理缩放任务
	for i := 0; i < p.cfg.MaxConcurrent; i++ {
		go p.worker(i)
	}
	p.logger.Info("image auto-resize processor started",
		"concurrent", p.cfg.MaxConcurrent,
		"target_w", p.cfg.AutoResizeTargetW,
		"target_h", p.cfg.AutoResizeTargetH,
		"quality", p.cfg.AutoResizeQuality,
		"min_size", p.cfg.AutoResizeMinSize,
	)
}

// Stop 优雅停止处理器
func (p *ImageProcessor) Stop() {
	close(p.stopCh) // 关闭停止信号通道，所有 worker 会退出
	p.logger.Info("image auto-resize processor stopped")
}

// Submit 提交一个自动缩放任务到异步队列
// 如果队列已满则丢弃任务（不阻塞上传请求）
func (p *ImageProcessor) Submit(task AutoResizeTask) {
	select {
	case p.taskCh <- task:
		// 成功提交任务到队列
		p.logger.Info("auto-resize task submitted",
			"bucket", task.BucketName,
			"key", task.Key,
			"size", task.Size,
		)
	default:
		// 队列已满，丢弃任务（上传请求优先，不能阻塞）
		p.logger.Warn("auto-resize task dropped (queue full)",
			"bucket", task.BucketName,
			"key", task.Key,
		)
	}
}

// SubmitAutoResizeTask 实现 webdav.AutoResizeHook 接口
// WebDAV 上传文件后通过此方法触发自动缩放
func (p *ImageProcessor) SubmitAutoResizeTask(bucketName, key, contentType string, size int64) {
	// 检查是否需要自动缩放
	if !ShouldAutoResize(p.cfg, contentType, size) {
		return
	}
	// 构建缩放任务
	task := BuildAutoResizeTask(p.cfg, bucketName, key, contentType, size)
	if task == nil {
		return
	}
	// 非阻塞提交
	p.Submit(*task)
}

// worker 工作协程，从任务队列中取出任务并处理
func (p *ImageProcessor) worker(id int) {
	for {
		select {
		case <-p.stopCh:
			// 收到停止信号，退出工作协程
			return
		case task := <-p.taskCh:
			// 从队列取出任务，执行缩放处理
			p.processTask(id, task)
		}
	}
}

// processTask 处理单个自动缩放任务
func (p *ImageProcessor) processTask(workerID int, task AutoResizeTask) {
	start := time.Now()
	p.logger.Info("auto-resize processing",
		"worker", workerID,
		"bucket", task.BucketName,
		"key", task.Key,
		"size", task.Size,
	)

	// 第一步：从存储读取原始图片
	reader, err := p.storage.GetObject(task.BucketName, task.Key)
	if err != nil {
		p.logger.Error("auto-resize: failed to read object", "error", err, "key", task.Key)
		return
	}
	defer reader.Close()

	// 第二步：解码原始图片
	srcImg, err := imaging.Decode(reader, imaging.AutoOrientation(true))
	if err != nil {
		p.logger.Error("auto-resize: failed to decode image", "error", err, "key", task.Key)
		return
	}

	// 第三步：计算缩放尺寸
	srcBounds := srcImg.Bounds()
	srcW := srcBounds.Dx() // 原始宽度
	srcH := srcBounds.Dy() // 原始高度

	// 计算等比例缩放的目标尺寸（只缩小不放大）
	dstW, dstH, needResize := calcAutoResizeSize(srcW, srcH, task.TargetW, task.TargetH)
	if !needResize {
		// 原图已经在目标范围内，不需要缩放
		p.logger.Info("auto-resize: image already within target size, skipping",
			"key", task.Key, "src_w", srcW, "src_h", srcH,
			"target_w", task.TargetW, "target_h", task.TargetH,
		)
		return
	}

	// 第四步：执行缩放（使用 Fit 等比例适应目标边界框，Lanczos 高质量重采样）
	dstImg := imaging.Fit(srcImg, dstW, dstH, imaging.Lanczos)

	// 第五步：编码缩放后的图片
	var buf bytes.Buffer
	outputContentType := task.ContentType // 输出格式默认与输入相同

	switch task.ContentType {
	case "image/jpeg":
		// JPEG 编码，使用指定的质量参数
		if err := jpeg.Encode(&buf, dstImg, &jpeg.Options{Quality: task.Quality}); err != nil {
			p.logger.Error("auto-resize: jpeg encode failed", "error", err, "key", task.Key)
			return
		}
	case "image/png":
		// PNG 编码（无损格式）
		if err := png.Encode(&buf, dstImg); err != nil {
			p.logger.Error("auto-resize: png encode failed", "error", err, "key", task.Key)
			return
		}
	case "image/gif":
		// GIF 缩放后转为 PNG 输出以保持质量
		if err := png.Encode(&buf, dstImg); err != nil {
			p.logger.Error("auto-resize: gif->png encode failed", "error", err, "key", task.Key)
			return
		}
		outputContentType = "image/png"
	default:
		p.logger.Error("auto-resize: unsupported content type", "type", task.ContentType, "key", task.Key)
		return
	}

	resizedData := buf.Bytes()
	resizedSize := int64(len(resizedData))

	// 第六步：如果缩放后反而更大（PNG 等无损格式可能），则放弃替换
	if resizedSize >= task.Size {
		p.logger.Info("auto-resize: resized image larger than original, skipping",
			"key", task.Key,
			"original_size", task.Size,
			"resized_size", resizedSize,
		)
		return
	}

	// 第七步：计算新文件的 ETag
	hash := md5.Sum(resizedData)
	newETag := hex.EncodeToString(hash[:])

	// 第八步：替换存储中的文件
	reader = io.NopCloser(bytes.NewReader(resizedData))
	newSize, _, err := p.storage.PutObject(task.BucketName, task.Key, reader)
	if err != nil {
		p.logger.Error("auto-resize: failed to write resized object", "error", err, "key", task.Key)
		return
	}

	// 第九步：更新数据库中的元数据
	bucket, err := p.db.GetBucket(task.BucketName)
	if err != nil || bucket == nil {
		p.logger.Error("auto-resize: failed to get bucket", "error", err, "bucket", task.BucketName)
		return
	}

	// 获取现有元数据，保留 metadata 字段
	existingMeta, err := p.db.GetObjectMeta(bucket.ID, task.Key)
	if err != nil {
		p.logger.Error("auto-resize: failed to get object meta", "error", err, "key", task.Key)
		return
	}
	metadataStr := "{}"
	if existingMeta != nil && existingMeta.Metadata != "" {
		metadataStr = existingMeta.Metadata // 保留原有自定义元数据
	}

	newMeta := &db.ObjectMeta{
		BucketID:     bucket.ID,
		Key:          task.Key,
		Size:         newSize,
		ETag:         newETag,
		ContentType:  outputContentType, // 更新为实际输出类型（GIF 可能变为 PNG）
		LastModified: time.Now().UTC(),
		Metadata:     metadataStr,
		IsLatest:     true,
	}
	if err := p.db.PutObjectMeta(newMeta); err != nil {
		p.logger.Error("auto-resize: failed to update object meta", "error", err, "key", task.Key)
		return
	}

	// 缩放完成，记录日志
	elapsed := time.Since(start)
	savedBytes := task.Size - newSize
	savedPercent := float64(savedBytes) / float64(task.Size) * 100
	p.logger.Info("auto-resize completed",
		"bucket", task.BucketName,
		"key", task.Key,
		"src_size", fmt.Sprintf("%dx%d", srcW, srcH),
		"dst_size", fmt.Sprintf("%dx%d", dstW, dstH),
		"original_bytes", task.Size,
		"resized_bytes", newSize,
		"saved", fmt.Sprintf("%d (%.1f%%)", savedBytes, savedPercent),
		"elapsed", elapsed.Round(time.Millisecond),
	)
}

// calcAutoResizeSize 计算自动缩放的目标尺寸
// 核心原则：只缩小不放大，按最大边等比例缩放
// 返回值：目标宽度、目标高度、是否需要缩放
//
// 示例（targetW=1920, targetH=0）：
//   - 4000x3000 → 1920x1440（宽度超限，按宽度缩放）
//   - 750x1920  → 不缩放（宽度 750<1920，高度 1920 虽大但 targetH=0 不限制高度）
//   - 3000x5000 → 1920x3200（宽度超限，按宽度等比例缩放）
//
// 示例（targetW=1920, targetH=1920）：
//   - 4000x3000 → 1920x1440（宽度超限，Fit 到 1920x1920 边界框）
//   - 750x1920  → 不缩放（宽高都在边界框内）
//   - 3000x5000 → 1152x1920（高度超限，Fit 到 1920x1920 边界框）
func calcAutoResizeSize(srcW, srcH, targetW, targetH int) (int, int, bool) {
	// 如果目标尺寸都为 0，不需要缩放
	if targetW == 0 && targetH == 0 {
		return srcW, srcH, false
	}

	// 只指定宽度（targetH=0）：只检查宽度是否超限
	if targetW > 0 && targetH == 0 {
		if srcW <= targetW {
			// 原图宽度已小于等于目标宽度，不需要缩放（不放大）
			return srcW, srcH, false
		}
		// 宽度超限，按目标宽度等比例缩小
		return targetW, 0, true // imaging.Fit 会自动计算高度
	}

	// 只指定高度（targetW=0）：只检查高度是否超限
	if targetW == 0 && targetH > 0 {
		if srcH <= targetH {
			// 原图高度已小于等于目标高度，不需要缩放（不放大）
			return srcW, srcH, false
		}
		// 高度超限，按目标高度等比例缩小
		return 0, targetH, true // imaging.Fit 会自动计算宽度
	}

	// 同时指定宽高：使用 Fit 模式，等比例适应目标边界框
	// imaging.Fit 本身就是只缩小不放大的（如果原图在边界框内则返回原图尺寸）
	// 但我们提前判断避免不必要的解码后处理
	if srcW <= targetW && srcH <= targetH {
		// 原图宽高都在边界框内，不需要缩放
		return srcW, srcH, false
	}

	// 至少有一边超限，需要缩放
	return targetW, targetH, true
}

// ShouldAutoResize 判断上传的文件是否需要自动缩放
// 检查条件：1. 自动缩放已启用 2. 是图片类型 3. 文件大小超过阈值
func ShouldAutoResize(cfg *config.ImageResizeConfig, contentType string, fileSize int64) bool {
	// 自动缩放未启用
	if !cfg.AutoResizeEnabled {
		return false
	}

	// 不是图片类型，不需要缩放
	if !isImageContentType(contentType) {
		return false
	}

	// 解析最小文件大小阈值
	minBytes := parseSizeToBytes(cfg.AutoResizeMinSize)
	if fileSize < minBytes {
		return false // 文件太小，不需要缩放
	}

	return true
}

// isImageContentType 判断是否为图片类型（比 isResizableContentType 更宽松，包含 BMP/TIFF 等）
func isImageContentType(contentType string) bool {
	return strings.HasPrefix(contentType, "image/")
}

// parseSizeToBytes 将人类可读的大小字符串解析为字节数
// 支持格式：5MB, 10GB, 500KB, 1TB 等
func parseSizeToBytes(sizeStr string) int64 {
	if sizeStr == "" {
		return 0
	}

	sizeStr = strings.TrimSpace(strings.ToUpper(sizeStr))

	// 解析数字部分
	var num float64
	var unit string
	fmt.Sscanf(sizeStr, "%f%s", &num, &unit)

	// 根据单位计算字节数
	switch unit {
	case "TB", "T":
		return int64(num * 1024 * 1024 * 1024 * 1024)
	case "GB", "G":
		return int64(num * 1024 * 1024 * 1024)
	case "MB", "M":
		return int64(num * 1024 * 1024)
	case "KB", "K":
		return int64(num * 1024)
	default:
		return int64(num) // 无单位，视为字节
	}
}

// BuildAutoResizeTask 根据上传信息构建自动缩放任务
func BuildAutoResizeTask(cfg *config.ImageResizeConfig, bucketName, key, contentType string, size int64) *AutoResizeTask {
	// 推断更精确的 Content-Type（WebDAV 上传可能为 application/octet-stream）
	if contentType == "" || contentType == "application/octet-stream" {
		if guessed := guessImageContentType(key); guessed != "" {
			contentType = guessed
		} else {
			return nil // 无法推断为图片，不创建任务
		}
	}

	return &AutoResizeTask{
		BucketName:  bucketName,
		Key:         key,
		ContentType: contentType,
		Size:        size,
		Quality:     cfg.AutoResizeQuality,
		TargetW:     cfg.AutoResizeTargetW,
		TargetH:     cfg.AutoResizeTargetH,
		SubmittedAt: time.Now(),
	}
}

// guessImageContentType 根据文件扩展名推断图片 Content-Type
func guessImageContentType(key string) string {
	ext := strings.ToLower(filepath.Ext(key))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}
