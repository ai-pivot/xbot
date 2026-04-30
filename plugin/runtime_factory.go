package plugin

import (
	"fmt"
)

// ---------------------------------------------------------------------------
// CompositeRuntimeFactory — dispatches to native/grpc/script factories
// ---------------------------------------------------------------------------

// NewCompositeRuntimeFactory creates a RuntimeFactory that handles all
// runtime types: native (in-process Go), grpc (external JSON process),
// and script (simple periodic script execution).
func NewCompositeRuntimeFactory() RuntimeFactory {
	return &compositeRuntimeFactory{
		native: NewNativeRuntime(),
		grpc:   NewGRPCRuntime(),
		script: NewScriptRuntime(),
	}
}

type compositeRuntimeFactory struct {
	native RuntimeFactory
	grpc   RuntimeFactory
	script RuntimeFactory
}

func (f *compositeRuntimeFactory) Create(manifest *PluginManifest, dir string) (Plugin, error) {
	switch manifest.Runtime {
	case RuntimeNative:
		return f.native.Create(manifest, dir)
	case RuntimeGRPC:
		return f.grpc.Create(manifest, dir)
	case RuntimeScript:
		return f.script.Create(manifest, dir)
	default:
		return nil, fmt.Errorf("unsupported runtime: %q (supported: native, gRPC, script)", manifest.Runtime)
	}
}
