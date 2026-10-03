package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"xbot/channel"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// maxIncrementalIterations caps how many iteration-history entries
// GetActiveProgress transfers for an incremental pull (from_iter >= 0).
// Beyond this, the gap is too large for delta transfer — the client is
// signalled to reload from DB instead (ResyncRequired).
const maxIncrementalIterations = 30

// ⛔ active_progress 快照的 iteration_history **必须完整**（用户 2026-09-21：
// 「不能有任何 gap，任何 gap 都是破坏线性一致性」/「为什么我的 iter 还是缺」）——
// 这里**禁止**再引入尾部截断。体积/渲染性能归**渲染层**（TurnBody 迭代级窗口化：
// 只挂载视口附近的块 + contain，代价与迭代数解耦），绝不以丢数据换体积。

// SetCWD sets the current working directory for a session.
// It refreshes plugin workDir with the correct tenantID.
// cwdApplyDecision 决定是否采纳新的 cwd（纯函数，便于单测）。
//
// 2026-09-16 用户报告（严重）：「我创建会话时传的是**绝对路径**，为什么不用我的路径？」
// 根因：本函数旧实现（内联在 SetCWD 里）只在 existingDir=="" 或旧目录不存在时才写入
// ⇒ 当 Agent 初始化已把 cwd 设成 workspace 根（事故后 agent.work_dir 丢失 ⇒ 该根是
// **相对路径** `.xbot/users/<uid>/workspace`，且相对服务进程 cwd 恰好存在）时，
// 用户的绝对路径被**静默丢弃** ⇒ 新会话 pwd 既不是用户选的、还伴随
// `no such file or directory`（活日志实证）。
//
// 规则：
//   - force（显式用户动作，如新建会话弹窗的 set_cwd）→ **总是采纳**；
//   - 否则（自动路径，如 CLI 终端目录同步/重启恢复）：仅在无既有 cwd、或既有 cwd
//     已不存在时才采纳 —— 保留"不要用终端目录覆盖已持久化 cwd"的既有语义。
func cwdApplyDecision(existingDir string, existingExists bool, force bool) bool {
	if force {
		return true
	}
	if existingDir == "" {
		return true
	}
	return !existingExists
}

func (a *Agent) SetCWD(ch, chatID, dir string) error {
	return a.setCWD(ch, chatID, dir, false)
}

// SetCWDForced 应用一次**显式**的工作目录选择（用户/API 直接指定）。
//
// 用户要求（2026-09-16，原话）：「我不管你用什么方法，那个接口我传的是什么路径就得是
// 什么路径，而不是给我转换。」⇒ 本函数**原样应用**传入的 dir：
//
//	· 不做任何改写（不 join workspace 根、不相对化、不 trim 成别的路径）；
//	· 不因"不是绝对路径"或"目录不存在"而拒绝（仅记 warning 便于排查）——
//	  传什么就是什么。
//
// 与 SetCWD 的区别：SetCWD 是**自动**路径（终端目录同步/重启恢复），它不得覆盖已持久化
// 且仍存在的 cwd；SetCWDForced 是**显式**动作，总是生效（否则用户的绝对路径会被
// Agent 初始化写下的 workspace 根静默顶掉 —— 线上 bug）。
func (a *Agent) SetCWDForced(ch, chatID, dir string) error {
	if dir == "" {
		return fmt.Errorf("cwd is required")
	}
	if !filepath.IsAbs(dir) {
		log.WithFields(log.Fields{"cwd": dir, "session": ch + ":" + chatID}).
			Warn("SetCWDForced received a non-absolute path — applying it verbatim as requested")
	}
	// 显式工作目录（新建会话弹窗 / 会话信息里改路径）**不存在则自动创建**
	// （2026-09-20 用户要求：「创建新会话时如果选择了一个不存在的目录则自动创建」）。
	// 旧实现只打一条 Warn 然后原样落库 ⇒ 会话卡在一个不存在的 cwd 上（终端/工具/Cd
	// 全在错地方），而用户以为"路径没生效"。这里只创建目录本身（含多级父目录）；
	// 真正的错误（路径被普通文件占住、权限不足）必须**显式返回**，让 UI 报出
	// "工作目录设置失败"，绝不静默落一个坏 cwd。
	if info, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", dir, err)
		}
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return fmt.Errorf("create working directory %s: %w", dir, mkErr)
		}
		log.WithFields(log.Fields{"cwd": dir, "session": ch + ":" + chatID}).
			Info("SetCWDForced created a missing working directory")
	} else if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", dir)
	}
	return a.setCWD(ch, chatID, dir, true)
}

