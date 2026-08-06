# 计划：Config Tool 增强 — 支持完整的 LLM/订阅/模型管理

> 生成时间：2026-08-06
> 状态：待确认

## 背景与目标

config 工具是 AI agent 操作 xbot 配置的核心入口。当前它支持通用设置读写 (get/set)、订阅列表 (subscriptions)、runner CRUD、插件/hooks 重载。但缺少三项关键能力：

1. **切换当前会话的 LLM 模型** — 当前只能改默认订阅的 model（影响所有会话），无法做 per-session 切换
2. **设置 max context** — `max_context_tokens` 已从 `AllSettingDefs` 移除（改为 per-model），config 工具无法触及
3. **添加/删除/修改订阅** — 仅有列表，没有 CRUD

**目标**：让 config 工具成为 AI 管理一切用户可改设置的统一入口，包括模型切换、per-model max context、订阅 CRUD。

## 现状分析

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `tools/config_tool.go` | config 工具实现 | **修改**：新增 model/subscription action |
| `tools/interface.go` | ToolContext 结构体定义 | **修改**：新增 LLM 管理闭包字段 |
| `agent/engine.go` | buildToolContext 函数 | **修改**：从 UserContext 注入新闭包 |
| `tools/embed_skills/ai-config/SKILL.md` | AI 配置指南 | **修改**：文档更新 |
| `agent/user_context.go` | UserContext 结构体 | **只读参考**：已有 SelectModel/ResolveActiveSub/SubSvc |
| `agent/llm_factory.go` | LLM 工厂 | **只读参考**：已有 ListAllModelEntriesForUser 等方法 |
| `storage/sqlite/user_llm_subscription.go` | 订阅 DB 服务 | **只读参考**：已有 Add/Remove/Update/UpsertModel 等 |
| `channel/setting_keys.go` | 设置定义注册表 | **只读参考**：理解 AllSettingDefs 结构 |

### 现有后端 API（已实现，只需暴露到 config 工具）

**UserContext 闭包**（agent loop 内可用）：
- `uc.SelectModel(chatID, subID, model)` — per-session 模型切换
- `uc.ResolveActiveSub(chatID)` — 获取当前会话的 subscription + model
- `uc.RefreshModels()` — 从 provider 拉取最新模型列表
- `uc.SubSvc` — `*sqlite.LLMSubscriptionService`，支持 Add/Remove/Update/Rename/SetDefault/UpsertModel/RemoveModel/SetSubscriptionEnabled/SetModelEnabled
- `uc.InvalidateLLM()` — 使 LLM 缓存失效

**LLMFactory 方法**（需通过 UserContext 的 factoryRef 或新闭包暴露）：
- `ListAllModelEntriesForUser(senderID)` → `[]protocol.ModelEntry{SubID, SubName, Model, Status}`
- `InvalidateSubscription(subID)` — 使特定订阅的客户端缓存失效

**已有的 RPC handler**（rpc_table.go，供参考但 config 工具不走 RPC）：
- `select_model` / `set_default_model` / `set_model_enabled` / `remove_model` / `upsert_model`
- `add_subscription` / `update_subscription` / `remove_subscription` / `set_default_subscription` / `set_subscription_enabled` / `rename_subscription`
- `update_per_model_config`

### 架构约束（必须遵守）

1. **config 工具在 agent loop 内运行**，通过 `ToolContext` 闭包访问用户系统，不能直接访问 `LLMFactory`/`SettingsSvc`
2. **`max_context_tokens` 是 per-model 的**（存储在 `subscription_models` 表），不在 `AllSettingDefs` 中，必须通过 `SubSvc.UpsertModel(subID, model, maxCtx, ...)` 或 `SubSvc.Update` + per-model config 修改
3. **不能用 `UpdateSubscription` 改 per-model config**（会覆盖 credentials）— 用 `UpsertModel` 或专门的 per-model 方法
4. **修改后必须 InvalidateLLM/InvalidateSubscription** 使缓存失效
5. **订阅切换是 per-session 的**（写入 tenants 表），不影响其他会话

### 风险点

- **credentials 覆盖**：更新订阅字段时必须 Get→修改→Update（读 DB 真实值，不是 API 返回的 masked 值）
- **缓存失效**：所有 LLM 相关修改后必须 invalidate，否则下次 ResolveLLM 用旧缓存
- **per-session vs user-level**：`SelectModel(chatID, subID, model)` 是 per-session；`SetUserDefaultModel` 是 user-level。config 工具需明确区分
- **向后兼容**：现有 `subscriptions` action 必须保留

