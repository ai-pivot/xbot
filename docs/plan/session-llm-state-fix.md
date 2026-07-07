# 修复计划：session 切换时模型名消失 + 默认会话统一化

## 问题

侧边栏切回默认会话后，info bar 模型名消失（`cachedModelName=""`）。
只有默认会话有此问题，重启 TUI 后恢复正常。

## 根因

`postRestoreSessionSetup` 与 `refreshCachedModelName` 用了不同的 LLM 状态解析路径：

| 路径 | 优先级链 | 调用时机 |
|------|---------|---------|
| `refreshCachedModelName` | DB tenants 表 → 本地 JSON → savedSessions → GetDefault | 启动、settings 保存、idle tick |
| `postRestoreSessionSetup` | 本地 JSON → GetDefault("") | **session 切换（所有 9 个路径）** |

remote mode 下 `SaveSessionLLMState` 传 `skipBackendFields=true`，本地 JSON 不写 subID/model。
所以 `LoadSessionLLMState` 始终返回零值 → 进入 else 分支 → `GetDefault("")` 返回订阅的 `Model` 字段。

默认会话的 per-session model（用户通过 Ctrl+N 选的 "glm-5.2"）只存在于 DB tenants 表中，
`postRestoreSessionSetup` 不查 DB，直接退到 `GetDefault` → 模型名丢失。

### 时序

```
启动 → refreshCachedModelName → GetSessionSubscription(DB) → "glm-5.2" ✅
切到 session B → saveCurrentSession(默认会话: model="glm-5.2")
切回默认会话 → restoreSession → cachedModelName="glm-5.2" ✅
              → postRestoreSessionSetup → LoadSessionLLMState → 零值
                → GetDefault("") → sub.Model="" → cachedModelName="" ❌ 覆盖
重启 TUI → refreshCachedModelName → GetSessionSubscription(DB) → "glm-5.2" ✅
```

## 设计原则

1. **默认会话与普通会话走完全一样的创建和恢复逻辑**
2. **消除 `postRestoreSessionSetup` 和 `refreshCachedModelName` 的双路径问题** — 统一为单一数据源
3. **No hacks, no fallbacks** — 从根源修复，不叠加防护层

## 修改方案

### 改动 1：`postRestoreSessionSetup` 用 `refreshCachedModelName` 替代手写 LLM 恢复逻辑

**文件**：`channel/cli/cli_model_session.go`
**位置**：L468-498（`if m.channelName != "agent"` 块）

**当前**：
```go
if m.channelName != "agent" {
    state := LoadSessionLLMState(m.workDir, m.chatID)
    if !state.IsZero() {
        m.applySessionLLMState(state)
        if ... { m.channel.config.RefreshValuesCache(state.SubscriptionID) }
    } else {
        // GetDefault("") → 覆盖 cachedModelName ← BUG
    }
}
```

**改为**：
```go
if m.channelName != "agent" {
    // refreshCachedModelName is the SINGLE source of truth for per-session
    // LLM state resolution. It checks (in order):
    //   1. DB tenants table (GetSessionSubscription RPC) — authoritative in remote mode
    //   2. Local Session JSON — authoritative in local mode / cache fallback
    //   3. In-memory savedSessions — live state not yet persisted
    //   4. GetDefault — global fallback for brand-new sessions
    //
    // This replaces the old inline logic that only checked local JSON + GetDefault,
    // missing the DB tenants table path entirely. In remote mode, per-session model
    // choices (Ctrl+N) live ONLY in the DB — local JSON is never written
    // (skipBackendFields=true). The old code fell through to GetDefault, which
    // returned the subscription's default Model (often ""), overwriting the
    // correct per-session model.
    m.refreshCachedModelName()

    // Resolve context limits from the now-correct activeSubID + cachedModelName.
    state := SessionLLMState{
        SubscriptionID: m.activeSubID,
        Model:          m.cachedModelName,
    }
    m.cachedMaxContextTokens = ResolveEffectiveMaxContext(state, m.subscriptionMgr)
    m.cachedMaxOutputTokens = int64(ResolveEffectiveMaxOutputTokens(state, m.subscriptionMgr))

    // Refresh valuesCache so context bar / settings panel read correct subscription data.
    if m.activeSubID != "" && m.channel != nil && m.channel.config.RefreshValuesCache != nil {
        m.channel.config.RefreshValuesCache(m.activeSubID)
    }
}
```

**为什么这是正确的修复**：