func (a *Agent) setCWD(ch, chatID, dir string, force bool) error {
	// CWD 是**会话级**状态（DB tenants.cwd），与沙箱无关：本机（none）与 runner
	// （remote）都支持同步。旧代码对任何非 none 模式直接拒绝 —— 而 Agent 在
	// cfg.SandboxMode 为空时曾把模式写成 "docker"，于是**未配置 sandbox 的部署
	// 新建会话必失败**（2026-09-16 P0：「CWD sync not supported in docker sandbox
	// mode」）。本地 docker sandbox 已整体删除，这条拒绝分支不再需要。
	if a.MultiSession() == nil {
		return ErrNoSessionManager
	}
	sess, err := a.MultiSession().GetOrCreateSession(ch, chatID)
	if err != nil {
		return err
	}
	// Set CWD — 详见 cwdApplyDecision 的语义说明（显式 force 总是生效；自动路径
	// 不得覆盖已持久化且仍存在的 cwd）。
	existingCWD := sess.GetCurrentDir()
	existingExists := false
	if existingCWD != "" {
		if _, statErr := os.Stat(existingCWD); statErr == nil {
			existingExists = true
		}
	}
	if cwdApplyDecision(existingCWD, existingExists, force) {
		sess.SetCurrentDir(dir)
	}
	// Always refresh plugin contexts so script plugins see the correct workDir
	if a.pluginMgr != nil {
		cwd := sess.GetCurrentDir()
		a.pluginMgr.RefreshWorkDir(cwd, ch, chatID, sess.TenantID())
		a.pluginMgr.RefreshTenantID(sess.TenantID())
	}
	return nil
}

// IsProcessingByChannel returns true if there is an active Run for the given channel:chatID.
func (a *Agent) IsProcessingByChannel(ch, chatID string) bool {
	key := ch + ":" + chatID
	if _, found := a.chatCancelCh.Load(key); found {
		return true
	}
	// Agent (sub-agent) sessions: one-shot/interactive sub-agents run directly
	// via Run() and do NOT register chatCancelCh — check the interactive session
	// running state instead so the web sidebar shows running sub-agents reliably.
	if ch == "agent" {
		if value, ok := a.interactiveSubAgents.Load(chatID); ok {
			if ia, ok := value.(*interactiveAgent); ok && ia != nil {
				ia.mu.Lock()
				running := ia.running
				ia.mu.Unlock()
				return running
			}
		}
	}
	return false
}

// HasPendingAskUserFast reports whether the session has a pending AskUser
// prompt, using the SAME authoritative path as GetPendingAskUser: the
// in-memory entry is validated against the persisted ask_question/ask_answer
// records, and a memory miss probes the DB first (one O(1) indexed lookup via
// idx_sm_tenant_record; the expensive Replay only runs when the DB says a
// question is genuinely pending). Both entry points therefore always agree.
// Used by the session tree to mark waiting_input rows so the sidebar and the
// panel agree during a WaitingUser pause: chatCancelCh is already deregistered
// there, so IsProcessingByChannel reports false and the sidebar would
// otherwise show idle while the panel is waiting for an answer.
func (a *Agent) HasPendingAskUserFast(ch, chatID string) bool {
	if ch == "" || chatID == "" {
		return false
	}
	_, entry := a.loadPendingAskUserEntry(ch, chatID)
	return entry != nil
}

