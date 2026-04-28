// Package helloworld is an example xbot plugin demonstrating the plugin API.
//
// It registers two tools (hello, ping), a PostToolUse hook that logs all tool
// executions, and a context enricher that injects plugin status.
package helloworld

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"xbot/plugin"
)

// HelloWorldPlugin implements plugin.Plugin.
type HelloWorldPlugin struct {
	callCount atomic.Int64 // atomic counter for thread-safe increment
	startTime time.Time
}

// New creates a new HelloWorldPlugin.
func New() *HelloWorldPlugin {
	return &HelloWorldPlugin{}
}

// Manifest returns the plugin metadata.
func (p *HelloWorldPlugin) Manifest() plugin.PluginManifest {
	return plugin.PluginManifest{
		ID:               "xbot.hello-world",
		Name:             "Hello World",
		Version:          "1.0.0",
		Description:      "A simple example plugin demonstrating the xbot plugin system",
		Runtime:          plugin.RuntimeNative,
		ActivationEvents: []string{"onStart"},
		Permissions:      []string{"tools.register", "hooks.subscribe", "context.enrich", "storage.private"},
	}
}

// Activate initializes the plugin and registers all capabilities.
func (p *HelloWorldPlugin) Activate(pctx plugin.PluginContext) error {
	p.startTime = time.Now()

	// Register the "hello" tool
	if err := pctx.RegisterTool(&plugin.SimplePluginTool{
		Def: plugin.BuildToolDef("hello", "Greet someone by name. Returns a friendly greeting message.",
			plugin.ToolParamDef{Name: "name", Type: "string", Description: "The person to greet"},
		),
		ExecFn: func(ctx context.Context, input string) (*plugin.ToolResult, error) {
			name, err := plugin.ParseToolInputString(input, "name")
			if err != nil {
				name = "World"
			}
			p.callCount.Add(1)
			return plugin.NewToolResult(fmt.Sprintf("Hello, %s! 👋 Welcome to xbot plugins.", name)), nil
		},
	}); err != nil {
		return fmt.Errorf("register hello tool: %w", err)
	}

	// Register the "ping" tool
	if err := pctx.RegisterTool(&plugin.SimplePluginTool{
		Def: plugin.BuildToolDef("ping", "Simple ping-pong tool for testing plugin system connectivity."),
		ExecFn: func(ctx context.Context, input string) (*plugin.ToolResult, error) {
			p.callCount.Add(1)
			latency := time.Since(p.startTime)
			return plugin.NewToolResult(fmt.Sprintf("pong! (uptime: %s, calls: %d)", latency.Round(time.Second), p.callCount.Load())), nil
		},
	}); err != nil {
		return fmt.Errorf("register ping tool: %w", err)
	}

	// Capture pctx for use in the hook closure.
	// The hook closure receives context.Context (not plugin.PluginContext),
	// so we need the outer variable to access Storage().
	pluginCtx := pctx

	// Register a PostToolUse hook that logs all tool calls
	if err := pctx.OnPostToolUse("", func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
		// Log every tool call to plugin storage
		storage := pluginCtx.Storage()
		count, _ := storage.Get("tool_call_count")
		if count == "" {
			count = "0"
		}
		n, err := strconv.ParseInt(count, 10, 64)
		if err != nil {
			n = 0
		}
		_ = storage.Set("tool_call_count", strconv.FormatInt(n+1, 10))
		return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
	}); err != nil {
		return fmt.Errorf("register hook: %w", err)
	}

	// Register a context enricher
	if err := pctx.EnrichContext("hello_status", func(ctx context.Context) (string, error) {
		latency := time.Since(p.startTime)
		return fmt.Sprintf("Hello World plugin active (uptime: %s, tool calls served: %d)",
			latency.Round(time.Second), p.callCount.Load()), nil
	}); err != nil {
		return fmt.Errorf("register enricher: %w", err)
	}

	return nil
}

// Deactivate cleans up plugin resources.
func (p *HelloWorldPlugin) Deactivate(ctx plugin.PluginContext) error {
	ctx.Logger().Info("Goodbye from Hello World plugin!")
	return nil
}
