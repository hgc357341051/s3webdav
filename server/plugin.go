package server

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/onaonbir/Cloodsy-S3/config"
	"github.com/onaonbir/Cloodsy-S3/db"
	"github.com/onaonbir/Cloodsy-S3/storage"
)

type PluginDeps struct {
	DB     *db.DB
	Store  *storage.FileSystem
	Config *config.Config
	Logger *slog.Logger
}

type ServicePlugin interface {
	Name() string
	Start(deps PluginDeps) error
	Stop() error
	IsRunning() bool
	Status() map[string]interface{}
}

type PluginRegistry struct {
	mu      sync.Mutex
	plugins map[string]ServicePlugin
}

var globalRegistry = &PluginRegistry{
	plugins: make(map[string]ServicePlugin),
}

func RegisterPlugin(p ServicePlugin) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.plugins[p.Name()] = p
}

func GetPlugin(name string) ServicePlugin {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	return globalRegistry.plugins[name]
}

func AllPlugins() []ServicePlugin {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	result := make([]ServicePlugin, 0, len(globalRegistry.plugins))
	for _, p := range globalRegistry.plugins {
		result = append(result, p)
	}
	return result
}

func StartAllPlugins(deps PluginDeps) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	for _, p := range globalRegistry.plugins {
		if err := p.Start(deps); err != nil {
			deps.Logger.Error("plugin start failed", "plugin", p.Name(), "error", err)
		} else {
			deps.Logger.Info("plugin started", "plugin", p.Name())
		}
	}
}

func StopAllPlugins() {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	for _, p := range globalRegistry.plugins {
		if p.IsRunning() {
			if err := p.Stop(); err != nil {
				fmt.Printf("plugin %s stop error: %v\n", p.Name(), err)
			}
		}
	}
}

func StartPluginByName(name string, deps PluginDeps) error {
	globalRegistry.mu.Lock()
	p, ok := globalRegistry.plugins[name]
	globalRegistry.mu.Unlock()
	if !ok {
		return fmt.Errorf("plugin %q not found", name)
	}
	return p.Start(deps)
}

func StopPluginByName(name string) error {
	globalRegistry.mu.Lock()
	p, ok := globalRegistry.plugins[name]
	globalRegistry.mu.Unlock()
	if !ok {
		return fmt.Errorf("plugin %q not found", name)
	}
	return p.Stop()
}
