# Canonical User Identity & Cross-Channel Account Unification

> **Status**: Design (reviewed by roundtable — security, architecture, database experts)
>
> **Roundtable consensus**: Adopt canonical user model with one-shot migration (no dual-column coexistence), `Resolve` at channel entry layer, `ToolContext` carries `UserID`, merge concurrency via Go mutex.

## Problem Statement

xbot 当前有 4 个 channel（CLI、Web、飞书、QQ），每个 channel 有完全独立的身份体系：

| Channel | senderID 来源 | 示例 | DB key |
|---|---|---|---|
| CLI 本地 | 硬编码 | `cli_user` | 所有数据用 `cli_user` |
| CLI 远程 | AdminToken 验证 | `admin` (auth) + `cli_user` (biz) | `cli_user` |
| Web | `web_users.id` | `web-4` | `web-4` |
| Web+飞书联动 | `sessionInfo.feishuUserID` | `ou_xxx` (替换 `web-4`) | `ou_xxx` |
| 飞书 | 平台 open_id | `ou_xxx` | `ou_xxx` |

数据按 `sender_id` 分片存储在 8 张表中：`user_llm_subscriptions`、`runners`、`runner_tokens`、`user_settings`、`user_default_model`、`user_chats`、`cron_jobs`、`event_triggers`。同一个真人，在 CLI 注册了 runner，在 Web 看不到；在 Web 配了订阅，飞书用不了。

Admin 判断分散在 4 处，各自硬编码 `senderID == "admin"` 或 `userID == 1`，没有 DB role 字段，无法给其他用户 admin 权限。

### 现有的跨 Channel 关联（hack）

飞书-Web linking 用 `user_settings` 表存映射：`channel='feishu', sender_id='ou_xxx', key='web_user_id', value='4'`。联动后 web 用户的 senderID 从 `web-4` 变成 `ou_xxx`，共享飞书的数据。但这是把两个 sender_id 合并成一个，不是真正的身份统一。

## Design Goals

1. **Canonical User**：引入与 channel 无关的用户身份，所有内部操作用 `user_id` 而非 `sender_id`
2. **Cross-Channel Linking**：用户可以关联多个 channel 身份到同一个 canonical user
3. **Asset Unification**：关联后，订阅、runner、settings、session 跨 channel 可见
4. **Role-Based Access**：admin 是 DB role，可管理，可扩展
5. **One-Shot Migration**：一次 migration 完成全部 schema + backfill，不做双列共存

## Schema Design

### New Tables (v45 migration)

```sql
-- Canonical user identity
CREATE TABLE IF NOT EXISTS users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    display_name TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('admin', 'user')),
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Channel identity → canonical user mapping
CREATE TABLE IF NOT EXISTS user_identities (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL,
    channel         TEXT NOT NULL,         -- 'cli' | 'web' | 'feishu' | 'qq' | 'system' | ...
    channel_user_id TEXT NOT NULL,         -- 'cli_user' | 'web-4' | 'ou_xxx' | '__system__' | ...
    linked_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(channel, channel_user_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_identities_user ON user_identities(user_id);

-- One-time link codes for cross-channel association
CREATE TABLE IF NOT EXISTS link_codes (
    code        TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL,
    expires_at  TIMESTAMP NOT NULL,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
```

### Asset Tables: Add `user_id` Column

给所有以 `sender_id` / `user_id TEXT` 为 key 的表加 `user_id INTEGER` 列：

```sql
ALTER TABLE user_llm_subscriptions ADD COLUMN user_id INTEGER DEFAULT 0;
ALTER TABLE runners ADD COLUMN user_id INTEGER DEFAULT 0;
ALTER TABLE user_settings ADD COLUMN user_id INTEGER DEFAULT 0;
ALTER TABLE user_default_model ADD COLUMN user_id INTEGER DEFAULT 0;
ALTER TABLE user_chats ADD COLUMN user_id INTEGER DEFAULT 0;
ALTER TABLE tenants ADD COLUMN owner_user_id INTEGER DEFAULT 0;
ALTER TABLE cron_jobs ADD COLUMN user_id INTEGER DEFAULT 0;
ALTER TABLE event_triggers ADD COLUMN user_id INTEGER DEFAULT 0;
```

