# Plan: 统一 Bundle 打包与分发系统

## Summary

将 Skill、Agent、Plugin 三种扩展统一为可选的 `Bundle` 打包格式，通过 `xbot.json` 清单声明包含的内容类型。支持导出 `.xbot.zip` 压缩包和从压缩包安装，安装时按 type 分发到各自子系统。向后兼容现有单文件格式（SKILL.md / agent.md / plugin.json），不改变各子系统的加载和执行逻辑。

## 现状分析

### 三系统对比

| 维度 | Skill | Agent | Plugin |
|------|-------|-------|--------|
| 包格式 | 目录 + `SKILL.md` | 单个 `.md` 文件 | 目录 + `plugin.json` |
| 元数据 | YAML frontmatter | YAML frontmatter | JSON manifest |
| 内容 | Markdown 指导文本 | Markdown 系统提示词 | 可执行代码/脚本 |
| 加载方式 | LLM 按需 Read | SubAgent spawn | 进程激活 |
| 市场集成 | ✅ RegistryManager | ✅ RegistryManager | ❌ 仅本地 InstallPlugin |
| 版本管理 | ❌ | ❌ | ✅ Semver |
| 依赖管理 | ❌ | ❌ | ✅ PluginDependency |
| 权限模型 | ❌ | ❌ | ✅ Permissions |
| 沙箱适配 | ✅ | ✅ | ❌ 主进程 |

### 关键代码位置

| 文件 | 作用 |
|------|------|
| `agent/registry.go` | `RegistryManager` — Publish/Install/Uninstall/Browse/ListMy |
| `storage/sqlite/registry.go` | `SharedSkillRegistry` — DB CRUD |
| `storage/sqlite/schema.go:130` | `shared_registry` 表定义（CHECK type IN ('skill','agent')） |
| `storage/sqlite/migrations.go:577` | v12→v13 建表，v13→v14 加 UNIQUE 约束 |
| `agent/command_builtin.go:668` | `registerBuiltinCommands` — 6 个市场命令 |
| `plugin/manager.go:1022` | `InstallPlugin` — 本地安装 |
| `plugin/manifest.go:21` | `LoadManifest` — 解析 plugin.json |
| `channel/web/web_api.go:680` | Web 市场 REST API |
| `channel/feishu/feishu_settings.go:2010` | 飞书市场卡片 |
| `serverapp/callbacks.go:120` | `registryCallbacks` — 渠道→Agent 桥接 |

### 核心约束

- `shared_registry` 表 `type` 列有 `CHECK(type IN ('skill', 'agent'))` 约束，需迁移
- `RegistryManager` 的 Publish/Install 按 type 硬编码分发到 `publishSkill`/`publishAgent`，需扩展
- Plugin 安装路径 `~/.xbot/plugins/<id>/` 与 Skill/Agent 的 sandbox-aware 路径不同
- Plugin 执行代码，发布到市场需安全审查（至少展示 permissions）
- 当前 schemaVersion = 44

## 设计方案

### 1. Bundle 包格式

```
my-bundle.xbot.zip
├── xbot.json              ← 统一清单（必需）
├── skills/                ← Skill 内容（可选）
│   └── my-skill/
│       └── SKILL.md
├── agents/                ← Agent 内容（可选）
│   └── my-agent.md
├── plugins/               ← Plugin 内容（可选）
│   └── my-plugin/
│       ├── plugin.json
│       └── entry.sh
└── README.md              ← 可选文档
```

### 2. xbot.json 清单格式

```jsonc
{
  "schema": 1,
  "id": "my-bundle",
  "name": "My Bundle",
  "version": "1.0.0",
  "author": "user@example.com",
  "description": "Bundle 描述",
  "homepage": "https://...",
  "license": "MIT",
  "contents": [
    {
      "type": "skill",
      "name": "my-skill",
      "source": "skills/my-skill/",
      "description": "..."
    },
    {
      "type": "agent",
      "name": "my-agent",
      "source": "agents/my-agent.md",
      "model": "swift",
      "tools": ["Grep", "Read"]
    },
    {
      "type": "plugin",
      "name": "my-plugin",
      "source": "plugins/my-plugin/",
      "runtime": "stdio",
      "permissions": ["shell", "fs"],
      "dependencies": [{"id": "other-plugin", "version": ">=1.0"}]
    }
  ]
}
```