## 详细计划

### 阶段一：扩展 ToolContext — 新增 LLM 管理闭包

**涉及文件：`tools/interface.go`**

在 `ToolContext` 结构体中新增以下闭包字段（紧跟现有 `ListSubscriptions` 之后）：

```go
// ── LLM Model Management (for config tool) ──

// SelectModel switches the current session's model (per-session, not user-level).
// Uses SelectModel(chatID, subID, model) from UserContext.
SelectModelFn func(subID, model string) error

// ListModels returns all available models across all subscriptions.
// Each entry includes sub_id, sub_name, model name, and status (normal/offline/disabled).
ListModelsFn func() []ModelInfo

// RefreshModels live-fetches /models from all enabled subscriptions and returns fresh entries.
RefreshModelsFn func() []ModelInfo

// GetActiveModel returns the current session's (subID, model).
GetActiveModelFn func() (subID, model string, err error)

// ── Per-Model Config (for config tool) ──

// SetModelContext sets max_context for a specific (subID, model) pair.
SetModelContextFn func(subID, model string, maxContext int) error

// SetModelOutput sets max_output_tokens for a specific (subID, model) pair.
SetModelOutputFn func(subID, model string, maxOutput int) error

// SetModelEnabled enables/disables a specific model.
SetModelEnabledFn func(subID, model string, enabled bool) error

// UpsertModel registers a new model or updates its per-model config.
UpsertModelFn func(subID, model string, maxContext, maxOutput int, apiType string) error

// RemoveModel permanently deletes a model from subscription_models.
RemoveModelFn func(subID, model string) error

// ── Subscription CRUD (for config tool) ──

// AddSubscription creates a new LLM subscription.
AddSubscriptionFn func(params SubscriptionCreateParams) (string, error)

// RemoveSubscription deletes a subscription by ID.
RemoveSubscriptionFn func(subID string) error

// UpdateSubscriptionFields updates specific fields on an existing subscription.
// Only non-empty fields are updated; credentials are preserved (Get→modify→Update).
UpdateSubscriptionFieldsFn func(subID string, params SubscriptionUpdateParams) error

// SetDefaultSubscription sets the user-level default subscription.
SetDefaultSubscriptionFn func(subID string) error

// SetSubscriptionEnabled enables/disables a subscription.
SetSubscriptionEnabledFn func(subID string, enabled bool) error

// RenameSubscription renames a subscription.
RenameSubscriptionFn func(subID, name string) error
```

新增辅助类型：

```go
// ModelInfo describes a selectable model for the config tool.
type ModelInfo struct {
    SubID   string `json:"sub_id"`
    SubName string `json:"sub_name"`
    Model   string `json:"model"`
    Status  string `json:"status"` // normal | offline | disabled
}

// SubscriptionCreateParams holds fields for creating a new subscription.
type SubscriptionCreateParams struct {
    Name            string `json:"name"`
    Provider        string `json:"provider"`
    BaseURL         string `json:"base_url"`
    APIKey          string `json:"api_key"`
    Model           string `json:"model"`
    MaxOutputTokens int    `json:"max_output_tokens"`
    IsDefault       bool   `json:"is_default"`
}

// SubscriptionUpdateParams holds fields for updating a subscription.
// Only non-empty/non-zero fields are updated.
type SubscriptionUpdateParams struct {
    Name            string `json:"name"`
    Provider        string `json:"provider"`
    BaseURL         string `json:"base_url"`
    APIKey          string `json:"api_key"`
    Model           string `json:"model"`
    MaxOutputTokens int    `json:"max_output_tokens"`
}
```

### 阶段二：在 buildToolContext 注入闭包

**涉及文件：`agent/engine.go`（`buildToolContext` 函数，约 line 1235-1340）**

在现有 `tc.ListSubscriptions` 注入之后，新增闭包注入：

