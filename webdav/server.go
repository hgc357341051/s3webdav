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
	"github.com/onaonbir/Cloodsy-S3/storage"
)

type ServerManager struct {
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

func NewServerManager(database *db.DB, store *storage.FileSystem, cfg *config.Config, logger *slog.Logger) *ServerManager {
	return &ServerManager{
		listen:   cfg.WebDAV.Listen,
		prefix:   cfg.WebDAV.Prefix,
		database: database,
		store:    store,
		logger:   logger,
		cfg:      cfg,
	}
}

func (m *ServerManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *ServerManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("webdav server already running")
	}

	handler := NewHandler(m.database, m.store, m.logger, m.prefix)

	m.server = &http.Server{
		Addr:              m.listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", m.listen)
	if err != nil {
		return fmt.Errorf("webdav listen: %w", err)
	}

	go func() {
		m.logger.Info(fmt.Sprintf("WebDAV server listening on %s", m.listen))
		if err := m.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			m.logger.Error("webdav server error", "error", err)
		}
	}()

	m.running = true
	m.cfg.WebDAV.Enabled = true
	return nil
}

func (m *ServerManager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running || m.server == nil {
		return fmt.Errorf("webdav server not running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := m.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("webdav shutdown: %w", err)
	}

	m.running = false
	m.server = nil
	m.cfg.WebDAV.Enabled = false
	m.logger.Info("WebDAV server stopped")
	return nil
}

func (m *ServerManager) Status() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	return map[string]interface{}{
		"enabled": m.running,
		"listen":  m.listen,
		"prefix":  m.prefix,
	}
}