**设计决策**：
- `xbot.json` 是**可选的打包层**，不替代现有 SKILL.md / agent.md / plugin.json
- 单个 Skill/Agent/Plugin 仍可直接发布到市场（现有方式不变）
- Bundle 是"组合分发"的增强能力，一个 Bundle 可包含多个不同类型的 item
- Bundle 本身也作为一种 `type='bundle'` 条目存在于市场

### 3. DB 迁移：v44 → v45

`shared_registry` 表 `type` 列 CHECK 约束需扩展：

```sql
-- v45: 扩展 type 列，支持 'plugin' 和 'bundle'
CREATE TABLE shared_registry_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL CHECK(type IN ('skill', 'agent', 'plugin', 'bundle')),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    author      TEXT NOT NULL,
    tags        TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL,
    sharing     TEXT NOT NULL DEFAULT 'private' CHECK(sharing IN ('private', 'public')),
    version     TEXT NOT NULL DEFAULT '',          -- 新增：Semver 版本号
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE(type, name, author)
);
-- 复制数据、DROP 旧表、RENAME、重建索引
```

新增 `version` 列用于版本管理。旧数据 `version=''` 表示无版本信息。

### 4. 新增 `agent/bundle.go` — BundlePackager

```go
// BundleManifest 是 xbot.json 的 Go 结构体
type BundleManifest struct {
    Schema      int             `json:"schema"`
    ID          string          `json:"id"`
    Name        string          `json:"name"`
    Version     string          `json:"version"`
    Author      string          `json:"author"`
    Description string          `json:"description"`
    Homepage    string          `json:"homepage,omitempty"`
    License     string          `json:"license,omitempty"`
    Contents    []BundleContent `json:"contents"`
}

type BundleContent struct {
    Type        string            `json:"type"`         // skill | agent | plugin
    Name        string            `json:"name"`
    Source      string            `json:"source"`       // 包内相对路径
    Description string            `json:"description,omitempty"`
    // agent 专属
    Model       string            `json:"model,omitempty"`
    Tools       []string          `json:"tools,omitempty"`
    // plugin 专属
    Runtime     string            `json:"runtime,omitempty"`
    Permissions []string          `json:"permissions,omitempty"`
    Dependencies []BundleDep      `json:"dependencies,omitempty"`
}

// BundlePackager 处理打包和解包
type BundlePackager struct {
    workDir string
    sandbox tools.Sandbox
}

// Pack 将指定 items 打包为 .xbot.zip
func (bp *BundlePackager) Pack(items []PackItem, output string) error

// Unpack 解压 .xbot.zip 到临时目录，返回 BundleManifest
func (bp *BundlePackager) Unpack(zipPath string) (*BundleManifest, string, error)

// Validate 校验包完整性（文件存在性、清单格式）
func (bp *BundlePackager) Validate(manifest *BundleManifest, baseDir string) error
```

### 5. RegistryManager 扩展

在 `agent/registry.go` 中新增：

```go
// PublishBundle 将一个 Bundle 发布到市场
// 1. 遍历 contents，逐个快照到缓存
// 2. 写入 shared_registry（type='bundle'）
func (rm *RegistryManager) PublishBundle(manifestPath, author string) error

// InstallBundle 从市场安装一个 Bundle
// 1. GetByID → 获取 bundle 条目
// 2. 解压缓存中的 zip
// 3. 遍历 contents，按 type 分发：
//    skill → installSkill, agent → installAgent, plugin → PluginManager.InstallPlugin
func (rm *RegistryManager) InstallBundle(id int64, senderID string) error

// PackBundle 将本地 items 打包为 .xbot.zip
// 供 /app export 命令调用
func (rm *RegistryManager) PackBundle(items []PackItem, outputPath, author string) error

// InstallFromFile 从本地 .xbot.zip 文件安装
// 供 /app install <file> 命令调用
func (rm *RegistryManager) InstallFromFile(zipPath, senderID string) (*InstallResult, error)
```