```go
// ── LLM Model Management ──
if uc != nil {
    // SelectModel: per-session model switch
    tc.SelectModelFn = func(subID, model string) error {
        return uc.SelectModel(cfg.ChatID, subID, model)
    }

    // GetActiveModel: current session's model
    tc.GetActiveModelFn = func() (string, string, error) {
        sub, model, err := uc.ResolveActiveSub(cfg.ChatID)
        if err != nil || sub == nil {
            return "", "", fmt.Errorf("no active subscription: %w", err)
        }
        return sub.ID, model, nil
    }

    // ListModels: all models for this user
    tc.ListModelsFn = func() []tools.ModelInfo {
        entries := a.userSys.llmFactory.ListAllModelEntriesForUser(cfg.OriginUserID)
        result := make([]tools.ModelInfo, 0, len(entries))
        for _, e := range entries {
            result = append(result, tools.ModelInfo{
                SubID: e.SubID, SubName: e.SubName,
                Model: e.Model, Status: e.Status,
            })
        }
        return result
    }

    // RefreshModels: live-fetch from providers
    tc.RefreshModelsFn = func() []tools.ModelInfo {
        entries, _ := uc.RefreshModels()
        result := make([]tools.ModelInfo, 0, len(entries))
        for _, e := range entries {
            result = append(result, tools.ModelInfo{
                SubID: e.SubID, SubName: e.SubName,
                Model: e.Model, Status: e.Status,
            })
        }
        return result
    }

    // ── Per-Model Config ──
    if uc.SubSvc != nil {
        svc := uc.SubSvc

        tc.SetModelContextFn = func(subID, model string, maxContext int) error {
            // UpsertModel updates the subscription_models table
            if err := svc.UpsertModel(subID, model, maxContext, 0, "", ""); err != nil {
                return err
            }
            uc.InvalidateLLM()
            a.userSys.llmFactory.InvalidateSubscription(subID)
            return nil
        }

        tc.SetModelOutputFn = func(subID, model string, maxOutput int) error {
            if err := svc.UpsertModel(subID, model, 0, maxOutput, "", ""); err != nil {
                return err
            }
            uc.InvalidateLLM()
            a.userSys.llmFactory.InvalidateSubscription(subID)
            return nil
        }

        tc.SetModelEnabledFn = func(subID, model string, enabled bool) error {
            if err := svc.SetModelEnabled(subID, model, enabled); err != nil {
                return err
            }
            uc.InvalidateLLM()
            a.userSys.llmFactory.InvalidateSubscription(subID)
            return nil
        }

        tc.UpsertModelFn = func(subID, model string, maxContext, maxOutput int, apiType string) error {
            if err := svc.UpsertModel(subID, model, maxContext, maxOutput, "", apiType); err != nil {
                return err
            }
            uc.InvalidateLLM()
            a.userSys.llmFactory.InvalidateSubscription(subID)
            return nil
        }

        tc.RemoveModelFn = func(subID, model string) error {
            if err := svc.RemoveModel(subID, model); err != nil {
                return err
            }
            uc.InvalidateLLM()
            a.userSys.llmFactory.InvalidateSubscription(subID)
            return nil
        }

        // ── Subscription CRUD ──
        tc.AddSubscriptionFn = func(params tools.SubscriptionCreateParams) (string, error) {
            sub := &sqlite.LLMSubscription{
                Name:            params.Name,
                Provider:        params.Provider,
                BaseURL:         params.BaseURL,
                APIKey:          params.APIKey,
                Model:           params.Model,
                MaxOutputTokens: params.MaxOutputTokens,
                SenderID:        cfg.OriginUserID,
                IsDefault:       params.IsDefault,
            }
            if err := svc.Add(sub); err != nil {
                return "", err
            }
            uc.InvalidateLLM()
            return sub.ID, nil
        }

        tc.RemoveSubscriptionFn = func(subID string) error {
            if err := svc.Remove(subID); err != nil {
                return err
            }
            uc.InvalidateLLM()
            a.userSys.llmFactory.InvalidateSubscription(subID)
            return nil
        }

        tc.UpdateSubscriptionFieldsFn = func(subID string, params tools.SubscriptionUpdateParams) error {
            // Get→modify→Update pattern preserves credentials (reads real DB values)
            sub, err := svc.Get(subID)
            if err != nil {
                return fmt.Errorf("subscription not found: %w", err)
            }
            if params.Name != "" { sub.Name = params.Name }
            if params.Provider != "" { sub.Provider = params.Provider }
            if params.BaseURL != "" { sub.BaseURL = params.BaseURL }
            if params.APIKey != "" { sub.APIKey = params.APIKey }
            if params.Model != "" { sub.Model = params.Model }
            if params.MaxOutputTokens > 0 { sub.MaxOutputTokens = params.MaxOutputTokens }
            if err := svc.Update(sub); err != nil {
                return err
            }
            uc.InvalidateLLM()
            a.userSys.llmFactory.InvalidateSubscription(subID)
            return nil
        }

        tc.SetDefaultSubscriptionFn = func(subID string) error {
            if err := svc.SetDefault(subID); err != nil {
                return err
            }
            uc.InvalidateLLM()
            return nil
        }

        tc.SetSubscriptionEnabledFn = func(subID string, enabled bool) error {
            if err := svc.SetSubscriptionEnabled(subID, enabled); err != nil {
                return err
            }
            uc.InvalidateLLM()
            a.userSys.llmFactory.InvalidateSubscription(subID)
            return nil
        }

        tc.RenameSubscriptionFn = func(subID, name string) error {
            if err := svc.Rename(subID, name); err != nil {
                return err
            }
            uc.InvalidateLLM()
            return nil
        }
    }
}
```

