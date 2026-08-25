---
title: "PluginTool API"
weight: 4
---

插件工具接口参考（`plugin/plugin.go`），覆盖工具定义、执行与结果构造。

## 接口

### PluginTool

插件提供工具的插件侧接口：

```go
type PluginTool interface {
    // Definition 返回工具定义（JSON schema），供 LLM 消费。
    Definition() ToolDef

    // Execute 以给定输入运行工具并返回结果。
    // input 是符合工具输入 schema 的 JSON 字符串。
    Execute(ctx context.Context, input string) (*ToolResult, error)
}
```

### PluginToolV2

扩展接口，接收富调用上下文而非裸 `context.Context`。`PluginToolBridge` 优先检查 V2，回退到 V1。

```go
type PluginToolV2 interface {
    PluginTool
    // ExecuteWithContext 以富调用上下文运行工具。
    ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error)
}
```

### ToolCallContext

```go
type ToolCallContext struct {
    SessionID string          // 当前会话
    Channel   string          // 消息渠道（"cli"、"feishu"、"web" 等）
    ChatID    string          // 渠道内的会话 ID
    UserID    string          // 触发工具调用的用户
    TenantID  int64           // 多租户租户 ID
    Ctx       context.Context // 取消与截止时间信息
}
```

## ToolDef

```go
type ToolDef struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  []llm.ToolParam `json:"parameters"`
    Version     string          `json:"version,omitempty"`     // semver；设置后包含在 ToJSONSchema() 输出中
    InputSchema map[string]any  `json:"input_schema,omitempty"` // 自动生成的 JSON Schema；手动构造 ToolDef 时为 nil
}
```

### ToJSONSchema()

以 OpenAI function calling 格式返回工具定义：

```json
{
  "type": "function",
  "function": {
    "name": "...",
    "description": "...",
    "parameters": { "type": "object", "properties": {...}, "required": [...] },
    "version": "..."
  }
}
```

若 `InputSchema` 已填充（如来自 `BuildToolDef`），直接作为 parameters 值使用；否则从 `Parameters` 切片重建（注意：回退路径尚不支持 `ToolParam.Items` 嵌套结构）。

## ToolResult

```go
type ToolResult struct {
    Content  string            `json:"content"`              // 返回给 LLM 的主输出
    IsError  bool              `json:"is_error,omitempty"`   // 工具执行失败（但插件本身正常运行）
    Metadata map[string]string `json:"metadata,omitempty"`   // 下游处理的可选键值对
}
```

构造函数：

```go
func NewToolResult(content string) *ToolResult // 成功
func NewToolError(content string) *ToolResult  // 错误结果
```

## ToolResultBuilder

构建 `ToolResult` 的流式 API：

```go
result := NewResultBuilder().
    Content("hello").
    Metadata("format", "json").
    Build()
```

| 方法 | 签名 | 说明 |
|------|------|------|
| `NewResultBuilder` | `func NewResultBuilder() *ToolResultBuilder` | 创建带默认值的构建器。 |
| `Content` | `func (b *ToolResultBuilder) Content(content string) *ToolResultBuilder` | 设置主输出内容。 |
| `Error` | `func (b *ToolResultBuilder) Error(content string) *ToolResultBuilder` | 设置内容并标记为错误。 |
| `IsError` | `func (b *ToolResultBuilder) IsError(isError bool) *ToolResultBuilder` | 显式设置错误标记。 |
| `Metadata` | `func (b *ToolResultBuilder) Metadata(key, value string) *ToolResultBuilder` | 添加键值对（惰性初始化 map）。 |
| `Build` | `func (b *ToolResultBuilder) Build() *ToolResult` | 返回构造完成的结果。 |

## SDK 格式化助手

来自 `plugin/sdk.go`：

| 助手 | 签名 | 输出 |
|------|------|------|
| `FormatToolResult` | `func FormatToolResult(title string, sections map[string]string) *ToolResult` | `"title\nkey: value\nkey2: value2"` — key 排序保证确定性输出；sections 为空 → 仅标题。 |
| `FormatListResult` | `func FormatListResult(items []string) *ToolResult` | 编号列表 `"1. alpha\n2. beta"`；空/nil → `"(no items)"`。 |
| `FormatErrorResult` | `func FormatErrorResult(operation string, err error) *ToolResult` | `"<operation> failed: <msg>"` 且 `IsError: true`；err 为 nil → `"unknown error"`。 |

## 快捷工具工厂

来自 `plugin/sdk.go`：

```go
// 纯字符串输入 / 字符串输出。
func ToolFromFunc(name, desc string, fn func(ctx context.Context, input string) (string, error)) PluginTool

// JSON 输入 / 结构化输出（自动序列化为 JSON）。
func ToolFromJSONFunc(name, desc string, params []ToolParamDef,
    fn func(ctx context.Context, input json.RawMessage) (any, error)) PluginTool
```

`ToolFromJSONFunc` 使用 `BuildToolDef` 自动生成 JSON Schema。
