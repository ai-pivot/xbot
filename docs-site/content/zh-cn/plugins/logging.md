---
title: "日志与审计"
weight: 17
---

插件的运行日志与生命周期审计追踪与 xbot 主日志完全隔离。每插件日志写入独立的按天轮转文件；审计事件写入 JSONL 审计日志。两者都存放在 `~/.xbot/plugins/` 下。

## 每插件日志

插件通过 `PluginContext.Logger()` 获得结构化日志器，它实现了 `plugin.Logger` 接口：

```go
type Logger interface {
    Debug(msg string, fields ...Field)
    Info(msg string, fields ...Field)
    Warn(msg string, fields ...Field)
    Error(msg string, fields ...Field)

    Debugf(format string, args ...any)
    Infof(format string, args ...any)
    Warnf(format string, args ...any)
    Errorf(format string, args ...any)

    WithField(key string, value any) Logger
    WithFields(fields ...Field) Logger
}

type Field struct {
    Key   string
    Value any
}
```

使用示例：

```go
ctx.Logger().Info("widget refreshed", plugin.Field{Key: "widget", Value: "git"})
ctx.Logger().WithField("attempt", 3).Warnf("retry failed: %v", err)
```

### 位置与轮转

- 位置：`~/.xbot/plugins/<pluginID>/logs/<pluginID>-YYYY-MM-DD.log`
- 写入器是 `rotateWriter`（`plugin/plog.go`）——一个线程安全的 `io.Writer`，按日期轮转。`Write` 每次调用都检查当前日期，日期变化时打开新文件；文件以 `O_CREATE|O_WRONLY|O_APPEND`、`0644` 打开。
- 插件 ID 在用作文件名前会经过 `sanitizeBaseName` 清洗——`[a-zA-Z0-9._-]` 之外的任何字符都会被替换为 `_`。

### 行格式

```
2006-01-02 15:04:05 [INFO] plugin=my-plugin key=value message
```

### 日志级别

`Debug`、`Info`、`Warn`、`Error`（以及格式化变体）。每插件文件不做级别过滤——所有级别都会写入。

### 清理

`pluginLogManager` 启动一个清理 goroutine：启动时立即执行一次，之后每小时执行一次（`cleanupLoop`，1 小时 ticker）。`doCleanup` 扫描每个 `~/.xbot/plugins/<id>/logs/` 目录以及审计目录，删除修改时间早于 `DefaultPluginLogMaxAge`（7 天）的 `.log`/`.jsonl` 文件。

### 兜底

每插件日志只写入插件自己的日志文件——绝不写入全局 logrus 日志。如果每插件写入器创建失败（`fileOut == nil`），`pluginLogger.emit` 会回退到全局 logrus 日志器，保证日志不静默丢失。

## 审计日志

`AuditLogger`（`plugin/audit.go`）记录插件生命周期操作的只追加 JSONL 审计追踪。`PluginManager` 在 `~/.xbot/plugins/audit.jsonl` 创建审计日志，按天轮转为 `~/.xbot/plugins/audit-YYYY-MM-DD.jsonl`。

### AuditEntry

```go
type AuditEntry struct {
    Timestamp time.Time      `json:"timestamp"`
    PluginID  string         `json:"plugin_id"`
    Action    string         `json:"action"`
    Details   map[string]any `json:"details,omitempty"`
    Error     string         `json:"error,omitempty"`
}
```

### 操作类型

| 常量 | 值 | 记录时机 |
|------|-----|----------|
| `AuditActivate` | `"activate"` | 插件激活 |
| `AuditDeactivate` | `"deactivate"` | 插件停用 |
| `AuditInstall` | `"install"` | 插件安装 |
| `AuditUninstall` | `"uninstall"` | 插件卸载 |
| `AuditReload` | `"reload"` | 插件重载 |
| `AuditDisable` | `"disable"` | 插件禁用 |

### API

```go
al, err := plugin.NewAuditLogger(path) // path: ~/.xbot/plugins/audit.jsonl
if err != nil {
    // 处理错误
}

al.Log(plugin.AuditEntry{PluginID: "xbot.genui", Action: plugin.AuditReload})

entries := al.Query(plugin.AuditFilter{PluginID: "xbot.genui"})
al.Clear()
al.Close()
```

- `Log` 在 `Timestamp` 为零值时自动设为 `time.Now()`，并静默忽略写入错误——审计日志绝不能阻塞调用方。
- `Query` 扫描所有 `audit-*.jsonl` 文件，应用 `AuditFilter`（`PluginID`、`From`、`To`；零值表示「不过滤」），返回按 `Timestamp` 升序排序的条目。
- 轮转使用相同的 `rotateWriter`，后缀为 `.jsonl`。如果轮转写入器创建失败，日志器回退到单个 `audit.jsonl` 文件（legacy 模式，以 `0600` 打开）。
- `Clear` 截断当天的审计文件并重建轮转写入器（legacy 模式截断并重新打开单文件）。

## 参见

- [热重载与监控](./hot-reload/) — 重载事件会被审计
- [PluginContext API](./plugin-context/) — `Logger()` 访问器
