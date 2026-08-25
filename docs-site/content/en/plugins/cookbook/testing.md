---
title: "Testing"
weight: 20
---

Test plugins in isolation with the built-in `TestKit` and mocks — no running xbot instance required. The helpers live in `plugin/testkit.go` and `plugin/mock.go`; the real examples ship tests: `plugins/xbot-genui/main_test.go`, `plugins/xbot-git-fancy/main_test.go`.

## TestKit — the full-context harness

`TestKit` provides a complete in-memory `PluginContext` (map storage, test logger, registries):

```go
func TestMyPlugin(t *testing.T) {
	tk := plugin.NewTestKit(t)
	defer tk.Clear()

	p := NewMyPlugin()
	if err := tk.Activate(p); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Assert declared capabilities were actually registered
	tk.AssertToolRegistered("hello")
	tk.AssertHookRegistered(plugin.HookPostToolUse)
	tk.AssertEnricherRegistered("hello_status")

	// Call a tool and inspect the result
	result, err := tk.CallTool("hello", `{"name":"Alice"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(result.Content, "Hello, Alice") {
		t.Errorf("unexpected result: %s", result.Content)
	}
}
```

Other members: `tk.Context` (the PluginContext), `tk.Deactivate(p)`, `tk.Debug`/`tk.Debugf` (write into the captured log). The test logger records everything and formats structured fields.

## MockPlugin / MockTool — compose test doubles

`plugin/mock.go` — chainable builders (each `With*` mutates and returns the same pointer):

```go
mp := plugin.NewMockPlugin("xbot.mock").
	WithManifest(func(m *plugin.PluginManifest) {
		m.Name = "Mock"
	}).
	WithActivate(func(ctx plugin.PluginContext) error { return nil }).
	WithDeactivate(func(ctx plugin.PluginContext) error { return nil })

mt := plugin.NewMockTool("mock_tool").
	WithDefinition(func(d *plugin.ToolDef) { d.Description = "..." }).
	WithExecute(func(ctx context.Context, input string) (*plugin.ToolResult, error) {
		return plugin.NewToolResult("mocked"), nil
	})
```

⚠️ **Do not share a single mock across parallel tests** — clone it per test (the chain API mutates in place).

## What to test — the checklist

1. **Manifest validity** — ID format, permission strings, version semver:
   ```go
   if !plugin.IsValidPermission("tools.register") { t.Fatal(...) }
   ```
   The git-fancy pattern (`plugins/xbot-git-fancy/main_test.go TestManifestPermissions`) reads `plugin.json` from disk and asserts every declared permission is known — catches the "backend whitelist drifted" failure mode.

2. **Activation idempotency** — call `Activate` twice; the second must succeed or cleanly no-op.

3. **Tool contract** — every declared tool name exists, parses its input, returns structured output. Test both happy paths and malformed input (missing params → graceful default or error result).

4. **Hook decisions** — `PreToolUse` denial blocks; `PostToolUse` observes with correct payload fields.

5. **Storage round-trip** — `Set` → `Get` → restart (new storage from same dir) → `Get`.

6. **Deactivation** — resources released; calling `Deactivate` twice is safe.

## Stdio plugin tests

For stdio backends, test the handlers directly (the git-fancy pattern):

```go
func TestGitStatus(t *testing.T) {
	// call handleWebPluginRPC / gitStatus with a temp git repo
	dir := t.TempDir()
	runGit(t, dir, "init")
	result := gitStatus(dir)
	if !result.is_repo { t.Fatal("expected repo") }
}
```

Plus one **protocol-level test** that feeds JSON lines into the process and checks responses (spawn the binary with `protocol.Run` against an in-memory reader/writer — `protocol.run` accepts injected `io.Reader`/`io.Writer`).

## Integration patterns

- `plugin/integration.go` wires plugins into a full agent for end-to-end tests — `WireAll` connects tools/hooks/enrichers to the registry; individual `Wire*` functions allow partial wiring.
- For channel plugins, test `handleActivate`'s declaration JSON, `handleExecuteTool` for each tool, and `handle_xbot_event` routing with synthetic `channel_config` messages (mirror `echo-channel/main.py`'s handlers).
- Rate limiter and quota manager (`plugin/ratelimit.go`) have their own test hooks (`SetRetryInterval`, etc.) — use them to keep tests fast.

## Frontend plugin tests (vitest)

`web/src/plugin-api/types.test.ts` uses `@ts-expect-error` compile-time assertions to pin the type contracts. For view components:

- mock `usePluginRuntime` with a **stable reference** (a fresh object per render hangs the worker — `vi.hoisted` pattern).
- inject `window.React` **before dynamic import** of the plugin module (static imports are hoisted above injection — see the git-fancy index test).
- assert hook-count stability across loading → loaded transitions (the React #310 regression: hooks after conditional early returns).
