# Settings System Architecture

## Single Source of Truth

### Runtime Setting Handlers: `agent/setting_runtime.go`
One `SettingHandlerRegistry` map used by both CLI and server. Each entry defines how a setting key updates runtime state:
- `ApplyConfig`: updates in-memory `config.Config` struct
- `ApplyBackend`: applies runtime side-effects via Backend interface
- `ApplyFull`: combined config + backend update (for complex cases like sandbox reinit)

To add a new runtime setting:
1. Add key to `channel.CLIRuntimeSettingKeys`
2. Add handler in `agent/setting_runtime.go`
3. Done — no switch-case, no if-chain, no second registry to update.

### Settings Panel Read/Write: `channel/cli_settings.go`
- `readSettings()`: merges all scopes → `map[string]string` for UI display
- `saveSettings()`: dispatches each key to its scope's writer
- `resolveMaxContext()`: reads from `subscription.PerModelConfigs[model].MaxContext`

### Scope Classification: `channel/setting_keys.go`
Every setting key has a scope:
- `ScopeSubscription`: stored in subscription (provider, key, model, max_context via PerModelConfigs)
- `ScopeUser`: stored in user_settings DB (layout_mode, language, etc.)
- `ScopeGlobal`: stored in config.json (sandbox_mode)

## Data Flow

```
User Ctrl+S in /settings
  → cli_settings.go:saveSettings()
       ├→ PerModelConfigs: subscriptionMgr.UpdatePerModelConfig(subID, model, pmc)
       ├→ User-scoped keys: settingsSvc.SetSetting(key, val)
       └→ Runtime: ApplySettings(values) → agent.ApplyRuntimeSettings(cfg, backend, ...)
```

## Subscription Manager

Single implementation: `backendSubscriptionManager` (in cmd/xbot-cli/main.go).
Works identically for local and remote modes via Backend interface → Transport:
- Local: `localTransport` → function call → sqlite DB
- Remote: `RemoteTransport` → WebSocket RPC → server DB

### Methods
- `List/GetDefault/Add/Remove/SetDefault/SetModel/Rename/Update` — standard CRUD
- `UpdatePerModelConfig(id, model, pmc)` — **safe single-field write** that only touches
  PerModelConfigs, never touches credentials (API key, provider, base_url)

## Backend Interface Extensions

All CLI operations go through `AgentBackend` interface. Transport layer handles routing:

| Method | localTransport | Server RPC |
|--------|---------------|------------|
| `UpdatePerModelConfig` | sqlite.SubscriptionService.Update | `update_per_model_config` |
| `CreateWebUser` | channel.CreateWebUser | `create_web_user` |
| `ListWebUsers` | channel.ListWebUsers | `list_web_users` |
| `DeleteWebUser` | channel.DeleteWebUser | `delete_web_user` |
| `DeleteChat` | sqlite.ChatService.DeleteChat | `delete_chat` |
| `RenameChat` | sqlite.ChatService.RenameChat | `rename_chat` |
| `get_history` | sqlite SessionService.GetAllMessages | server backend.GetHistory |
| `get_token_state` | sqlite MemoryService.GetTokenState | server backend.GetTokenState |

## Key Architecture Decisions

1. **PerModelConfig writes use `UpdatePerModelConfig` API**, never `UpdateSubscription`. The old
   List→modify→Update pattern reads masked keys from the API, then writes them back, destroying
   real credentials. The new API only touches PerModelConfigs.

2. **Server-side `updateSubscription` RPC starts from EXISTING subscription**, only overlays
   non-masked fields. Client sends masked keys (`****`) — the handler preserves real credentials
   from DB.

3. **`max_context_tokens` is ScopeSubscription**. Stored in `PerModelConfigs[model].MaxContext`,
   NOT in user_settings DB or config.Agent.MaxContextTokens.

4. **All CLI modes use the same SubscriptionManager** (`backendSubscriptionManager`). It calls
   Backend interface methods which route through Transport (local or remote). No IsRemote
   branches in subscription management.

5. **`serverapp/setting_handlers.go` is a thin wrapper** that calls `agent.ApplyRuntimeSettings`
   + `saveServerConfig`. No duplicate handler registry.

6. **All modes use cache for UI reads** (GetCurrentValues, SessionsList, AgentCount/AgentList).
   Background goroutines refresh caches every 5 seconds for both local and remote modes.

7. **All modes use Backend methods for history/token state**. localTransport handlers call
   sqlite directly (function call, zero overhead). Remote mode uses WebSocket RPC.

## Credential Protection

The #1 goal of this refactoring was eliminating credential loss bugs. The protection is multi-layered:

1. **`UpdatePerModelConfig` API** — /settings max_context changes only touch PerModelConfigs.
   Never sends subscription credentials over the wire.

2. **Server `updateSubscription` handler** — builds `dbSub` from EXISTING DB record, only
   overlays fields that are explicitly non-empty AND non-masked. Masked keys (`****`) are
   never written regardless of length.

3. **`configSubscriptionManager` deleted** — the old CLI config.json manager had its own
   credential protection logic (len<=20 check on masked keys) that was fragile and failed
   on long masked strings.

4. **`cli_settings.go:saveSettings` only writes PerModelConfigs** — subscription field
   updates (provider, key, model) go through `ApplySettings` → `updateActiveSubscription`
   which has its own masked-key protection.

## 迭代次数上限：已彻底移除（2026-10-14，用户要求「以后不加这种限制」）

`max_iterations` / `AgentConfig.MaxIterations` / `RunConfig.MaxIterations` /
`DefaultMaxIterations`（原 2000）/ `AGENT_MAX_ITERATIONS` 环境变量 /
`set_max_iterations` RPC / `Agent.SetMaxIterations` / `user_settings` 行 /
设置面板条目（`channel/setting_keys.go` + `channel/i18n.go` 三语）**全部删除**。

- 引擎主循环现在是 `for n := 1; ; n++`（`agent/engine.go`）—— **没有次数上限**，
  直到 LLM 给出最终回答、用户取消、或其它既有熔断（迭代循环检测等）触发。
- ⛔ **不要重新引入任何「每 turn 迭代次数上限」**。长 turn（数千次工具迭代）是正常负载：
  上限曾导致 ① 迭代记录在 2000 条后**停止落库**（`iteration_history` 断流，UI 看不到
  后半段迭代）；② 压缩点（`[Compacted context]`）失去可定位的迭代时间戳，前端只能把
  它们堆在 turn 末尾。判别测试 `TestRun_NoIterationCap`（agent/engine_test.go）。