// GetActiveProgress returns the latest progress snapshot for the given channel:chatID.
// The fromIter parameter is the TUI's watermark — only iterations with
// Iteration > fromIter are included in the returned IterationHistory. This keeps
// pull payloads proportional to the number of missing iterations, not the total
// turn length. Pass fromIter=0 (or -1) to get all iterations (for /su switch or
// initial restore).
//
// For agent sessions, corrects Phase from the authoritative running state in
// interactiveSubAgents when the agent is between iterations (Phase="done" but
// still running). This unifies the busy/idle logic across all session types.
func (a *Agent) GetActiveProgress(ch, chatID string, fetch protocol.ProgressFetch) *protocol.ProgressEvent {
	key := ch + ":" + chatID
	v, ok := a.lastProgressSnapshot.Load(key)
	if !ok {
		// Turn has ended (snapshot deleted). Return a minimal snapshot with
		// only todos so the client can restore the TODO list on session switch.
		// Without this, switching to an idle session with todos shows no todos
		// until the next TodoWrite tool call.
		//
		// Distinguish two cases:
		//   - HasTodos=false → the session never ran and never wrote a todo
		//     list → return nil (no active progress, nothing to restore).
		//   - HasTodos=true (possibly empty) → the session ran and its todo
		//     list was cleared (todo_write([]) / turn-end cleanupTodos) → return
		//     done + [] so the frontend LEARNS the list is now empty. Returning
		//     nil for an empty list meant the client could never tell "cleared"
		//     from "no data", so stale items survived refreshes.
		if a.todoManager != nil && a.todoManager.HasTodos(key) {
			snap := &protocol.ProgressEvent{
				Phase: "done",
				Todos: a.GetTodos(ch, chatID),
			}
			// Also include goal so the frontend can display it on idle sessions.
			if a.goalManager != nil {
				snap.Goal = a.goalManager.GoalInfo(key)
			}
			return snap
		}
		// Even without todos, if there's a goal, return it so the
		// frontend can display the active goal on idle sessions.
		if a.goalManager != nil {
			if goal := a.goalManager.GoalInfo(key); goal != nil {
				return &protocol.ProgressEvent{
					Phase: "done",
					Todos: []protocol.TodoItem{},
					Goal:  goal,
				}
			}
		}
		return nil
	}
	snapshot := v.(*protocol.ProgressEvent)
	result := *snapshot

	// Merge live stream state (updated by stream callbacks between structured events).
	// This is the pull-model replacement for stream event push — the client reads
	// live streaming content via tick pull instead of receiving push events.
	a.mergeStreamState(key, &result)

	// Always inject the latest goal state (goal may have been set/cleared/completed
	// via RPC since the snapshot was last refreshed by refreshStructuredTodos).
	if a.goalManager != nil {
		result.Goal = a.goalManager.GoalInfo(key)
	}

	// Agent sessions: correct Phase from authoritative running state.
	// interactiveSubAgents stores entries keyed by interactiveKey (no "agent:" prefix),
	// so we look up with chatID directly. When running=true but Phase="done"
	// (between iterations), correct Phase from iteration history.
	if ch == "agent" {
		if entry, loaded := a.interactiveSubAgents.Load(chatID); loaded {
			ia := entry.(*interactiveAgent)
			ia.mu.Lock()
			isRunning := ia.running
			// 最新 Run 的 turnID：send（action=send）在 Pre-Run reset 中把
			// assignSubAgentTurnID 分配的新 turnID 写回 ia.cfg.TurnID（wireSubAgentProgress
			// 的 stream 闭包与本校正分支都经 ia.cfg 延迟读取同一值）。校正 phase 时必须
			// 同步 turnID —— 否则校正后的 snapshot 仍是【上一个 Run 的 turnID（T1 旧）】，
			// 而 DB 最新 turn 是 T2 → 前端 history_replaced 的 active(T1) 与 DB turns(T2)
			// 不一致 → active.turnID 不在 turns 中 → 创建【重复 live turn T1】→
			// "迭代完成后打开渲染两遍历史"（用户报告）。
			runTurnID := uint64(0)
			if ia.cfg != nil {
				runTurnID = ia.cfg.TurnID
			}
			ia.mu.Unlock()
			if isRunning && result.Phase == "done" {
				corrected := false
				if histPtr, ok := a.iterationHistories.Load(key); ok {
					hist := *histPtr.(*[]protocol.ProgressEvent)
					for i := len(hist) - 1; i >= 0; i-- {
						if hist[i].Phase != "done" {
							result.Phase = hist[i].Phase
							if runTurnID > 0 {
								result.TurnID = runTurnID
							}
							corrected = true
							break
						}
					}
				}
				if !corrected {
					result.Phase = "running"
					if runTurnID > 0 {
						result.TurnID = runTurnID
					}
				}
			}
		}
	}

	if histPtr, ok := a.iterationHistories.Load(key); ok {
		hist := *histPtr.(*[]protocol.ProgressEvent)
		if len(hist) > 0 {
			flat := progressHistoryWithoutNested(hist)
			a.iterationHistories.CompareAndSwap(key, histPtr, &flat)
			filtered := make([]protocol.ProgressEvent, 0, len(flat))
			for _, h := range flat {
				if fetch.Filter(h.Iteration) {
					filtered = append(filtered, h)
				}
			}
			// Gap-too-large guard: when the caller's from_iteration watermark is
			// far behind the server's current iteration (long SSE disconnect /
			// reconnect gap), transferring dozens of iterations is wasteful and
			// error-prone. Signal the client to reload from DB (authoritative)
			// instead — the client already handles resync_required via replay_gap.
			// Only applies to incremental pulls (from_iter >= 0); FetchAll
			// (from_iter=-1, /su switch / initial restore) always returns all.
			if fetch.ToFromIter() >= 0 && len(filtered) > maxIncrementalIterations {
				result.ResyncRequired = true
				result.IterationHistory = nil
				return &result
			}
			result.IterationHistory = filtered
			return &result
		}
	}
	return &result
}

