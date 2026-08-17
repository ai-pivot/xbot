# Plan: xbot.git-fancy —— stdio IPC 独立插件

> 状态：设计定稿，实施中
> 关联：grpc→stdio 运行时命名统一（RuntimeGRPC 仅保留历史兼容别名）

## 目标

实现 `xbot.git-fancy`：非内置、可热加载、VSC 级 diff 查看的 Git 插件。

| 需求 | 验收标准 |
|---|---|
| 非内置插件 | 走 `web_plugin_list` 发现 + `plugin.json` 声明，面板可启用/禁用 |
| 热加载 | 面板点「刷新」→ 立即扫描磁盘 + 激活新插件 + 前端 view 出现，无需重启 |
| stdio IPC 后端 | 后端是独立进程（stdin/stdout JSON-RPC），非 serverapp 内置 RPC |
| VSC 级 diff | 文件变更列表 + 点击展开 diff（行级 +/- 着色）+ 提交历史 |
| 优雅卸载 | 停用插件时先发 `deactivate` 通知，超时（5s）才 kill |

## 架构

```
┌─ 前端（独立 ESM view, right_sidebar）───────────────┐
│  Git Fancy 面板                                      │
│  ├─ 分支徽章 + 领先/落后                             │
│  ├─ 变更文件树（M/A/D/U 徽章 + ±行数）              │
│  ├─ diff 视图（点击文件展开，行级着色）             │
│  └─ 最近提交历史                                     │
└──────────┬─────────────────────────────────────────┘
           │ ctx.rpc.call('xbot.git-fancy.status', {chatID})
┌──────────▼─────────────────────────────────────────┐
│ serverapp web_plugin_rpc（已存在）                  │
│  → ChannelPluginCall(pluginID, "web_plugin_rpc")    │
│  → 注入 chatID→cwd 解析                             │
└──────────┬─────────────────────────────────────────┘
           │ stdin/stdout JSON-RPC（双向）
┌──────────▼─────────────────────────────────────────┐
│ stdio 插件进程（独立 Go 二进制，protocol.Run）      │
│  ├─ status / log / diff / branches                 │
│  └─ Deactivate：清理资源后自行退出                 │
└────────────────────────────────────────────────────┘
```

## 实施步骤

### P0：运行时命名统一（已完成）

- `RuntimeGRPC` 仅保留为历史兼容别名；代码注释、错误信息、文档全部统一为 `stdio`
- 已删除历史计划文档 `docs/plan-grpc-transport.md`、`docs/plan-plugin-transport-rename.md`
- 验证：`go build ./...` ✅ / `go test ./plugin/` ✅

### P1：协议扩展（plugin/protocol/protocol.go）

1. `deactivate` 后进程自行退出——`run()` 收到 `deactivate` 处理完 `return`（进程 exit 0）
2. 新增 `web_plugin_rpc` dispatch + `Handler.WebPluginRPC` 字段：
   ```go
   WebPluginRPC func(params *WebPluginRPCParams) (*WebPluginRPCResult, error)
   ```

### P2：优雅卸载（plugin/runtime.go）

`stopLocked()` 从直接 Kill 改为：

```go
const gracefulStopTimeout = 5 * time.Second

func (p *StdioPluginProcess) stopLocked() {
    if !p.running { return }
    _ = p.stdin.write(&PluginRequest{Method: "deactivate"})
    done := make(chan struct{})
    go func() { _ = p.cmd.Wait(); close(done) }()
    select {
    case <-done:          // 优雅退出
    case <-time.After(gracefulStopTimeout):
        _ = p.cmd.Process.Kill()
        <-done
    }
    p.running = false
}
```

`Call()` 的 timeout/cancel 分支统一走 `stopLocked()`。

### P3：git-fancy stdio 进程（plugin/git-fancy/main.go）

- `protocol.Run(handler)`，`Activate` 返回 `ChannelProvider{name:"git-fancy"}`
- `WebPluginRPC` 分派：status / log / diff / branches
- CWD 由 server 在转发时注入（params.chatID → session CWD）

### P4：后端路由（serverapp/rpc_table.go）

- 现有 `web_plugin_rpc` → `ChannelPluginCall`；补 chatID→cwd 注入

### P5：前端（web/src/plugins/git-fancy/entry.tsx）

- `activate(ctx)` 存 `ctx.rpc`
- `ctx.rpc.call('xbot.git-fancy.status', {chatID})` 拉数据
- 复用现有 fancy Git 面板（分支/变更列表/提交历史/diff 行级着色）
- 3s 自动轮询 + 手动刷新

### P6：热加载

- 后端：`plugin_status {rescan:true}` 已做 Discover + ActivateAll；stdio 进程由 Activate 拉起 ✓
- 前端：刷新按钮已调 `plugin_status {rescan:true}`；补刷新后主动拉 `web_plugin_list` 激活新 view

## 验收命令

- `go test ./plugin/ ./plugin/protocol/`
- `go build ./...`
- 前端：`tsc --noEmit` + `vite build` + esbuild 独立模块
- 手动：插件面板刷新 → Git Fancy 出现 → 启用 → 右侧边栏 Git tab → 点文件看 diff

## 风险

1. `web_plugin_rpc` 30s 固定超时——大仓库 git status 可能慢，需按方法区分或调大
2. 多会话 CWD——git 命令在 session CWD 执行，由 server 转发时注入
