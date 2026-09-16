package feishu

import (
	"strconv"
	"sync"

	"xbot/protocol"
)

// feishu_cot_renderer.go — 把 xbot 的**结构化进度**映射成飞书原生 CoT 的 AG-UI
// 事件（对照 dsh-lark 的 src/cot.ts createCotRenderer）。
//
//	dsh-lark 事件                                  ← 本实现的事件源
//	  RUN_STARTED{threadId,runId}                  ← turn_started / 首个进度事件
//	  REASONING_MESSAGE_START{messageId,role}      ← 第一次收到 reasoning 流
//	  REASONING_MESSAGE_CONTENT{messageId,delta}   ← reasoning 全量文本的增量差分
//	  REASONING_MESSAGE_END{messageId}             ← 工具调用开始 / turn 结束
//	  TOOL_CALL_START{toolCallId,icon,title,name}  ← ActiveTools 首次出现
//	  TOOL_CALL_ARGS{toolCallId,delta}             ← 参数
//	  TOOL_CALL_END{toolCallId}
//	  TOOL_CALL_RESULT{…,content:{type:'code'}}    ← CompletedTools（结果按代码块）
//	  RUN_FINISHED{status:'done'} / RUN_ERROR      ← phase=done / 失败
//
// **答案不进 CoT**：平台把最终答复留给普通消息（dsh-lark 的 answer renderer 同理；
// xbot 的最终回复本来就走普通消息路径）。思考区只承载「推理 + 工具调用 + 工具结果」。
//
// ⚠️ xbot 的流式推送是**全量**（每次发累积文本，engine_wire 的 delta_push=false），
// 所以这里必须自己做差分，只把新增后缀作为 delta 写入 —— 否则思考区会把整段推理
// 重复 N 遍。
type feishuCoTRenderer struct {
	chatID string
	cot    *feishuCoT

	mu            sync.Mutex
	runTurnID     uint64
	runOpen       bool
	reasoningOpen bool
	lastReasoning string
	startedTools  map[string]struct{}
	doneTools     map[string]struct{}
}

func newFeishuCoTRenderer(chatID string, cot *feishuCoT) *feishuCoTRenderer {
	return &feishuCoTRenderer{
		chatID:       chatID,
		cot:          cot,
		startedTools: map[string]struct{}{},
		doneTools:    map[string]struct{}{},
	}
}

// onProgress 消费一个结构化进度事件。
func (r *feishuCoTRenderer) onProgress(ev *protocol.ProgressEvent) {
	if ev == nil || r.cot == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	turnID := ev.TurnID
	if turnID == 0 {
		turnID = r.runTurnID
	}
	r.ensureRunLocked(turnID)

	for _, tp := range ev.ActiveTools {
		key := cotToolKey(tp)
		if _, seen := r.startedTools[key]; seen {
			continue
		}
		r.startedTools[key] = struct{}{}
		// 工具开始即结束「思考」块（思考块不跨工具调用，与 dsh-lark 一致）。
		r.closeReasoningLocked()
		r.cot.emit("TOOL_CALL_START", map[string]any{
			"toolCallId":   key,
			"icon":         cotToolIcon(tp.Name),
			"title":        cotToolTitle(tp),
			"toolCallName": tp.Name,
		})
		if tp.Args != "" {
			r.cot.emit("TOOL_CALL_ARGS", map[string]any{"toolCallId": key, "delta": tp.Args})
		}
		r.cot.emit("TOOL_CALL_END", map[string]any{"toolCallId": key})
	}

	for _, tp := range ev.CompletedTools {
		key := cotToolKey(tp)
		if _, seen := r.doneTools[key]; seen {
			continue
		}
		r.doneTools[key] = struct{}{}
		body := tp.Detail
		if body == "" {
			body = tp.Summary
		}
		payload := map[string]any{
			"messageId":  "result-" + key,
			"toolCallId": key,
			"role":       "tool",
			"content":    map[string]any{"type": "code", "code": cotBoundResult(body)},
		}
		if cotToolFailed(tp.Status) {
			payload["error"] = "TOOL_FAILED"
		}
		r.cot.emit("TOOL_CALL_RESULT", payload)
	}

	if ev.Phase == "done" {
		r.closeRunLocked("")
	}
}

// onStreamContent 消费流式文本。xbot 推的是**全量**累积文本 ⇒ 这里做增量差分；
// 只把 reasoning 写进思考区（答案留给普通消息）。
func (r *feishuCoTRenderer) onStreamContent(_ /*content*/, reasoning string) {
	if r.cot == nil || reasoning == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureRunLocked(r.runTurnID)

	delta := cotDelta(r.lastReasoning, reasoning)
	if delta == "" {
		return
	}
	r.lastReasoning = reasoning
	if !r.reasoningOpen {
		r.reasoningOpen = true
		r.cot.emit("REASONING_MESSAGE_START", map[string]any{
			"messageId": r.reasoningMessageID(),
			"role":      "reasoning",
		})
	}
	r.cot.emit("REASONING_MESSAGE_CONTENT", map[string]any{
		"messageId": r.reasoningMessageID(),
		"delta":     delta,
	})
}

