# Plan: OpenAI Responses API Support

## Summary

为 xbot 增加 OpenAI Responses API (`POST /v1/responses`) 作为 Chat Completions 之外的可选路径，通过 subscription 级 `api_type` 字段切换。默认 `chat_completions` 保持向后兼容；设为 `responses` 时走新路径。功能与 Chat Completions 对等（text + function calling + streaming + reasoning 适配）。

## Changes

### 1. `llm/openai.go` — 加字段 + 路由分发

- **What**: `OpenAIConfig` 加 `APIType string`；`OpenAILLM` 加 `apiType string`；`NewOpenAILLM` 存储 `apiType`；`Generate()`/`GenerateStream()` 开头加 `if o.apiType == "responses"` 分发到新方法。
- **Why**: 路由决策在客户端构造时确定（subscription 级），运行时每次调用按 `apiType` 分发。保持现有 Chat Completions 路径完全不变。

### 2. `llm/openai_responses.go` — NEW: Responses API 实现

核心文件，包含以下函数：

**消息转换**（`ChatMessage[]` → `ResponseNewParams`）:
- `toResponsesParams(messages, thinkingMode)` → `openai.ResponseNewParams`
- `toResponsesInput(messages)` → `[]openai.ResponseInputItemUnionParam`
  - `role: system` → 拼接为 `Instructions` 字段（不进 Input）
  - `role: user, string` → `EasyInputMessageParam{Role:"user"}`
  - `role: user, multimodal` → `ResponseInputItemMessageParam` with content parts
  - `role: assistant, text only` → `EasyInputMessageParam{Role:"assistant"}`
  - `role: assistant, tool_calls` → 每个 tool call 一个 `ResponseFunctionToolCallParam`
  - `role: tool` → `ResponseInputItemFunctionCallOutputParam{CallID: toolCallID, Output: content}`

**工具转换**:
- `toResponsesTools(tools)` → `[]openai.ToolUnionParam`，每个映射为 `FunctionToolParam{Name, Parameters, Description, Strict: false}`
- 注意：Responses API 的 `Strict` 默认 true，需显式设 false 保持兼容

**reasoning 适配**:
- `thinkingMode == "enabled"` → `Reasoning{Effort: "medium", Summary: "auto"}`
- `thinkingMode == "disabled"` → `Reasoning{Effort: "none"}`
- 自定义 JSON → 解析后映射到 `ReasoningParam`

**非流式**:
- `generateResponses(ctx, model, messages, tools, thinkingMode)` → `*LLMResponse`
  - 调 `o.client.Responses.New(ctx, params, opts...)`
  - 遍历 `resp.Output[]`：`type=="message"` 提取 text；`type=="function_call"` 提取 ToolCall（CallID→ID）；`type=="reasoning"` 提取 summary → ReasoningContent
  - Usage: `InputTokens→PromptTokens`, `OutputTokens→CompletionTokens`, `CachedTokens→CacheHitTokens`
  - Status → FinishReason 映射

**流式**:
- `generateStreamResponses(ctx, model, messages, tools, thinkingMode)` → `<-chan StreamEvent`
- 处理 `ResponseStreamEventUnion`:
  - `response.output_text.delta` → EventContent
  - `response.reasoning_summary_text.delta` / `response.reasoning_text.delta` → EventReasoningContent
  - `response.output_item.added` (type==function_call) → EventToolCall（ID + Name）
  - `response.function_call_arguments.delta` → EventToolCall（Arguments delta）
  - `response.function_call_arguments.done` → EventToolCall（完整 Arguments + Name + CallID）
  - `response.completed` → EventUsage + EventDone
  - `error` → EventError

### 3. `llm/openai_responses_test.go` — NEW: 测试

- 消息转换测试：各 role → Responses Input 映射
- 工具转换测试：ToolDefinition → FunctionToolParam
- reasoning 映射测试：thinkingMode → ReasoningParam
- 响应解析测试：mock Response → LLMResponse（text + tool_calls + reasoning + usage）
- 流式事件测试：模拟事件序列 → StreamEvent 序列

### 4. `protocol/events.go` — Subscription 加字段

- `Subscription` struct 加 `APIType string \`json:"api_type,omitempty"\``
- 影响范围：config.json 订阅配置 + RPC 传输 + DB struct

### 5. `storage/sqlite/user_llm_subscription.go` — DB 层

- `LLMSubscription` struct 加 `APIType string`
- `scanSubscription()` 的 `Scan()` 加 `&sub.APIType`
- 所有 SELECT 语句加 `api_type` 列
- INSERT 语句加 `api_type` 列 + 占位符
- UPDATE 语句加 `api_type = ?`

### 6. `storage/sqlite/user_llm_config.go` — DB 传输层

- `UserLLMConfig` struct 加 `APIType string`
- SELECT/INSERT/UPDATE 加 `api_type` 列

### 7. `storage/sqlite/migrations.go` — v36 migration

- `const schemaVersion = 36`
- 新增 `migrateV35ToV36`: `ALTER TABLE user_llm_subscriptions ADD COLUMN api_type TEXT DEFAULT ''`
- 注册到 `migrateSchema` 的 migration list

### 8. `agent/llm_factory.go` — 工厂层透传

- `createClient()`: `OpenAIConfig{...}` 加 `APIType: cfg.APIType`
- `createEntryFromSub()`: `UserLLMConfig{...}` 加 `APIType: sub.APIType`
- `createClientFromSub()`: `UserLLMConfig{...}` 加 `APIType: sub.APIType`（同时补全现有 ThinkingMode 遗漏）

### 9. `serverapp/rpc_table.go` — RPC handler

- `updateSubscription` / `setDefaultSubscription` 等 handler 中 subscription 字段映射加 `api_type`（overlay 模式，非空才覆盖）

## Risks

- **SQL 列顺序不匹配**: scanSubscription 的 Scan 参数必须与 SELECT 列顺序严格对应。→ 逐条核对所有 SQL 语句。
- **Responses API system message 处理**: Responses API 用 `Instructions` 字段而非 Input 中的 system role 消息。多个 system 消息需拼接。→ 在 `toResponsesParams` 中特殊处理。
- **CallID vs tool_call_id 映射**: Responses API 用 `CallID` 关联 function call 和 output，xbot 的 `ToolCall.ID` / `ChatMessage.ToolCallID` 需要正确映射。→ 双向转换在转换函数中集中处理。
- **MaxOutputTokens 语义差异**: Responses API 的 `max_output_tokens` 包含 reasoning tokens，Chat Completions 的 `max_tokens` 不含。→ 初期不做特殊调整，直接传同一个值，用户可能需要调大配置值。
- **Strict mode**: Responses API FunctionToolParam 默认 `Strict: true`，会强制 JSON Schema 严格匹配。→ 显式设 `false` 保持与 Chat Completions 一致。

## Definition of Done

- [ ] `go build ./...` 通过
- [ ] `go test ./llm/...` 通过（含新测试）
- [ ] `go test ./...` 全部通过
- [ ] 配置 `api_type: "responses"` 后，text 对话正常工作
- [ ] 配置 `api_type: "responses"` 后，function calling 正常工作（调用+回传）
- [ ] 配置 `api_type: "responses"` 后，streaming 正常工作
- [ ] 配置 `api_type: "responses"` 后，thinkingMode → reasoning 映射正常
- [ ] 不配置 `api_type`（默认 chat_completions）时，行为完全不变

## Open Questions

（已全部确认）
1. reasoning 兼容映射 → 做（`type=="reasoning"` 的 summary → ReasoningContent）
2. CLI 面板不需要暴露
3. 第三方普遍支持，无需 fallback
