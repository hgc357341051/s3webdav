package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Database    DatabaseConfig    `yaml:"database"`
	Storage     StorageConfig     `yaml:"storage"`
	Logging     LoggingConfig     `yaml:"logging"`
	Admin       AdminConfig       `yaml:"admin"`
	WebDAV      WebDAVConfig      `yaml:"webdav"`
	ImageResize ImageResizeConfig `yaml:"image_resize"` // 图片缩放配置
}

// ImageResizeConfig 图片缩放安全限制配置
type ImageResizeConfig struct {
	MaxWidth      int `yaml:"max_width"`      // 输出图片最大宽度（像素），默认 4096
	MaxHeight     int `yaml:"max_height"`     // 输出图片最大高度（像素），默认 4096
	MaxUpscale    int `yaml:"max_upscale"`    // 最大放大倍数（相对原图），默认 2（即最多放大到原图 2 倍）
	MaxConcurrent int `yaml:"max_concurrent"` // 最大并发缩放数，默认 4（防止内存爆满）

	// 自动缩放配置：上传大图时自动异步缩放替换原图
	AutoResizeEnabled bool   `yaml:"auto_resize_enabled"`  // 是否启用上传自动缩放，默认 false
	AutoResizeMinSize string `yaml:"auto_resize_min_size"` // 触发自动缩放的最小文件大小，默认 "5MB"
	AutoResizeTargetW int    `yaml:"auto_resize_target_w"` // 自动缩放目标宽度（0=不限制），默认 1920
	AutoResizeTargetH int    `yaml:"auto_resize_target_h"` // 自动缩放目标高度（0=不限制），默认 0（按宽度等比例）
	AutoResizeQuality int    `yaml:"auto_resize_quality"`  // 自动缩放 JPEG 质量，默认 85
}

type WebDAVConfig struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"`
	Prefix  string `yaml:"prefix"`
}

type AdminConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Listen      string   `yaml:"listen"`
	CORSOrigins []string `yaml:"cors_origins"`
}

type ServerConfig struct {
	Listen      string    `yaml:"listen"`
	Region      string    `yaml:"region"`
	TLS         TLSConfig `yaml:"tls"`
	CORSOrigins []string  `yaml:"cors_origins"`
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type DatabaseConfig struct {
	Path        string `yaml:"path"`
	BusyTimeout int    `yaml:"busy_timeout"` // ms, default 5000
	CacheSize   int    `yaml:"cache_size"`   // KB, default 64000 (64MB)
	MmapSize    int    `yaml:"mmap_size"`    // bytes, default 134217728 (128MB)
	MaxReaders  int    `yaml:"max_readers"`  // default 4
}

type StorageConfig struct {
	RootDir           string `yaml:"root_dir"`
	MultipartMaxAge   string `yaml:"multipart_max_age"`  // Duration string, e.g. "24h"
	LifecycleInterval string `yaml:"lifecycle_interval"` // Duration string, e.g. "1h"
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default returns a Config with all default values.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:      ":9000",
			Region:      "us-east-1",
			CORSOrigins: []string{"*"}, // 默认允许所有来源，方便本地测试
		},
		Database: DatabaseConfig{
			Path:        "./.cloodsys3/cloodsys3.db",
			BusyTimeout: 5000,
			CacheSize:   64000,
			MmapSize:    134217728,
			MaxReaders:  4,
		},
		Storage: StorageConfig{
			RootDir:         "./.cloodsys3/data",
			MultipartMaxAge: "24h",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
		},
		ImageResize: ImageResizeConfig{
			MaxWidth:          4096,  // 输出图片最大宽度 4096px
			MaxHeight:         4096,  // 输出图片最大高度 4096px
			MaxUpscale:        2,     // 最多放大到原图 2 倍
			MaxConcurrent:     4,     // 最多 4 个并发缩放
			AutoResizeEnabled: false, // 默认关闭自动缩放
			AutoResizeMinSize: "5MB", // 5MB 以上触发
			AutoResizeTargetW: 1920,  // 目标宽度 1920px
			AutoResizeTargetH: 0,     // 高度按比例自动计算
			AutoResizeQuality: 85,    // JPEG 质量 85
		},
	}
}

func applyDefaults(cfg *Config) {
	d := Default()
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = d.Server.Listen
	}
	if cfg.Server.Region == "" {
		cfg.Server.Region = d.Server.Region
	}
	// CORSOrigins 为空时设置默认值，允许所有来源
	if len(cfg.Server.CORSOrigins) == 0 {
		cfg.Server.CORSOrigins = d.Server.CORSOrigins
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = d.Database.Path
	}
	if cfg.Database.BusyTimeout <= 0 {
		cfg.Database.BusyTimeout = d.Database.BusyTimeout
	}
	if cfg.Database.CacheSize <= 0 {
		cfg.Database.CacheSize = d.Database.CacheSize
	}
	if cfg.Database.MmapSize <= 0 {
		cfg.Database.MmapSize = d.Database.MmapSize
	}
	if cfg.Database.MaxReaders <= 0 {
		cfg.Database.MaxReaders = d.Database.MaxReaders
	}
	if cfg.Storage.RootDir == "" {
		cfg.Storage.RootDir = d.Storage.RootDir
	}
	if cfg.Storage.MultipartMaxAge == "" {
		cfg.Storage.MultipartMaxAge = d.Storage.MultipartMaxAge
	}
	if cfg.Storage.LifecycleInterval == "" {
		cfg.Storage.LifecycleInterval = "1h"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = d.Logging.Level
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = d.Logging.Format
	}
	if cfg.Admin.Listen == "" {
		cfg.Admin.Listen = ":9001"
	}
	if cfg.WebDAV.Listen == "" {
		cfg.WebDAV.Listen = ":9002"
	}
	if cfg.WebDAV.Prefix == "" {
		cfg.WebDAV.Prefix = "/"
	}
	// 图片缩放安全限制默认值
	if cfg.ImageResize.MaxWidth <= 0 {
		cfg.ImageResize.MaxWidth = d.ImageResize.MaxWidth
	}
	if cfg.ImageResize.MaxHeight <= 0 {
		cfg.ImageResize.MaxHeight = d.ImageResize.MaxHeight
	}
	if cfg.ImageResize.MaxUpscale <= 0 {
		cfg.ImageResize.MaxUpscale = d.ImageResize.MaxUpscale
	}
	if cfg.ImageResize.MaxConcurrent <= 0 {
		cfg.ImageResize.MaxConcurrent = d.ImageResize.MaxConcurrent
	}
	// 自动缩放默认值
	if cfg.ImageResize.AutoResizeMinSize == "" {
		cfg.ImageResize.AutoResizeMinSize = d.ImageResize.AutoResizeMinSize
	}
	if cfg.ImageResize.AutoResizeTargetW <= 0 {
		cfg.ImageResize.AutoResizeTargetW = d.ImageResize.AutoResizeTargetW
	}
	if cfg.ImageResize.AutoResizeQuality <= 0 {
		cfg.ImageResize.AutoResizeQuality = d.ImageResize.AutoResizeQuality
	}
}

// Load reads a YAML config file and applies defaults for missing fields.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	applyDefaults(cfg)
	return cfg, nil
}
