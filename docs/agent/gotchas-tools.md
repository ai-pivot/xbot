# gotchas-tools.md — moved verbatim from AGENTS.md (2026-09-23)

> These sections were moved OUT of the root `AGENTS.md` to keep the
> system-prompt injection under its 100k-character budget. Content is
> **verbatim** — nothing was dropped. `AGENTS.md` keeps a one-line index
> of every ⛔/⚠️ rule pointing here.

## `view_image` 任意路径可读（可读根目录白名单已删）

- **2026-09-15 用户指令**：「把这个删了，哪里的都允许读」（现场报错 `path .shots/desktop.png is outside the readable roots (workspace, working dir, view_images)`）。`tools/view_image.go` 的 `readLocal` 原先只允许 **workspace root / working dir / view_images / `ctx.ReadOnlyRoots`** 下的路径，其余一律拒绝 —— 结果 agent **看不到自己刚截的图**（/tmp、别的仓库、别的会话目录全被挡）。现已删除该白名单：相对路径仍按 `ctx.WorkingDir` 解析，**任意绝对路径可读**；**非图片仍必须被拒**（与路径无关，由图片解码/类型判定保证）。连带删除失去消费者的 `isSubPath`。守护用例：`tools/view_image_test.go` 的 `TestViewImage_OutsidePathReadable`（workspace 外绝对路径必须可读 + `../../etc/passwd` 仍被拒）。

## 工具必须立刻返回（禁止无界等待）

- **`task_status` / `task_read` / `SubAgent(action="inspect")` 的【返回体】必须自带"别轮询"提示**（用户 2026-09-15：「返回中提示模型不要一直调用这些工具轮询，做有意义的事情」）：文案是 **单一实现** —— `tools/task_tools.go` 的 `tools.PollingHint`，由 `formatTask`（task_status/task_read）、`formatSubAgentTask`、`agent.InspectInteractiveSession`（SubAgent inspect）三处返回**统一追加**（提示只在**最终返回前**追加 —— 插进提前 return 分支会漏掉主路径，`formatSubAgentTask` 曾踩到）。文案点明三件事：不要反复轮询、完成会**自动以通知送达**、去做别的有意义的事。守护：`tools/tool_guidance_test.go` 的 `TestTaskFormats_CarryPollingHint`（running / done / subagent 三种返回都必须含该提示 + 文案关键词断言）。

- **`send_message`（agent 目标）与 `SubAgent(action="send")`（run 中排队）必须立刻成功**（用户 2026-09-14：「这两个工具都必须立刻成功」）。两者都只在**短窗口**内顺手拿 ack：`tools/limits.go` 的 `SendMessageAwaitReply`(2s) 与 `agent/interactive.go` 的 `subAgentSendAckWait`(3s)；超时即返回"已投递/已入队"，**投递在后台继续** —— 用 `context.WithoutCancel(baseCtx)` + 上限 ctx，**绝不用工具 ctx**（工具返回后它会被取消），SubAgent 的消息**留在 `pendingMessages`**（下次迭代间隙照旧投递）。旧行为：agent 目标等满 `AgentRPCTimeout`=30s（目标忙即卡死）；SubAgent 排队**无限**等 drain ack（子代理长跑工具时调用方永不返回）。守护用例：`tools/send_message_test.go` 的 `TestSendToAgent_DoesNotBlockOnUnresponsiveTarget`（目标 `SendMessageCtx` 永久阻塞 ⇒ 工具 3s 内必须成功返回）。

- **转后台（promote_shell）必须对【所有会话】可用**（用户 2026-09-14：「所有会话都要支持转移到 background」）。`ForegroundShellHandle` 注册时用的是 **shell 自己的 `ctx.SessionKey`**，而前端发来的 `session_key` 是面板所见会话（`channel:chatID`）—— 两者在 physicalChannel override（CLI 会话在 web 里看是 `web:chatID`）、SubAgent（`agent:role/instance`）等场景下**并不一致**，只按会话键查会落空并报 `no running foreground shell in this session`。修复：registry 维护**全局 callID 索引**（callID = LLM tool_call id，**全局唯一**），会话键查不到时按 callID 兜底 ⇒ 任意会话视图都能转后台，且仍以 callID 精确定位、不会误伤并行的另一个 shell。守护用例：`tools/shell_promote_test.go` 的 `TestPromoteForegroundShell_AcrossSessionKeys`（注册在 `cli:/repo`，以 `web:/repo` + 同 callID 转后台必须成功）。