**注意**：`a.userSys.llmFactory` 在 engine.go 的 `buildToolContext` 中需要通过 agent 指针访问。检查 `buildToolContext` 是否有 `a` 的引用 — 它是 `(*Agent)` 的方法，所以 `a.userSys.llmFactory` 可用。但按 AGENTS.md 约束，agent loop 代码不应直接访问 `a.llmFactory`。需要通过 `uc` 的 factoryRef 间接调用 `InvalidateSubscription`。

**修正**：`InvalidateSubscription` 不在 `UserContext` 上。有两个选择：
1. 在 `UserContext` 上新增 `InvalidateSubscription func(subID string)` 闭包
2. 直接用 `uc.InvalidateLLM()`（invalidates 全部用户缓存，粒度粗但更安全）

**选择方案 2**：`uc.InvalidateLLM()` 已经够用。`InvalidateSubscription` 是优化（只清一个订阅的缓存），但 `InvalidateLLM` 清全部也不会出错。删掉所有 `InvalidateSubscription` 调用，只用 `uc.InvalidateLLM()`。

### 阶段三：扩展 ConfigTool — 新增 model 和 subscription action

**涉及文件：`tools/config_tool.go`**

#### 3.1 更新 Description 和 Parameters

```go
func (t *ConfigTool) Description() string {
    return "Read, list, and modify any xbot configuration setting. " +
        "This is the PRIMARY tool for all configuration management — subscriptions, models, settings, plugins, hooks, and runners. " +
        "Actions: list, get, set, subscriptions (legacy list), model, subscription, reload_plugins, reload_hooks, runner. " +
        "For theme switching and TUI layout, use tui_control."
}

func (t *ConfigTool) Parameters() []llm.ToolParam {
    return []llm.ToolParam{
        {Name: "action", Type: "string", Description: "Action: list, get, set, subscriptions, model, subscription, reload_plugins, reload_hooks, runner", Required: true},
        {Name: "key", Type: "string", Description: "Config key (for get/set)", Required: false},
        {Name: "value", Type: "string", Description: "New value (for set) or boolean (for model enable/disable, subscription set_enabled)", Required: false},
        {Name: "sub", Type: "string", Description: "Sub-action for model/subscription/runner. Model: list|switch|set_context|set_output|enable|disable|add|remove|refresh|active. Subscription: list|add|remove|update|set_default|set_enabled|rename. Runner: create|list|delete|switch|rename", Required: false},
        {Name: "sub_id", Type: "string", Description: "Subscription ID (for model/subscription actions)", Required: false},
        {Name: "model", Type: "string", Description: "Model name (for model actions)", Required: false},
        {Name: "max_context", Type: "integer", Description: "Max context tokens (for model set_context/add)", Required: false},
        {Name: "max_output", Type: "integer", Description: "Max output tokens (for model set_output/add, subscription add)", Required: false},
        {Name: "name", Type: "string", Description: "Name (for subscription add/rename, runner create/delete/switch/rename)", Required: false},
        {Name: "provider", Type: "string", Description: "LLM provider (for subscription add/update)", Required: false},
        {Name: "base_url", Type: "string", Description: "API base URL (for subscription add/update)", Required: false},
        {Name: "api_key", Type: "string", Description: "API key (for subscription add/update, masked)", Required: false},
        {Name: "api_type", Type: "string", Description: "API type: chat_completions or responses (for model add)", Required: false},
        {Name: "is_default", Type: "boolean", Description: "Set as default subscription (for subscription add)", Required: false},
        // ... existing runner params
        {Name: "new_name", Type: "string", Description: "New name (for runner rename)", Required: false},
        {Name: "mode", Type: "string", Description: "Runner mode: native or docker", Required: false},
        {Name: "docker_image", Type: "string", Description: "Docker image (for runner create)", Required: false},
        {Name: "workspace", Type: "string", Description: "Workspace dir (for runner create)", Required: false},
        {Name: "llm_provider", Type: "string", Description: "LLM provider for runner", Required: false},
        {Name: "llm_api_key", Type: "string", Description: "LLM API key for runner (masked)", Required: false},
        {Name: "llm_model", Type: "string", Description: "LLM model for runner", Required: false},
        {Name: "llm_base_url", Type: "string", Description: "LLM base URL for runner", Required: false},
    }
}
```

