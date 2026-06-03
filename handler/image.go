package handler

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	// gif 编解码支持
	_ "image/gif"
	// jpeg 编解码支持
	_ "image/jpeg"
	// png 编解码支持
	_ "image/png"

	"github.com/disintegration/imaging"
	"github.com/onaonbir/Cloodsy-S3/config"
)

// resizeSemaphore 用于限制并发图片缩放数量的信号量
// 防止大量并发缩放请求耗尽内存
var resizeSemaphore struct {
	mu    sync.Mutex // 互斥锁，保护计数器
	count int        // 当前正在进行的缩放数量
	limit int        // 最大并发缩放数
	once  sync.Once  // 确保只初始化一次
}

// initResizeSemaphore 初始化并发缩放信号量（仅执行一次）
func initResizeSemaphore(limit int) {
	resizeSemaphore.once.Do(func() {
		resizeSemaphore.limit = limit
	})
}

// acquireResizeSlot 尝试获取一个缩放槽位，成功返回 true，并发已满返回 false
func acquireResizeSlot() bool {
	resizeSemaphore.mu.Lock()
	defer resizeSemaphore.mu.Unlock()
	// 当前并发数已达上限，拒绝新的缩放请求
	if resizeSemaphore.count >= resizeSemaphore.limit {
		return false
	}
	resizeSemaphore.count++ // 占用一个槽位
	return true
}

// releaseResizeSlot 释放一个缩放槽位
func releaseResizeSlot() {
	resizeSemaphore.mu.Lock()
	defer resizeSemaphore.mu.Unlock()
	resizeSemaphore.count-- // 释放槽位
}

// ResizeParams 图片缩放参数
type ResizeParams struct {
	Width   int    // 目标宽度（0 表示不指定）
	Height  int    // 目标高度（0 表示不指定）
	Quality int    // 图片质量 1-100（仅对 JPEG 有效，默认 85）
	Mode    string // 缩放模式：fit=等比例缩放(默认), fill=填充, crop=裁剪
}

// parseResizeParams 从请求的查询参数中解析图片缩放参数
// 支持参数：w=宽度, h=高度, q=质量(1-100), m=模式(fit/fill/crop)
// cfg 参数用于安全限制校验
func parseResizeParams(r *http.Request, cfg *config.ImageResizeConfig) *ResizeParams {
	q := r.URL.Query()

	// 解析宽度参数 w 和高度参数 h
	widthStr := q.Get("w")
	heightStr := q.Get("h")

	// 如果没有指定宽度和高度，则不需要缩放
	if widthStr == "" && heightStr == "" {
		return nil
	}

	params := &ResizeParams{
		Quality: 85,    // 默认 JPEG 质量
		Mode:    "fit", // 默认等比例缩放
	}

	// 解析宽度
	if widthStr != "" {
		w, err := strconv.Atoi(widthStr)
		if err != nil || w <= 0 {
			return nil // 无效宽度参数，忽略缩放
		}
		// 限制最大宽度，防止用户请求超大尺寸导致内存爆炸
		if w > cfg.MaxWidth {
			w = cfg.MaxWidth
		}
		params.Width = w
	}

	// 解析高度
	if heightStr != "" {
		h, err := strconv.Atoi(heightStr)
		if err != nil || h <= 0 {
			return nil // 无效高度参数，忽略缩放
		}
		// 限制最大高度，防止用户请求超大尺寸导致内存爆炸
		if h > cfg.MaxHeight {
			h = cfg.MaxHeight
		}
		params.Height = h
	}

	// 解析质量参数 q（1-100，仅对 JPEG 有效）
	if qStr := q.Get("q"); qStr != "" {
		quality, err := strconv.Atoi(qStr)
		if err != nil || quality < 1 || quality > 100 {
			// 无效质量参数，使用默认值
			params.Quality = 85
		} else {
			params.Quality = quality
		}
	}

	// 解析缩放模式 m
	if mode := q.Get("m"); mode != "" {
		switch strings.ToLower(mode) {
		case "fit":
			// 等比例缩放（默认），保持宽高比，适应指定尺寸
			params.Mode = "fit"
		case "fill":
			// 填充模式，等比例缩放并填充到指定尺寸（可能裁剪）
			params.Mode = "fill"
		case "crop":
			// 裁剪模式，从中心裁剪到指定尺寸
			params.Mode = "crop"
		default:
			// 未知模式，使用默认的等比例缩放
			params.Mode = "fit"
		}
	}

	return params
}

// isResizableContentType 判断 Content-Type 是否为可缩放的图片类型
// 支持 JPEG、PNG、GIF 格式的图片缩放
func isResizableContentType(contentType string) bool {
	switch contentType {
	case "image/jpeg", "image/png", "image/gif":
		return true
	default:
		return false
	}
}

