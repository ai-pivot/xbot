---
title: "Widget Zones"
weight: 9
---

UI widget 区域参考（`plugin/manifest.go` `validUISlots` + `plugin/widget.go` `RenderSessionWidgets`）。

## 区域列表

| 区域 | 位置 |
|------|------|
| `titleBarLeft` | 标题栏左侧。 |
| `titleBarRight` | 标题栏右侧。 |
| `statusBarLeft` | 状态栏左侧。 |
| `statusBarRight` | 状态栏右侧。 |
| `infoBar` | 信息栏（Web 底部状态栏区域）。 |
| `footer` | 页脚区域。 |
| `toolHint` | 插件提供的 markdown 提示，渲染在进度面板工具输出旁。 |

所有区域按会话的工作目录逐会话渲染——`RenderSessionWidgets` 恰好遍历此列表：

```go
for _, z := range []string{"titleBarLeft", "titleBarRight", "statusBarLeft", "statusBarRight", "infoBar", "footer", "toolHint"} {
    zones[z] = wr.RenderZoneForWorkDir(z, cwd)
}
```

## 使用方式

### Manifest 声明

```json
{
  "contributes": {
    "ui": [
      { "id": "git-status", "slot": "statusBarLeft", "priority": 10 }
    ]
  }
}
```

需要 `"ui.contribute"` 权限；每插件最多 10 个 widget；插件内 widget ID 唯一。

### 编程式注册

```go
ctx.ContributeUI(widgetID, zone string, widget UIWidget, priority int) error
```

`widgetID` 必须与 manifest 中声明的贡献点匹配。优先级排序：越小越靠前/左（默认 100）。

## Widget 接口

```go
// UIWidget 按给定宽度渲染样式 span（0 = 不限制）。
type UIWidget interface {
    Render(width int) []WidgetSpan
}

// 可选：为特定 workDir 渲染，不修改共享 PluginContext。
type WorkDirRenderer interface {
    RenderForWorkDir(width int, workDir string) []WidgetSpan
}
```

## StyleClass

```go
type WidgetSpan struct {
    Text  string
    Style StyleClass
}
```

| StyleClass | 含义 |
|------------|------|
| `normal` | 默认文本。 |
| `dim` | 暗淡文本。 |
| `accent` | 强调色。 |
| `success` | 成功（绿）。 |
| `warning` | 警告（黄）。 |
| `error` | 错误（红）。 |
| `info` | 信息（蓝）。 |
| `muted` | 弱化（白色暗淡）。 |
| `raw` | 直通——文本自带 ANSI 转义序列，不做包装。 |

插件**不得**在 `Text` 中输出原始 ANSI 转义序列（`StyleRaw` span 除外）。TUI 将 `StyleClass` 映射为主题色；`BasicANSIRender` 是服务端默认渲染器。

## 更新机制

- **推送（首选）**：`ctx.UpdateWidget(widgetID)` 触发异步重渲染。
- **轮询（建议）**：`UISlotContribution.RefreshInterval`（如 `"30s"`）。
- **Hook 触发器**：`UISlotContribution.Triggers` — hook 事件触发即时脚本运行（script 运行时）。

`WidgetRegistry` 支持防抖（`SetDebounce`）与抑制（`SuppressUpdates`），用于批量操作期间合并高频更新。
