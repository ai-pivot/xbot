package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"

	log "xbot/logger"
)

// ─── 元素级增量更新（方案 A）────────────────────────────────────────────────
//
// 设计目标（用户 2026-09-13）：小噪音 + 信息可展开 + 好看，且**不再整卡替换**。
//
// 为什么不用 Card.Update（整卡替换）：
//   - 它会打断正在流式的打字机（元素被重建，动画从头开始）；
//   - 每次结构变化（新迭代 / 新工具 / 工具状态变化 / 思考字数 +1）都要重发整卡
//     JSON —— 卡片越大越慢（工具一多就退化）；
//   - 无法局部更新：改一个字也重排全卡。
//
// CardKit 给了完整的元素级原子操作（真实 API 探针实测），本文件把它们组合成一条
// 低噪音路径：
//   - 结构变化 → add_elements（append 一个元素，一次/元素）
//   - 文本更新 → partial_update_element（补丁 `content`；见 pushText / pushReasoning）
//   - 属性变化 → partial_update_element（例如折叠面板标题里的实时字数）
//   - 收尾    → partial_update_setting（关 streaming_mode）+ 折叠思考面板
//
// ⛔ 为什么文本**不能**用 CardElement.Content（打字机）—— 真实 API 探针实测：
//
//	CardElement.Content 只认**建卡模板里声明过的 element_id**；对 add_elements
//	追加出来的元素做 Content 一律 `300313 ErrMsg: not find elementID : X;`。
//	本实现的模型是"每迭代一个元素（ans_n / think_n）+ 只 append"，所以除迭代 1 的
//	ans_1 之外，其它元素都是 append 的 —— 早期实现用 Content 推文本 ⇒ 迭代 2+ 的
//	正文/思考全部 300313 静默失败，卡片冻结在迭代 1（本 bug 的根因）。
//	partial_update_element（补丁，含 `content`）对**任意已存在元素**（含 append 的）
//	都成功（实测），因此文本统一走补丁。
//
// 另外一个探针事实：Content 传**空串**会被拒（`99992402 field validation failed`），
// 所以任何写文本的路径都必须保证非空（本实现只推非空文本；清空/占位用 "\u200b"）。
//
// 所有排队 op 合并成**一次** BatchUpdateCard 请求（同元素 last-writer-wins），
// 因此「迭代边界 + 新工具 + 关上一个工具」这类同 tick 的多改动只花一次调用。
//
// ⛔ 提交语义：**commit-on-success** —— 请求成功才清空 ops / 记账；失败保留 ops
// 供下次 flush 重试，日志 Warn（带 card_id/code/msg），连续失败到阈值就熔断本会话
// 的进度卡片（回落普通消息）。旧实现"先 c.ops=nil 再发请求、失败只打 Debug"使
// 结构变化和文本永久丢失且无人知晓 —— 这正是本 bug 迟迟没被发现的原因。

const (
	// panelIDPrefix/answerIDPrefix/toolIDPrefix：元素 id 前缀。Feishu 元素 id 允许
	// 字母/数字/下划线，≤20 字符 —— 前缀保持短。
	panelIDPrefix  = "tp_"
	answerIDPrefix = "ans_"
	toolIDPrefix   = "tl_"
)

// panelElementID is the collapsible thinking panel of iteration n.
func panelElementID(n int) string { return fmt.Sprintf("%s%d", panelIDPrefix, n) }

// answerElementID is iteration n's streamable answer element.
//
// 每迭代一个独立元素 id（旧的单一 "content" 需要整卡重排才能把流式目标挪到新迭代）。
func answerElementID(n int) string { return fmt.Sprintf("%s%d", answerIDPrefix, n) }

// toolRowElementID is the (stable) element id of the i-th tool row of iteration n.
func toolRowElementID(n, i int) string { return fmt.Sprintf("%s%d_%d", toolIDPrefix, n, i) }

// cardOpKind enumerates the queued element-level mutations.
type cardOpKind int

const (
	opAdd      cardOpKind = iota // append element(s)
	opPatch                      // partial_update_element
	opSettings                   // partial_update_setting
)

// cardOp is one queued mutation inside a single BatchUpdateCard request.
type cardOp struct {
	kind      cardOpKind
	elementID string           // opPatch
	elements  []map[string]any // opAdd
	partial   map[string]any   // opPatch
	settings  map[string]any   // opSettings
}

