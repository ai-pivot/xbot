package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/session"
	"xbot/tools"

	"github.com/google/uuid"
)

// injectSystemNotes appends runtime state notes (background tasks, interactive
// agents, active groups) to the last user message in the slice. The message
// struct is copied to avoid mutating session data.
func (a *Agent) injectSystemNotes(messages []llm.ChatMessage, channel, chatID string) []llm.ChatMessage {
	var systemNotes []string

	// Background tasks
	if bgMgr := a.bgTaskMgr.Load(); bgMgr != nil {
		sessionKey := qualifyChatID(channel, chatID)
		running := bgMgr.ListRunning(sessionKey)
		if len(running) > 0 {
			var ids []string
			for _, t := range running {
				ids = append(ids, t.ID)
			}
			systemNotes = append(systemNotes, fmt.Sprintf("Running background tasks: %s", strings.Join(ids, ", ")))
		}
	}

	// Interactive agent sessions
	sessions := a.ListInteractiveSessions(channel, chatID)
	if len(sessions) > 0 {
		var agentParts []string
		for _, s := range sessions {
			status := "idle"
			if s.Running {
				status = "running"
			}
			mode := "fg"
			if s.Background {
				mode = "bg"
			}
			agentParts = append(agentParts, fmt.Sprintf("%s/%s(%s,%s)", s.Role, s.Instance, mode, status))
		}
		systemNotes = append(systemNotes, fmt.Sprintf("Active interactive agents: %s", strings.Join(agentParts, ", ")))
	}

	// Active group chats
	groups := tools.ListGroups()
	if len(groups) > 0 {
		var groupParts []string
		for _, g := range groups {
			status := "open"
			if g.Closed {
				status = "closed"
			}
			members := strings.Join(g.Members, ",")
			groupParts = append(groupParts, fmt.Sprintf("%s(%s, %d members: %s)", g.Name, status, len(g.Members), members))
		}
		systemNotes = append(systemNotes, fmt.Sprintf("Groups: %s", strings.Join(groupParts, "; ")))
	}

	if len(systemNotes) > 0 {
		info := "\n[System] " + strings.Join(systemNotes, " | ")
		// Append to a copy of the last user message to avoid mutating session data
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				m := messages[i] // shallow copy
				m.Content += info
				messages[i] = m
				break
			}
		}
	}
	return messages
}

func (a *Agent) enqueueBgNotification(notif tools.BgNotification) {
	sessionKey := notif.SessionKey()
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	if a.bgRunPending == nil {
		a.bgRunPending = make(map[string][]tools.BgNotification)
	}
	a.bgRunPending[sessionKey] = append(a.bgRunPending[sessionKey], notif)
}

func (a *Agent) enqueueBgNotifications(notifs []tools.BgNotification) {
	if len(notifs) == 0 {
		return
	}
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	if a.bgRunPending == nil {
		a.bgRunPending = make(map[string][]tools.BgNotification)
	}
	for _, notif := range notifs {
		sessionKey := notif.SessionKey()
		a.bgRunPending[sessionKey] = append(a.bgRunPending[sessionKey], notif)
	}
}

func (a *Agent) takePendingBgNotifications(sessionKey string) []tools.BgNotification {
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	if len(a.bgRunPending) == 0 {
		return nil
	}
	pending := a.bgRunPending[sessionKey]
	delete(a.bgRunPending, sessionKey)
	return pending
}

// sameBgNotification 判断两条通知是不是**同一条事件实例**。
//
// BgNotification 的四种实现（*BackgroundTask / *SubAgentBgNotify / *CronFired /
// *AsyncMessageNotification）都是指针，指向同一对象的两个接口值即同一条事件。
// 这里用 reflect 取指针值而不是直接 `x == y`：接口比较在动态类型不可比较
// （未来有人加一个含 slice 的值类型实现）时会**直接 panic**，而这条路径在
// 投递失败的回滚链路上——panic 比丢一条通知更糟。
func sameBgNotification(x, y tools.BgNotification) bool {
	if x == nil || y == nil {
		return x == nil && y == nil
	}
	vx, vy := reflect.ValueOf(x), reflect.ValueOf(y)
	if vx.Kind() != reflect.Ptr || vy.Kind() != reflect.Ptr {
		return false
	}
	if vx.Type() != vy.Type() || vx.IsNil() || vy.IsNil() {
		return false
	}
	return vx.Pointer() == vy.Pointer()
}

// takeOneBgNotification 从 sessionKey 的 pending 队列里精确移除**这一条**通知，
// 其余兄弟通知按原顺序留在队列里。返回是否真的找到并移除。
//
// 为什么必须逐条取（2026-11-04 工部验收打回项 1）：
// takePendingBgNotifications 是"取走该 key 全部"，调用方只把**自己那一条**
// 在失败时补回 ⇒ 同一 key 上并发的 N 条 pending 里有 N−1 条被永久吞掉。
// 典型场景：一个子代理会话的 cron 到期事件与另一条通知同 key 同时在队，
// 前者投递失败补回自己，后者的兄弟条目就再也不会被任何路径投递。
//
// 锁：bgRunPendingMu（与 enqueue/take/acknowledge 共用同一把互斥锁，
// 所以"移除本条"与"并发入队"之间不会丢事件）。
//
// 返回值是**认领凭证**，调用方必须用它判定（2026-11-04 门下省 N1）：
// 命中=本条归调用方独占；未命中=事件已被并发路径（同 session chatWorker 的
// wireBgNotificationDrain）整队列取走并已注入，调用方**必须放手**，
// 不许再投一次、也不许把它补回队列。
func (a *Agent) takeOneBgNotification(sessionKey string, target tools.BgNotification) bool {
	if target == nil {
		return false
	}
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	pending := a.bgRunPending[sessionKey]
	for i, n := range pending {
		if !sameBgNotification(n, target) {
			continue
		}
		rest := make([]tools.BgNotification, 0, len(pending)-1)
		rest = append(rest, pending[:i]...)
		rest = append(rest, pending[i+1:]...)
		if len(rest) == 0 {
			delete(a.bgRunPending, sessionKey)
		} else {
			a.bgRunPending[sessionKey] = rest
		}
		return true
	}
	return false
}

