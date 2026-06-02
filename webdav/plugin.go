package webdav

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/onaonbir/Cloodsy-S3/config"
	"github.com/onaonbir/Cloodsy-S3/db"
	"github.com/onaonbir/Cloodsy-S3/server"
	"github.com/onaonbir/Cloodsy-S3/storage"
	"golang.org/x/net/webdav"
)

type WebDAVPlugin struct {
	mu       sync.Mutex
	server   *http.Server
	running  bool
	listen   string
	prefix   string
	database *db.DB
	store    *storage.FileSystem
	logger   *slog.Logger
	cfg      *config.Config
}

func init() {
	server.RegisterPlugin(&WebDAVPlugin{})
}

func (p *WebDAVPlugin) Name() string {
	return "webdav"
}

func (p *WebDAVPlugin) Start(deps server.PluginDeps) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("webdav plugin already running")
	}

	p.database = deps.DB
	p.store = deps.Store
	p.logger = deps.Logger
	p.cfg = deps.Config
	p.listen = deps.Config.WebDAV.Listen
	p.prefix = deps.Config.WebDAV.Prefix

	if !deps.Config.WebDAV.Enabled {
		p.logger.Info("webdav plugin disabled by config, skipping start")
		return nil
	}

	s3fs := NewS3FileSystem(p.database, p.store, p.logger)

	davHandler := &webdav.Handler{
		Prefix:     p.prefix,
		FileSystem: s3fs,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err == nil {
				return
			}
			p.logger.Error("webdav error", "method", r.Method, "path", r.URL.Path, "error", err)
		},
	}

	authHandler := NewBasicAuthHandler(davHandler, p.database, p.logger)

	p.server = &http.Server{
		Addr:              p.listen,
		Handler:           authHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", p.listen)
	if err != nil {
		return fmt.Errorf("webdav listen: %w", err)
	}

	go func() {
		p.logger.Info("WebDAV plugin listening", "addr", p.listen)
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			p.logger.Error("webdav server error", "error", err)
		}
	}()

	p.running = true
	return nil
}

func (p *WebDAVPlugin) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running || p.server == nil {
		return fmt.Errorf("webdav plugin not running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := p.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("webdav shutdown: %w", err)
	}

	p.running = false
	p.server = nil
	p.logger.Info("WebDAV plugin stopped")
	return nil
}

func (p *WebDAVPlugin) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *WebDAVPlugin) Status() map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]interface{}{
		"enabled": p.running,
		"listen":  p.listen,
		"prefix":  p.prefix,
	}
}
