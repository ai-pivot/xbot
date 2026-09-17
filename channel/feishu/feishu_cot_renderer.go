package feishu

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

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
	// ⚠️ 推理 messageId **每轮一个**（reasoningMessageID()，对齐 dsh-lark 的
	// `reasoning-${turn}`）：平台按 id 归并内容 ⇒ 工具开始后**迟到的尾巴**再用同一
	// id START 会并回同一块。曾改成"每块独立 id"，迟到的尾巴就成了新块 ⇒ 飞书里
	// 「推理停在工具调用那一刻（不是推理结尾）+ 位置错乱」（用户 2026-09-17 截图；
	// DB 真值：该迭代推理 1864 字符，飞书只渲染到 "Let me create /tmp/ems_weather.py"）。
	lastReasoning string
	// pendingReasoning + lastTextFlush：尚未写出的推理增量与上次写出时刻。
	// ⚠️ 引擎**每个 LLM chunk 回调一次**（一轮上千次）——若每次都写一个 CoT 事件，
	// 一轮就是上千事件 / 几十次 HTTP，平台侧表现为「只渲染开头，后面不再更新」
	// （用户 2026-09-17 报告）。按时间片合并成少量大批（dsh-lark 同为"少量大批"，
	// MAX_EVENTS_PER_WRITE=50），工具开始 / 收尾时强制 flush，绝不丢内容。
	pendingReasoning string
	lastTextFlush    time.Time
	// 正文：只保留「最后一次」作为答案（走普通消息）；被顶替的中间文本是
	// narration，按 dsh-lark 的做法 flush 进思考过程（TEXT_MESSAGE_*，role=assistant）。
	heldText     string
	textSeq      int
	curIteration int
	startedTools map[string]struct{}
	// startedKeyBySlot：（名字#迭代）→ 该槽位已 START 的 toolCallId。
	// ⛔ 平台契约（dsh-lark 同）：TOOL_CALL_RESULT.toolCallId 必须等于同一次调用的
	// TOOL_CALL_START.toolCallId，否则 RESULT 成为孤儿 ⇒ 平台**多数一次**
	//（用户 2026-09-17 截图：web 2 个工具 ⇒ 飞书「Called tools 3 times」）。
	// 某些来源的完成快照会丢 CallID（SubAgent 进度转换 / 合成工具），按槽位找回。
	startedKeyBySlot map[string]string
	doneTools        map[string]struct{}
}

// cotTextFlushInterval / cotTextChunkRunes 是推理/正文的**写出节奏**。
//
// ⚠️ 引擎**每个 LLM chunk 回调一次** ⇒ 逐 chunk 写会把一轮放大成上千个 CoT 事件 /
// 几十次 HTTP，平台侧表现为「只渲染开头，后面不再更新」（用户 2026-09-17 报告）；
// 且单个事件的 content 有 4096 字符上限（超了会被传输层换成截断标记 = 毁内容）⇒
// 必须**按时间片合并成少量大批** + **长文本分片**，二者缺一不可。
const (
	cotTextFlushInterval = 250 * time.Millisecond
	cotTextChunkRunes    = 1024
)

