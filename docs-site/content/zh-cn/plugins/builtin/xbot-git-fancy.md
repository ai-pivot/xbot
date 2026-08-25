---
title: "xbot-git-fancy — Fancy Git 面板"
weight: 3
---

Git Fancy 插件是 Web UI 的 Fancy Git 面板：分支、带 ±行数统计的工作区变更、分页提交历史、commit 详情，以及全宽 Monaco diff tab。它遵循 VSCode "editor view" 语义——diff 作为动态 tab 在主编辑区打开。

后端（`plugins/xbot-git-fancy/`）是纯 stdio IPC Go 插件：xbot 拉起该二进制并通过 stdio 上的 JSON 驱动（`protocol.Run`）。所有 git 命令**只读**，并在会话的工作目录中执行（由服务器以 `params.cwd` 注入）；插件本身无状态。

## 功能

- **状态面板**（右侧栏）——当前分支、clean/dirty 状态、ahead/behind、工作区变更（路径、状态、±行数）
- **提交历史**——分页（`skip`/`limit` + total 计数，支持"加载更多"）
- **commit 详情**——作者、邮箱、ISO 日期、提交信息、numstat ±行数统计的变更文件列表
- **Diff tab**——任意变更文件的全宽 Monaco `DiffEditor`（工作区 vs HEAD，或 commit 维度经 `git show`）；提供 original/modified 文件内容供原生 diff 视图使用
- **分支列表**——当前分支 + 本地分支
- **设计安全**——每条命令带 `GIT_OPTIONAL_LOCKS=0`；无写入、无变更

## 安装

```bash
cd plugins/xbot-git-fancy
make build          # → bin/git-fancy-plugin
make install        # → ~/.xbot/plugins/xbot.git-fancy/
```

或在仓库根目录执行：`make plugins-install`。
免安装开发模式：`make plugins-build` + `XBOT_PLUGIN_DIRS="$(pwd)/plugins" xbot`。

前端视图（`web/src/plugins/git-fancy/`：`index.tsx`、`commit.tsx`、
`shared.tsx`）随 web 构建打包——`plugin.json` 的 web 声明（`entry`、view
贡献点）负责向前端插件运行时注册它们。后端二进制通过 RPC 提供面板数据。

## 配置

插件在 `plugin.json` 中声明配置 schema（可在插件管理面板中编辑）：

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `defaultLogLimit` | number | 10 | 日志面板默认加载的 commit 条数（1–100） |
| `showDiffStats` | boolean | true | commit 详情中显示 numstat ±行数统计 |

## 架构

```text
Web Git 面板（右侧栏）
  → ctx.rpc.call('xbot.git-fancy.status', { cwd })
  → xbot web_plugin_rpc handler → 向 git-fancy 进程发送 stdio RPC
  → 在会话 CWD 执行 git 命令（只读）
  → 结构化 JSON 结果 → 面板渲染

点击 commit → openViewTab('xbot.git-fancy.commit', { hash })
  → commit 视图（主编辑区，动态 tab）
  → ctx.ui.openFileTab(path) / openViewTab(diff) → Monaco DiffEditor
```

### Manifest

```json
{
  "id": "xbot.git-fancy",
  "runtime": "stdio",
  "entry": "./bin/git-fancy-plugin",
  "activationEvents": ["onStart"],
  "permissions": ["rpc", "ui"],
  "web": {
    "entry": "index.js",
    "contributes": [
      { "kind": "view", "id": "xbot.git-fancy.panel", "container": "right_sidebar", "title": "Git" },
      { "kind": "view", "id": "xbot.git-fancy.commit", "container": "main", "title": "Commit", "dynamic": true }
    ]
  }
}
```

注意 `permissions` 数组：前端插件上下文按权限构建——`ui` 权限使
`ctx.ui.openViewTab` 可用。声明了 `container:"main"` 视图但缺少 `"ui"` 的
manifest 会**静默失效**（打开 diff tab 的点击无反应）。这由
`plugins/xbot-git-fancy/main_test.go`（`TestManifestPermissions`）守护。

### RPC 方法（全部只读）

| 方法 | 参数 | 返回 |
|------|------|------|
| `status` | `cwd` | `repo`、`branch`、`clean`、`changes[]`（路径/状态/±行数）、`ahead`、`behind` |
| `log` | `cwd`、`limit`（≤100）、`skip` | `commits[]`（hash/作者/时间/标题）、`total` |
| `commit` | `cwd`、`hash` | 完整 hash、作者、邮箱、ISO 日期、信息、`files[]`（路径/状态/±行数） |
| `diff` | `cwd`、`path`、`commit?` | unified diff、行级解析（add/del/ctx/hunk 带行号）、`original`/`modified` 内容、增减计数 |
| `branches` | `cwd` | 当前分支 + 排序后的本地分支列表 |

`cwd` 由服务器的 `web_plugin_rpc` handler 从会话当前目录注入——插件从不
猜测用户的工作区。

### Diff 解析

`parseUnifiedDiff` 将 `git diff` 输出转换为行级条目（`hunk`/`add`/`del`/
`ctx`/`meta`，带新旧行号），前端据此渲染 VSCode 风格 ± 着色。未跟踪文件
渲染为全新增（original 为空）。commit 维度 diff 用 `git show
<commit>:<path>` 取两侧内容；缺失的一侧（新增/删除文件、root commit）返回
空字符串而非报错。

## 测试

```bash
cd plugins/xbot-git-fancy
go test ./...
```

## 源文件

| 文件 | 作用 |
|------|------|
| `main.go` | stdio `protocol.Run` 主循环；git 命令封装；diff 解析器 |
| `plugin.json` | Manifest：id `xbot.git-fancy`、配置 schema、web 视图贡献点 |
| `main_test.go` | 单元测试（manifest 权限、RPC handler） |
| `Makefile` | build / test / install / clean 目标 |