func (a *Agent) pendingBgNotifications(sessionKey string) []tools.BgNotification {
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	pending := a.bgRunPending[sessionKey]
	if len(pending) == 0 {
		return nil
	}
	out := make([]tools.BgNotification, len(pending))
	copy(out, pending)
	return out
}

func (a *Agent) acknowledgePendingBgNotifications(sessionKey string, count int) {
	if count <= 0 {
		return
	}
	a.bgRunPendingMu.Lock()
	defer a.bgRunPendingMu.Unlock()
	pending := a.bgRunPending[sessionKey]
	if count >= len(pending) {
		delete(a.bgRunPending, sessionKey)
		return
	}
	a.bgRunPending[sessionKey] = append([]tools.BgNotification(nil), pending[count:]...)
}

func backgroundNotificationSyntheticTool(notif tools.BgNotification, seq int) (llm.ChatMessage, llm.ChatMessage, IterationToolSnapshot, bool) {
	toolName := ""
	toolID := ""
	toolContent := ""
	label := ""
	// summary is the concise one-liner the web shows on the collapsed card;
	// hints is the UI-only structured payload (original task/command, status,
	// duration, output preview) the web expands into. Neither is sent to the
	// model — it only sees toolContent.
	summary := ""
	hints := ""
	var elapsedMS int64

	switch n := notif.(type) {
	case *tools.BackgroundTask:
		toolName = "background_task_result"
		toolID = "bg_" + n.ID
		toolContent = tools.FormatBgTaskCompletion(n, "")
		label = fmt.Sprintf("bg:%s", n.ID)
		summary = fmt.Sprintf("背景任务 %s · %s", n.ID, n.Status)
		hints = tools.EncodeSyntheticToolHints(tools.BgTaskHints(n))
		if n.FinishedAt != nil {
			elapsedMS = n.FinishedAt.Sub(n.StartedAt).Milliseconds()
		}
	case *tools.SubAgentBgNotify:
		if n.Type != tools.SubAgentBgNotifyCompleted {
			return llm.ChatMessage{}, llm.ChatMessage{}, IterationToolSnapshot{}, false
		}
		toolName = "bg_subagent_" + string(n.Type)
		toolID = fmt.Sprintf("bgsub_%s_%s_%d", n.Role, n.Instance, seq)
		toolContent = tools.FormatSubAgentBgNotify(n)
		label = fmt.Sprintf("bgsub:%s/%s", n.Role, n.Instance)
		summary = fmt.Sprintf("子代理 %s/%s 已完成", n.Role, n.Instance)
		hints = tools.EncodeSyntheticToolHints(tools.SubAgentHints(n))
		elapsedMS = n.Elapsed.Milliseconds()
	case *tools.CronFired:
		toolName = "cron_fired"
		toolID = fmt.Sprintf("cron_cancel_%d", seq)
		toolContent = fmt.Sprintf("A scheduled cron job fired.\n\nMessage: %s", n.Message)
		label = "cron"
		summary = "定时任务已触发"
		hints = tools.EncodeSyntheticToolHints(tools.MessageHints("cron", n.Message))
	case *tools.AsyncMessageNotification:
		toolName = "async_message"
		toolID = fmt.Sprintf("async_cancel_%d", seq)
		toolContent = n.Content
		label = "async_message"
		summary = "收到异步消息"
		hints = tools.EncodeSyntheticToolHints(tools.MessageHints("async", n.Content))
	default:
		return llm.ChatMessage{}, llm.ChatMessage{}, IterationToolSnapshot{}, false
	}

	assistantMsg, toolMsg := newSyntheticToolPair(toolName, toolID, toolContent)
	assistantMsg.DisplayOnly = true
	toolMsg.DisplayOnly = true
	snapshot := IterationToolSnapshot{
		Name:      toolName,
		Label:     label,
		Status:    string(ToolDone),
		ElapsedMS: elapsedMS,
		Summary:   summary,
		// Unified field: injected tools carry their (user-facing) result text in
		// Detail, exactly like Shell/Read carry their output — the web renderer
		// shows it as markdown.
		Detail:    toolContent,
		ToolHints: hints,
	}
	return assistantMsg, toolMsg, snapshot, true
}

func userCancelledSyntheticTool() (llm.ChatMessage, llm.ChatMessage, IterationToolSnapshot) {
	const toolName = "user_cancelled"
	const toolID = "user_cancelled"
	const content = "User cancelled this run with Ctrl+C. Treat the previous turn as interrupted. Do not continue unfinished actions unless the user asks to resume."

	assistantMsg, toolMsg := newSyntheticToolPair(toolName, toolID, content)
	assistantMsg.DisplayOnly = true
	toolMsg.DisplayOnly = true
	snapshot := IterationToolSnapshot{
		Name:    toolName,
		Label:   "cancelled by user",
		Status:  string(ToolDone),
		Summary: content,
	}
	return assistantMsg, toolMsg, snapshot
}

