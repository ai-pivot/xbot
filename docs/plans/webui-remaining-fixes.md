# Plan: WebUI 剩余问题修复（3 项）

## Summary

修复 Web 前端 3 个问题：Dockview Tab 样式（CSS 覆盖）、CWD 获取失败（后端 RPC 对齐活跃会话）、消息渲染折叠逻辑（三级折叠 + 工具合并 + 迭代分组）。

## 子代理审计结论

| # | 问题 | 根因 | 子代理 |
|---|------|------|--------|
| 1 | Tab 装饰条 + 反色 | dockview VS 主题硬编码 border-bottom/border-top + .dv-tab 背景色 | tab-dockview-audit |
| 2 | 工作路径获取失败 | get_cwd RPC 在 params 为空时默认 channel="web" chatID=bizID，与活跃会话 cli:/root/Code/xbot 错位 | ws-cwd-debug |
| 3 | 消息渲染/折叠逻辑 | minimal 等同 all；工具逐个独立成卡无合并；流式中无迭代分组；无总耗时数据 | render-logic-audit |

## Changes

### 1. Tab 样式（CSS 覆盖）

#### `web/src/index.css`
- What: 在文件末尾添加 3 条 CSS 规则覆盖 dockview VS 主题：
  1. `.dv-dockview .dv-groupview > .dv-tabs-and-actions-container { border-bottom: none }` — 移除 tab strip 下方装饰条
  2. `.dv-dockview .dv-groupview .dv-tab { border-top: none }` — 移除每个 tab 顶部装饰边
  3. `.dv-dockview .dv-tab.dv-inactive-tab { background-color: transparent }` — 未激活 tab 背景透明
- Why: dockview VS 主题硬编码了 border + 背景色，无法用变量关闭，必须用 CSS 覆盖。specificity 与原规则持平靠源序胜出，不需要 !important。
- 注意：覆盖选择器需带 `.dv-tabs-container >` 限定作用域，避免误伤 overflow 下拉菜单

#### `web/src/workspace/TabHeader.tsx`
- What: 删除底部 accent bar 的 `<span>`（由 CSS 覆盖取代 dockview 装饰后，TabHeader 不再需要自己的 accent bar）
- Why: 用户要求删除上方装饰条改为下方主题色，但 dockview 的 border 已被 CSS 移除，TabHeader 的 bottom accent bar 保留即可

### 2. CWD 获取失败（后端 RPC 对齐活跃会话）

#### `serverapp/rpc_table.go`
- What: `get_cwd` handler 在 `p.Channel==""` 时不再默认 `"web"`，改为从 RPC context 的活跃会话获取；`p.ChatID==""` 时用活跃会话的 chatID 而非 bizID
- Why: RPC 路径与消息路由路径未共用 `getCurrentSession`，导致 get_cwd 查到了从未使用的 `web:cli_user` 会话
- 实现：需要让 `HandleCLIRPC` 能接收活跃会话信息。方案：在 WS RPC 闭包中调用 `webCh.getCurrentSession(senderID)` 获取 `{Channel, ChatID}`，通过新增 context key 传入

#### `serverapp/server.go`
- What: WS RPC 闭包中解析 `webCh.getCurrentSession(senderID)`，把 `sel.Channel`/`sel.ChatID` 注入 context 传给 `HandleCLIRPC`
- Why: 让 get_cwd/set_cwd 等按会话隔离的 RPC 能感知到真实活跃会话

#### `web/src/components/sidebar/SessionInfo.tsx`
- What: set_cwd 调用去掉 `channel:'web'`
- Why: 同上

### 3. 消息渲染/折叠逻辑（三级折叠）

#### 后端补齐耗时数据

##### `channel/web/web_api.go`
- What: `histIterSnapshot` 增加 `ElapsedWall int64 json:"elapsed_wall"`；`histTool` 增加 `ElapsedMs int json:"elapsed_ms,omitempty"`
- Why: web 边界丢弃了耗时数据，阻塞"总耗时"特性

#### 前端类型与归一化

##### `web/src/types/agent.ts`
- What: `IterationSnapshot` 增 `elapsedMs?: number`

##### `web/src/components/agent/normalize.ts`
- What: `normalizeIteration` 读 `r.elapsed_wall`；`normalizeTool` 读 `t.elapsed_ms`

##### `web/src/components/agent/api.ts`
- What: `HistProgress` / `HistMsg` 类型补 `elapsed_wall` / `elapsed_ms`

#### 折叠语义

##### `web/src/hooks/useCollapseLevel.ts`
- What: 重写 `defaultOpenForLevel`：
  - `'all'` → 全部 false（组件层整体隐藏中间过程）
  - `'minimal'` → iteration: false, tool: false, reasoning: false（卡片折叠但可见）
  - `'none'` → iteration: true, tool: true, reasoning: false（T 始终折叠）

#### 渲染层

##### `web/src/components/agent/AssistantMessage.tsx`
- What: `'all'` 级别时隐藏 ProgressPanel/IterationHistory，只显示最终输出 + "N次迭代 · 总耗时"摘要行；`'minimal'`/`'none'` 保持渲染

##### `web/src/components/agent/ProgressPanel.tsx`
- What: 流式中按 `progress.iterationHistory` 分组渲染（每迭代一组），而非 active/completed 扁平两列表

##### `web/src/components/agent/IterationHistory.tsx`
- What: 同迭代内连续工具调用合并为一张 `ToolGroupCard`（折叠时显示工具数+名称摘要，展开后列出各 ToolCallBlock）；`thinking` 改用普通 Markdown（O）而非 ReasoningBlock（T）

##### `web/src/components/agent/ToolGroupCard.tsx`（新增）
- What: 连续工具合并卡片组件，头部显示工具数+名称/状态徽标摘要，展开后列出各 ToolCallBlock

##### `web/src/components/agent/CollapsibleCard.tsx`
- What: 支持受控 `open`/`onOpenChange`，让折叠级别切换实时生效

## Risks

- **后端 RPC context 改动**：`HandleCLIRPC` 签名/调用链改动可能影响 CLI 测试。缓解：保留旧签名供测试，新增带活跃会话的重载或通过 context value 传递。
- **CSS 覆盖 specificity**：dockview 升级可能改变选择器结构。缓解：用 `.dv-dockview` 前缀镜像全路径，不用 !important。
- **折叠级别响应性**：已挂载的 CollapsibleCard 不会跟随级别切换。缓解：改为受控 open 模式。

## Definition of Done

- [ ] Tab strip 下方无多余装饰条，未激活 tab 背景透明
- [ ] CwdProvider 能获取到活跃会话的真实工作路径
- [ ] 折叠级别 `all` 只显示最终输出 + 总耗时摘要
- [ ] 折叠级别 `minimal` 按迭代分组，连续工具调用合并为一张卡
- [ ] 折叠级别 `none` 除 T 外都展开
- [ ] `npm run build` 零错误
- [ ] `go build ./...` 零错误
- [ ] `go test ./channel/web/...` 通过

## Open Questions

- `.workspace-docs/notes/xbot/opencode-web-agent-output.md` 文件不存在（.workspace-docs 是失效的符号链接）。用户已在消息中提供了完整的折叠规则描述，直接使用。