// ensureElementsLocked queues the ADD ops for iteration n's elements that do not
// exist yet: the thinking panel, the answer element, and one row per tool.
//
// 这是「固定骨架 + 只追加」的核心：结构变化永远只 append，从不重排整卡。
// The caller must hold c.mu.
func (c *feishuStreamCard) ensureElementsLocked(n int) {
	if n <= 0 {
		return
	}
	it := c.iter(n)
	if !c.appendThink[n] {
		c.appendThink[n] = true
		if it.reasoning != "" {
			c.queueAdd(reasoningPanel(n, it.reasoning))
		} else {
			c.queueAdd(reasoningPanel(n, ""))
		}
	}
	if !c.appendAnswer[n] {
		c.appendAnswer[n] = true
		c.queueAdd(map[string]any{
			"tag": "markdown", "element_id": answerElementID(n),
			"content": it.content, "text_size": "normal",
		})
	}
	for i := range it.tools {
		if c.appendTool[toolRowElementID(n, i)] {
			continue
		}
		c.appendTool[toolRowElementID(n, i)] = true
		c.queueAdd(map[string]any{
			"tag": "markdown", "element_id": toolRowElementID(n, i),
			"content": toolChip(it.tools[i]), "text_size": "notation",
		})
	}
}

// ensureAllElementsLocked queues ADD ops for every known iteration/tool.
// The caller must hold c.mu.
func (c *feishuStreamCard) ensureAllElementsLocked() {
	nums := make([]int, 0, len(c.iters))
	for n := range c.iters {
		nums = append(nums, n)
	}
	// 迭代号升序 → 追加顺序与时间顺序一致。
	for i := 1; i < len(nums); i++ {
		for j := i; j > 0 && nums[j] < nums[j-1]; j-- {
			nums[j], nums[j-1] = nums[j-1], nums[j]
		}
	}
	for _, n := range nums {
		c.ensureElementsLocked(n)
	}
}

// queueAdd appends element(s) to the card body.
//
// 只入队，**不**动 c.elements：c.elements 表示"已经在卡片上的元素"，
// 由 submitOpsLocked 在 batch_update **成功之后**才提交（commit-on-success）。
// The caller must hold c.mu.
func (c *feishuStreamCard) queueAdd(elements ...map[string]any) {
	if len(elements) == 0 {
		return
	}
	c.ops = append(c.ops, cardOp{kind: opAdd, elements: elements})
}

// queueElementContentLocked replaces the FULL text of one element via
// partial_update_element.
//
// 这是文本唯一的写入通道（当前迭代正文/思考 + 已完结迭代的文本都走它）：
//   - CardElement.Content 只认建卡模板声明过的元素，append 出来的元素会被 300313
//     拒绝（真实 API 探针实测）—— 本实现除 ans_1 外全是 append 的，所以不能用它；
//   - partial_update_element 对任意已存在元素都成功（实测），且它天然享受本文件的
//     commit-on-success / 重试 / 熔断；同一元素多次入队只保留最后一次（全量替换语义）。
//
// 空串一律不写：Content/补丁里的空 content 会被飞书拒（Content 实测 99992402），
// 需要"清空"时由调用方给非空占位符。
// The caller must hold c.mu.
func (c *feishuStreamCard) queueElementContentLocked(elementID, text string) {
	if elementID == "" || text == "" {
		return
	}
	c.queuePatch(elementID, map[string]any{"content": text})
}

// queuePatch updates some fields of ONE element (e.g. a panel header title).
func (c *feishuStreamCard) queuePatch(elementID string, partial map[string]any) {
	if elementID == "" || len(partial) == 0 {
		return
	}
	// 同元素 last-writer-wins：把此前的 patch 丢掉，只保留最新的一份。
	kept := c.ops[:0]
	for _, op := range c.ops {
		if op.kind == opPatch && op.elementID == elementID {
			continue
		}
		kept = append(kept, op)
	}
	c.ops = append(kept, cardOp{kind: opPatch, elementID: elementID, partial: partial})
}