// resizeImage 对图片数据进行缩放处理，返回缩放后的图片字节
// 参数 reader 为原始图片数据读取器，contentType 为图片类型，params 为缩放参数
// cfg 参数用于安全限制校验（放大倍数限制等）
func resizeImage(reader io.Reader, contentType string, params *ResizeParams, cfg *config.ImageResizeConfig) ([]byte, string, error) {
	// 尝试获取并发缩放槽位，防止大量并发请求耗尽内存
	if !acquireResizeSlot() {
		return nil, "", fmt.Errorf("too many concurrent resize requests (limit: %d)", cfg.MaxConcurrent)
	}
	// 确保函数返回时释放槽位
	defer releaseResizeSlot()

	// 解码原始图片，启用自动方向校正（根据 EXIF 信息旋转图片）
	srcImg, err := imaging.Decode(reader, imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", fmt.Errorf("decode image failed: %w", err)
	}

	// 获取原始图片尺寸
	srcBounds := srcImg.Bounds()
	srcW := srcBounds.Dx() // 原始宽度
	srcH := srcBounds.Dy() // 原始高度

	// 安全检查：限制放大倍数，防止小图放大到超大尺寸导致内存爆炸
	// 例如：100x100 的图不允许放大到 90000x90000
	clampUpscale(params, srcW, srcH, cfg)

	// 计算目标尺寸
	dstW, dstH := calcTargetSize(srcW, srcH, params.Width, params.Height, params.Mode)

	var dstImg image.Image

	switch params.Mode {
	case "crop":
		// 裁剪模式：先等比例缩放到覆盖目标区域，再从中心裁剪
		// 第一步：等比例缩放，确保缩放后的图片完全覆盖目标区域
		resized := imaging.Resize(srcImg, dstW, dstH, imaging.Lanczos)
		// 第二步：从中心裁剪到目标尺寸
		dstImg = imaging.CropCenter(resized, params.Width, params.Height)

	case "fill":
		// 填充模式：等比例缩放适应目标区域，不足部分用黑色填充
		// 第一步：等比例缩放，确保图片完全在目标区域内
		resized := imaging.Fit(srcImg, dstW, dstH, imaging.Lanczos)
		// 第二步：创建目标尺寸的黑色背景画布
		canvas := imaging.New(dstW, dstH, image.Black)
		// 第三步：将缩放后的图片居中粘贴到画布上
		dstImg = imaging.PasteCenter(canvas, resized)

	default:
		// fit 模式（默认）：等比例缩放，适应指定尺寸，不裁剪不填充
		dstImg = imaging.Resize(srcImg, dstW, dstH, imaging.Lanczos)
	}

	// 将缩放后的图片编码为字节
	var buf bytes.Buffer
	outputContentType := contentType // 输出格式默认与输入相同

	switch contentType {
	case "image/jpeg":
		// JPEG 编码，使用指定的质量参数
		if err := jpeg.Encode(&buf, dstImg, &jpeg.Options{Quality: params.Quality}); err != nil {
			return nil, "", fmt.Errorf("jpeg encode failed: %w", err)
		}
	case "image/png":
		// PNG 编码（无损格式，质量参数无效）
		if err := png.Encode(&buf, dstImg); err != nil {
			return nil, "", fmt.Errorf("png encode failed: %w", err)
		}
	case "image/gif":
		// GIF 缩放后转为 PNG 输出以保持质量（GIF 仅支持 256 色）
		if err := png.Encode(&buf, dstImg); err != nil {
			return nil, "", fmt.Errorf("gif->png encode failed: %w", err)
		}
		outputContentType = "image/png" // GIF 缩放后输出为 PNG
	default:
		// 不支持的格式，返回错误（不应到达此处，因为已做类型检查）
		return nil, "", imaging.ErrUnsupportedFormat
	}

	return buf.Bytes(), outputContentType, nil
}

// clampUpscale 限制放大倍数，防止小图放大到超大尺寸导致内存爆炸
// 例如：原图 100x100，MaxUpscale=2，则最大只能放大到 200x200
func clampUpscale(params *ResizeParams, srcW, srcH int, cfg *config.ImageResizeConfig) {
	if cfg.MaxUpscale <= 0 {
		return // 不限制放大倍数
	}

	// 计算原图允许的最大输出尺寸
	maxW := srcW * cfg.MaxUpscale // 按放大倍数计算最大宽度
	maxH := srcH * cfg.MaxUpscale // 按放大倍数计算最大高度

	// 如果指定了宽度且超过最大放大尺寸，则限制
	if params.Width > 0 && params.Width > maxW {
		params.Width = maxW
	}
	// 如果指定了高度且超过最大放大尺寸，则限制
	if params.Height > 0 && params.Height > maxH {
		params.Height = maxH
	}
}

// calcTargetSize 根据原始尺寸和目标尺寸计算实际缩放尺寸
// srcW/srcH: 原始宽高, dstW/dstH: 目标宽高(0表示不指定), mode: 缩放模式
func calcTargetSize(srcW, srcH, dstW, dstH int, mode string) (int, int) {
	// 如果目标宽度和高度都为0，返回原始尺寸（不应发生）
	if dstW == 0 && dstH == 0 {
		return srcW, srcH
	}

	// 根据模式计算
	switch mode {
	case "crop":
		// 裁剪模式：需要等比例缩放到完全覆盖目标区域
		// 计算宽度和高度的缩放比，取较大值确保覆盖
		if dstW > 0 && dstH > 0 {
			// 同时指定宽高，计算覆盖缩放比
			scaleW := float64(dstW) / float64(srcW) // 宽度缩放比
			scaleH := float64(dstH) / float64(srcH) // 高度缩放比
			// 取较大缩放比，确保缩放后完全覆盖目标区域
			if scaleW > scaleH {
				return dstW, 0 // 按宽度缩放，高度自适应
			}
			return 0, dstH // 按高度缩放，宽度自适应
		}
		// 只指定一个维度，直接使用
		return dstW, dstH

	case "fill":
		// 填充模式：与 fit 类似，等比例缩放适应目标区域
		if dstW > 0 && dstH > 0 {
			return dstW, dstH // imaging.Fit 会自动计算等比例尺寸
		}
		return dstW, dstH

	default:
		// fit 模式（默认）：等比例缩放
		// 如果只指定宽度，高度按比例计算
		if dstH == 0 && dstW > 0 {
			return dstW, 0 // imaging.Resize 会自动计算高度
		}
		// 如果只指定高度，宽度按比例计算
		if dstW == 0 && dstH > 0 {
			return 0, dstH // imaging.Resize 会自动计算宽度
		}
		// 同时指定宽高，等比例缩放适应
		return dstW, dstH
	}
}