> **Roundtable decision**: One-shot migration — all schema changes + backfill in a single migration transaction. No dual-column coexistence. Old `sender_id` columns are retained for backward compat but new code reads `user_id INTEGER`.

### Seed & Backfill

```sql
-- 1. Seed user 1 as admin (for existing cli_user/admin)
INSERT INTO users (id, display_name, role) VALUES (1, 'Admin', 'admin');

-- 2. Map CLI identities
INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
VALUES (1, 'cli', 'cli_user');
INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
VALUES (1, 'cli', 'admin');

-- 3. Map __system__ subscription owner (fixes NULL trap identified by DB expert)
INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
VALUES (1, 'system', '__system__');

-- 4. Map existing web users
INSERT OR IGNORE INTO users (id, display_name, role)
SELECT id, username, CASE WHEN id = 1 THEN 'admin' ELSE 'user' END
FROM web_users;

INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
SELECT id, 'web', 'web-' || CAST(id AS TEXT) FROM web_users;

-- 5. Map existing Feishu identities (from user_llm_subscriptions.sender_id)
INSERT OR IGNORE INTO users (display_name, role)
SELECT DISTINCT sender_id, 'user' FROM user_llm_subscriptions
WHERE sender_id LIKE 'ou_%'
AND sender_id NOT IN (
    SELECT channel_user_id FROM user_identities WHERE channel = 'feishu'
);

INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
SELECT u.id, 'feishu', u.display_name FROM users u
WHERE u.display_name LIKE 'ou_%'
AND u.id NOT IN (SELECT user_id FROM user_identities);

-- 6. Migrate Feishu-Web links (existing hack in user_settings)
-- This must run AFTER steps 4-5 so both identities exist.
INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
SELECT ui.user_id, 'feishu', us.sender_id
FROM user_settings us
JOIN user_identities ui ON ui.channel = 'web'
    AND ui.channel_user_id = ('web-' || us.value)
WHERE us.channel = 'feishu' AND us.key = 'web_user_id';

-- 7. Backfill user_id columns on asset tables
-- For each table, look up the canonical user_id from user_identities.

-- user_llm_subscriptions
UPDATE user_llm_subscriptions SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = user_llm_subscriptions.sender_id
    ORDER BY
        CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 ELSE 3 END
    LIMIT 1
) WHERE user_id = 0;

-- runners (user_id TEXT → user_id INTEGER)
UPDATE runners SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = runners.user_id
    LIMIT 1
) WHERE user_id = 0;
-- NOTE: runners.user_id is TEXT (old), new column is user_id INTEGER.
-- Migration renames old column to user_id_text, adds user_id INTEGER.

-- user_settings
UPDATE user_settings SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel = user_settings.channel
    AND ui.channel_user_id = user_settings.sender_id
    LIMIT 1
) WHERE user_id = 0;

-- user_default_model
UPDATE user_default_model SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = user_default_model.sender_id
    ORDER BY
        CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 ELSE 3 END
    LIMIT 1
) WHERE user_id = 0;

-- user_chats
UPDATE user_chats SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel = user_chats.channel
    AND ui.channel_user_id = user_chats.sender_id
    LIMIT 1
) WHERE user_id = 0;

-- tenants
UPDATE tenants SET owner_user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel = tenants.channel
    AND ui.channel_user_id = tenants.chat_id
    LIMIT 1
) WHERE owner_user_id = 0;

-- cron_jobs
UPDATE cron_jobs SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = cron_jobs.sender_id
    ORDER BY
        CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 ELSE 3 END
    LIMIT 1
) WHERE user_id = 0 AND sender_id != '';

-- event_triggers
UPDATE event_triggers SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = event_triggers.sender_id
    ORDER BY
        CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 ELSE 3 END
    LIMIT 1
) WHERE user_id = 0 AND sender_id != '';
```

> **NULL trap fix (roundtable)**: The `__system__` subscription row (`sender_id='__system__'`) has no matching identity in the naive backfill. Step 3 creates the `system/__system__` identity mapping to user 1, so the backfill in step 7 correctly assigns it.

## IdentityResolver

