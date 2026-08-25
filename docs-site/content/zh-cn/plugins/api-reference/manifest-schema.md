---
title: "Manifest Schema"
weight: 2
---

`plugin.json` manifest 文件的完整参考，基于 `plugin/plugin.go`（`PluginManifest`）与 `plugin/manifest.go`（验证逻辑）。

## 顶层字段

| 字段 | JSON Key | 类型 | 必填 | 说明 |
|------|----------|------|------|------|
| ID | `id` | string | ✅ | 全局唯一插件标识。必须匹配 `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`（防止路径穿越/注入）。推荐反向 DNS 命名。 |
| Name | `name` | string | ✅ | 人类可读的插件名称。 |
| Version | `version` | string | ✅ | 严格 semver `MAJOR.MINOR.PATCH`（如 `"1.0.0"`）。 |
| Description | `description` | string | ✅ | 插件功能简述。 |
| Author | `author` | string | | 插件作者或组织。 |
| Homepage | `homepage` | string | | 源码或文档 URL。 |
| Runtime | `runtime` | string | | 执行环境：`"native"`、`"stdio"`、`"grpc"`（stdio 的历史别名）、`"wasm"`、`"script"`。空值默认 `"native"`。 |
| Entry | `entry` | string | | 入口。script 运行时：要执行的命令（如 `"bash my-script.sh"`）；stdio 运行时：启动插件进程的命令。默认/回退——平台专属 entry 优先。 |
| EntryWindows | `entry_windows` | string | | Windows 专属入口覆盖。 |
| EntryDarwin | `entry_darwin` | string | | macOS 专属入口覆盖。 |
| EntryLinux | `entry_linux` | string | | Linux 专属入口覆盖。 |
| Executable | `executable` | string | | 启动插件进程的命令（gRPC 运行时）。设置后优先于 `entry`。 |
| Args | `args` | string[] | | 传给 `executable` 的命令行参数。 |
| ActivationEvents | `activation_events` | string[] | | 触发插件激活的事件。格式：`"onStart"`、`"onTool:<name>"`、`"onHook:<event>"`、`"onCommand:<cmd>"`。空 → 默认 `["onStart"]`。 |
| Permissions | `permissions` | string[] | | 所需能力。manifest 中允许 `"*"` 通配符。未知权限校验失败。 |
| Contributes | `contributes` | object | | 声明插件提供的能力（见下文）。 |
| Dependencies | `dependencies` | array | | 依赖的其他插件。目前仅做格式校验；版本解析是未来工作。 |
| Web | `web` | object | | 前端 ESM 插件模块声明（v2 Web 插件运行时）。 |
| Timeout | `timeout` | string | | Go duration 字符串（`"30s"`、`"1m"`、`"500ms"`）。最大 5 分钟。零/空 → `DefaultPluginTimeout`（30s）。 |

> **验证**：stdio/grpc 运行时插件必须提供非空 `entry` 或 `executable`。

## `web` 对象（`WebPluginDecl`）

| 字段 | JSON Key | 类型 | 说明 |
|------|----------|------|------|
| Entry | `entry` | string | 前端模块路径，相对插件 `web/` 目录（如 `"index.js"`）。托管于 `/plugins/<id>/web/<entry>`。 |
| Contributes | `contributes` | JSON | 不透明 JSON，原样透传给前端运行时。前端是贡献点语义的唯一权威校验门（形状、权限↔能力对应、ID 唯一性）。后端仅做传输层检查。 |

## `dependencies[]` 对象（`PluginDependency`）

| 字段 | JSON Key | 类型 | 说明 |
|------|----------|------|------|
| ID | `id` | string | 所需插件的唯一标识。必须是合法插件 ID。 |
| Version | `version` | string | 版本约束。接受宽松 semver 格式（`"^1.0.0"`、`">=1.0.0"`、`"~1.0.0"`、`"1.x"`、`"*"`）。 |

## `contributes` 对象（`PluginContributes`）

