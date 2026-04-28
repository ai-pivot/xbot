# Plugin System Design

## Overview

xbot 插件系统提供类似 VSCode 的可扩展性，允许第三方开发者通过统一的 Plugin API 扩展 xbot 的行为。

**设计哲学**: 统一 Plugin API + 多运行时支持。插件开发者只关心一套接口，运行时（native/gRPC/WASM）是透明的实现细节。

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                  Plugin API (Go Interface)                │
│  Plugin · PluginManifest · PluginContext                  │
├──────────────┬───────────────────┬───────────────────────┤
│   Native     │     gRPC          │      WASM (Phase 2)   │
│  (in-process)│ (external process)│  (wazero sandbox)     │
├──────────────┴───────────────────┴───────────────────────┤
│                 PluginManager (lifecycle)                  │
├───────────────────────────────────────────────────────────┤
│  Integration Layer: Tool Registry · Hooks · Middleware     │
└───────────────────────────────────────────────────────────┘
```

## Core Concepts

### 1. Plugin Manifest (`plugin.json`)

每个插件目录下的声明式配置文件，描述插件的元信息、能力贡献、激活条件和权限需求。

```json
{
  "id": "com.example.code-reviewer",
  "name": "Code Reviewer",
  "version": "1.0.0",
  "description": "AI-powered code review tool",
  "author": "example.com",
  "runtime": "native",
  "entry": "main.go",
  "activationEvents": ["onTool:code_review", "onStart"],
  "permissions": ["tools.register", "hooks.subscribe", "storage.private", "network.outbound"],
  "contributes": {
    "tools": [
      {
        "name": "code_review",
        "description": "Review code changes and suggest improvements",
        "inputSchema": { ... }
      }
    ],
    "hooks": [
      { "event": "PostToolUse", "matcher": "Shell(*git commit*)" }
    ],
    "contextEnrichers": [
      { "name": "git_status", "description": "Inject current git status" }
    ]
  }
}
```

### 2. Plugin Interface

```go
type Plugin interface {
    // Manifest returns plugin metadata. Called once during discovery.
    Manifest() PluginManifest

    // Activate initializes the plugin and registers capabilities.
    // Called when an activation event fires.
    Activate(ctx PluginContext) error

    // Deactivate cleans up plugin resources. Called on shutdown.
    Deactivate(ctx PluginContext) error
}
```

### 3. PluginContext Interface

受限的能力接口，不暴露 ToolContext 原始字段：

```go
type PluginContext interface {
    // Tool registration
    RegisterTool(tool PluginTool) error

    // Hook subscription
    OnPreToolUse(matcher string, handler HookHandler) error
    OnPostToolUse(matcher string, handler HookHandler) error
    OnUserPrompt(handler HookHandler) error
    OnAgentStop(handler HookHandler) error

    // Context enrichment (upgrade Skills to executable)
    EnrichContext(name string, enricher ContextEnricher) error

    // Isolated storage (per-plugin namespace)
    Storage() StorageAccessor

    // Logging
    Logger() Logger

    // Metadata
    PluginID() string
    WorkingDir() string
    Channel() string
    ChatID() string
}
```

### 4. Runtime Types

| Runtime | Isolation | Latency | Language Support | Status |
|---------|-----------|---------|-----------------|--------|
| native  | Interface boundary | ~μs (zero-copy) | Go only | Phase 1 |
| gRPC    | Process isolation | ~1-5ms | Any (via protobuf) | Phase 1 |
| wasm    | Sandbox isolation | ~0.5-2ms | WASM-targeting languages | Phase 2 |

### 5. Plugin Lifecycle

```
Discovery → Load Manifest → [Wait for Activation Event]
  → Activate() → Register capabilities → [Running]
  → [Deactivation Event] → Deactivate() → Cleanup
```

Activation Events:
- `onStart` — xbot 启动时立即激活
- `onTool:<name>` — 首次调用指定工具时激活
- `onHook:<event>` — 首次触发指定钩子事件时激活
- `onCommand:<cmd>` — 用户输入指定命令时激活

### 6. Permission System

| Permission | Description |
|-----------|-------------|
| `tools.register` | 注册新工具 |
| `tools.call` | 调用其他工具 |
| `hooks.subscribe` | 订阅生命周期钩子 |
| `context.enrich` | 注入系统提示内容 |
| `storage.private` | 插件私有 KV 存储 |
| `storage.shared` | 跨插件共享存储 |
| `network.outbound` | 发起网络请求 |
| `bus.read` | 读取消息总线 |
| `bus.write` | 写入消息总线 |

### 7. Integration with Existing Systems

**Tool Registration**: PluginTool → adapter → tools.Tool → Registry.Register()

**Hook Subscription**: Plugin HookHandler → adapter → hooks.CallbackHook → Manager.RegisterBuiltin()

**Context Enrichment**: ContextEnricher → adapter → MessageMiddleware → Pipeline.Use()

**Storage**: ~/.xbot/plugins/<id>/storage.db (per-plugin isolated SQLite)

## File Structure

```
plugin/
├── plugin.go           # Plugin interface, PluginManifest, PluginTool
├── context.go          # PluginContext interface + implementations
├── manager.go          # PluginManager (discovery, lifecycle, routing)
├── manifest.go         # Manifest parsing and validation
├── permissions.go      # Permission checker
├── storage.go          # Per-plugin KV storage
├── runtime_native.go   # Native (in-process) runtime
├── runtime_grpc.go     # gRPC external process runtime
├── adapter_tool.go     # PluginTool → tools.Tool adapter
├── adapter_hook.go     # HookHandler → hooks.CallbackHook adapter
├── adapter_middleware.go # ContextEnricher → MessageMiddleware adapter
├── plugin.proto        # gRPC service definition (for remote plugins)
└── plugin_test.go      # Tests
```

## Design Decisions

### Why not pure WASM?

Roundtable 结论（5/5 专家同意）：WASM 不适合作为 V1 主运行时。

1. **调试工具链不成熟**：无法 attach debugger，crash 只能拿到 trap，无 coredump
2. **ToolContext 序列化复杂**：45+ 字段包含 6 个 callback 字段无法跨边界传递
3. **开发者体验差**：TS 开发者需要学习 WASM 工具链，无法使用 npm 生态
4. **Go + WASM 生态不成熟**：WASI 支持不完整，wazero 尚未大规模验证

WASM 作为 Phase 2 引入，用于高频轻量 hook 的沙箱场景。

### Why unified Plugin API first?

Platform Engineer 的论点被全员接受：先统一内部扩展点（Tool + Hook + Middleware + Skill），再在 API 下方替换运行时。这确保：
- 插件开发者只学一套 API
- 运行时切换对插件透明
- 现有 Go 工具可渐进迁移到 Plugin 接口

### Why PluginContext instead of ToolContext?

ToolContext 有 45+ 字段，包含 SendFunc、InjectInbound、Registry 等高权限成员。直接暴露给第三方插件等于裸奔。PluginContext 是按权限过滤的安全子集。

## Future (Phase 2)

1. WASM runtime via wazero (lightweight sandbox for trusted env)
2. TypeScript SDK (via protobuf-generated client)
3. Python SDK
4. Plugin Marketplace (registry + install command)