```go
// IdentityResolver resolves channel-specific senderID to canonical user_id.
// Called at every system entry point (WS connect, HTTP request, CLI startup).
type IdentityResolver struct {
    db *sql.DB
}

// Resolve looks up or auto-creates a canonical user for the given channel identity.
// Returns (userID, role, error).
//
// Race-safe: uses INSERT OR IGNORE + re-SELECT to avoid orphan rows
// when two concurrent requests resolve the same new identity.
func (r *IdentityResolver) Resolve(channel, channelUserID string) (int64, string, error) {
    // 1. Fast path: check if already linked
    var userID int64
    err := r.db.QueryRow(
        `SELECT user_id FROM user_identities WHERE channel = ? AND channel_user_id = ?`,
        channel, channelUserID,
    ).Scan(&userID)
    if err == nil {
        var role string
        r.db.QueryRow(`SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
        return userID, role, nil
    }
    // 2. Not linked — auto-create a new user (race-safe via ON CONFLICT)
    r.db.Exec(`INSERT INTO users (role) VALUES ('user')`)
    // 3. Link identity (ON CONFLICT handles race: if another goroutine already inserted)
    r.db.Exec(
        `INSERT INTO user_identities (user_id, channel, channel_user_id)
         VALUES (last_insert_rowid(), ?, ?)
         ON CONFLICT(channel, channel_user_id) DO NOTHING`,
        channel, channelUserID,
    )
    // 4. Re-SELECT to get the canonical user_id (may differ from our INSERT if race lost)
    err = r.db.QueryRow(
        `SELECT user_id FROM user_identities WHERE channel = ? AND channel_user_id = ?`,
        channel, channelUserID,
    ).Scan(&userID)
    if err != nil {
        return 0, "", fmt.Errorf("resolve identity: %w", err)
    }
    var role string
    r.db.QueryRow(`SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
    return userID, role, nil
}

// IsAdmin checks if the canonical user has admin role.
func (r *IdentityResolver) IsAdmin(userID int64) bool {
    var role string
    err := r.db.QueryRow(`SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
    if err != nil {
        return false
    }
    return role == "admin"
}

// SetRole updates a user's role (admin only).
func (r *IdentityResolver) SetRole(userID int64, role string) error {
    _, err := r.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, userID)
    return err
}
```

> **No cache (roundtable consensus)**: `ResolveLLM` already reads DB directly every call with no in-memory cache — `IdentityResolver` follows the same design philosophy. A local SQLite SELECT is ~0.1ms. If profiling shows bottlenecks in Phase 2, add LRU+TTL then.

### Resolve Call Site: Channel Entry Layer

`Resolve` is called at each channel's entry point, **not** in the agent layer. The resolved `userID` + `role` are injected into `bus.InboundMessage.Metadata`:

```go
// channel/web/web.go handleWS (after auth)
userID, role, _ := identityResolver.Resolve("web", senderID)
// inject into metadata for agent layer to read
msg.Metadata["user_id"] = strconv.FormatInt(userID, 10)
msg.Metadata["user_role"] = role

// agent/engine_wire.go buildMainRunConfig
userID, _ := strconv.ParseInt(msg.Metadata["user_id"], 10, 64)
role := msg.Metadata["user_role"]
cfg.UserID = userID
cfg.Role = role
```

> **Roundtable decision**: `Resolve` at channel entry layer, NOT by changing `GetOrCreateTenantID` signature (which has 14 call sites). The agent layer reads `userID` from metadata.

## ToolContext & SubAgent Inheritance

> **Roundtable finding**: `RunConfig` alone is insufficient — SubAgent inherits from `parentCtx *tools.ToolContext`, not from `RunConfig`. If `UserID`/`Role` is only on `RunConfig`, SubAgent loses identity.

```go
// tools/interface.go — ToolContext gains:
type ToolContext struct {
    // ... existing fields ...
    UserID int64    // canonical user ID (from IdentityResolver)
    Role   string   // user role ("admin" | "user")
}

// agent/engine_wire.go buildToolContext:
toolCtx.UserID = cfg.UserID
toolCtx.Role = cfg.Role