// flushOps submits all queued mutations in ONE BatchUpdateCard request.
// Throttled unless force; no-op when nothing is queued.
// The caller must hold c.mu.
// submitOpsLocked submits the queued ops NOW (no throttle, ignores the finished
// gate — finalize must be able to flush its closing ops).
//
// commit-on-success：请求成功才提交（清空 ops、把新增元素写进 c.elements、清零连续
// 失败计数）；**失败则原样保留 ops**，下一次 flush 重试 —— 绝不静默丢弃。
// The caller must hold c.mu.
func (c *feishuStreamCard) submitOpsLocked() error {
	if len(c.ops) == 0 {
		return nil
	}
	actions := make([]map[string]any, 0, len(c.ops))
	adds := make([]map[string]any, 0, len(c.ops))
	for _, op := range c.ops {
		switch op.kind {
		case opAdd:
			adds = append(adds, op.elements...)
		case opPatch:
			actions = append(actions, map[string]any{
				"partial_update_element": map[string]any{
					"element_id":      op.elementID,
					"partial_element": op.partial,
				},
			})
		case opSettings:
			actions = append(actions, map[string]any{
				"partial_update_setting": map[string]any{"settings": op.settings},
			})
		}
	}
	if len(adds) > 0 {
		// 所有新增合成一个 action（一次请求里追加多个元素），且**排在补丁/配置更新之前**
		// —— 同一批里可能有针对这些新元素的 partial_update_element（文本补丁），
		// 元素必须先存在。
		actions = append([]map[string]any{{
			"add_elements": map[string]any{"type": "append", "elements": adds},
		}}, actions...)
	}
	payload, err := json.Marshal(actions)
	if err != nil {
		// 本地序列化失败：重试同一批没有意义，但同样不静默丢弃（由熔断兜底）。
		log.WithError(err).WithField("card_id", c.cardID).
			Warn("Feishu: marshal card ops failed")
		return c.recordCardFailureLocked("marshal", err, 0, "")
	}
	c.seq++
	req := larkcardkit.NewBatchUpdateCardReqBuilder().
		CardId(c.cardID).
		Body(larkcardkit.NewBatchUpdateCardReqBodyBuilder().
			Actions(string(payload)).
			Sequence(c.seq).
			Uuid(newStreamCardUUID()).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.Card.BatchUpdate(context.Background(), req)
	if err != nil {
		return c.recordCardFailureLocked("request", err, 0, "")
	}
	if !resp.Success() {
		return c.recordCardFailureLocked("rejected", nil, resp.Code, resp.Msg)
	}
	// 成功 → 提交：清掉这批 ops，并把新增元素记进"卡片上已存在的元素"。
	c.ops = nil
	c.elements = append(c.elements, adds...)
	c.lastCardAt = time.Now()
	c.consecutiveCardFailures = 0
	return nil
}

// recordCardFailureLocked keeps the queued ops for the next flush, logs the failure
// at Warn (with card_id + stage + code + msg + the consecutive failure count) and
// trips the per-chat card breaker once the consecutive-failure threshold is reached.
// The caller must hold c.mu.
func (c *feishuStreamCard) recordCardFailureLocked(stage string, apiErr error, code int, msg string) error {
	c.consecutiveCardFailures++
	fields := map[string]any{
		"card_id":              c.cardID,
		"stage":                stage,
		"consecutive_failures": c.consecutiveCardFailures,
	}
	if code != 0 || msg != "" {
		fields["code"] = code
		fields["msg"] = msg
	}
	if apiErr != nil {
		log.WithError(apiErr).WithFields(fields).
			Warn("Feishu: stream card write failed; ops kept for retry")
	} else {
		log.WithFields(fields).
			Warn("Feishu: stream card write rejected; ops kept for retry")
	}
	if c.consecutiveCardFailures >= streamCardMaxConsecutiveFailures {
		log.WithFields(fields).
			Warn("Feishu: stream card broken after repeated failures; falling back to plain replies for this chat")
		c.markCardBrokenLocked()
	}
	if apiErr != nil {
		return apiErr
	}
	return fmt.Errorf("feishu stream card update rejected: code=%d msg=%s stage=%s", code, msg, stage)
}

// markCardBrokenLocked tells the channel to stop using progress cards for this chat.
// The caller must hold c.mu.
func (c *feishuStreamCard) markCardBrokenLocked() {
	if c.channel == nil || c.chatID == "" {
		return
	}
	c.channel.markStreamCardsBroken(c.chatID)
}

// flushOps is the throttled entry point used by the streaming paths.
// The caller must hold c.mu.
func (c *feishuStreamCard) flushOps(force bool) {
	if c.finished {
		return
	}
	if !force && time.Since(c.lastCardAt) < streamCardPanelMinInterval {
		return
	}
	c.submitOpsLocked()
}

// patchThinkingCountLocked refreshes the thinking-panel HEADER ("💭 思考 N 字").
// A panel header can only change through a partial element update — this replaces
// the old full-card update (which interrupted the typewriter every 400ms).
// The caller must hold c.mu.
func (c *feishuStreamCard) patchThinkingCountLocked(n int, reasoning string) {
	title := thinkingPanelTitle(reasoning)
	if c.panelTitles == nil {
		c.panelTitles = map[int]string{}
	}
	c.panelTitles[n] = title
	c.queuePatch(panelElementID(n), map[string]any{
		"header": map[string]any{
			"title": map[string]any{
				"tag": "plain_text", "content": title,
				"text_color": "grey", "text_size": "notation",
			},
		},
	})
}