// wireBgNotificationDrain creates a DrainBgNotifications callback for Run()
// that returns only notifications matching the given session key.
func (a *Agent) wireBgNotificationDrain(sessionKey string) func() []tools.BgNotification {
	return func() []tools.BgNotification {
		// 插队优先队列优先：结构化消息（answer/decision/普通插话）在同一个
		// iteration 边界被消费，且排在后台通知之前（设计 §9.2 严格优先）。
		structured := a.drainStructuredQueue(sessionKey)
		mine := a.takePendingBgNotifications(sessionKey)
		// Track drained notifications so cancel can persist them explicitly. If the
		// Run is cancelled after draining, these notifications were consumed from
		// bgRunPending and must be recorded in the interrupted turn instead of
		// delivered as a fresh user message after Ctrl+C.
		if len(mine) > 0 || len(structured) > 0 {
			if state, ok := a.bgSessionStates.Load(sessionKey); ok {
				ss := state.(*bgSessionState)
				ss.drainedThisRunMu.Lock()
				ss.drainedThisRun = append(ss.drainedThisRun, structured...)
				ss.drainedThisRun = append(ss.drainedThisRun, mine...)
				ss.drainedThisRunMu.Unlock()
			}
		}
		if len(structured) == 0 {
			return mine
		}
		return append(structured, mine...)
	}
}

func (a *Agent) wireBgNotificationAcknowledge(sessionKey string) func(int) {
	return func(count int) {
		if state, ok := a.bgSessionStates.Load(sessionKey); ok {
			state.(*bgSessionState).acknowledgeDrainedThisRun(count)
		}
	}
}

func (a *Agent) requeueDrainedBgNotifications(sessionKey string) {
	state, ok := a.bgSessionStates.Load(sessionKey)
	if !ok {
		return
	}
	drained := state.(*bgSessionState).takeDrainedThisRun()
	if len(drained) == 0 {
		return
	}

	a.bgRunPendingMu.Lock()
	a.bgRunPending[sessionKey] = append(drained, a.bgRunPending[sessionKey]...)
	a.bgRunPendingMu.Unlock()
}

// bgNotificationSectionLabel 生成每条通知的**来源标识**，用于「N 合 1」时的分节头。
//
// 契约（用户 2026-09-18 要求）：N 条通知合并成一条消息时，必须一眼看出**每条通知的
// 范围与来源** —— 分节头形如 `【i/N】后台任务 3f8f492a · cargo check`；节间沿用既有
// 分隔符（"\n\n---\n\n"），单条通知保持原有形态不加标号。
func bgNotificationSectionLabel(notif tools.BgNotification) string {
	switch n := notif.(type) {
	case *tools.BackgroundTask:
		label := "后台任务 " + n.ID
		if cmd := tools.TruncateHeadPreview(firstNonEmptyLine(n.Command), 80); cmd != "" {
			label += " · " + cmd
		}
		return label
	case *tools.SubAgentBgNotify:
		return "子代理 " + n.Role + "/" + n.Instance
	case *tools.CronFired:
		label := "定时任务"
		if msg := tools.TruncateHeadPreview(firstNonEmptyLine(n.Message), 80); msg != "" {
			label += " · " + msg
		}
		return label
	case *tools.AsyncMessageNotification:
		if n.Source != "" {
			return "异步消息（" + n.Source + "）"
		}
		return "异步消息"
	default:
		return "通知"
	}
}