func newFeishuCoTRenderer(chatID string, cot *feishuCoT) *feishuCoTRenderer {
	return &feishuCoTRenderer{
		chatID:           chatID,
		cot:              cot,
		startedTools:     map[string]struct{}{},
		startedKeyBySlot: map[string]string{},
		doneTools:        map[string]struct{}{},
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

	// ⚠️ 迭代推进必须**最先**处理（在消费推理/正文之前）：新迭代要开**新的推理块**
	//（逐迭代交错 = web 的 Thought→工具→Thought→工具 布局），上一迭代的推理块与
	// 正文旧稿必须先收尾。
	//
	// ⚠️ 顺序至关重要：**先** flush 上一迭代的旧稿，**再**记录本迭代的新稿。
	// 曾经把"记录新稿"放在前面 ⇒ 每个迭代的正文刚到就被当作"被顶替的旧稿"塞进
	// 思考过程（用户 2026-09-17 两个现象的共同根因）：
	//   ① 每个工具批之间被插入一段文本 ⇒ 平台把连续工具调用拆成多条
	//      「Called tools 1 time」（截图里的五条）；
	//   ② 正文与推理在思考区里混成一片、看不出区别。
	if ev.Iteration > 0 && ev.Iteration != r.curIteration {
		r.flushNarrationLocked()
		// 上一迭代的推理块到此为止（flush 累积的尾巴 + END）⇒ 新迭代开新块。
		r.closeReasoningLocked()
		r.curIteration = ev.Iteration
	}

	// 推理/正文有**两条来源**：流式回调（SendStreamContent）与**结构化进度**
	// （protocol.ProgressEvent 的 ReasoningStreamContent / StreamContent —— 真实
	// 部署上推理就是随结构化进度下发的）。两条都消费、共用同一份差分逻辑，确保
	// 「思考区一定有推理」（曾经只消费工具字段 ⇒ 飞书 CoT 里只有工具、没有推理）。
	if ev.ReasoningStreamContent != "" {
		r.emitReasoningLocked(ev.ReasoningStreamContent)
	} else if ev.ReasoningStreamDelta != "" {
		r.emitReasoningLocked(r.lastReasoning + ev.ReasoningStreamDelta)
	}
	// 本迭代的正文（答案候选，走普通消息；旧稿已在上面 flush 过）。
	if ev.StreamContent != "" {
		r.heldText = ev.StreamContent
	}

	for _, tp := range ev.ActiveTools {
		key := cotToolKey(tp)
		if _, seen := r.startedTools[key]; seen {
			continue
		}
		r.startedTools[key] = struct{}{}
		// 记录槽位（名字#迭代）→ 已 START 的 id：完成快照丢 CallID 时按槽位找回，
		// 保证 RESULT 永远与 START 同 id（否则平台多数一次）。
		r.startedKeyBySlot[cotToolSlot(tp)] = key
		// 工具开始只结束「思考」块（dsh-lark 同）；
		r.closeReasoningLocked()
		// ⚠️ 本迭代的正文必须落在**它自己的**工具之前（用户 2026-09-17 报告：
		// 「第一个迭代的 content 渲染在第一个迭代的 toolcall 之后」）。旧实现只在
		// 「被更新的文本顶替」时才写 ⇒ 迭代 1 的正文要等迭代 2 的正文到达才落盘，
		// 于是排到了迭代 1 的工具之后（web 上是正文在前）。
		// 工具出现 = 这段正文是"过程叙述"而非最终答案 ⇒ 立刻写出；最终答案之后
		// 没有工具，会一直 held（绝不进思考区，由普通消息发送）。
		r.flushNarrationLocked()
		r.emitToolCallLocked(tp, key)
	}

	for _, tp := range ev.CompletedTools {
		key := cotToolKey(tp)
		if _, seen := r.doneTools[key]; seen {
			continue
		}
		r.doneTools[key] = struct{}{}
		// ⛔ 平台契约（dsh-lark 同）：RESULT.toolCallId 必须等于**同一次调用**的
		// START.toolCallId，否则 RESULT 成为孤儿 ⇒ 平台**多数一次**（用户
		// 2026-09-17 截图：web 2 个工具 ⇒ 飞书「Called tools 3 times」）。
		// 完成快照丢 CallID 的来源（SubAgent 进度转换 / 合成工具）按槽位找回；
		// 从未 START 过的完成条目（合成通知）补一次完整调用，绝不发孤儿 RESULT。
		if _, started := r.startedTools[key]; !started {
			if prev, ok := r.startedKeyBySlot[cotToolSlot(tp)]; ok {
				key = prev
			} else {
				r.emitToolCallLocked(tp, key)
			}
		}
		body := tp.Detail
		if body == "" {
			body = tp.Summary
		}
		payload := map[string]any{
			"messageId":  "result-" + key,
			"toolCallId": key,
			"role":       "tool",
			"content":    map[string]any{"type": "code", "code": cotBoundResult(redactSensitive(body))},
		}
		if cotToolFailed(tp.Status) {
			// 对齐 dsh-lark：error 承载**真实错误标识**（他们用 event.data.error.code；
			// 我们的等价物是工具状态：error / failed / killed）。
			payload["error"] = tp.Status
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
	r.emitReasoningLocked(reasoning)
}

// emitReasoningLocked 把**全量推理文本**按增量写进思考区（唯一的推理写出点：
// 流式回调与结构化进度两条来源共用，避免两份实现漂移）。
func (r *feishuCoTRenderer) emitReasoningLocked(full string) {
	if full == "" {
		return
	}
	delta := cotDelta(r.lastReasoning, full)
	if delta == "" {
		return
	}
	r.lastReasoning = full
	// 先累积，再按时间片写出（见 pendingReasoning 注释）：引擎每个 chunk 回调一次，
	// 逐 chunk 写会把一轮放大成上千个 CoT 事件（平台只渲染开头）。
	r.pendingReasoning += delta
	r.flushReasoningLocked(false)
}

// flushReasoningLocked 把累积的推理增量写成 CoT 事件（必要时开块）。
//
// force=true 用于**结构性节点**（工具开始 / 块关闭 / 收尾）：这些时刻必须已经把
// 之前的推理全部写出，否则尾巴会迟到（用户 2026-09-17 截图：推理停在工具调用处）。
// 非 force 时按 cotTextFlushInterval 合并成少量大批。
func (r *feishuCoTRenderer) flushReasoningLocked(force bool) {
	if r.pendingReasoning == "" {
		return
	}
	if !force && time.Since(r.lastTextFlush) < cotTextFlushInterval {
		return
	}
	text := r.pendingReasoning
	r.pendingReasoning = ""
	r.lastTextFlush = time.Now()
	if !r.reasoningOpen {
		r.reasoningOpen = true
		// 每轮一个 id（dsh-lark 的 `reasoning-${turn}`）：平台按 id 归并内容 ⇒
		// 工具开始后迟到的尾巴用**同一 id** 再 START，并回同一块，永不"截断"。
		r.cot.emit("REASONING_MESSAGE_START", map[string]any{
			"messageId": r.reasoningMessageID(),
			"role":      "reasoning",
		})
	}
	r.emitDeltaChunkedLocked("REASONING_MESSAGE_CONTENT", r.reasoningMessageID(), text)
}

// emitDeltaChunkedLocked 按 ≤cotTextChunkRunes 把长文本分片写成多个事件。
//
// ⚠️ 单个事件的 content 有 4096 字符上限，超过会被传输层换成 `{"truncated":true,…}`
// 标记（**内容被毁**）⇒ 长文本必须分片，绝不能靠截断（与「只渲染开头」同一类事故）。
func (r *feishuCoTRenderer) emitDeltaChunkedLocked(eventType, messageID, text string) {
	rs := []rune(text)
	for len(rs) > 0 {
		n := cotTextChunkRunes
		if n > len(rs) {
			n = len(rs)
		}
		r.cot.emit(eventType, map[string]any{
			"messageId": messageID,
			"delta":     string(rs[:n]),
		})
		rs = rs[n:]
	}
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
	r.pendingReasoning = ""
	r.lastTextFlush = time.Time{}
	r.startedKeyBySlot = map[string]string{}
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
	// 长正文（例如 3000 字符的迭代总结）必须分片：单事件超 4096 会被传输层换成
	// 截断标记（那等于毁掉内容）。
	r.emitDeltaChunkedLocked("TEXT_MESSAGE_CONTENT", messageID, text)
	r.cot.emit("TEXT_MESSAGE_END", map[string]any{"messageId": messageID})
}

func (r *feishuCoTRenderer) closeReasoningLocked() {
	// 关闭前必须先把累积的推理写出（否则尾巴迟到 = 内容跑到工具之后）。
	r.flushReasoningLocked(true)
	if !r.reasoningOpen {
		return
	}
	r.cot.emit("REASONING_MESSAGE_END", map[string]any{"messageId": r.reasoningMessageID()})
	r.reasoningOpen = false
}

// reasoningMessageID 返回**当前迭代**的推理块 id（`reasoning-<turn>-<iter>`）。
//
// ⚠️ 必须**逐迭代**一个 id：平台按 id 归并内容 ⇒ 同一 id 的推理永远并进同一块。
// 「每块独立 id」⇒ 工具开始后迟到的尾巴变成新块（截断观感）；「每轮一个 id」⇒
// 整轮推理全并进第一块（用户 2026-09-17 截图：「所有 cot 合并到了最开头」，而
// web 是 Thought→工具→Thought→工具 逐迭代交错）。正确契约 = **每迭代一个 id**：
// 同迭代内迟到的尾巴用同一 id 并回原块（不截断），跨迭代开新块（位置正确）。
// curIteration<=0（流式回调先于首个结构化事件）归入迭代 1，避免 0→1 切换把同一段
// 推理劈成两块。
func (r *feishuCoTRenderer) reasoningMessageID() string {
	it := r.curIteration
	if it <= 0 {
		it = 1
	}
	return "reasoning-" + r.runID() + "-" + strconv.Itoa(it)
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

// cotToolKey 是**一次工具调用**的身份 —— 首选宿主给的 `CallID`（与 dsh-lark 的
// `event.data.callId` 同源：宿主分配、跨事件稳定）。
//
// ⚠️ 曾用「名字 # 迭代号 # (Label|Args)」当身份，实测**会多算一次**（用户
// 2026-09-17 截图：web 上只调了 1 个 FileCreate，飞书却显示「Called tools 2 times」，
// 而同轮的 Shell 全部正常）：工具完成时 `updateToolResultLine` 会用
// `formatToolProgress(Name, Arguments)` **重算 label**，而 active 快照里 label 还是空的
// ⇒ 同一次调用得到两个 key ⇒ TOOL_CALL_START(idA) 与 TOOL_CALL_RESULT(idB) 被平台
// 算成两次调用。CallID 在两种快照里都是同一个值，天然免疫这类字段漂移。
//
// CallID 缺失（老历史 / 未走 call-id 链路的路径）时才回落到复合 key —— 保证不同
// 迭代、不同命令仍各自成条。
func cotToolKey(tp protocol.ToolProgress) string {
	if tp.CallID != "" {
		return tp.CallID
	}
	tag := tp.Label
	if tag == "" {
		tag = tp.Args
	}
	return tp.Name + "#" + strconv.Itoa(tp.Iteration) + "\x00" + tag
}

// cotToolSlot 是「工具调用槽位」=（名字, 迭代）：同一次调用的执行/完成快照必然
// 落在同一槽位，是 START↔RESULT 配对的兜底键（CallID 缺失时使用）。
func cotToolSlot(tp protocol.ToolProgress) string {
	return tp.Name + "#" + strconv.Itoa(tp.Iteration)
}

// emitToolCallLocked 写出一次完整的工具调用（START + ARGS + END）。
// START 循环与「补发从未 START 的完成条目」共用，保证事件族完整（平台按
// START 计数，孤儿 RESULT 会被多数一次）。
func (r *feishuCoTRenderer) emitToolCallLocked(tp protocol.ToolProgress, key string) {
	// ⛔ 出口脱敏（用户 2026-09-17「飞书工具脱敏」）：title 摘自 args、args 原文 ——
	// 命令里常带真实凭据（GH_TOKEN=… 的 shell、含密脚本），飞书是外部渠道。
	r.cot.emit("TOOL_CALL_START", map[string]any{
		"toolCallId":   key,
		"icon":         cotToolIcon(tp.Name),
		"title":        redactSensitive(cotToolTitle(tp)),
		"toolCallName": tp.Name,
	})
	if tp.Args != "" {
		r.cot.emit("TOOL_CALL_ARGS", map[string]any{"toolCallId": key, "delta": redactSensitive(tp.Args)})
	}
	r.cot.emit("TOOL_CALL_END", map[string]any{"toolCallId": key})
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
		// ⚠️ 单模式语义（对齐 dsh-lark：cot 与 stream **从不混用**）：本轮一旦由
		// CoT 接手，即使它中途降级（平台拒绝/网络故障）也**绝不**回落到卡片 ——
		// 否则同一轮里「思考过程 + 卡片」同时出现，正是用户报告的「完成后渲染
		// 两张卡片」。broken 后事件被 emit 静默丢弃（思考过程缺内容，但答案照发）。
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
//
// 唯一实现委托给 closeCoTRunReporting（它额外报告"本轮是否用过 CoT"，供最终
// 答复决定走普通消息还是卡片）—— 一处实现，避免两份漂移。
func (f *FeishuChannel) closeCoTRun(chatID, errMsg string) {
	f.closeCoTRunReporting(chatID, errMsg)
}