- **工具去重必须"终态优先"**（`web/src/components/agent/progressStore.ts` 的 `dedupTools`）：同一个 `name\x00label` 同时出现在 `activeTools`（**陈旧快照**，仍是 running）与 `completedTools`（done/error）时，旧的"先到先得"让 running 赢 ⇒ 工具**永远不转绿**（用户 2026-09-14：「一个 iter 两个 tool，已完成还是渲染成进行中」）。现按状态优先级取优：`done/error > running > pending > generating`，与数组顺序无关。**推论：任何"运行中"的判据必须与卡片观感同源** —— 转后台按钮曾用严格 `tool.status === 'running'`，而卡片运行态来自 `isToolInProgress`（pending|generating|running），两者不一致就会出现"看着在跑却没有按钮"（2026-09-14 用户报告）。

## share_file / 文件下载 key 必须【统一 sanitize】（2026-09-19 用户报告 share 链接带鉴权 404 根治）

- **现象**：`share_file` 给出的链接 `/api/files/download?key=agent%2F<uuid>%2FFerrite+%E4%B8%93…md` 带鉴权访问返回 `not_found`。取证：URL 文本按 query 语义解出的 key 与磁盘路径**逐字节相同**、文件确实在盘上 ⇒ **分歧发生在传输途中的 `+`**。
- **根因**：key 里含**空格**，`url.QueryEscape` 把空格编成 `+` —— 而 `+` 只在"按 query 语义解码"的客户端里等于空格；换个客户端（把 `+` 当字面加号、二次编码成 `%2B`）服务端就收到**另一个 key** ⇒ 读盘失败 ⇒ 404（`channel/web` 的 `serveLocalFile`）。上传路径从不中招，因为它的 key 是 `uploads/<uid>/<uuid><ext>`（完全没有名字）。
- **规范 = 唯一 sanitizer**：`serverapp/file_sharer.go` 的 `urlSafeKeyName`（全仓唯一实现）。**折叠边界只取"真正造成编码分歧/敌意"的字符** —— 空白、控制符、`+`、以及路径/外壳敌意字符 `/\:*?"<>|` → 单个 `_`（连续折叠），裁首尾 `._-`，按 **rune** 截断 ≤80（CJK 名字按字节切会造出非法 UTF-8 文件名）。**CJK 等其余字符保留**：`QueryEscape` 与 `encodeURIComponent` 对它们产出**完全相同的百分号编码** ⇒ 任何解码器解析到同一路径，同时保住人类可读的下载名（`Ferrite 专用…方案.md` → `Ferrite_专用高性能推理引擎架构改进方案.md`）。⇒ 新 key **绝不含空格** ⇒ URL 里**不可能**出现 `+`（`+`/`%20`/`encodeURIComponent` 三种客户端对同一 key 产出同一字节）。
- ⛔ **名字不承担唯一性**：key 形如 `agent/<uuid>/<name>`，uuid 是**每次发布新铸**的 ⇒ 不同会话分享同名文件天然各自独立（守护测试断言两次分享 key 不同）。❌ 不要为了"可读"把原始空格/任意字符塞回 key；用户可见的名字由 markdown 标签（display name）承载。
- **存量链接**：2026-09-19 之前发布的 key **确实含空格**（磁盘名如此，不可回写）—— 在解码正确的客户端仍可下载；用**修好的代码重新分享一次**即得到干净 key。
- 守护：`serverapp/file_sharer_test.go` 的 `TestWebFileSharer_KeyIsURLSafeAndEncodingAgnostic`（URL 不得含 `+`/空白 + **`+` 语义与 `%20` 语义必须解出同一个 key**（本 bug 的判别点）+ 同名两次分享 key 不同）；既有 `TestWebFileSharer_LocalCopiesFileAndReturnsURL` 的口径随之更新为"key 名必须 URL 安全（空格 → `_`）"。