// =============================================================================
// P1（docs/plan-history-fold-windowing.md §3.5 D5）：active_progress 折叠视图变体
// =============================================================================

// GetActiveProgressFolded 是 GetActiveProgress 的**折叠视图变体**（REST 历史路径的
// active_progress 专用：切 busy 会话时的快照水合）。与 GetActiveProgress 的差别
// **只在投影层**——快照获取、mergeStreamState、goal 注入、agent 相位校正、
// resync_required 语义全部原样复用（直接调用原方法，零重复、零风险面改动）：
//
//	· FetchAll：IterationHistory = 尾部 channel.HistoryRegionWindow 个**展示区域**
//	  （区域边界对齐 ⇒ 永不劈开工具组；区域定义见 channel/region_view.go），窗口内
//	  非 GenUI 工具省略 summary/args/detail/tool_hints 并打 ToolsFolded（GenUI 豁免）；
//	  IterationRegionsBefore = 更早未下发区域数（显式声明的可取回窗口，**不是 gap**：
//	  窗口内迭代号连续、更早段经 POST /api/regions 整段取回）。
//	· FetchSinceWatermark（增量）：**不窗口化、不折叠** —— 增量段是「客户端已建立
//	  窗口的延伸」，增量协议语义必须逐字节保持（含 resync_required 判定）。
//
// live 进行中状态**一个字节不动**：ActiveTools/CompletedTools/StreamingTools/Content/
// Reasoning/Iteration/Phase/Seq/TurnID/SubAgents/Todos/Goal/TokenUsage/StreamStats 等
// 都是「正在跑」的权威数据，投影只重写 IterationHistory（已完成迭代）与
// IterationRegionsBefore（窗口声明）。
//
// 调用面（§7-R5 隔离）：**只有** REST 历史路径（serverapp/callbacks.go 的
// HistorySnapshot）切换到本方法；SSE 恢复（channel/web/web_sse.go）、CLI
// （cmd/xbot-cli/main.go）、RPC（serverapp/rpc_table.go）继续调用 GetActiveProgress
// —— 原方法行为与输出零变化。
func (a *Agent) GetActiveProgressFolded(ch, chatID string, fetch protocol.ProgressFetch) *protocol.ProgressEvent {
	base := a.GetActiveProgress(ch, chatID, fetch)
	if base == nil {
		return nil
	}
	// 增量路径：不折叠（见上方说明）。from_iter >= 0 即增量/水位线拉取。
	if fetch.ToFromIter() >= 0 {
		return base
	}
	// resync_required（>maxIncrementalIterations）/ 无快照（todos-only）/ 无历史：
	// 没有可折叠的迭代，原样返回（原方法已收口这些分支）。
	if len(base.IterationHistory) == 0 {
		return base
	}
	recs := activeProgressRecords(base.IterationHistory)
	window, regionsBefore := channel.RegionWindow(recs, channel.HistoryRegionWindow)
	// RegionWindow 的窗口是**输入的子切片**（尾部区域对齐的连续区间，region_view.go:170）
	// ⇒ 起始下标 = 总长 − 窗口长，二者逐位对应同一迭代。
	start := len(recs) - len(window)
	if start < 0 {
		start = 0 // 防御：RegionWindow 契约保证不会发生
	}
	folded := make([]protocol.ProgressEvent, 0, len(window))
	for i := range window {
		folded = append(folded, foldProgressIteration(base.IterationHistory[start+i], window[i]))
	}
	base.IterationHistory = folded
	base.IterationRegionsBefore = regionsBefore
	return base
}