#### 3.2 新增 `model` action 处理

```go
case "model":
    return t.modelAction(ctx, params)
```

```go
func (t *ConfigTool) modelAction(ctx *ToolContext, p configParams) (*ToolResult, error) {
    switch p.Sub {
    case "list":
        if ctx.ListModelsFn == nil { return nil, fmt.Errorf("model listing not available") }
        models := ctx.ListModelsFn()
        b, _ := json.MarshalIndent(models, "", "  ")
        return NewResult(string(b)), nil

    case "active":
        if ctx.GetActiveModelFn == nil { return nil, fmt.Errorf("active model query not available") }
        subID, model, err := ctx.GetActiveModelFn()
        if err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Active: sub_id=%s, model=%s", subID, model)), nil

    case "switch":
        if ctx.SelectModelFn == nil { return nil, fmt.Errorf("model switch not available") }
        if p.SubID == "" || p.Model == "" {
            return nil, fmt.Errorf("model switch requires sub_id and model")
        }
        if err := ctx.SelectModelFn(p.SubID, p.Model); err != nil { return nil, err }
        // Notify TUI to refresh status bar
        if ctx.TUIControl != nil {
            ctx.TUIControl("reload_settings", map[string]string{"key": "llm_model"})
        }
        return NewResult(fmt.Sprintf("Switched session model to %s (sub: %s)", p.Model, p.SubID)), nil

    case "set_context":
        if ctx.SetModelContextFn == nil { return nil, fmt.Errorf("set_model_context not available") }
        if p.SubID == "" || p.Model == "" {
            return nil, fmt.Errorf("set_context requires sub_id and model")
        }
        maxCtx, err := strconv.Atoi(p.Value)
        if err != nil { return nil, fmt.Errorf("max_context must be an integer, got %q", p.Value) }
        if err := ctx.SetModelContextFn(p.SubID, p.Model, maxCtx); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Set max_context=%d for model %s (sub: %s)", maxCtx, p.Model, p.SubID)), nil

    case "set_output":
        if ctx.SetModelOutputFn == nil { return nil, fmt.Errorf("set_model_output not available") }
        if p.SubID == "" || p.Model == "" {
            return nil, fmt.Errorf("set_output requires sub_id and model")
        }
        maxOut, err := strconv.Atoi(p.Value)
        if err != nil { return nil, fmt.Errorf("max_output must be an integer, got %q", p.Value) }
        if err := ctx.SetModelOutputFn(p.SubID, p.Model, maxOut); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Set max_output=%d for model %s (sub: %s)", maxOut, p.Model, p.SubID)), nil

    case "enable":
        if ctx.SetModelEnabledFn == nil { return nil, fmt.Errorf("model enable not available") }
        if p.SubID == "" || p.Model == "" {
            return nil, fmt.Errorf("enable requires sub_id and model")
        }
        if err := ctx.SetModelEnabledFn(p.SubID, p.Model, true); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Enabled model %s (sub: %s)", p.Model, p.SubID)), nil

    case "disable":
        if ctx.SetModelEnabledFn == nil { return nil, fmt.Errorf("model disable not available") }
        if p.SubID == "" || p.Model == "" {
            return nil, fmt.Errorf("disable requires sub_id and model")
        }
        if err := ctx.SetModelEnabledFn(p.SubID, p.Model, false); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Disabled model %s (sub: %s)", p.Model, p.SubID)), nil

    case "add":
        if ctx.UpsertModelFn == nil { return nil, fmt.Errorf("model add not available") }
        if p.SubID == "" || p.Model == "" {
            return nil, fmt.Errorf("add requires sub_id and model")
        }
        maxCtx, _ := strconv.Atoi(p.MaxContext) // 0 if empty
        maxOut, _ := strconv.Atoi(p.MaxOutput)
        if err := ctx.UpsertModelFn(p.SubID, p.Model, maxCtx, maxOut, p.APIType); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Added/updated model %s on subscription %s", p.Model, p.SubID)), nil

    case "remove":
        if ctx.RemoveModelFn == nil { return nil, fmt.Errorf("model remove not available") }
        if p.SubID == "" || p.Model == "" {
            return nil, fmt.Errorf("remove requires sub_id and model")
        }
        if err := ctx.RemoveModelFn(p.SubID, p.Model); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Removed model %s from subscription %s", p.Model, p.SubID)), nil

    case "refresh":
        if ctx.RefreshModelsFn == nil { return nil, fmt.Errorf("model refresh not available") }
        models := ctx.RefreshModelsFn()
        b, _ := json.MarshalIndent(models, "", "  ")
        return NewResult(fmt.Sprintf("Refreshed %d models from providers:\n%s", len(models), string(b))), nil

    default:
        return nil, fmt.Errorf("model: unknown sub-action: %s (valid: list, active, switch, set_context, set_output, enable, disable, add, remove, refresh)", p.Sub)
    }
}
```