**Plugin 集成**：`RegistryManager` 需持有 `*plugin.PluginManager` 引用。当前未持有，需在 `Agent` 初始化时注入。

### 6. 命令设计

新增 `/app` 命令（命名空间），替代现有 6 个扁平市场命令：

```
/app browse [skill|agent|plugin|bundle]   — 浏览市场
/app install <type> <id|file>             — 从市场或本地文件安装
/app uninstall <type> <name>              — 卸载
/app publish <type> <name>                 — 发布到市场
/app unpublish <type> <name>               — 取消发布
/app my [skill|agent|plugin|bundle]       — 查看我的
/app export <name> --skill <s> --agent <a> --plugin <p>  — 打包导出
```

**向后兼容**：旧命令 `/browse`、`/install`、`/publish` 等保留为 `/app` 的别名，一个版本后标记 deprecated。

### 7. 渠道层适配

#### Web API

| 端点 | 方法 | 说明 |
|------|------|------|
| `GET /api/market?type=bundle` | GET | 浏览 bundle 类型 |
| `POST /api/market/install` | POST | 已有，扩展 type 支持 bundle/plugin |
| `POST /api/market/pack` | POST | 新增：打包导出 |
| `POST /api/market/install-file` | POST | 新增：从上传的 zip 安装 |

#### 飞书卡片

`buildMarketTabContent` 新增 "📦 应用包" 标签页，展示 bundle 类型条目。安装/卸载按钮复用现有 callback。

### 8. Sandbox 适配

| 类型 | 无 Sandbox | Docker/Remote |
|------|-----------|---------------|
| Skill | `~/.xbot/skills/<user>/` | `<workspace>/skills/` |
| Agent | `~/.xbot/agents/<user>/` | `<workspace>/agents/` |
| Plugin | `~/.xbot/plugins/<id>/` | 同左（主进程） |
| Bundle 缓存 | `.xbot/registry/bundle/<name>/` | 同左（服务端） |

Bundle 的缓存目录在服务端（非 sandbox），与现有 skill/agent 缓存一致。安装时按 content type 分发到各自路径。

## Changes

### `storage/sqlite/migrations.go`
- What: 新增 `migrateV44ToV45`，重建 `shared_registry` 表扩展 CHECK 约束 + 新增 `version` 列
- Why: 支持 `type='plugin'` 和 `type='bundle'`，版本管理需要 `version` 列

### `storage/sqlite/schema.go`
- What: 更新 `shared_registry` 表定义的 CHECK 约束和新增 `version` 列
- Why: 新建 DB 需要使用最新 schema

### `storage/sqlite/registry.go`
- What: `SharedEntry` 结构体新增 `Version` 字段；`Publish` / `ListShared` / `SearchShared` / `GetByID` / `ListByAuthor` 的 SQL 和 scan 增加 `version` 列
- Why: 读写 version 字段

### `agent/bundle.go`（新增）
- What: `BundleManifest`、`BundleContent`、`BundlePackager` 结构体和方法
- Why: Bundle 打包/解包/校验的核心逻辑

### `agent/registry.go`
- What: 新增 `PublishBundle`、`InstallBundle`、`PackBundle`、`InstallFromFile` 方法；`RegistryManager` 新增 `pluginMgr *plugin.PluginManager` 字段
- Why: Bundle 的发布/安装逻辑，Plugin 集成需要 PluginManager 引用

### `agent/agent.go`
- What: `Agent` 初始化时将 `pluginMgr` 注入 `RegistryManager`
- Why: `RegistryManager` 需要调用 `PluginManager.InstallPlugin`

### `agent/command_builtin.go`
- What: 新增 `appCmd` 命令（子命令分发）；旧命令保留兼容
- Why: 统一市场命令入口

### `channel/callbacks.go`
- What: `RegistryCallbacks` 新增 `RegistryPack` 和 `RegistryInstallFile` 回调
- Why: Web/飞书渠道需要打包和从文件安装的能力

### `serverapp/callbacks.go`
- What: `registryCallbacks` 新增 `RegistryPack` / `RegistryInstallFile` 闭包
- Why: 桥接渠道回调到 `RegistryManager`