// agent/engine_wire.go buildSubAgentRunConfig:
// SubAgent inherits from parentCtx (which is *tools.ToolContext)
// No extra wiring needed — parentCtx already carries UserID/Role.
```

## User Creation Flows

### 5 种用户创建场景

| 触发方式 | channel | channel_user_id | 何时创建 canonical user |
|---|---|---|---|
| Web 注册 | `web` | `web-{id}` | 注册成功后，`web_users` INSERT 后立即 |
| 飞书首次消息 | `feishu` | `ou_xxx` | 第一条消息到达，`Resolve` 未命中时 |
| CLI 本地启动 | `cli` | `cli_user` | migration v45 种子数据（user 1, admin） |
| Runner token 连接 | `cli` | token 对应的 senderID | `validateCLIToken` 后 `Resolve` |
| Admin 手动创建 | 任意 | — | `POST /api/admin/users` |

### Web 注册流程（改造后）

```
用户填表 → POST /api/auth/register
  1. INSERT INTO web_users (username, password) → web_user_id
  2. IdentityResolver.Resolve("web", "web-{web_user_id}") → 创建 canonical user
  3. 如果是 invite-only，验证邀请码
  4. 如果 users 表为空，role = 'admin'（第一个用户）
  5. 返回 session cookie
```

### 飞书首次消息

```
飞书用户 ou_xxx 发消息
  → channel/feishu/feishu.go handleEvent
  → IdentityResolver.Resolve("feishu", "ou_xxx")
  → 未命中 → 创建 user (role='user') + identity
  → userID 注入到 InboundMessage.Metadata
  → agent 层从 metadata 读取 userID
```

## Cross-Channel Account Linking

### 场景：三个 channel 各自已有独立 user_id

```
迁移后自动创建的状态：
  CLI:    user_id = 1 (admin), identity (cli, cli_user)
  Web:    user_id = 2,         identity (web, web-4)
  飞书:   user_id = 3,         identity (feishu, ou_alice)

各自已有独立数据：
  user 1: runners(2个), subscriptions(1个), settings(5条), sessions(10个)
  user 2: runners(0个), subscriptions(1个), settings(3条), sessions(5个)
  user 3: runners(1个), subscriptions(0个), settings(2条), sessions(8个)
```

### 关联 = 选一个 primary + 合并全部资产

```
Alice 在 Web (user 2) 发起关联：
  → POST /api/account/link-code → 生成码 "AB3X9K"
  → Alice 在 CLI (user 1) 执行 /link-account AB3X9K

系统检测到 CLI 的 identity (cli, cli_user) 已存在(绑定到 user 1)：
  → 这是 MERGE 操作，不是简单关联
  → 返回合并预览
  → 用户确认后执行合并
```

### 合并策略 (修正了 CASCADE 误删风险)

> **Roundtable fix**: Original design's `DELETE FROM users WHERE id = source` triggers `ON DELETE CASCADE` on `user_identities`, deleting ALL of source's identities. Fix: migrate ALL identities first, then delete.

```sql
-- target_user = user 1 (CLI/admin), source_user = user 2 (Web)

BEGIN TRANSACTION;

-- 1. Migrate ALL identities from source to target (BEFORE deleting source user)
UPDATE user_identities SET user_id = 1 WHERE user_id = 2;

-- 2. Migrate asset tables
UPDATE user_llm_subscriptions SET user_id = 1 WHERE user_id = 2;
UPDATE runners SET user_id = 1 WHERE user_id = 2;
UPDATE user_settings SET user_id = 1 WHERE user_id = 2;
UPDATE user_default_model SET user_id = 1 WHERE user_id = 2;
UPDATE user_chats SET user_id = 1 WHERE user_id = 2;
UPDATE tenants SET owner_user_id = 1 WHERE owner_user_id = 2;
UPDATE cron_jobs SET user_id = 1 WHERE user_id = 2;
UPDATE event_triggers SET user_id = 1 WHERE user_id = 2;

-- 3. Conflict resolution:
--    a) user_default_model: keep target's, delete source's
DELETE FROM user_default_model WHERE user_id = 1 AND sender_id != '';
-- (target already has its own; source's was just UPDATE'd to target —
--  if both had one, we keep whichever was last UPDATE'd; for deterministic
--  behavior, DELETE source's BEFORE the UPDATE above and re-INSERT if needed)

--    b) runners same-name conflict: suffix source's
UPDATE runners SET name = name || ' (web)' WHERE user_id = 1 AND name IN (
    SELECT name FROM runners WHERE user_id = 1
    GROUP BY name HAVING COUNT(*) > 1
);