// firstNonEmptyLine 取首个非空行（用 strings.Lines —— 不在 Go 源码里写换行字面量）。
func firstNonEmptyLine(s string) string {
	for line := range strings.Lines(s) {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// drainAndProcessNotifications drains bg notifications for the given session
// from bgRunPending and processes them via processBgNotification/processSubAgentBgNotification.
// Called by chatProcessLoop after each turn completes (response sent), and by
// chatWorker when idle. Safe for concurrent use — bgRunPendingMu serializes access.
//
// ⛔ **一次突发 = 一条 user 消息 = 一个 turn**（2026-09-18 用户 P0 修复）：
// 所有排空的通知按 `"\n\n---\n\n"` 拼成**一条** user 消息注入。旧实现「每条通知
// 各自注入一条 user 消息」会让 N 条通知变成 N 条消息 —— 收尾 drain 一次性注入后，
// 队列开始跑第 1 条（busy=true），**其余 N-1 条卡在用户可见的排队 tray 里**
// （用户截图：Next turn 83 + Turn 84–88，既不能立刻发出、也拿不到一次处理完）。
// 批量成一条同时保证：一个 turn 处理完所有事件、不会挤爆队列。
func (a *Agent) drainAndProcessNotifications(sessionKey string) {
	// ⚠️ 校验必须在 take **之前**。
	//
	// 2026-10-27 故障：key 有时是 agent 调用链（如
	// `main/court.shoufu/court.bingbu`），而不是 `channel:chatID`。旧实现先
	// takePendingBgNotifications 把通知取走，再校验 key，非法就直接 return
	// —— 通知被**永久吞掉**。表现就是：子代理明明结束了，父会话/御前收不到
	// 任何完成通知（本次首辅 idle 落库于 08:49:24Z，我却一直没收到）。
	// 现在：非法 key 走 agent-路径兜底，绝不丢。
	if strings.Count(sessionKey, ":") == 0 {
		a.deliverNotificationsByAgentPath(sessionKey)
		return
	}

	parts := strings.SplitN(sessionKey, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		a.deliverNotificationsByAgentPath(sessionKey)
		return
	}

	mine := a.takePendingBgNotifications(sessionKey)
	if len(mine) == 0 {
		return
	}
	channelName, chatID := parts[0], parts[1]
	// 兜底归一化（2026-11-03 P0 子代理通知投错会话）：
	// 上游若把子代理的 parentKey 存成执行上下文镜像 "agent:<ch>:<chatID>"，
	// 通知会被投进一个独立的镜像会话，父会话永远收不到（线上实证 36 次：
	// {"channel":"agent","chat_id":"web:web-4","msg":"Bg notifications: injected ..."}）。
	// 归一化统一走 sessionAddress，不在调用点手拼。
	if addr, ok := sessionAddress(channelName, chatID); ok {
		ch, rest, _ := strings.Cut(addr, ":")
		channelName, chatID = ch, rest
	}
	if isMirrorSessionAddress(channelName + ":" + chatID) {
		log.WithFields(log.Fields{"session_key": sessionKey, "target": channelName + ":" + chatID}).
			Error("drainAndProcessNotifications: target is still a mirror session — notification will be misdelivered")
	}

	// Inject each notification individually. Notifications are admitted
	// sequentially through the session queue, so per-session FIFO ordering is
	// preserved (queue seq = turn_id = processing order).
	type bgSection struct{ label, content string }
	var sections []bgSection
	// cron/非 cron 节计数：一次突发 = 一条 user 消息 = 一个 turn，所以「这条
	// 合并消息是否整条来自 cron」只能按整条判（见 bgSectionsTriggerSource）。
	cronSections, otherSections := 0, 0
	// 发送方取**第一条有 sender 的通知**（批量成一条消息后只有一个 sender 字段）；
	// 后续无 sender 的通知不会"继承"上一条身份 —— 与逐条注入时的语义一致（CR#4）。
	firstSender := ""
	for _, notif := range mine {
		var content string
		// Per-notification sender: NOT inherited across iterations. A
		// SenderID-less notification (e.g. CronFired without a sender) following
		// one WITH a sender must not adopt the previous identity (CR#4).
		senderID := ""
		switch n := notif.(type) {
		case *tools.BackgroundTask:
			// Offload large output per-task
			outputOverride := ""
			if a.offloadStore != nil && n.Output != "" {
				offloadCtx := context.Background()
				if offloaded, ok := a.offloadStore.MaybeOffload(offloadCtx, sessionKey, sessionKey,
					"background_task_result", n.Command, n.Output,
					"", "", ""); ok {
					outputOverride = offloaded.Summary
				}
			}
			content = tools.FormatBgTaskCompletion(n, outputOverride)
			senderID = n.SenderID()
		case *tools.SubAgentBgNotify:
			if n.Type != tools.SubAgentBgNotifyCompleted {
				continue // drop progress during idle
			}
			content = tools.FormatSubAgentBgNotify(n)
			senderID = n.SenderID()
		case *tools.CronFired:
			content = fmt.Sprintf("⏰ [定时任务触发] %s", n.Message)
			senderID = n.SenderID()
		case *tools.AsyncMessageNotification:
			content = n.Content
			senderID = n.SenderID()
		default:
			continue
		}
		if content == "" {
			continue
		}
		sections = append(sections, bgSection{label: bgNotificationSectionLabel(notif), content: content})
		if _, isCron := notif.(*tools.CronFired); isCron {
			cronSections++
		} else {
			otherSections++
		}
		if firstSender == "" && senderID != "" {
			firstSender = senderID
		}
	}

	if len(sections) == 0 {
		return
	}

	// ⛔ **一次突发 = 一条 user 消息 = 一个 turn**（2026-09-18 用户 P0）。
	// 旧实现「每条通知各自注入一条 user 消息」会让 6 条通知变成 6 条消息：
	// 收尾 drain 一次性注入后，队列开始跑第 1 条（busy=true），**其余 5 条就卡在
	// 用户可见的排队 tray 里**（截图：Next turn 83 + Turn 84–88），用户既没法立刻
	// 发出、也拿不到"一次处理完"。这正是 AGENTS.md 早已写明的契约：
	//   「drainAndProcessNotifications batches all notifications into ONE user
	//     message … joined with "\n\n---\n\n" … avoids … triggering N separate
	//     agent turns when multiple bg tasks complete simultaneously.」
	var joined string
	if len(sections) == 1 {
		// 单条通知：**保持原有形态**（⏰ / [System Notification] 等前导标记是既有约定，
		// 其他消费者依赖它们 —— 不加标号、不加总览）。
		joined = sections[0].content
	} else {
		sectionTexts := make([]string, 0, len(sections))
		for i, sec := range sections {
			// 范围（【i/N】）+ 来源（节头标签）显式标出，正文紧随其后。
			sectionTexts = append(sectionTexts, fmt.Sprintf("【%d/%d】%s\n%s", i+1, len(sections), sec.label, sec.content))
		}
		joined = strings.Join(sectionTexts, "\n\n---\n\n")
		// 总览：让读者立刻知道这条消息里有 N 条通知、每节都有【i/N】标号与来源。
		joined = fmt.Sprintf("🔔 合并通知 ×%d（每节以 【i/N】 标注范围，来源见节头）\n\n%s", len(sections), joined)
	}
	// 整条都是 cron 才标记（混批不标：那一轮里可能夹着人要看的真实结果）。
	triggerSource := bgSectionsTriggerSource(cronSections, otherSections)
	a.injectBgUserMessageWithTriggerSource(channelName, chatID, firstSender, joined, triggerSource)
	log.WithFields(log.Fields{
		"channel":        channelName,
		"chat_id":        chatID,
		"notif_count":    len(sections),
		"trigger_source": triggerSource,
	}).Info("Bg notifications: injected as ONE batched user message")
}

// handleCancelledRun persists un-saved engine messages and iteration history
// when a Run is cancelled, then returns a minimal OutboundMessage so the
// channel knows processing ended.
func (a *Agent) handleCancelledRun(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession) (*channel.OutboundMsg, error) {
	// Cancel path completeness (STATE-MODEL C-6 / C-7).
	//
	// Cancellation can land mid-iteration, BEFORE snapshotCompletedIteration
	// ran, so the in-flight iteration would otherwise have no `done` and the
	// turn would never be closed — the client then keeps rendering a live block
	// that never commits (contract violation visible to the user as "the
	// iteration disappeared"). Reconstruct the block from the live progress
	// snapshot (interrupted tools → failed) and close the turn. Both emissions
	// are idempotent: the tracker drops a duplicate already sent by the
	// snapshot path.
	progressKey := qualifyChatID(msg.Channel, msg.ChatID)
	cancelTurnID := turnIDFromMetadata(msg.Metadata)
	if cancelTurnID == 0 {
		cancelTurnID = a.getActiveTurnID(progressKey)
	}
	if block := a.cancelledIterationBlock(progressKey, cancelTurnID); block != nil {
		a.emitIterationDone(msg.Channel, msg.ChatID, *block)
	}
	a.emitTurnFinal(msg.Channel, msg.ChatID, protocol.TurnFinalFrame{
		TurnID:    cancelTurnID,
		Cancelled: true,
	})

	// Persist pending notifications for this session into the interrupted turn.
	// Ctrl+C should not start a fresh bg-notification turn, but completed work
	// should remain visible to the next model call as tool observations.
	sessionKey := qualifyChatID(msg.Channel, msg.ChatID)
	pendingNotifications := a.pendingBgNotifications(sessionKey)
	var drainedNotifications []tools.BgNotification
	var sessionState *bgSessionState
	if state, ok := a.bgSessionStates.Load(sessionKey); ok {
		sessionState = state.(*bgSessionState)
		drainedNotifications = sessionState.snapshotDrainedThisRun()
	}
	if len(pendingNotifications)+len(drainedNotifications) > 0 {
		log.Ctx(ctx).WithFields(log.Fields{
			"pending": len(pendingNotifications),
			"drained": len(drainedNotifications),
		}).Info("Recording background notifications in cancelled turn")
	}

	notifications := make([]tools.BgNotification, 0, len(drainedNotifications)+len(pendingNotifications))
	notifications = append(notifications, drainedNotifications...)
	notifications = append(notifications, pendingNotifications...)
	batch := make([]llm.ChatMessage, 0, len(out.EngineMessages)+2*len(notifications)+3)
	// Save any un-persisted engine messages from the interrupted iteration.
	for _, em := range out.EngineMessages {
		if err := assertNoSystemPersist(em); err != nil {
			continue
		}
		batch = append(batch, em)
	}
	if len(out.EngineMessages) > 0 {
		log.Ctx(ctx).Infof("Cancelled: prepared %d un-persisted engine messages", len(out.EngineMessages))
	}
	// iteration_history is written by snapshotCompletedIteration during the Run.
	// handleCancelledRun only persists the [interrupted] message and synthetic
	// tool messages (notifications + user_cancelled). No Detail JSON, no
	// iteration_history writes — no duplication.
	iterHistory := out.IterationHistory

	// Restart recovery: after a graceful-shutdown restart, the resumed Run's
	// iterationSnapshots is empty. reconstructIterationsFromMessages rebuilds
	// from DB tool_calls so the [interrupted] message has iteration context.
	if len(iterHistory) == 0 && tenantSession != nil {
		if dbMsgs, err := tenantSession.GetMessages(); err == nil {
			iterHistory = reconstructIterationsFromMessages(dbMsgs)
		}
	}

	appendCancelToolSnapshot := func(snapshot IterationToolSnapshot) {
		if len(iterHistory) == 0 {
			iterHistory = []IterationSnapshot{{Iteration: 1}}
		}
		idx := len(iterHistory) - 1
		if iterHistory[idx].Iteration == 0 {
			iterHistory[idx].Iteration = idx + 1
		}
		iterHistory[idx].Tools = append(iterHistory[idx].Tools, snapshot)
	}
	appendCancelTool := func(assistantMsg, toolMsg llm.ChatMessage, snapshot IterationToolSnapshot) {
		batch = append(batch, assistantMsg, toolMsg)
		appendCancelToolSnapshot(snapshot)
	}

	for i, notif := range notifications {
		assistantMsg, toolMsg, snapshot, ok := backgroundNotificationSyntheticTool(notif, i+1)
		if !ok {
			continue
		}
		appendCancelTool(assistantMsg, toolMsg, snapshot)
	}
	cancelAssistantMsg, cancelToolMsg, cancelSnapshot := userCancelledSyntheticTool()
	appendCancelTool(cancelAssistantMsg, cancelToolMsg, cancelSnapshot)

	if len(iterHistory) > 0 {
		cancelMsg := llm.NewAssistantMessage("[interrupted]")
		cancelMsg.Interrupted = true
		if tid, err := strconv.ParseUint(msg.Metadata["turn_id"], 10, 64); err == nil && tid > 0 {
			cancelMsg.TurnID = tid
		}
		// Detail JSON is no longer written — iteration_history table is the
		// single source of truth (v55+). Detail remains only for old data.
		batch = append(batch, cancelMsg)
	}
	if tenantSession != nil {
		_, err := tenantSession.AppendMessages(batch)
		if err != nil {
			a.requeueDrainedBgNotifications(sessionKey)
			return nil, fmt.Errorf("append cancelled run batch: %w", err)
		}
		// iteration_history is already written by snapshotCompletedIteration
		// (called after each executeToolCalls). handleCancelledRun does NOT
		// write additional records — no duplication.
	}
	// Pending notifications and the per-run drained ledger are acknowledgements:
	// clear them only after the interrupted turn is durably committed.
	a.acknowledgePendingBgNotifications(sessionKey, len(pendingNotifications))
	if sessionState != nil {
		sessionState.clearDrainedThisRun()
	}
	// Send a minimal outbound so the web channel knows processing ended.
	// cancel ack 只带 cancelled=true —— 不传 progress_history 大数据。
	// 前端 cancel 后进行中迭代（tool executing 中断）通过现有 SSE gap 恢复机制
	// 获取：前端 detect 迭代 gap → get_active_progress RPC 请求增量迭代数据
	// （后端 lastProgressSnapshot 保留进行中迭代的 ActiveTools）。
	meta := map[string]string{"cancelled": "true", channel.MetaFinalReply: "true"}
	// ⛔ TurnID **必须**打上（2026-10-22 用户 P0）：这条 ack 是**被取消那一轮**的
	// 权威收尾帧。旧实现直接返回 `&channel.OutboundMsg{}`（绕过 sendMessage 的
	// turn_id 盖章）⇒ SSE 载荷里 `turn_id` 缺省（`omitempty`）⇒ 前端拿到
	// `text_final{turn_id:null, cancelled:true}`：
	//   · web/`reduce.ts` 回落到 activeTurn（碰巧对上，所以 web 看不出问题）；
	//   · 原生模型链路**没有兜底 turn**，只能丢弃 ⇒ 该回合永远没有 `final`
	//     ⇒ 行恒 `turn-N-live`（`isLive`）⇒ cancel 后发新消息时那条 assistant
	//     被当成"在飞尾项"搬到列表末尾且一直 busy（用户现场：
	//     「历史变成 user1 user2 + iter1-20 busy」）。
	// `cancelTurnID` 是对这一轮的权威身份（即使 activeTurnID 已清也不会错），
	// 所以走 ① 优先级（explicit），**不能**只依赖 getActiveTurnID。
	return &channel.OutboundMsg{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		Content:  "",
		Metadata: meta,
		TurnID:   a.outboundTurnID(msg.Channel, msg.ChatID, meta, cancelTurnID),
	}, nil
}

// handleRunOutput processes the successful result of a Run() call:
// - WaitingUser: send WaitingUser outbound
// - Empty content with mandatory reply: send warning
// buildWaitingUserOutbound constructs the WaitingUser OutboundMsg from a RunOutput.
// Shared by handleRunOutput (main message path) and card_handler.go (card action path).
func buildWaitingUserOutbound(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession) *channel.OutboundMsg {
	log.Ctx(ctx).Info("Tool is waiting for user response, sending WaitingUser outbound")
	meta := map[string]string{}
	for k, v := range out.Metadata {
		meta[k] = v
	}
	if meta["request_id"] == "" {
		meta["request_id"] = uuid.NewString()
	}
	// Persist iteration history to session so it survives restarts.
	// iteration_history is already written by snapshotCompletedIteration
	// (called after each executeToolCalls). No Detail JSON needed (v55+).
	if len(out.IterationHistory) > 0 {
		histMsg := llm.NewAssistantMessage("")
		// Stamp the current turn ID so the empty waiting-user assistant row
		// belongs to turn N — without it the row is turn_id=0 and
		// deriveTurnIDs mis-attributes it to the ANSWER turn (N+1), producing
		// a forged assistant row before the answer user message.
		if tid, err := strconv.ParseUint(msg.Metadata["turn_id"], 10, 64); err == nil && tid > 0 {
			histMsg.TurnID = tid
		}
		if err := tenantSession.AddMessage(histMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save waitingUser iteration history")
		}
	}
	return &channel.OutboundMsg{
		Channel:     msg.Channel,
		ChatID:      msg.ChatID,
		Content:     out.Content,
		WaitingUser: true,
		Metadata:    meta,
	}
}

func (a *Agent) storePendingAskUserOutbound(msg bus.InboundMessage, outbound *channel.OutboundMsg) {
	if a == nil || outbound == nil {
		return
	}
	askPayload := &protocol.ProgressEvent{}
	if outbound.Metadata != nil {
		askPayload.RequestID = outbound.Metadata["request_id"]
		if qJSON := outbound.Metadata["ask_questions"]; qJSON != "" {
			var questions []protocol.AskUserQuestion
			if json.Unmarshal([]byte(qJSON), &questions) == nil {
				askPayload.Questions = questions
			}
		}
	}
	a.setPendingAskUser(msg.Channel, msg.ChatID, askPayload)
}

// - Empty content with optional reply: clear progress state
// - Normal: persist assistant message, send, add reaction
func (a *Agent) handleRunOutput(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession, replyPolicy string) (*channel.OutboundMsg, error) {
	finalContent := out.Content
	waitingUser := out.WaitingUser

	// Turn closing frame (STATE-MODEL C-7): exactly ONE `final` per turn,
	// including EMPTY turns. Emitted before the empty-content early returns —
	// a turn that produced nothing must still be closed, otherwise the client
	// has no legal way to stop treating it as running.
	if !waitingUser {
		doneTurnID := turnIDFromMetadata(msg.Metadata)
		if doneTurnID == 0 {
			doneTurnID = a.getActiveTurnID(qualifyChatID(msg.Channel, msg.ChatID))
		}
		a.emitTurnFinal(msg.Channel, msg.ChatID, protocol.TurnFinalFrame{
			TurnID:  doneTurnID,
			Content: out.Content,
		})
	}

	// If a tool is waiting for user response, send WaitingUser outbound
	if waitingUser {
		outbound := buildWaitingUserOutbound(ctx, msg, out, tenantSession)
		// Store the pending AskUser payload so reconnect can resend it.
		a.storePendingAskUserOutbound(msg, outbound)
		return outbound, nil
	}

	// Empty content without waiting for user and not optional reply
	if finalContent == "" && replyPolicy != bus.ReplyPolicyOptional {
		log.Ctx(ctx).Warn("Run produced empty content without waiting for user input")
		if err := a.sendMessage(msg.Channel, msg.ChatID,
			"⚠️ 处理完成，但未生成回复内容。请尝试重新描述您的需求。",
			map[string]string{channel.MetaFinalReply: "true"}); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to send empty content notification")
		}
		return nil, nil
	}

	if finalContent == "" && replyPolicy == bus.ReplyPolicyOptional {
		log.Ctx(ctx).WithFields(log.Fields{
			"channel":      msg.Channel,
			"chat_id":      msg.ChatID,
			"reply_policy": replyPolicy,
		}).Info("Optional reply policy: no final response generated, skipping outbound")
		// Send an empty outbound to clear TUI progress state.
		// ⛔ 这是**该回合的收尾信号**（TurnID 必须带上，见 outboundTurnID）；
		//   旧实现无 TurnID ⇒ 前端只能靠"无 turn 的 text"猜归属（原生侧 2026-10-22
		//   才补的兜底），且 CLI 之外的渠道无法把它挂回该轮。
		if ch, ok := a.channelFinder(msg.Channel); ok {
			ch.Send(channel.OutboundMsg{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "",
				TurnID:  a.outboundTurnID(msg.Channel, msg.ChatID, msg.Metadata, 0),
			})
		}
		return nil, nil
	}

	// Persist the final assistant reply.
	// iteration_history is the single source of truth (v55+) — Detail JSON
	// is no longer written. The final assistant message is a plain message
	// with content + reasoning; iteration data lives in iteration_history.
	assistantMsg := llm.NewAssistantMessage(finalContent)
	assistantMsg.ReasoningContent = out.ReasoningContent
	assistantMsg.ReasoningItems = out.ReasoningItems
	// v55+ 数据模型：assistant 回复不再写 session_messages.content —— 回复文本
	// 在 iteration_history 的最终迭代（msg 是 iter 组成的集合，content 是历史
	// 遗留字段）。内存 cfg.Messages 保留 content（同 Run 内 LLM 上下文需要），
	// DB 持久化不写 —— 读取时 buildPrompt 从迭代 fallback（迭代权威），前端
	// 从迭代渲染。
	persistMsg := assistantMsg // struct 值复制
	persistMsg.Content = ""
	// Set TurnID from the active turn so the frontend can dedup the live SSE
	// message against the DB-persisted message by turnID:role. Without this,
	// the DB row has turn_id=0 while the SSE text event carries the real
	// turn_id (stamped by sendMessage via getActiveTurnID) — the mismatch
	// defeats dedupMessages and reconcileHistoryWithLiveRows, producing
	// two consecutive assistant messages (DB + live) with the same content.
	// handleCancelledRun already does this; handleRunOutput must too.
	if tid, err := strconv.ParseUint(msg.Metadata["turn_id"], 10, 64); err == nil && tid > 0 {
		assistantMsg.TurnID = tid
		persistMsg.TurnID = tid
	}
	// Detail JSON is no longer written — iteration_history table is the
	// single source of truth for iteration data (v55+). Detail JSON remains
	// only as a backward-compat fallback for old data pre-v55.
	if err := tenantSession.AddMessage(persistMsg); err != nil {
		return nil, fmt.Errorf("append assistant message: %w", err)
	}

	// Send via sendMessage (reuses session message tracking).
	// Pass the authoritative turn_id (from RunConfig.TurnID, parsed out of
	// msg.Metadata) so sendMessage stamps it on the SSE text event. Without this,
	// sendMessage falls back to getActiveTurnID which can return 0 (the reply
	// would be committed to turn 0 while the live progress was written to the
	// real turn — leaving an empty live shell + a turn-0 assistant row).
	sendMeta := map[string]string{channel.MetaFinalReply: "true"}
	if tid := msg.Metadata["turn_id"]; tid != "" {
		sendMeta["turn_id"] = tid
	}
	// text 事件带 progress_history（权威迭代数据，含最后迭代 content）——
	// 否则前端 commit 的 iterations 只依赖 snap.iterationHistory（progress
	// 事件的时序增量，最后迭代 content 可能缺失 → typing 完成后 content
	// 消失；刷新后从 DB 恢复正常，用户报告）。web.go 用
	// msg.Metadata["progress_history"] 填充 WSMessage.ProgressHistory。
	if len(out.IterationHistory) > 0 {
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			sendMeta["progress_history"] = string(jsonBytes)
		}
	}
	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent, sendMeta); err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to send final response via sendMessage")
		// ⛔ 兜底回包同样必须带 TurnID（turn 域消息的同一规则）：否则 sendMessage
		//   失败时这一轮的最终回复在客户端无法归属，回合永不收尾（行停在 live）。
		return &channel.OutboundMsg{
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Content:  finalContent,
			Metadata: sendMeta,
			TurnID:   a.outboundTurnID(msg.Channel, msg.ChatID, sendMeta, 0),
		}, nil
	}

	// Add reaction to user's original message
	a.addReaction(msg)

	return nil, nil
}