// activeProgressToolSnap 是 channel/region_view.go:30 `regionToolSnap` 的 JSON 镜像
// （同一组 tag、逐字段同构）—— 存在的唯一原因：把快照迭代的工具**还原成 DB 侧
// iteration_history.tools 的持久化形状**，好让区域判定只走 region_view 这一份规范
// 实现（§7-R1 双实现漂移是最高风险项）。
//
// 字段与写库路径（agent/engine_run_tools.go:518-531 的 IterationToolSnapshot）逐字段
// 对齐：name/label/status/elapsed_ms/summary/args/detail/ui_mode/ui_libs/ui_surface。
// 这里**故意不写 call_id**：region_view 的解析形状里没有它（它不影响区域判定），
// 而真正的元素保留 CallID 不丢（见 foldProgressIteration 的说明）。
type activeProgressToolSnap struct {
	Name      string              `json:"name"`
	Label     string              `json:"label,omitempty"`
	Status    string              `json:"status"`
	ElapsedMS int64               `json:"elapsed_ms"`
	Summary   string              `json:"summary,omitempty"`
	Args      string              `json:"args,omitempty"`
	Detail    string              `json:"detail,omitempty"`
	UIMode    string              `json:"ui_mode,omitempty"`
	UILibs    []string            `json:"ui_libs,omitempty"`
	UISurface *protocol.UISurface `json:"ui_surface,omitempty"`
}

// activeProgressToolsJSON 把快照元素的工具还原为 DB 形状的工具 JSON 串。
//
// 类型适配决策（P1）：快照 iteration_history 的元素是 protocol.ProgressEvent
// （工具在 CompletedTools），而 RegionWindow/MapIterationRecord 的输入是
// []sqlite.IterationRecord（工具在 Tools JSON 串）——这里只做**输入方向**的最小适配：
// 用与 DB 侧同构的 JSON 形状重建 Tools，投影输出仍是 protocol.ProgressEvent
// （见 foldProgressIteration：把窗口元素重建成 HistoryIteration 会丢元素独有字段）。
//
// 空工具 ⇒ 空串（与 DB 侧「无工具」形态一致："" 与 "[]" 在 parseRegionTools 里同义）；
// marshal 失败 ⇒ 同样回落空串（绝不 panic、绝不半成品 —— 与 DB 侧解析失败同向）。
func activeProgressToolsJSON(tools []protocol.ToolProgress) string {
	if len(tools) == 0 {
		return ""
	}
	snaps := make([]activeProgressToolSnap, len(tools))
	for i, t := range tools {
		snaps[i] = activeProgressToolSnap{
			Name:      t.Name,
			Label:     t.Label,
			Status:    t.Status,
			ElapsedMS: t.Elapsed,
			Summary:   t.Summary,
			Args:      t.Args,
			Detail:    t.Detail,
			UIMode:    t.UIMode,
			UILibs:    t.UILibs,
			UISurface: t.UISurface,
		}
	}
	b, err := json.Marshal(snaps)
	if err != nil {
		return ""
	}
	return string(b)
}