### `channel/web/web_api.go`
- What: `handleMarket` 扩展 type 过滤；新增 `handleMarketPack` / `handleMarketInstallFile`
- Why: Web 端支持 bundle 和文件安装

### `channel/web/web.go`
- What: 注册新路由 `/api/market/pack`、`/api/market/install-file`
- Why: 新 API 端点

### `channel/feishu/feishu_settings.go`
- What: `buildMarketTabContent` 新增 bundle 标签页
- Why: 飞书卡片展示 bundle 类型

## Risks

- **DB 迁移风险**：重建 `shared_registry` 表需复制数据，如果表很大可能耗时。缓解：现有数据量小（单机市场），迁移在启动时执行，影响可接受。
- **Plugin 安全风险**：Plugin 执行代码，从市场安装 plugin 需展示 permissions 并要求用户确认。缓解：安装前打印 permissions 列表，CLI 需输入 y 确认，Web/飞书需点击确认按钮。
- **Plugin 依赖解析**：Bundle 中的 plugin 可能依赖其他 plugin。缓解：Phase 1 仅支持 skill+agent，Phase 3 再纳入 plugin，届时实现依赖检查。
- **Sandbox 路径差异**：Plugin 安装到 `~/.xbot/plugins/`（主进程），Skill/Agent 安装到 sandbox workspace。缓解：`InstallBundle` 按 type 分发到各自路径，不混用。
- **向后兼容**：旧命令保留别名。缓解：一个版本后标记 deprecated，两个版本后移除。

## Definition of Done

### Phase 1：Bundle 打包与安装（skill + agent）
- [ ] `xbot.json` schema 定义完成，`BundleManifest` 结构体实现
- [ ] `BundlePackager.Pack` / `Unpack` / `Validate` 实现
- [ ] DB v45 迁移完成（CHECK 约束 + version 列）
- [ ] `RegistryManager.PackBundle` / `InstallFromFile` 实现（仅 skill + agent）
- [ ] `/app export` 和 `/app install <file>` 命令实现
- [ ] 单元测试：打包 → 解包 → 安装全流程
- [ ] `go build ./...` 通过
- [ ] `go test ./agent/... ./storage/...` 通过

### Phase 2：统一市场
- [ ] `RegistryManager.PublishBundle` / `InstallBundle` 实现
- [ ] `/app browse` / `/app publish` / `/app install <id>` 支持 bundle 类型
- [ ] Web API `/api/market?type=bundle` 可用
- [ ] 飞书市场卡片新增 bundle 标签页
- [ ] 旧命令 `/browse` / `/install` / `/publish` 等保留兼容
- [ ] 集成测试：发布 bundle → 浏览 → 安装全流程
- [ ] `go build ./...` 通过
- [ ] `go test ./...` 通过

### Phase 3：Plugin 纳入
- [ ] `RegistryManager` 持有 `PluginManager` 引用
- [ ] `InstallBundle` 支持 plugin content type（调用 `PluginManager.InstallPlugin`）
- [ ] 安装前展示 permissions 并要求确认
- [ ] Plugin 依赖检查（dependencies 字段）
- [ ] `/app export` 支持打包 plugin
- [ ] 集成测试：包含 plugin 的 bundle 安装全流程
- [ ] `go build ./...` 通过
- [ ] `go test ./...` 通过

### Phase 4：增强功能（可选）
- [ ] `/app` 旧命令标记 deprecated
- [ ] 签名验证（`xbot.sig`）
- [ ] 更新检测（对比已安装版本与市场版本）
- [ ] `xbot install <url>` 从 URL 安装

## Open Questions

1. **命名**：`/app` 还是 `/bundle` 还是 `/market`？建议 `/app`（简短、语义清晰，"应用"概念用户易理解）。
2. **Plugin 纳入时机**：Phase 1 先做 skill+agent 验证设计，Phase 3 再纳入 plugin？还是 Phase 1 就做？建议分阶段，降低风险。
3. **旧命令废弃时间线**：保留几个版本？建议 2 个版本后移除。
4. **文件扩展名**：`.xbot.zip` 还是 `.xbot`？建议 `.xbot.zip`（明确是 zip 格式，用户可手动解压检查）。