- `refreshCachedModelName` 已实现了完整的 4 级优先级链（DB → JSON → savedSessions → GetDefault）
- remote mode 下 `GetSessionSubscription` 查 DB tenants 表 → 拿到 per-session model
- `restoreSession()` 已从 `savedSessions` 恢复了 `cachedModelName`，但 `refreshCachedModelName` 会重新验证：
  - 如果 DB 有值 → 用 DB 的值（权威源）
  - 如果 DB 为空 → 本地 JSON → savedSessions → GetDefault
- **注意**：`refreshCachedModelName` 内部有 `defer m.refreshCachedSubName()`，所以 `cachedSubName` 也会同步更新

**消除的冗余**：
- `applySessionLLMState` 不再在 session 切换路径中被调用（它内部也调 `refreshCachedSubName`，与 `refreshCachedModelName` 的 defer 重复）
- `RefreshValuesCache` 的两处调用（if/else 分支各一处）合并为一处

### 改动 2：`refreshCachedModelName` 的 GetDefault fallback 保持 `m.cachedModelName` 守卫

**文件**：`channel/cli/cli_subscription.go`
**位置**：L213-221

**当前**（已有，无需修改）：
```go
if m.cachedModelName == "" && m.channel.subscriptionMgr != nil {
    if sub, err := m.channel.subscriptionMgr.GetDefault(m.senderID); err == nil && sub != nil {
        m.cachedModelName = sub.Model
        if m.activeSubID == "" {
            m.activeSubID = sub.ID
        }
    }
}
```

这个守卫确保只有当 `cachedModelName` 仍为空时才用 GetDefault 兜底。
如果 `restoreSession()` 已恢复了值，且 DB 也有值，则不会走到这里。
如果 DB 为空且 savedSessions 也为空（真正的全新会话），才用 GetDefault。

**无需修改**，但需要验证：`restoreSession()` 后 `savedSessions[key]` 已被 `delete` 删除，
所以 `refreshCachedModelName` 的第 3 级（savedSessions）在 session 切换时不会命中。
这是正确的 — DB 和本地 JSON 是跨 session 持久化源，savedSessions 只在单次生命周期内有效。

### 改动 3：消除 `refreshCachedModelName` 的重复注释

**文件**：`channel/cli/cli_subscription.go`
**位置**：L169-174

两段完全相同的注释，删除一段。

### 改动 4：测试

**文件**：`channel/cli/cli_progress_test.go` 或新建 `channel/cli/cli_session_llm_test.go`

测试用例：

1. **TestSessionSwitch_PreservesPerSessionModel**：
   - 启动 → refreshCachedModelName → mock GetSessionSubscription 返回 ("sub-X", "glm-5.2")
   - saveCurrentSession → 切到 session B
   - 切回 → restoreSession + postRestoreSessionSetup
   - 验证 cachedModelName == "glm-5.2"（未被 GetDefault 覆盖）

2. **TestSessionSwitch_DefaultFallback_WhenNoDBEntry**：
   - mock GetSessionSubscription 返回空（新会话）
   - mock LoadSessionLLMState 返回零值
   - postRestoreSessionSetup
   - 验证 cachedModelName == GetDefault().Model

3. **TestSessionSwitch_RestoreSessionThenRefreshFromDB**：
   - restoreSession 恢复 cachedModelName="old-model"
   - refreshCachedModelName 从 DB 拿到 "new-model"
   - 验证 cachedModelName == "new-model"（DB 是权威源）

## 验证清单

- [ ] `go build ./...`
- [ ] `go test ./channel/cli/ -timeout 120s`
- [ ] `golangci-lint run ./channel/cli/`
- [ ] 手动验证：默认会话 Ctrl+N 切模型 → 切到其他 session → 切回 → 模型名仍在
- [ ] 手动验证：新创建会话 → 模型名正确显示
- [ ] 手动验证：/su 切到飞书 → 切回 → 模型名正确显示
- [ ] 更新 AGENTS.md gotcha

## 影响范围

| 文件 | 改动类型 | 风险 |
|------|---------|------|
| `cli_model_session.go` | 替换 LLM 恢复逻辑 | 中 — 核心路径，但有测试覆盖 |
| `cli_subscription.go` | 删除重复注释 | 极低 |

## 不改动的部分

- `refreshCachedModelName` 的逻辑不变（已正确实现 4 级优先级链）
- `applySessionLLMState` 不删除（`handleSwitchLLMDoneMsg` 等路径仍使用）
- `handleSuHistoryLoad` 的 LLM 恢复逻辑不变（agent 会话路径独立）
- `SaveSessionLLMState` 的 `skipBackendFields` 机制不变（DB 是权威源的设计正确）
- `restoreSession` 不变（savedSessions 的保存/恢复逻辑正确）