#### 3.3 新增 `subscription` action 处理

```go
case "subscription":
    return t.subscriptionAction(ctx, p)
```

```go
func (t *ConfigTool) subscriptionAction(ctx *ToolContext, p configParams) (*ToolResult, error) {
    switch p.Sub {
    case "list":
        // Reuse existing ListSubscriptions
        if ctx.ListSubscriptions == nil { return nil, fmt.Errorf("subscription listing not available") }
        subs := ctx.ListSubscriptions()
        b, _ := json.MarshalIndent(subs, "", "  ")
        return NewResult(string(b)), nil

    case "add":
        if ctx.AddSubscriptionFn == nil { return nil, fmt.Errorf("subscription add not available") }
        if p.Name == "" || p.Provider == "" {
            return nil, fmt.Errorf("add requires name and provider")
        }
        id, err := ctx.AddSubscriptionFn(tools.SubscriptionCreateParams{
            Name: p.Name, Provider: p.Provider,
            BaseURL: p.BaseURL, APIKey: p.APIKey,
            Model: p.Model, MaxOutputTokens: p.MaxOutput,
            IsDefault: p.IsDefault,
        })
        if err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Created subscription %q (id: %s)", p.Name, id)), nil

    case "remove":
        if ctx.RemoveSubscriptionFn == nil { return nil, fmt.Errorf("subscription remove not available") }
        if p.SubID == "" { return nil, fmt.Errorf("remove requires sub_id") }
        if err := ctx.RemoveSubscriptionFn(p.SubID); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Removed subscription %s", p.SubID)), nil

    case "update":
        if ctx.UpdateSubscriptionFieldsFn == nil { return nil, fmt.Errorf("subscription update not available") }
        if p.SubID == "" { return nil, fmt.Errorf("update requires sub_id") }
        if err := ctx.UpdateSubscriptionFieldsFn(p.SubID, tools.SubscriptionUpdateParams{
            Name: p.Name, Provider: p.Provider,
            BaseURL: p.BaseURL, APIKey: p.APIKey,
            Model: p.Model, MaxOutputTokens: p.MaxOutput,
        }); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Updated subscription %s", p.SubID)), nil

    case "set_default":
        if ctx.SetDefaultSubscriptionFn == nil { return nil, fmt.Errorf("set_default not available") }
        if p.SubID == "" { return nil, fmt.Errorf("set_default requires sub_id") }
        if err := ctx.SetDefaultSubscriptionFn(p.SubID); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Set default subscription to %s", p.SubID)), nil

    case "set_enabled":
        if ctx.SetSubscriptionEnabledFn == nil { return nil, fmt.Errorf("set_enabled not available") }
        if p.SubID == "" { return nil, fmt.Errorf("set_enabled requires sub_id") }
        enabled := p.Value == "true" || p.Value == "1"
        if err := ctx.SetSubscriptionEnabledFn(p.SubID, enabled); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Subscription %s %s", p.SubID, map[bool]string{true: "enabled", false: "disabled"}[enabled])), nil

    case "rename":
        if ctx.RenameSubscriptionFn == nil { return nil, fmt.Errorf("rename not available") }
        if p.SubID == "" || p.Name == "" { return nil, fmt.Errorf("rename requires sub_id and name") }
        if err := ctx.RenameSubscriptionFn(p.SubID, p.Name); err != nil { return nil, err }
        return NewResult(fmt.Sprintf("Renamed subscription %s to %q", p.SubID, p.Name)), nil

    default:
        return nil, fmt.Errorf("subscription: unknown sub-action: %s (valid: list, add, remove, update, set_default, set_enabled, rename)", p.Sub)
    }
}
```

