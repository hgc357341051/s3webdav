package handler

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	// gif 编解码支持
	_ "image/gif"
	"image/jpeg"

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
	Quality     int       // 初始 JPEG 质量（二分法会自动降低）
	TargetW     int       // 目标宽度（0=不限制）
	TargetH     int       // 目标高度（0=不限制）
	TargetSize  int64     // 目标文件大小字节数（0=不限制）
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
// 策略：先按尺寸缩放，再按目标文件大小二分法降低质量
// 这是微信/WhatsApp/TinyPNG 等主流图片压缩服务的通用做法
func (p *ImageProcessor) processTask(workerID int, task AutoResizeTask) {
	start := time.Now()
	p.logger.Info("auto-resize processing",
		"worker", workerID,
		"bucket", task.BucketName,
		"key", task.Key,
		"size", task.Size,
		"target_size", task.TargetSize,
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

	// 记录原始尺寸
	srcBounds := srcImg.Bounds()
	srcW := srcBounds.Dx() // 原始宽度
	srcH := srcBounds.Dy() // 原始高度

	// 第三步：按尺寸缩放（只缩小不放大）
	var workImg = srcImg // 工作图片，初始为原图
	dstW, dstH := srcW, srcH

	needDimResize, resizeW, resizeH := calcAutoResizeDims(srcW, srcH, task.TargetW, task.TargetH)
	if needDimResize {
		// 执行尺寸缩放（Fit 等比例适应，Lanczos 高质量重采样）
		workImg = imaging.Fit(srcImg, resizeW, resizeH, imaging.Lanczos)
		workBounds := workImg.Bounds()
		dstW = workBounds.Dx()
		dstH = workBounds.Dy()
		p.logger.Info("auto-resize: dimension resize applied",
			"key", task.Key,
			"src", fmt.Sprintf("%dx%d", srcW, srcH),
			"dst", fmt.Sprintf("%dx%d", dstW, dstH),
		)
	} else {
		p.logger.Info("auto-resize: no dimension resize needed",
			"key", task.Key,
			"src", fmt.Sprintf("%dx%d", srcW, srcH),
			"target_w", task.TargetW,
			"target_h", task.TargetH,
		)
	}

	// 如果既不需要尺寸缩放，也没有目标文件大小限制，则跳过
	if !needDimResize && task.TargetSize <= 0 {
		p.logger.Info("auto-resize: image within all targets, skipping",
			"key", task.Key, "src_w", srcW, "src_h", srcH,
		)
		return
	}

	// 第四步：编码并压缩到目标文件大小
	// 策略：先用初始质量编码，如果超过目标文件大小，用二分法降低质量
	resizedData, outputContentType, finalQuality, err := encodeToTargetSize(
		workImg, task.ContentType, task.Quality, task.TargetSize,
	)
	if err != nil {
		p.logger.Error("auto-resize: encode failed", "error", err, "key", task.Key)
		return
	}

	resizedSize := int64(len(resizedData))

	// 第五步：如果压缩后反而更大（PNG 等无损格式可能），则放弃替换
	if resizedSize >= task.Size {
		p.logger.Info("auto-resize: resized image larger than original, skipping",
			"key", task.Key,
			"original_size", task.Size,
			"resized_size", resizedSize,
		)
		return
	}

	// 第六步：计算新文件的 ETag
	hash := md5.Sum(resizedData)
	newETag := hex.EncodeToString(hash[:])

	// 第七步：替换存储中的文件
	reader = io.NopCloser(bytes.NewReader(resizedData))
	newSize, _, err := p.storage.PutObject(task.BucketName, task.Key, reader)
	if err != nil {
		p.logger.Error("auto-resize: failed to write resized object", "error", err, "key", task.Key)
		return
	}

	// 第八步：更新数据库中的元数据
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
		ContentType:  outputContentType, // 更新为实际输出类型
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
		"src_dims", fmt.Sprintf("%dx%d", srcW, srcH),
		"dst_dims", fmt.Sprintf("%dx%d", dstW, dstH),
		"quality", finalQuality,
		"original_bytes", task.Size,
		"resized_bytes", newSize,
		"saved", fmt.Sprintf("%d (%.1f%%)", savedBytes, savedPercent),
		"elapsed", elapsed.Round(time.Millisecond),
	)
}

// calcAutoResizeDims 计算自动缩放的目标尺寸
// 核心原则：只缩小不放大，按最大边等比例缩放
// 返回值：是否需要缩放、目标宽度（传给 imaging.Fit）、目标高度（传给 imaging.Fit）
//
// 示例（targetW=1920, targetH=0）：
//   - 4000x3000 → true, 1920, 0（宽度超限，按宽度缩放）
//   - 750x1920  → false, 0, 0（宽度 750<1920，不缩放）
//   - 3000x5000 → true, 1920, 0（宽度超限，按宽度缩放）
//
// 示例（targetW=1920, targetH=1920）：
//   - 4000x3000 → true, 1920, 1920（宽度超限，Fit 到边界框）
//   - 750x1920  → false, 0, 0（宽高都在边界框内）
//   - 3000x5000 → true, 1920, 1920（高度超限，Fit 到边界框）
func calcAutoResizeDims(srcW, srcH, targetW, targetH int) (bool, int, int) {
	// 如果目标尺寸都为 0，不需要缩放
	if targetW == 0 && targetH == 0 {
		return false, 0, 0
	}

	// 只指定宽度（targetH=0）：只检查宽度是否超限
	if targetW > 0 && targetH == 0 {
		if srcW <= targetW {
			// 原图宽度已小于等于目标宽度，不需要缩放（不放大）
			return false, 0, 0
		}
		// 宽度超限，按目标宽度等比例缩小
		return true, targetW, 0 // imaging.Fit 会自动计算高度
	}

	// 只指定高度（targetW=0）：只检查高度是否超限
	if targetW == 0 && targetH > 0 {
		if srcH <= targetH {
			// 原图高度已小于等于目标高度，不需要缩放（不放大）
			return false, 0, 0
		}
		// 高度超限，按目标高度等比例缩小
		return true, 0, targetH // imaging.Fit 会自动计算宽度
	}

	// 同时指定宽高：使用 Fit 模式，等比例适应目标边界框
	if srcW <= targetW && srcH <= targetH {
		// 原图宽高都在边界框内，不需要缩放
		return false, 0, 0
	}

	// 至少有一边超限，需要缩放
	return true, targetW, targetH
}

