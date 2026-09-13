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
// CardKit 给了完整的元素级原子操作（SDK 实测），本文件把它们组合成一条低噪音路径：
//   - 结构变化 → add_elements（append 一个元素，一次/元素）
//   - 文本流式 → ContentCardElement（打字机；见 pushText / pushReasoning）
//   - 属性变化 → partial_update_element（例如折叠面板标题里的实时字数）
//   - 收尾    → partial_update_setting（关 streaming_mode）+ 批量折叠各思考面板
//
// 所有排队 op 合并成**一次** BatchUpdateCard 请求（同元素 last-writer-wins），
// 因此「迭代边界 + 新工具 + 关上一个工具」这类同 tick 的多改动只花一次调用。

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

// queueAdd appends element(s) to the card body (idempotent per batch).
func (c *feishuStreamCard) queueAdd(elements ...map[string]any) {
	if len(elements) == 0 {
		return
	}
	c.ops = append(c.ops, cardOp{kind: opAdd, elements: elements})
	c.elements = append(c.elements, elements...)
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
// The caller must hold c.mu.
func (c *feishuStreamCard) submitOpsLocked() {
	if len(c.ops) == 0 {
		return
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
		// 所有新增合成一个 action（一次请求里追加多个元素）。
		actions = append(actions, map[string]any{
			"add_elements": map[string]any{"type": "append", "elements": adds},
		})
	}
	payload, err := json.Marshal(actions)
	if err != nil {
		log.WithError(err).WithField("card_id", c.cardID).Debug("Feishu: marshal card ops failed")
		return
	}
	c.ops = nil
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
		log.WithError(err).WithField("card_id", c.cardID).Debug("Feishu: batch update failed")
		return
	}
	if !resp.Success() {
		log.WithFields(map[string]any{"card_id": c.cardID, "code": resp.Code, "msg": resp.Msg}).
			Debug("Feishu: batch update rejected")
		return
	}
	c.lastCardAt = time.Now()
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

// takeOpsElements consumes the queued ADD elements (used to render the card's
// initial skeleton; afterwards structure changes are appended, never re-rendered).
// The caller must hold c.mu.
func (c *feishuStreamCard) takeOpsElements() []map[string]any {
	var out []map[string]any
	for _, op := range c.ops {
		if op.kind == opAdd {
			out = append(out, op.elements...)
		}
	}
	c.ops = nil
	return out
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