#### 3.4 重构 params 结构体

将现有的 `Execute` 方法中的匿名 struct 提取为 `configParams`，新增字段：

```go
type configParams struct {
    Action      string `json:"action"`
    Key         string `json:"key"`
    Value       string `json:"value"`
    Sub         string `json:"sub"`
    SubID       string `json:"sub_id"`
    Model       string `json:"model"`
    MaxContext  string `json:"max_context"`
    MaxOutput   string `json:"max_output"`
    Name        string `json:"name"`
    Provider    string `json:"provider"`
    BaseURL     string `json:"base_url"`
    APIKey      string `json:"api_key"`
    APIType     string `json:"api_type"`
    IsDefault   bool   `json:"is_default"`
    // Runner params (unchanged)
    NewName     string `json:"new_name"`
    Mode        string `json:"mode"`
    DockerImage string `json:"docker_image"`
    Workspace   string `json:"workspace"`
    LLMProvider string `json:"llm_provider"`
    LLMAPIKey   string `json:"llm_api_key"`
    LLMModel    string `json:"llm_model"`
    LLMBaseURL  string `json:"llm_base_url"`
}
```

### 阶段四：更新 embed skill 文档

**涉及文件：`tools/embed_skills/ai-config/SKILL.md`**

在 Tool Summary 表格后新增 Model Management 和 Subscription Management 章节：

```markdown
## Model Management

| Task | Tool call | Notes |
|------|-----------|-------|
| List all models | `config action=model sub=list` | Shows all models across subscriptions with status |
| Show current session model | `config action=model sub=active` | Returns sub_id + model of current session |
| Switch session model | `config action=model sub=switch sub_id=xxx model=yyy` | Per-session, doesn't affect other sessions |
| Set max context | `config action=model sub=set_context sub_id=xxx model=yyy value=131072` | Per-model max context tokens |
| Set max output | `config action=model sub=set_output sub_id=xxx model=yyy value=8192` | Per-model max output tokens |
| Enable model | `config action=model sub=enable sub_id=xxx model=yyy` | Makes model selectable |
| Disable model | `config action=model sub=disable sub_id=xxx model=yyy` | Greys out model |
| Add/register model | `config action=model sub=add sub_id=xxx model=yyy max_context=131072` | Register a model not in provider's list |
| Remove model | `config action=model sub=remove sub_id=xxx model=yyy` | Permanently delete model config |
| Refresh model list | `config action=model sub=refresh` | Live-fetch /models from all providers |

Use `config action=model sub=list` first to get `sub_id` and available `model` names.

## Subscription Management

| Task | Tool call | Notes |
|------|-----------|-------|
| List subscriptions | `config action=subscription sub=list` | Same as `config action=subscriptions` |
| Add subscription | `config action=subscription sub=add name=xxx provider=openai api_key=sk-xxx model=gpt-4o` | Creates new LLM subscription |
| Remove subscription | `config action=subscription sub=remove sub_id=xxx` | Deletes subscription + its models |
| Update subscription | `config action=subscription sub=update sub_id=xxx api_key=newkey` | Only specified fields are changed |
| Set default | `config action=subscription sub=set_default sub_id=xxx` | User-level default for new sessions |
| Enable/disable | `config action=subscription sub=set_enabled sub_id=xxx value=false` | Disabled subs keep credentials |
| Rename | `config action=subscription sub=rename sub_id=xxx name=newname` | Rename subscription |

### Typical workflow: add a new LLM provider
```
config action=subscription sub=add name="deepseek" provider=openai base_url="https://api.deepseek.com" api_key="sk-xxx" model="deepseek-chat"
→ Returns subscription ID
config action=model sub=set_context sub_id=<id> model="deepseek-chat" value=131072
```

### Typical workflow: switch current session's model
```
config action=model sub=list
→ Find desired model + sub_id
config action=model sub=switch sub_id=<id> model=<model>
```
```