// activeProgressRecord 是单条快照迭代 → sqlite.IterationRecord 的适配（只填区域判定
// 与轻字段化真正消费的字段：Iteration/Content/Reasoning/Tools）：
//   - Content/Reasoning 原样搬运 ⇒ `IsPureToolIteration`（Content==""&&Reasoning==""）
//     的判定与 DB 路径逐字同构（前端 !it.content 同构，不做 TrimSpace）；
//   - Tools 见 activeProgressToolsJSON（与 DB 形状同构 ⇒ 区域边界在
//     「busy 快照」与「提交后的历史 reload」两条链路必然一致）；
//   - 迭代级指标（Tokens/TTFTMs/…）**不搬运**：轻字段化只省略**工具详情**，
//     迭代级字段留在元素里原样下发，搬进来只会制造第二份真相。
func activeProgressRecord(it protocol.ProgressEvent) sqlite.IterationRecord {
	return sqlite.IterationRecord{
		Iteration: it.Iteration,
		Content:   it.Content,
		Reasoning: it.Reasoning,
		Tools:     activeProgressToolsJSON(it.CompletedTools),
	}
}

func activeProgressRecords(iters []protocol.ProgressEvent) []sqlite.IterationRecord {
	recs := make([]sqlite.IterationRecord, len(iters))
	for i := range iters {
		recs[i] = activeProgressRecord(iters[i])
	}
	return recs
}

// foldProgressIteration 把一个快照迭代元素投影为**折叠视图**（D3 轻字段化）。
//
// 判定来自唯一规范 channel.MapIterationRecord(rec, true)（channel/region_view.go:232）：
//   - mapped.ToolsFolded=false（GenUI-only 迭代 ⇒ 没有任何字段被省略）⇒ 元素原样返回、
//     **不打标**（与 D3「GenUI-only 迭代不瘦身、不打标」一致）；
//   - mapped.ToolsFolded=true ⇒ 逐工具省略非 GenUI 工具（UIMode==""）的
//     Summary/Args/Detail/ToolHints，元素打 ToolsFolded（与 HistoryIteration.ToolsFolded
//     同 JSON 键 tools_folded ⇒ 前端对两条链路共用一份解析）。
//
// 为什么在内联处应用轻字段、而不是把窗口元素重建成 HistoryIteration（波 0 的
// MapIterationRecord 输出）：
//  1. 容器类型由 ProgressEvent.IterationHistory []ProgressEvent 钉死 —— 换成
//     []HistoryIteration 需要一个新字段（超出 P1 边界）；
//  2. 重建会**丢元素独有字段**：CallID（regionToolSnap 无 call_id ⇒ 丢 CoT
//     START↔RESULT 配对 / promote 目标）、GenChars/StartedAt，以及 Phase/Seq/
//     SubAgents/TokenUsage/StreamStats 等 —— 这些是 CLI/前端在 live 恢复期直接消费的。
//
// 内联的那一条判定（UIMode=="" 才省略）与 region_view.go:232 逐字一致，并由
// agent/active_progress_folded_test.go 的 isomorphism 守护测试逐字段比对钉死
// （mutation：改内联规则而不改 region_view ⇒ 该测试必红）。
//
// ⚠️ out.CompletedTools **总是新建切片**：迭代历史存在 a.iterationHistories 里，
// 元素之间共享 CompletedTools 的 backing array —— 原地清字段会污染权威数据
// （原方法随后返回的快照会变成缺详情，且 DB 之外的 live 路径也读到被改过的工具）。
func foldProgressIteration(el protocol.ProgressEvent, rec sqlite.IterationRecord) protocol.ProgressEvent {
	if len(el.CompletedTools) == 0 {
		return el
	}
	if !channel.MapIterationRecord(rec, true).ToolsFolded {
		return el // GenUI-only / 无工具：无字段被省略 ⇒ 不打标
	}
	out := el
	out.CompletedTools = make([]protocol.ToolProgress, len(el.CompletedTools))
	copy(out.CompletedTools, el.CompletedTools)
	for i := range out.CompletedTools {
		if out.CompletedTools[i].UIMode != "" {
			continue // GenUI 工具豁免（ui_mode 非空 ⇒ 顶层卡片默认渲染可能消费详情）
		}
		out.CompletedTools[i].Summary = ""
		out.CompletedTools[i].Args = ""
		out.CompletedTools[i].Detail = ""
		out.CompletedTools[i].ToolHints = ""
	}
	out.ToolsFolded = true
	return out
}