// deliverNotificationsByAgentPath 处理"key 是 agent 调用链而非会话 key"的通知。
//
// key 形如 `main/court.shoufu/court.bingbu`：这是 AgentID 路径（根/父/子），
// 不是 channel:chatID。通知真正的接收方是**父 agent**（倒数第二段）。
// 做法：按角色名在 interactiveSubAgents 里找活跃会话，把通知作为一条用户消息
// 投进去；找不到就记 ERROR 并保留到主会话队列，绝不静默丢弃。
// interactiveSessionByAgentID finds the exact live owner of a background run.
// AgentID is the per-run BgSessionKey for subagents; role names alone are not
// identities because multiple sessions may use the same role concurrently.
func (a *Agent) interactiveSessionByAgentID(agentID string) (string, *interactiveAgent, bool) {
	want := strings.Trim(agentID, "/")
	if want == "" {
		return "", nil, false
	}
	var foundKey string
	var found *interactiveAgent
	a.interactiveSubAgents.Range(func(k, v any) bool {
		key, ok := k.(string)
		if !ok {
			return true
		}
		ia, ok := v.(*interactiveAgent)
		if !ok || ia == nil || ia.isDestroyed() {
			return true
		}
		ia.mu.Lock()
		id := ""
		if ia.cfg != nil {
			id = strings.Trim(ia.cfg.AgentID, "/")
		}
		ia.mu.Unlock()
		if id == want {
			foundKey, found = key, ia
			return false
		}
		return true
	})
	return foundKey, found, found != nil
}