### 阶段五：测试

**新增文件：`tools/config_tool_test.go`（如果已有则扩展）**

测试用例：

1. **model list** — 验证返回 ModelInfo 列表
2. **model switch** — 验证 SelectModelFn 被调用
3. **model set_context** — 验证 SetModelContextFn 被调用，参数正确
4. **model enable/disable** — 验证 SetModelEnabledFn
5. **model add/remove** — 验证 UpsertModelFn/RemoveModelFn
6. **subscription add** — 验证 AddSubscriptionFn 被调用，参数完整
7. **subscription update** — 验证只更新非空字段
8. **subscription set_default** — 验证 SetDefaultSubscriptionFn
9. **向后兼容** — 现有 `subscriptions` action 仍然工作

## 验证方案

- **编译检查**：`go build ./...` 通过
- **单元测试**：`go test ./tools/... -run TestConfigTool` 通过
- **集成验证**：在 CLI 中运行以下操作序列：
  1. `config action=subscription sub=add name=test provider=openai api_key=sk-test model=gpt-4o` → 返回新订阅 ID
  2. `config action=model sub=list` → 看到新订阅的模型
  3. `config action=model sub=switch sub_id=<id> model=gpt-4o` → 当前会话切换模型
  4. `config action=model sub=set_context sub_id=<id> model=gpt-4o value=131072` → max context 设置成功
  5. `config action=model sub=active` → 显示当前模型
  6. `config action=subscription sub=remove sub_id=<id>` → 删除成功

## 回滚策略

所有修改集中在 3 个文件：
1. `tools/config_tool.go` — 新增 model/subscription action 处理
2. `tools/interface.go` — 新增 ToolContext 字段
3. `agent/engine.go` — 注入闭包

如需回滚，git revert 这 3 个文件的修改即可。现有功能不受影响（新增的 case 分支不影响现有 action）。

## 注意事项

- **不能用 `a.userSys.llmFactory` 直接调用**：按 AGENTS.md 约束，agent loop 代码不能直接访问 `a.llmFactory`。但 `buildToolContext` 已经通过 `UserContext` 间接访问了 `LLMFactory`（如 `uc.ResolveLLM`）。新增闭包也应通过 `uc` 访问。`uc.SubSvc` 已直接可用（是 `*sqlite.LLMSubscriptionService`）。`uc.SelectModel` 已可用。`uc.RefreshModels` 已可用。`uc.InvalidateLLM` 已可用。唯一需要的是 `ListAllModelEntriesForUser` — 这个在 `LLMFactory` 上，不在 `UserContext` 上。
  - **解决方案**：在 `UserContext` 上新增 `ListModels func() []protocol.ModelEntry` 闭包，在 `ResolveUserContext` 中注入 `a.userSys.llmFactory.ListAllModelEntriesForUser(senderID)`。
  - 或者：在 `engine.go:buildToolContext` 中直接通过 `a.userSys.llmFactory` 注入（因为 `buildToolContext` 本就在 agent 层，已有 `a.userSys` 引用如 `a.listLLMSubsFn(uc)` 等模式）。

- **InvalidateSubscription vs InvalidateLLM**：`InvalidateSubscription(subID)` 是精确清除单个订阅的客户端缓存；`InvalidateLLM()` (= `Invalidate(senderID)`) 清除该用户所有缓存。在 config 工具场景下用 `InvalidateLLM()` 足够，因为配置变更不频繁。

- **SubscriptionUpdate 的 credentials 安全**：`UpdateSubscriptionFieldsFn` 使用 Get→modify→Update 模式，从 DB 读取真实值（非 masked），只修改传入的非空字段，所以不会覆盖 credentials。

- **向后兼容**：现有 `subscriptions` action 保留（内部直接调用 `subscriptionAction` 的 `list` 子操作），或者保留原逻辑不改动。

✅ 自审通过