// GetTodos returns the TODO items for the given channel:chatID session.
func (a *Agent) GetTodos(ch, chatID string) []protocol.TodoItem {
	key := ch + ":" + chatID
	if a.todoManager == nil {
		return []protocol.TodoItem{}
	}
	items := a.todoManager.GetTodos(key)
	if len(items) == 0 {
		return []protocol.TodoItem{}
	}
	result := make([]protocol.TodoItem, len(items))
	for i, t := range items {
		result[i] = protocol.TodoItem{Text: t.Text, Status: t.Status}
	}
	return result
}

// SetTodos replaces the persisted TODO list for a session (user edit from the
// web UI: rename an item, toggle status, or drop one). Emits a progress event
// carrying the new list so every client — and the active-progress snapshot
// used on session switch — sees the edit immediately instead of waiting for
// the next agent iteration.
func (a *Agent) SetTodos(ch, chatID string, items []protocol.TodoItem) {
	if a.todoManager == nil {
		return
	}
	key := ch + ":" + chatID
	todos := make([]tools.TodoItem, len(items))
	for i, it := range items {
		todos[i] = tools.TodoItem{Text: it.Text, Status: it.Status}
	}
	a.todoManager.SetTodos(key, todos)
	a.emitTodosProgress(ch, chatID)
}

// emitTodosProgress pushes a lightweight progress event carrying the current
// TODO list (plus the goal when one is active). Mirrors emitGoalProgress, but
// does not require a goal to exist — an edit to the todo list must reach the UI
// even in a session with no goal.
func (a *Agent) emitTodosProgress(chName, chatID string) {
	progressKey := qualifyChatID(chName, chatID)
	seqPtr, _ := a.builtinProgressSeq.LoadOrStore(progressKey, &atomic.Uint64{})
	seq := seqPtr.(*atomic.Uint64).Add(1)
	payload := &protocol.ProgressEvent{
		ChatID:    progressKey,
		Phase:     "",
		Seq:       seq,
		TurnID:    a.getActiveTurnID(progressKey),
		Iteration: 0,
		Todos:     a.GetTodos(chName, chatID),
		Goal:      a.GetGoal(chName, chatID),
	}
	if a.channelRange != nil {
		a.channelRange(func(_ string, ch channel.Channel) bool {
			if sender, ok := ch.(channel.ProgressSender); ok {
				sender.SendProgress(chatID, cloneProgressEvent(payload))
			}
			return true
		})
	}
	// Keep the snapshot fresh so GetActiveProgress returns the edited list.
	a.lastProgressSnapshot.Store(progressKey, progressSnapshotWithoutHistory(payload))
}

// GetGoal returns the goal state for the given channel:chatID session.
func (a *Agent) GetGoal(ch, chatID string) *protocol.GoalInfo {
	if a.goalManager == nil {
		return nil
	}
	return a.goalManager.GoalInfo(ch + ":" + chatID)
}

// SetGoal sets a goal for the given channel:chatID session.
func (a *Agent) SetGoal(ch, chatID, objective string) {
	if a.goalManager == nil {
		return
	}
	a.goalManager.Set(ch+":"+chatID, objective)
	// Push a progress event so the frontend displays the GoalBanner immediately.
	a.emitGoalProgress(ch, chatID)
}