// encodeToTargetSize 编码图片到目标文件大小
// 策略（主流做法，参考微信/WhatsApp/TinyPNG）：
//  1. 先用初始质量编码，检查文件大小
//  2. 如果未超过目标大小，直接返回
//  3. 如果超过目标大小，用二分法在 [minQuality, maxQuality] 之间搜索最优质量
//  4. 对于 PNG/GIF 等无损格式，先转为 JPEG 再压缩（无损格式无法调质量）
//
// 参数：
//   - img: 待编码的图片
//   - contentType: 原始内容类型
//   - initialQuality: 初始 JPEG 质量（1-100）
//   - targetSize: 目标文件大小字节数（0=不限制，直接用初始质量编码）
//
// 返回：编码后的字节数据、输出内容类型、最终使用的质量、错误
func encodeToTargetSize(img image.Image, contentType string, initialQuality int, targetSize int64) ([]byte, string, int, error) {
	// 对于 JPEG 图片，使用二分法质量压缩
	if contentType == "image/jpeg" {
		return encodeJpegToTargetSize(img, initialQuality, targetSize)
	}

	// 对于 PNG/GIF/BMP 等无损格式，转为 JPEG 再压缩
	// 因为无损格式无法通过降低质量来减小文件大小
	// 这是主流图片服务的通用做法：统一转为 JPEG 压缩
	return encodeJpegToTargetSize(img, initialQuality, targetSize)
}

// encodeJpegToTargetSize 用二分法将图片编码为 JPEG，直到文件大小不超过目标值
// 这是微信/WhatsApp 等图片压缩的核心算法
//
// 算法流程：
//  1. 先用 initialQuality 编码，如果文件大小 ≤ targetSize，直接返回
//  2. 否则在 [minQuality, maxQuality] 之间二分搜索
//  3. 每次取中间质量编码，根据结果大小调整搜索范围
//  4. 最多迭代 maxIterations 次（默认 10 次，足够精确）
//  5. 最终取满足目标大小的最高质量
func encodeJpegToTargetSize(img image.Image, initialQuality int, targetSize int64) ([]byte, string, int, error) {
	// 限制初始质量在合理范围 [1, 100]
	if initialQuality < 1 {
		initialQuality = 1
	}
	if initialQuality > 100 {
		initialQuality = 100
	}

	// 第一步：用初始质量编码，检查是否已满足目标
	data, err := encodeJpeg(img, initialQuality)
	if err != nil {
		return nil, "", initialQuality, fmt.Errorf("jpeg encode at quality %d: %w", initialQuality, err)
	}

	// 如果没有目标大小限制，或已满足目标，直接返回
	if targetSize <= 0 || int64(len(data)) <= targetSize {
		return data, "image/jpeg", initialQuality, nil
	}

	// 第二步：二分法搜索最优质量
	const (
		minQuality    = 10 // 最低质量下限，低于此值画质严重劣化
		maxIterations = 10 // 最大迭代次数，10 次足以覆盖 [10, 100] 的质量范围
	)

	lo := minQuality         // 二分搜索下界（最低质量）
	hi := initialQuality - 1 // 二分搜索上界（比初始质量低 1，因为初始质量已确认超标）
	var bestData []byte      // 记录满足条件的最佳编码数据
	var bestQuality int      // 记录满足条件的最佳质量

	for i := 0; i < maxIterations; i++ {
		mid := (lo + hi) / 2 // 取中间质量
		if mid < minQuality {
			mid = minQuality
		}

		// 用当前中间质量编码
		data, err := encodeJpeg(img, mid)
		if err != nil {
			// 编码失败，降低质量重试
			hi = mid - 1
			continue
		}

		currentSize := int64(len(data))

		if currentSize <= targetSize {
			// 当前质量满足目标大小，记录并尝试更高质量
			bestData = data
			bestQuality = mid
			lo = mid + 1 // 尝试更高质量
		} else {
			// 当前质量仍然超标，降低质量
			hi = mid - 1
		}

		// 搜索范围已收敛，提前退出
		if lo > hi {
			break
		}
	}

	// 如果二分法没找到满足条件的质量，用最低质量兜底
	if bestData == nil {
		data, err := encodeJpeg(img, minQuality)
		if err != nil {
			return nil, "", minQuality, fmt.Errorf("jpeg encode at min quality %d: %w", minQuality, err)
		}
		bestData = data
		bestQuality = minQuality
	}

	return bestData, "image/jpeg", bestQuality, nil
}

// encodeJpeg 将图片编码为 JPEG 格式
func encodeJpeg(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

	// 解析目标文件大小（0 表示不限制）
	targetSize := parseSizeToBytes(cfg.AutoResizeTargetSize)

	return &AutoResizeTask{
		BucketName:  bucketName,
		Key:         key,
		ContentType: contentType,
		Size:        size,
		Quality:     cfg.AutoResizeQuality,
		TargetW:     cfg.AutoResizeTargetW,
		TargetH:     cfg.AutoResizeTargetH,
		TargetSize:  targetSize,
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
