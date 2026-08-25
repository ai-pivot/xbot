---
title: "Widget 组件"
weight: 12
---

组件把插件内容渲染进 CLI 的 UI 区域——状态栏、信息栏、标题栏、页脚——以及 Web UI。有三条实现路径：脚本输出解析、Go `UIWidget` 实现、Web 视图贡献。

## 注册表与区域

`WidgetRegistry`（`plugin/widget.go`）以 `pluginID:widgetID` 为键持有组件槽位，每个槽位带区域（slot）、提供者（`UIWidget`）与优先级：

```go
func (p *HelloWorldPlugin) Activate(ctx plugin.PluginContext) error {
	return ctx.ContributeUI("hello-widget", "statusBarRight", p, 10)
}
```

`WidgetSpan` 是渲染原子：

```go
type WidgetSpan struct {
	Text  string
	Style WidgetStyle
	// ...
}
```

样式：`StyleDim`、`StyleOk`、`StyleWarn`、`StyleErr`、`StyleInfo`、`StyleAccent`（另有 `StyleNormal`）。

## UIWidget 接口

```go
type UIWidget interface {
	Render(width int) []WidgetSpan
}
```

`width` 是可用单元格宽度。若组件输出依赖会话工作目录，另实现 `WorkDirRenderer`：

```go
type WorkDirRenderer interface {
	RenderForWorkDir(width int, workDir string) []WidgetSpan
}
```

## 从 Hook 推送重渲染

`git-pr-status` 示例（`plugin/examples/git-pr-status/main.go`）展示 Hook → 组件桥接：`PostToolUse` 处理器修改状态并调用 `ctx.UpdateWidget("git-branch")`——注册表立即重渲染该组件（TUI 不存在时调用也安全，错误可忽略）。

## Script 组件

Script 插件在 `contributes.ui[]` 中声明组件，运行时把 `style|text` 输出解析为 spans（见 [Script 插件](../script-plugins/)）。每个声明的组件获得一个 `widgetAdapter`（`plugin/script_runtime.go:90`），经 `XBOT_WIDGET_ID` 把组件 ID 传给脚本。

示例用到的区域：`infoBar`（git-info）、`toolHint`（file-diff，sync）、`statusBarRight`（git-pr-status）。其他 CLI 区域包括 `titleBar` 与 `footer`。

## 变更通知（防抖）

`WidgetRegistry.NotifyUpdated()` / `FireUpdated()` 安排更新通知；`SetDebounce(d)` 控制合并窗口。`OnUpdated(fn)` 注册监听器——`Agent` 的 `WidgetRegistry.OnUpdated` 处理器做 `channel.WidgetSubscriber` 类型断言并调用各渠道的 `NotifyWidgetsUpdated()`，让每个渠道决定自己的渲染（CLI → ANSI，web → 结构化 JSON）。

## Web 组件

Web UI 的组件输出是结构化的，不是 ANSI。Channel 插件用 `web_ui` 协议（`plugin/examples/web-ui-demo/`）推送声明式组件（sparkline/table/badge）或自由代码；交互经 `web_ui_action` RPC 回传。Web 视图贡献（`web.contributes` 中的 `contributes`）声明完整 React 面板——见 [Web 插件](../web-plugins/)。

## 按会话正确性

- Script 插件输出按 `workDir → widgetID` 缓存——绝不共享单一输出（`runAndUpdate`，`plugin/script_runtime.go:380`）。`Render()` 回退到共享的 `pctx.WorkingDir()`；`RenderForWorkDir` 是远程多会话的权威路径。
- 原生插件：管理器在 Cd 时调用 `RefreshWorkDir(wd, channel, chatID, tenantID)`，更新每个插件上下文；`WorkDirAware` 插件立即收到 `OnWorkDirChanged`。
- ⚠️ **绝不在后台 goroutine 写全局组件槽位缓存**——`runAndUpdate` 调用 `NotifyUpdated()` 而非 `UpdateWidget()`。写全局缓存导致跨会话覆盖（会话 B 的 git 分支覆盖会话 A 的）。

## 多组件插件

清单声明 N 个组件时，脚本**每个组件各跑一次**（`XBOT_WIDGET_ID` 已设置）。不按该变量分支，N 个组件渲染相同输出——经典的"5 个重复的 `main +10 ↑13`"bug。在脚本里分支：

```bash
case "$XBOT_WIDGET_ID" in
  git-branch)  # 分支状态
    echo "ok|git:${branch}";;
  git-changes) # 变更数
    echo "info|Δ${changes}";;
esac
```
