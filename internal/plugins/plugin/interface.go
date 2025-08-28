package plugin

import (
	"context"
	"sync"
)

// Plugin represents a plugin that can be executed by Anklet
type Plugin interface {
	// Run executes the plugin with the given contexts
	Run(
		workerCtx context.Context,
		pluginCtx context.Context,
		pluginCancel context.CancelFunc,
	) (context.Context, error)

	// Name returns the unique name/identifier for this plugin
	Name() string

	// Type returns the type of plugin (handler, receiver, etc.)
	Type() string
}

// Global registry for automatic plugin discovery
var (
	globalPluginRegistry = make(map[string]Plugin)
	registryMutex        = sync.RWMutex{}
)

// Register allows plugins to self-register during init()
func Register(p Plugin) {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	globalPluginRegistry[p.Name()] = p
}

// Get retrieves a plugin by name from the global registry
func Get(name string) (Plugin, bool) {
	registryMutex.RLock()
	defer registryMutex.RUnlock()
	p, exists := globalPluginRegistry[name]
	return p, exists
}

// List returns all registered plugins
func List() map[string]Plugin {
	registryMutex.RLock()
	defer registryMutex.RUnlock()

	// Return a copy to prevent external modifications
	result := make(map[string]Plugin)
	for name, p := range globalPluginRegistry {
		result[name] = p
	}
	return result
}