// ClearGoal clears the goal for the given channel:chatID session.
func (a *Agent) ClearGoal(ch, chatID string) {
	if a.goalManager == nil {
		return
	}
	a.goalManager.Clear(ch + ":" + chatID)
	// Push a progress event with nil goal so the frontend removes the GoalBanner.
	progressKey := ch + ":" + chatID
	seqPtr, _ := a.builtinProgressSeq.LoadOrStore(progressKey, &atomic.Uint64{})
	seq := seqPtr.(*atomic.Uint64).Add(1)
	payload := &protocol.ProgressEvent{
		ChatID:    progressKey,
		Phase:     "",
		Seq:       seq,
		TurnID:    a.getActiveTurnID(progressKey),
		Iteration: 0,
		Todos:     a.GetTodos(ch, chatID),
		Goal:      &protocol.GoalInfo{Objective: "", Status: protocol.GoalStatusCleared}, // explicit cleared marker (objective:"" survives GoalInfo's non-omitempty json tag) — Goal: nil is invisible to the frontend (ProgressEvent.Goal omitempty drops the field entirely, indistinguishable from "not carried"), which broke the clear chain (xbotgh CR: "用户删除目标后 GoalBanner 不消失")
	}
	if a.channelRange != nil {
		a.channelRange(func(_ string, ch channel.Channel) bool {
			if sender, ok := ch.(channel.ProgressSender); ok {
				sender.SendProgress(chatID, cloneProgressEvent(payload))
			}
			return true
		})
	}
	a.lastProgressSnapshot.Store(progressKey, progressSnapshotWithoutHistory(payload))
}

// GetExportIterations returns per-iteration records for session export,
// combining the persisted iteration_history table (completed iterations with
// TTFT/TPOT/tokens/timing) with the in-flight iteration's partial stream
// content (graceful shutdown / benchmark timeout).
func (a *Agent) GetExportIterations(ch, chatID string) []protocol.ExportedIteration {
	// 1. Completed iterations from iteration_history (DB authoritative).
	var records []sqlite.IterationRecord
	if a.multiSession != nil {
		if sess, err := a.multiSession.GetOrCreateSession(ch, chatID); err == nil {
			if tenantID := sess.TenantID(); tenantID > 0 {
				// v71（每会话一个 DB）：iteration_history 在会话库 —— 经
				// TenantSession.SessionService()（绑定会话库）查询。
				records, _ = sess.SessionService().GetAllIterationHistory(tenantID)
			}
		}
	}

	iterations := make([]protocol.ExportedIteration, 0, len(records)+1)
	for _, r := range records {
		iterations = append(iterations, protocol.ExportedIteration{
			TurnID:       r.TurnID,
			Iteration:    r.Iteration,
			Content:      r.Content,
			Reasoning:    r.Reasoning,
			Tools:        r.Tools,
			Tokens:       r.Tokens,
			TTFTMs:       r.TTFTMs,
			TPOTMs:       r.TPOTMs,
			TokensPerSec: r.TokensPerSec,
			TotalMs:      r.TotalMs,
		})
	}

	// 2. In-flight iteration (partial stream content) from lastProgressSnapshot.
	key := ch + ":" + chatID
	v, ok := a.lastProgressSnapshot.Load(key)
	if !ok {
		return iterations
	}
	snapshot := v.(*protocol.ProgressEvent)
	result := *snapshot
	a.mergeStreamState(key, &result)

	// The in-flight iteration number is the snapshot's current Iteration
	// (set by beginIteration); derive from history when it's 0.
	inFlightIter := result.Iteration
	if inFlightIter == 0 && len(result.IterationHistory) > 0 {
		inFlightIter = result.IterationHistory[len(result.IterationHistory)-1].Iteration + 1
	}

	// Only add when there's real partial content worth preserving.
	if inFlightIter > 0 && (result.StreamContent != "" || result.ReasoningStreamContent != "" || len(result.ActiveTools) > 0) {
		var ttft, tpot, tps, totalMs int64
		if result.StreamStats != nil {
			ttft = result.StreamStats.TTFTMs
			tpot = result.StreamStats.TPOTMs
			tps = result.StreamStats.TokensPerSec
			totalMs = result.StreamStats.TotalMs
		}
		iterations = append(iterations, protocol.ExportedIteration{
			TurnID:       result.TurnID,
			Iteration:    inFlightIter,
			Content:      result.StreamContent,
			Reasoning:    result.ReasoningStreamContent,
			TTFTMs:       ttft,
			TPOTMs:       tpot,
			TokensPerSec: tps,
			TotalMs:      totalMs,
			InFlight:     true,
		})
	}

	return iterations
}