| 字段 | JSON Key | 类型 | 说明 |
|------|----------|------|------|
| Tools | `tools` | array | 工具贡献点。每项：`{ name（必填）, description（必填）, input_schema（object）}`。 |
| Hooks | `hooks` | array | Hook 订阅。每项：`{ event（必填，必须是合法 hook 事件）, matcher（工具名模式，"" = 全部）}`。 |
| ContextEnrichers | `context_enrichers` | array | 每项：`{ name, description }`。 |
| Commands | `commands` | array | 斜杠命令。每项：`{ name（如 "/deploy"）, description }`。 |
| Crons | `crons` | array | 定时任务（见下文）。 |
| Themes | `themes` | array | 每项：`{ id, file }` — `file` 相对插件目录（如 `"themes/dracula.json"`）。 |
| Overlays | `overlays` | array | 全屏覆盖层。每项：`{ id, description }`。 |
| Configuration | `configuration` | object | 用户可配置设置（见下文）。 |
| UI | `ui` | array | Widget 槽位保留（见下文）。 |

### `crons[]` 对象（`CronContribution`）

| 字段 | JSON Key | 类型 | 说明 |
|------|----------|------|------|
| Message | `message` | string | 触发时发送给 agent 的消息。 |
| CronExpr | `cron_expr` | string | Cron 表达式（可选）。 |
| EverySeconds | `every_seconds` | int | 间隔秒数（可选）。 |
| At | `at` | string | 绝对时间点（可选）。 |
| DelaySeconds | `delay_seconds` | int | 相对延迟秒数（可选）。 |

### `configuration` 对象（`ConfigurationContribution`）

| 字段 | JSON Key | 类型 | 说明 |
|------|----------|------|------|
| Title | `title` | string | 配置分区的人类可读标题。 |
| Properties | `properties` | map | 属性 key → `ConfigProperty` 的映射。 |

用户在 `~/.xbot/plugins/<id>/config.json` 中覆盖这些设置。manifest 的 `default` 值作为合并配置的种子。

### `configuration.properties.*`（`ConfigProperty`）

| 字段 | JSON Key | 类型 | 说明 |
|------|----------|------|------|
| Type | `type` | string | JSON schema 类型：`"string"`、`"number"`、`"boolean"`、`"select"`、`"multiselect"`。 |
| Label | `label` | string | 显示名。为空时回退到属性 key。 |
| Description | `description` | string | 属性用途说明。 |
| Default | `default` | any | 无用户配置时的默认值。 |
| Options | `options` | array | `select`/`multiselect` 的可选项。每项：`{ label, value }`。 |
| Section | `section` | string | 在设置 UI 中将属性归入命名分组。 |
| Secret | `secret` | bool | 敏感值，UI 中以掩码显示。 |
| Placeholder | `placeholder` | string | 文本输入框提示。 |
| Required | `required` | bool | 值必须设置。 |
| Minimum | `minimum` | float | `number` 类型的包含下界。 |
| Maximum | `maximum` | float | `number` 类型的包含上界。 |

### `ui[]` 对象（`UISlotContribution`）

| 字段 | JSON Key | 类型 | 说明 |
|------|----------|------|------|
| ID | `id` | string | 插件内唯一 widget ID。用作运行时更新的 key。 |
| Slot | `slot` | string | 目标区域：`titleBarLeft`、`titleBarRight`、`statusBarLeft`、`statusBarRight`、`infoBar`、`footer`、`toolHint`。 |
| Priority | `priority` | int | 区域内排序（越小越靠前/左）。默认 100。 |
| Description | `description` | string | 该 widget 显示内容的人类可读说明。 |
| RefreshInterval | `refresh_interval` | string | 建议轮询间隔（如 `"30s"`）。仅建议——优先使用推送式 `UpdateWidget`。 |
| Triggers | `triggers` | string[] | 触发即时脚本运行的 hook 匹配器。格式：`"EventName:Matcher"`（如 `"PostToolUse:Shell*"`）。仅 script 运行时生效。 |
| Sync | `sync` | bool | hook 触发器同步运行（在 hook goroutine 内联执行）。默认 false（异步）。 |
| Interactive | `interactive` | bool | widget 支持用户操作（v2）。默认 false。 |

**`ui[]` 验证规则**：
- 需要 `"ui.contribute"` 权限（或 `"*"`）。
- 每插件最多 10 个 widget。
- 插件内 widget ID 必须唯一。
- 非法 slot 名校验失败。

## 校验和验证

`plugin.json` 旁的 `plugin.sha256` 可存放 manifest 的 SHA256。通过 `LoadManifestWithOptions(dir, {VerifyChecksum: true})` 启用验证。接受 `"hash"` 或 `"hash  filename"`（GNU coreutils）格式。

## 发现机制

插件通过扫描目录（默认 `~/.xbot/plugins` 与 `~/.xbot/plugins/builtin`）下包含合法 `plugin.json` 的子目录被发现。非法 manifest 记录警告并跳过。