--    c) user_settings same (channel, key): keep target's
DELETE FROM user_settings WHERE user_id = 1 AND (channel, key) IN (
    SELECT channel, key FROM user_settings WHERE user_id = 1
    GROUP BY channel, key HAVING COUNT(*) > 1
);

-- 4. Delete source user (CASCADE is now safe — identities already moved)
DELETE FROM users WHERE id = 2;

COMMIT;
```

### 三路合并（CLI + Web + 飞书）

```
Step 1: link Web (user 2) → CLI (user 1)
  → 合并 user 2 资产到 user 1，删除 user 2
  → user 1 现在有: CLI + Web 的全部资产

Step 2: link 飞书 (user 3) → CLI (user 1)
  → 合并 user 3 资产到 user 1，删除 user 3
  → user 1 现在有: CLI + Web + 飞书 的全部资产
```

### 合并并发安全

> **Roundtable decision**: SQLite does not support row-level locks (`SELECT FOR UPDATE` is a no-op). Use Go-level mutex keyed by the user pair.

```go
var mergeMu sync.Map // key: "min(sourceID,targetID)-max(sourceID,targetID)"

func mergeUsers(sourceID, targetID int64) error {
    lockKey := fmt.Sprintf("%d-%d", min(sourceID, targetID), max(sourceID, targetID))
    actual, _ := mergeMu.LoadOrStore(lockKey, &sync.Mutex{})
    mu := actual.(*sync.Mutex)
    mu.Lock()
    defer mu.Unlock()
    // ... execute merge transaction ...
}
```

### API Design

```
POST /api/account/link-code
  → 生成 6 位随机码，存入 link_codes 表 (user_id, code, expires_at=now+5min)
  → 返回 {code: "AB3X9K", expires_in: 300}

POST /api/account/link  {code: "AB3X9K"}
  → 查 link_codes 表获取 target_user_id (检查未过期)
  → 获取当前 channel identity (channel, channel_user_id)
  → 情况 1: 当前 identity 不存在 → 直接 INSERT user_identities
  → 情况 2: 当前 identity 已存在(另一个 user) → MERGE

  → 如果是 MERGE，返回合并预览：
    {
      "action": "merge",
      "source_user_id": 2,
      "target_user_id": 1,
      "assets": {
        "subscriptions": 1,
        "runners": 0,
        "settings": 3,
        "sessions": 5
      },
      "conflicts": {
        "default_model": "keep_target",
        "runners_duplicate": [],
        "settings_duplicate": ["cli:max_concurrency"]
      }
    }
    → 需要 confirm=true 才执行

POST /api/account/link  {code: "AB3X9K", confirm: true}
  → 执行合并事务

GET /api/account/identities
  → SELECT * FROM user_identities WHERE user_id = ?
  → 返回 [{channel: "cli", channel_user_id: "cli_user", linked_at: ...}, ...]

DELETE /api/account/identities/{id}
  → DELETE FROM user_identities WHERE id = ? AND user_id = ?
  → 用户解除某个 channel 的关联（不影响已迁移的资产）

POST /api/admin/users/{id}/role  {role: "admin"}
  → UPDATE users SET role = ? WHERE id = ?
  → admin only

GET /api/admin/users
  → SELECT * FROM users ORDER BY id
  → admin only
