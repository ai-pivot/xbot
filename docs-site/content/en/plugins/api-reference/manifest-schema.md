---
title: "Manifest Schema"
weight: 2
---

Complete reference for the `plugin.json` manifest file, based on `plugin/plugin.go` (`PluginManifest`) and `plugin/manifest.go` (validation).

## Top-Level Fields

| Field | JSON Key | Type | Required | Description |
|-------|----------|------|----------|-------------|
| ID | `id` | string | ✅ | Unique plugin identifier. Must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` (prevents path traversal/injection). Reverse DNS recommended. |
| Name | `name` | string | ✅ | Human-readable plugin name. |
| Version | `version` | string | ✅ | Strict semver `MAJOR.MINOR.PATCH` (e.g. `"1.0.0"`). |
| Description | `description` | string | ✅ | Short summary of what the plugin does. |
| Author | `author` | string | | Plugin author or organization. |
| Homepage | `homepage` | string | | URL to source or docs. |
| Runtime | `runtime` | string | | Execution environment: `"native"`, `"stdio"`, `"grpc"` (historical alias for stdio), `"wasm"`, `"script"`. Empty defaults to `"native"`. |
| Entry | `entry` | string | | Entry point. For script runtime: command to execute (e.g. `"bash my-script.sh"`). For stdio runtime: command to start the plugin process. Default/fallback — platform entries take precedence. |
| EntryWindows | `entry_windows` | string | | Windows-specific entry override. |
| EntryDarwin | `entry_darwin` | string | | macOS-specific entry override. |
| EntryLinux | `entry_linux` | string | | Linux-specific entry override. |
| Executable | `executable` | string | | Command to start the plugin process (gRPC runtime). Takes precedence over `entry` when set. |
| Args | `args` | string[] | | Command-line arguments passed to `executable`. |
| ActivationEvents | `activation_events` | string[] | | Events that trigger activation. Formats: `"onStart"`, `"onTool:<name>"`, `"onHook:<event>"`, `"onCommand:<cmd>"`. Empty → defaults to `["onStart"]`. |
| Permissions | `permissions` | string[] | | Required capabilities. `"*"` is a wildcard allowed in manifests. Unknown permissions fail validation. |
| Contributes | `contributes` | object | | Declares what the plugin provides (see below). |
| Dependencies | `dependencies` | array | | Other plugins this plugin depends on. Only format validation currently; version resolution is future work. |
| Web | `web` | object | | Frontend ESM plugin module declaration (v2 web plugin runtime). |
| Timeout | `timeout` | string | | Go duration string (`"30s"`, `"1m"`, `"500ms"`). Max 5 minutes. Zero/empty → `DefaultPluginTimeout` (30s). |

> **Validation**: `entry` or `executable` must be non-empty for stdio/grpc runtime plugins.

## `web` Object (`WebPluginDecl`)

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| Entry | `entry` | string | Frontend module path relative to the plugin's `web/` dir (e.g. `"index.js"`). Served at `/plugins/<id>/web/<entry>`. |
| Contributes | `contributes` | JSON | Opaque JSON blob passed verbatim to the frontend runtime. The frontend is the single authoritative gate for contribution semantics. |

## `dependencies[]` Object (`PluginDependency`)

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| ID | `id` | string | Unique identifier of the required plugin. Must be a valid plugin ID. |
| Version | `version` | string | Version constraint. Accepts loose semver formats (`"^1.0.0"`, `">=1.0.0"`, `"~1.0.0"`, `"1.x"`, `"*"`). |

## `contributes` Object (`PluginContributes`)

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| Tools | `tools` | array | Tool contributions. Each: `{ name (required), description (required), input_schema (object) }`. |
| Hooks | `hooks` | array | Hook subscriptions. Each: `{ event (required, must be a valid hook event), matcher (tool name pattern, "" = all) }`. |
| ContextEnrichers | `context_enrichers` | array | Each: `{ name, description }`. |
| Commands | `commands` | array | Slash commands. Each: `{ name (e.g. "/deploy"), description }`. |
| Crons | `crons` | array | Scheduled tasks (see below). |
| Themes | `themes` | array | Each: `{ id, file }` — `file` is relative to plugin dir (e.g. `"themes/dracula.json"`). |
| Overlays | `overlays` | array | Full-screen overlays. Each: `{ id, description }`. |
| Configuration | `configuration` | object | User-configurable settings (see below). |
| UI | `ui` | array | Widget slot reservations (see below). |

### `crons[]` Object (`CronContribution`)

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| Message | `message` | string | Message sent to the agent when triggered. |
| CronExpr | `cron_expr` | string | Cron expression (optional). |
| EverySeconds | `every_seconds` | int | Interval in seconds (optional). |
| At | `at` | string | Absolute time point (optional). |
| DelaySeconds | `delay_seconds` | int | Relative delay in seconds (optional). |

### `configuration` Object (`ConfigurationContribution`)

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| Title | `title` | string | Human-readable title for the settings section. |
| Properties | `properties` | map | Map of property key → `ConfigProperty`. |

Users override these settings in `~/.xbot/plugins/<id>/config.json`. Manifest `default` values seed the merged config.

### `configuration.properties.*` (`ConfigProperty`)

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| Type | `type` | string | JSON schema type: `"string"`, `"number"`, `"boolean"`, `"select"`, `"multiselect"`. |
| Label | `label` | string | Display name. Falls back to the property key when empty. |
| Description | `description` | string | Purpose of the property. |
| Default | `default` | any | Default value when no user configuration exists. |
| Options | `options` | array | Choices for `select`/`multiselect`. Each: `{ label, value }`. |
| Section | `section` | string | Group property under a named section in the settings UI. |
| Secret | `secret` | bool | Mask the value in UI. |
| Placeholder | `placeholder` | string | Hint text for text inputs. |
| Required | `required` | bool | Value must be set. |
| Minimum | `minimum` | float | Inclusive lower bound for `number` type. |
| Maximum | `maximum` | float | Inclusive upper bound for `number` type. |

### `ui[]` Object (`UISlotContribution`)

| Field | JSON Key | Type | Description |
|-------|----------|------|-------------|
| ID | `id` | string | Unique widget ID within the plugin. Used as the key for runtime updates. |
| Slot | `slot` | string | Target zone: `titleBarLeft`, `titleBarRight`, `statusBarLeft`, `statusBarRight`, `infoBar`, `footer`, `toolHint`. |
| Priority | `priority` | int | Ordering within a zone (lower = earlier/leftmost). Default 100. |
| Description | `description` | string | Human-readable explanation of what the widget shows. |
| RefreshInterval | `refresh_interval` | string | Suggested polling interval (e.g. `"30s"`). Advisory only — push-based `UpdateWidget` is preferred. |
| Triggers | `triggers` | string[] | Hook matchers that trigger an instant script run. Format: `"EventName:Matcher"` (e.g. `"PostToolUse:Shell*"`). Script runtime only. |
| Sync | `sync` | bool | Run hook triggers synchronously (inline in the hook goroutine). Default false (async). |
| Interactive | `interactive` | bool | Widget supports user actions (v2). Default false. |

**Validation rules** for `ui[]`:
- Requires `"ui.contribute"` permission (or `"*"`).
- Maximum 10 widgets per plugin.
- Widget IDs must be unique within the plugin.
- Invalid slot names fail validation.

## Checksum Verification

`plugin.sha256` next to `plugin.json` can hold the SHA256 of the manifest. Enable verification via `LoadManifestWithOptions(dir, {VerifyChecksum: true})`. Accepts `"hash"` or `"hash  filename"` (GNU coreutils) formats.

## Discovery

Plugins are discovered by scanning directories (default: `~/.xbot/plugins` and `~/.xbot/plugins/builtin`) for subdirectories containing a valid `plugin.json`. Invalid manifests are logged as warnings and skipped.