// close 收尾（最终回复 / 取消 / meta final 时调用；幂等）。
func (r *feishuCoTRenderer) close(errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeRunLocked(errMsg)
}

func (r *feishuCoTRenderer) ensureRunLocked(turnID uint64) {
	if r.runOpen && turnID == r.runTurnID {
		return
	}
	if r.runOpen {
		r.closeRunLocked("")
	}
	r.runTurnID = turnID
	r.runOpen = true
	r.reasoningOpen = false
	r.lastReasoning = ""
	r.startedTools = map[string]struct{}{}
	r.doneTools = map[string]struct{}{}
	r.cot.emit("RUN_STARTED", map[string]any{
		"threadId": r.chatID,
		"runId":    r.runID(),
	})
}

func (r *feishuCoTRenderer) closeRunLocked(errMsg string) {
	if !r.runOpen {
		return
	}
	r.closeReasoningLocked()
	if errMsg == "" {
		r.cot.emit("RUN_FINISHED", map[string]any{
			"threadId": r.chatID,
			"runId":    r.runID(),
			"status":   "done",
		})
	} else {
		r.cot.emit("RUN_ERROR", map[string]any{"message": errMsg, "code": "TURN_FAILED"})
	}
	r.runOpen = false
}

func (r *feishuCoTRenderer) closeReasoningLocked() {
	if !r.reasoningOpen {
		return
	}
	r.cot.emit("REASONING_MESSAGE_END", map[string]any{"messageId": r.reasoningMessageID()})
	r.reasoningOpen = false
}

func (r *feishuCoTRenderer) reasoningMessageID() string {
	return "reasoning-" + r.runID()
}

func (r *feishuCoTRenderer) runID() string {
	return "turn-" + strconv.FormatUint(r.runTurnID, 10)
}

// cotDelta 返回 accumulated 相对 previous 的新增后缀（全量推送的差分）。
func cotDelta(previous, accumulated string) string {
	if previous == accumulated || accumulated == "" {
		return ""
	}
	pr, ar := []rune(previous), []rune(accumulated)
	if len(ar) > len(pr) && string(ar[:len(pr)]) == previous {
		return string(ar[len(pr):])
	}
	// 文本被改写（非前缀扩展）⇒ 整段补一次，绝不静默丢内容。
	return accumulated
}

// cotToolKey 是工具调用在 CoT 里的稳定 id（同迭代同名工具只报一次）。
func cotToolKey(tp protocol.ToolProgress) string {
	return tp.Name + "#" + strconv.Itoa(tp.Iteration)
}

// cotToolTitle 是工具在 CoT 里的标题（首行摘要；与 dsh-lark 的 presenter title 同位）。
func cotToolTitle(tp protocol.ToolProgress) string {
	if tp.Label != "" {
		return tp.Name + " · " + firstLine(tp.Label)
	}
	if tp.Summary != "" {
		return tp.Name + " · " + firstLine(tp.Summary)
	}
	return tp.Name
}

// cotToolIcon 返回 dsh-lark 图标词表里的图标名（未知工具用 default）。
func cotToolIcon(name string) string {
	if k := cotToolKind(name); k != "" {
		return k
	}
	return "default"
}

func cotToolFailed(status string) bool {
	return status == "error" || status == "failed" || status == "killed"
}

// firstLine 取首行（标题里不放整段参数/输出）。
func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	return s
}

// cotRendererFor 返回该会话的原生 CoT 渲染器；不可用时返回 nil（调用方回落卡片）。
//
// ⚠️ 仅当渠道由构造函数正常初始化时启用（cotRenderers 非 nil）—— 测试里直接
// 构造结构体（无 map）时保持既有卡片行为，避免测试被真实 HTTP 路径污染。
func (f *FeishuChannel) cotRendererFor(chatID string) *feishuCoTRenderer {
	if !f.cotEnabled || f.client == nil || f.cotRenderers == nil {
		return nil
	}
	f.cotMu.Lock()
	defer f.cotMu.Unlock()
	if r, ok := f.cotRenderers[chatID]; ok {
		if r.cot.brokenNow() {
			return nil
		}
		return r
	}
	f.inboundMsgIDsMu.Lock()
	replyTo := f.inboundMsgIDs[chatID]
	f.inboundMsgIDsMu.Unlock()
	r := newFeishuCoTRenderer(chatID, newFeishuCoT(f.client, chatID, replyTo, false))
	f.cotRenderers[chatID] = r
	return r
}

// closeCoTRun 收尾该会话的思考过程（最终回复 / 取消 / 新一轮）。
func (f *FeishuChannel) closeCoTRun(chatID, errMsg string) {
	if f.cotRenderers == nil {
		return
	}
	f.cotMu.Lock()
	r := f.cotRenderers[chatID]
	delete(f.cotRenderers, chatID)
	f.cotMu.Unlock()
	if r != nil {
		r.close(errMsg)
	}
}