```

### Link Code 安全

> **Roundtable (security)**:
> - 6 位码 entropy = 36^6 ≈ 2.2B，暴力破解需 ~1000 次/秒 × 5min TTL ≈ 300K 次尝试，概率 0.014% — 可接受但不够好
> - 改为 8 位 base32 码（entropy = 32^8 ≈ 1T），暴力概率 0.000018%
> - 单次使用：验证后立即删除 code
> - 速率限制：同一 user_id 生成 code 间隔 ≥ 10s

### 冲突规则

| 资产 | 冲突场景 | 策略 |
|---|---|---|
| `user_llm_subscriptions` | 两个 user 各有订阅 | **全部保留**（不冲突，user_id 只是 owner） |
| `runners` | 同名 runner | source 的自动加后缀 `(web)` / `(feishu)` |
| `user_default_model` | 两个 user 各有一个 | 保留 target（primary）的 |
| `user_settings` | 同 `(channel, key)` | 保留 target 的，删 source 的 |
| `user_chats` | 同 `(channel, chat_id)` | 不可能冲突（UNIQUE 约束），直接迁移 |
| `tenants` | 同 `(channel, chat_id)` | 不可能冲突，直接迁移 owner_user_id |
| `cron_jobs` | 按 sender_id 关联的 | 迁移 user_id |

### 安全措施

1. **合并前快照**：合并前把 source user 的数据导出为 JSON 存到 `~/.xbot/backups/merge-user-{id}-{timestamp}.json`
2. **不可逆**：合并后 source user 被删除，不可撤销（但有备份）
3. **权限**：只能合并自己拥有的 source user（target 必须是当前登录用户）
4. **预览**：必须先 dry-run 预览冲突，用户确认后才执行

## Role-Based Access Control

### Admin 判断统一

所有 4 处 admin 判断统一为 `IsAdmin(userID)`：

| 位置 | 旧逻辑 | 新逻辑 |
|---|---|---|
| `serverapp/server.go:1167` | `authSenderID == "admin"` | `identityResolver.IsAdmin(userID)` |
| `web_api.go:1813` | `senderID == "admin" \|\| userID == 1` | `identityResolver.IsAdmin(userID)` |
| `web_api.go:1787` | `senderID == "admin" \|\| web-{id} 且 id==1` | `identityResolver.IsAdmin(userID)` |
| `main.go:1402` | `return true` | `identityResolver.IsAdmin(userID)` |

### SandboxRouter & Role

`SandboxRouter.SetIsAdminFn` 当前接受 `func(userID string) bool`。改为 `func(userID int64) bool`，由 `IdentityResolver.IsAdmin` 实现。

`DeniedSandbox` 的 web-user 限制改为基于 `role` 而非 `senderID` 前缀：非 admin 用户在没有 runner 时仍走 `DeniedSandbox`，admin 用户走 `NoneSandbox`。

### Role 可扩展性

`role` 是 TEXT 列带 CHECK 约束，当前值 `'admin'` / `'user'`。未来扩展时改 CHECK 约束：

```sql
-- Future: add 'viewer' and 'developer' roles
-- ALTER TABLE users DROP CHECK(role);
-- ALTER TABLE users ADD CHECK(role IN ('admin', 'user', 'viewer', 'developer'));
```

权限检查从 `if isAdmin(userID)` 扩展为 `if hasRole(userID, "admin")` 或更细粒度的 permission check。

## Runner Cross-Channel Sharing

`runners` 表的 `user_id TEXT` 改为 `user_id INTEGER`（FK `users.id`）。关联后：
- CLI 注册的 runner → `user_id = 1`
- Web 用户关联到 user 1 → 看到 user 1 的所有 runner
- 飞书用户关联到 user 1 → 同样看到

`SandboxRouter.SandboxForUser` 从 `senderID string` 改为 `userID int64` 路由。`RunnerTokenStore` 所有方法加 `userID int64` 参数。

## Cross-Channel Session Visibility

`tenants` 表加 `owner_user_id` 列。`GetOrCreateTenantID` 时从 `InboundMessage.Metadata["user_id"]` 读取并注入。查询用户所有 session：

```sql
SELECT * FROM tenants WHERE owner_user_id = ? AND channel != '_shared'
```

CLI 用户关联后能在 Web 看到自己的 CLI session 历史，反之亦然。

### 多租户兼容

飞书和 Web 的多租户模型不变——`tenants` 表仍然是 `(channel, chat_id)` 分隔。但每个 tenant 有 `owner_user_id`：
- 飞书群 A 的 tenant → `owner_user_id = 1`（群管理员的 canonical user）
- 飞书群 B 的 tenant → `owner_user_id = 2`（另一个群管理员）
- Web 用户关联到 user 1 → 能看到群 A 的 session，看不到群 B 的

权限隔离不变，只是多了一层"谁拥有这个 tenant"。

## Implementation Phases

### Phase 1: Foundation
- v45 migration: `users` + `user_identities` + `link_codes` 表 + 资产表加 `user_id` 列 + backfill
- `IdentityResolver` 结构 + `Resolve` + `IsAdmin` + `SetRole`
- 在 WS/HTTP/CLI 入口处调用 `Resolve`，注入 `userID` 到 `InboundMessage.Metadata`
- `ToolContext` + `RunConfig` 加 `UserID` + `Role` 字段
- Admin 判断统一为 `IsAdmin(userID)`
- `SandboxRouter.SetIsAdminFn` 改为 `int64` 参数

### Phase 2: Asset Migration
- `LLMFactory` 的 `senderID` 参数改为 `userID int64`
- `RunnerTokenStore` 的 `userID string` 改为 `userID int64`
- `user_settings` 读写改用 `user_id`
- `user_default_model` 读写改用 `user_id`
- `user_chats` 读写改用 `user_id`
- `tenants` 的 `owner_user_id` 注入

### Phase 3: Cross-Channel Linking
- `/api/account/link-code` + `/api/account/link` API
- 合并逻辑（dry-run preview + confirm + transaction + Go mutex）
- CLI `/link-account` 命令
- `/api/account/identities` 查看 + 解除关联
- `link_codes` 过期定时清理

### Phase 4: Admin Management
- `/api/admin/users` CRUD
- `/api/admin/users/{id}/role` 角色管理
- Web UI 管理面板

### Phase 5: Deprecation
- 标记 `sender_id` 列为 deprecated
- 新代码完全不读 `sender_id`
- 最终删除旧列（major version bump）

## Risks & Mitigations

| 风险 | 缓解 |
|---|---|
| 合并操作数据丢失 | 合并前 JSON 备份 + dry-run 预览 + 用户确认 |
| `Resolve` TOCTOU 竞争 | `INSERT OR IGNORE` + re-SELECT 模式 |
| 合并后 CASCADE 误删 identity | 先 `UPDATE user_identities SET user_id = target`，再 `DELETE FROM users` |
| `__system__` 订阅 backfill NULL | migration step 3 创建 `system/__system__` identity 映射到 user 1 |
| 合并并发 | Go `sync.Map` keyed by `min(sourceID, targetID)` |
| migration 失败 | 整个 migration 在事务中执行，失败回滚 |
| 旧代码用 sender_id，新代码用 user_id | Phase 1 一次 backfill 全部资产表，新代码直接读 `user_id INTEGER` |
| link_code 暴力破解 | 8 位 base32 + 5min TTL + 单次使用 + 速率限制 |

## What Doesn't Change

- `tenants` 的 `(channel, chat_id)` 唯一键不变——多租户隔离保持
- `session_messages` 不动——消息仍按 tenant_id 存储
- 飞书群聊模型不变——群聊的 tenant 仍由 `(feishu, chat_id)` 标识
- `user_settings` 的 `(channel, sender_id, key)` 唯一键不变——但新增 `user_id` 列用于跨 channel 查询
- `web_users` 表不动——仍是 Web 注册的用户名/密码存储
- 飞书-Web linking 的旧 `user_settings` hack 保留向后兼容——migration 把它迁移到 `user_identities`

## Final Effect

```
用户 Alice:
  CLI:  senderID = "cli_user"   → user_id = 1, role = admin
  Web:  senderID = "web-4"      → user_id = 1 (linked via /link-account)
  飞书: senderID = "ou_alice"   → user_id = 1 (linked via /link-account)

Alice 在 CLI 注册了 runner "my-macbook"
  → runners 表: user_id = 1
  → Alice 在 Web 看到这个 runner ✓
  → Alice 在飞书也能用这个 runner ✓

Alice 在 Web 创建了 session "debug-auth"
  → tenants 表: owner_user_id = 1
  → Alice 在 CLI /sessions 看到这个 session ✓
  → Alice 在飞书也能访问这个 session 的历史 ✓

Admin 管理:
  POST /api/admin/users/2/role {role: "admin"}  → Bob 成为 admin
  GET /api/admin/users  → 看到 Alice(1), Bob(2), Charlie(3)...
```