func (a *Agent) deliverNotificationsByAgentPath(key string) {
	mine := a.takePendingBgNotifications(key)
	if len(mine) == 0 {
		return
	}

	segs := strings.Split(strings.Trim(key, "/"), "/")
	parentRole := ""
	if len(segs) >= 2 {
		parentRole = segs[len(segs)-2]
	}
	// len(segs) == 1 ⇒ key 是**顶层子代理**（`taizi`，父就是主会话，没有 agent 路径根）。
	// parentRole 保持空 ⇒ 走下面的「落主会话」分支，这是正确落点，不是异常。
	// 2026-11-13 之前 subAgentID 会被拼成 `"/taizi"`（parentAgentID 为空），
	// Trim + Split 后同样是单段，但日志报成 Error "unresolvable"，把人往
	// 「会话找不到」的方向带偏 —— 实际只是「这个子代理没有 agent 父路径」。

	var targetKey, targetRole, targetInst, targetChannel, targetChatID string
	// A BackgroundTask uses the sub-agent's AgentID path as its isolated
	// BgSessionKey (for example main/court.shoufu/court.gongbu). That path is
	// the OWNER of the task, not a request to notify the path's parent. Resolve
	// the exact live interactive session first. The old code skipped directly
	// to parentRole (the penultimate path segment), which delivered a child's
	// Shell completion to its parent and made the child appear to never receive
	// its own task result.
	if exactKey, exactIA, ok := a.interactiveSessionByAgentID(key); ok {
		ch, cid, _, _ := parseInteractiveKeyTail(exactKey)
		if ch != "" && cid != "" {
			targetKey, targetRole, targetInst = exactKey, exactIA.roleName, exactIA.instance
			targetChannel, targetChatID = ch, cid
		}
	}
	// SubAgentBgNotify historically uses an agent path to mean "notify the
	// parent". Preserve that contract only when no exact owner session exists.
	if targetKey == "" && parentRole != "" {
		a.interactiveSubAgents.Range(func(k, v any) bool {
			ia, ok := v.(*interactiveAgent)
			if !ok || ia == nil || ia.roleName != parentRole {
				return true
			}
			ks, _ := k.(string)
			// 从会话 key（web:web-4/court.shoufu:main）还原真实 channel/chatID，
			// 不能凭空造 "agent:<role>/<inst>" —— 那会创建畸形 tenant。
			ch, cid, _, _ := parseInteractiveKeyTail(ks)
			if ch == "" || cid == "" {
				return true
			}
			targetKey, targetRole, targetInst = ks, ia.roleName, ia.instance
			targetChannel, targetChatID = ch, cid
			return false
		})
	}

	var b strings.Builder
	for _, n := range mine {
		if sn, ok := n.(*tools.SubAgentBgNotify); ok && sn != nil {
			fmt.Fprintf(&b, "[子代理通知] %s\n", sn.Content)
			continue
		}
		fmt.Fprintf(&b, "[子代理通知] %v\n", n)
	}
	body := b.String()

	if targetKey != "" {
		log.WithFields(log.Fields{
			"bad_key": key, "parent_role": targetRole,
			"target_key": targetKey, "count": len(mine),
		}).Warn("bg notification key was an agent path; routed to parent interactive session")
		go func() {
			_, err := a.SendToInteractiveSession(context.Background(), targetRole, bus.InboundMessage{
				Channel:  targetChannel,
				ChatID:   targetChatID,
				Content:  body,
				Metadata: map[string]string{"instance_id": targetInst, "bg_notify_recovered": "true"},
			})
			if err != nil {
				log.WithError(err).WithField("target_key", targetKey).
					Error("bg notification recovery delivery failed")
			}
		}()
		return
	}

	// 找不到父会话：绝不丢，落到主会话队列。
	// 单段 key（顶层子代理）走到这里是**正常路径**（父就是主会话），用 Warn；
	// 多段却没命中才是真的异常（父 role 在注册表里找不到），用 Error —— 两者
	// 之前混在同一个 Error 里，把「顶层子代理」误报成「会话找不到」（2026-11-13）。
	{
		entry := log.WithFields(log.Fields{"bad_key": key, "count": len(mine), "segs": len(segs)})
		if len(segs) <= 1 {
			entry.Warn("bg notification from a top-level subagent (no agent path root); delivered to main session")
		} else {
			entry.Error("bg notification key unresolvable and no matching parent session; keeping for main session")
		}
	}
	for _, n := range mine {
		if sn, ok := n.(*tools.SubAgentBgNotify); ok && sn != nil {
			sn.Key = "recovered"
		}
		a.enqueueBgNotification(n)
	}
}
