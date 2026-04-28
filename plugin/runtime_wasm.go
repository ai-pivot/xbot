package plugin

import (
	"context"
	"fmt"
	"sync"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// WASM Runtime — Phase 2 sandbox runtime using wazero
//
// This is a skeleton implementation for the future WASM runtime.
// It defines the interfaces and lifecycle hooks needed for WASM plugins,
// but does not yet use wazero (to avoid adding a heavy dependency).
//
// When WASM support is needed:
// 1. Add `github.com/tetratelabs/wazero` to go.mod
// 2. Implement wasmRuntime.Create() to compile and instantiate WASM modules
// 3. Implement WASI host functions for tool execution, hook dispatch, etc.
// 4. Add integration tests with a simple .wasm plugin
// ---------------------------------------------------------------------------

// wasmRuntime is the Phase 2 WASM sandbox runtime.
type wasmRuntime struct {
	mu       sync.RWMutex
	registry map[string]*wasmPlugin // pluginID → instance
}

// NewWASMRuntime creates a factory for WASM plugin instances.
// Returns a RuntimeFactory that can be passed to PluginManager.SetRuntimeFactory().
func NewWASMRuntime() RuntimeFactory {
	return &wasmRuntime{
		registry: make(map[string]*wasmPlugin),
	}
}

// Create implements RuntimeFactory.
// Currently returns a placeholder that errors on Activate.
func (w *wasmRuntime) Create(manifest *PluginManifest, dir string) (Plugin, error) {
	if manifest.Runtime != RuntimeWASM {
		return nil, fmt.Errorf("wasmRuntime: expected runtime=wasm, got %q", manifest.Runtime)
	}

	return &wasmPlugin{
		manifest: *manifest,
		dir:      dir,
	}, nil
}

// wasmPlugin implements Plugin for WASM sandbox plugins.
type wasmPlugin struct {
	manifest PluginManifest
	dir      string
	instance any // future: wazero instance
}

func (wp *wasmPlugin) Manifest() PluginManifest {
	return wp.manifest
}

// Activate initializes the WASM module.
// Phase 2: will compile and instantiate the WASM binary.
func (wp *wasmPlugin) Activate(ctx PluginContext) error {
	// Phase 2 implementation:
	// 1. Read WASM binary from wp.dir
	// 2. Compile with wazero
	// 3. Instantiate with WASI host functions
	// 4. Call _start or activate export
	// 5. Parse tool/hook/enricher registrations from WASM memory

	log.WithField("plugin", wp.manifest.ID).
		Warn("WASM runtime not yet implemented; plugin will be a no-op")
	return nil
}

// Deactivate cleans up the WASM module.
func (wp *wasmPlugin) Deactivate(ctx PluginContext) error {
	// Phase 2: close wazero instance
	return nil
}

// Compile-time check.
var _ Plugin = (*wasmPlugin)(nil)

// ---------------------------------------------------------------------------
// WASM Host Function Interfaces (Phase 2 API Design)
//
// These interfaces define the host functions that will be exported to
// WASM modules. They define the ABI between xbot and WASM plugins.
// ---------------------------------------------------------------------------

// WASMHostAPI defines the host functions available to WASM plugins.
// This will be implemented using wazero's host function mechanism.
type WASMHostAPI interface {
	// RegisterTool registers a tool from the WASM plugin.
	// The WASM plugin calls this with a JSON-encoded tool definition.
	RegisterTool(toolDefPtr, toolDefLen uint32) error

	// RegisterHook registers a lifecycle hook from the WASM plugin.
	RegisterHook(eventPtr, eventLen, matcherPtr, matcherLen uint32) error

	// EnrichContext registers a context enricher.
	EnrichContext(namePtr, nameLen uint32) error

	// StorageGet retrieves a value from plugin storage.
	StorageGet(keyPtr, keyLen uint32) (valuePtr, valueLen uint32, err error)

	// StorageSet stores a key-value pair in plugin storage.
	StorageSet(keyPtr, keyLen, valuePtr, valueLen uint32) error

	// Log emits a log message from the plugin.
	Log(level, msgPtr, msgLen uint32) error
}
