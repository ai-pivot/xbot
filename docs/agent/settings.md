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
- `ScopeUser`: stored in user_settings DB (max_iterations, language, etc.)
- `ScopeGlobal`: stored in config.json (sandbox_mode)

## Data Flow

```
User Ctrl+S in /settings
  → cli_settings.go:saveSettings()
       ├→ PerModelConfigs: subscriptionMgr.UpdatePerModelConfig(subID, model, pmc)
       ├→ User-scoped keys: settingsSvc.SetSetting(key, val)
       └→ Runtime: ApplySettings(values) → agent.ApplyRuntimeSettings(cfg, backend, ...)
```

## Key Architecture Decisions

1. **PerModelConfig writes use `UpdatePerModelConfig` API**, never `UpdateSubscription`. The old List→modify→Update pattern reads masked keys from the API, then writes them back, destroying real credentials. The new API only touches PerModelConfigs.

2. **Server-side `updateSubscription` RPC starts from EXISTING subscription**, only overlays non-masked fields. Client sends masked keys (`****`) — the handler preserves real credentials from DB.

3. **`max_context_tokens` is ScopeSubscription**. Stored in `PerModelConfigs[model].MaxContext`, NOT in user_settings DB or config.Agent.MaxContextTokens.

4. **CLI local and remote modes use the same SubscriptionManager** (`backendSubscriptionManager`). It calls Backend interface methods which route through Transport (local or remote). No IsRemote branches in subscription management.

5. **`serverapp/setting_handlers.go` is a thin wrapper** (32 lines) that calls `agent.ApplyRuntimeSettings` + `saveServerConfig`. No duplicate handler registry.
