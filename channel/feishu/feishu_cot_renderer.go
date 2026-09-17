package feishu

import (
	"encoding/json"
	"strconv"
	"strings"
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
	// 正文：只保留「最后一次」作为答案（走普通消息）；被顶替的中间文本是
	// narration，按 dsh-lark 的做法 flush 进思考过程（TEXT_MESSAGE_*，role=assistant）。
	heldText     string
	textSeq      int
	curIteration int
	startedTools map[string]struct{}
	doneTools    map[string]struct{}
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
	// 迭代推进 ⇒ 上一迭代的正文变成了「过程叙述」（dsh-lark：被顶替的文本进
	// 思考过程，只有最后一次是答案）。
	if ev.Iteration > 0 && ev.Iteration != r.curIteration {
		r.flushNarrationLocked()
		r.curIteration = ev.Iteration
	}

	for _, tp := range ev.ActiveTools {
		key := cotToolKey(tp)
		if _, seen := r.startedTools[key]; seen {
			continue
		}
		r.startedTools[key] = struct{}{}
		// 工具开始即结束「思考」块与上一段正文（两者都不跨工具调用，与 dsh-lark 一致）。
		r.closeReasoningLocked()
		r.flushNarrationLocked()
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
func (r *feishuCoTRenderer) onStreamContent(content, reasoning string) {
	if r.cot == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureRunLocked(r.runTurnID)

	// 正文：只记「最后一次」作为答案候选（答案由普通消息路径发送）；上一段在
	// 迭代推进/工具调用时被 flush 成过程叙述。
	if content != "" {
		r.heldText = content
	}
	if reasoning == "" {
		return
	}
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
	r.heldText = ""
	r.textSeq = 0
	r.curIteration = 0
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

// flushNarrationLocked 把「被顶替的正文」写进思考过程（dsh-lark 的 TEXT_MESSAGE_*）。
func (r *feishuCoTRenderer) flushNarrationLocked() {
	if r.heldText == "" {
		return
	}
	text := r.heldText
	r.heldText = ""
	r.textSeq++
	messageID := "text-" + strconv.FormatUint(r.runTurnID, 10) + "-" + strconv.Itoa(r.textSeq)
	r.cot.emit("TEXT_MESSAGE_START", map[string]any{"messageId": messageID, "role": "assistant"})
	r.cot.emit("TEXT_MESSAGE_CONTENT", map[string]any{"messageId": messageID, "delta": text})
	r.cot.emit("TEXT_MESSAGE_END", map[string]any{"messageId": messageID})
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

// cotToolTitle 是工具在 CoT 里的标题 —— 对齐 dsh-lark 的 presenter 语义：
// **「短小、始终可见、描述这一次调用在做什么」的单行标签**（不是工具名重复）。
//
// 参数优先从 Args(JSON) 里取该工具最有信息量的那一项（命令/路径/模式/查询…），
// 其次回落到 Label/Summary 的首行，最后才是工具名本身。长度按 rune 截断（单行）。
func cotToolTitle(tp protocol.ToolProgress) string {
	const maxRunes = 96
	if arg := cotPrimaryArg(tp); arg != "" {
		return cotBoundTitle(tp.Name+" · "+arg, maxRunes)
	}
	if tp.Label != "" {
		return cotBoundTitle(tp.Name+" · "+firstLine(tp.Label), maxRunes)
	}
	if tp.Summary != "" {
		return cotBoundTitle(tp.Name+" · "+firstLine(tp.Summary), maxRunes)
	}
	return tp.Name
}

// cotPrimaryArg 从参数 JSON 里取该工具最有信息量的字段（按工具语义）。
func cotPrimaryArg(tp protocol.ToolProgress) string {
	if tp.Args == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(tp.Args), &m) != nil {
		return ""
	}
	keys := cotArgKeys(tp.Name)
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if s := firstLine(strings.TrimSpace(t)); s != "" {
				return s
			}
		case []any: // 例如 task_id: ["abc"]
			if len(t) > 0 {
				if s, ok := t[0].(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// cotArgKeys 按工具语义给出「最有信息量」的参数优先级。
func cotArgKeys(name string) []string {
	switch strings.ToLower(name) {
	case "shell", "bash":
		return []string{"command", "cmd"}
	case "read", "file_read":
		return []string{"path", "file_path", "file"}
	case "filereplace", "filecreate", "edit", "write":
		return []string{"path", "file_path", "file"}
	case "grep", "search":
		return []string{"pattern", "query"}
	case "glob":
		return []string{"pattern", "path"}
	case "websearch":
		return []string{"query"}
	case "fetch", "webfetch":
		return []string{"url"}
	case "subagent", "createchat":
		return []string{"role", "task"}
	case "task_read", "task_status", "task_wait", "task_kill":
		return []string{"task_id"}
	default:
		return []string{"command", "path", "query", "pattern", "url", "task_id", "task"}
	}
}

// cotBoundTitle 单行 + rune 安全截断（标题永远只占一行）。
func cotBoundTitle(s string, maxRunes int) string {
	r := []rune(firstLine(s))
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes-1]) + "…"
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
	// receive_id 必须是**真实** chat_id（合成会话键会被飞书拒为 invalid receive_id）。
	r := newFeishuCoTRenderer(chatID, newFeishuCoT(f.client, f.cotReceiveID(chatID), replyTo, false))
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
