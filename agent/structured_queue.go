package agent

import (
	"container/heap"
	"strconv"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
	"xbot/tools"
)

// structured_queue.go — 插队优先队列（sendmessage-structured 设计 §9.2）。
//
// 语义：
//   - 优先级：answer/decision = 100；用户普通插话 = 10；后台通知 = 0。
//   - 同级 FIFO（seq 单调递增）。
//   - 防饿死：连续取出 8 条高优先级消息后，强制取一条低优先级消息。
//   - 数据结构是 container/heap（入队/出队 O(log n)）；不阻塞、不打断正在
//     执行的工具调用 —— 只在 engine iteration 边界被 drain。
//
// 消费点：Agent.wireBgNotificationDrain → runState.drainAndInjectBgNotifications
// （agent/engine_run.go 的 iteration 边界）。空闲时 DeliverStructuredMessage
// 直接走 inbound 队列开启新 turn。

const (
	// StructuredPriorityHigh 用户明确回答/批复。
	StructuredPriorityHigh = 100
	// StructuredPriorityNormal 用户普通插话。
	StructuredPriorityNormal = 10
	// StructuredPriorityBackground 后台通知。
	StructuredPriorityBackground = 0

	// structuredStarvationLimit 连续处理多少条高优先级后强制让位一条普通消息。
	structuredStarvationLimit = 8
)

// StructuredQueueItem 是一条等待在 iteration 边界注入的结构化消息。
type StructuredQueueItem struct {
	ID            string // 稳定标识（用于日志/去重）
	Channel       string
	ChatID        string
	SenderID      string
	SenderName    string
	Content       string // 已渲染的人读文本
	Kind          string
	CorrelationID string
	Priority      int
	EnqueuedAt    time.Time

	seq uint64 // 入队序号（同级 FIFO）
}

// structuredHeap 实现 heap.Interface：优先级高者在前；同级 seq 小者在前。
type structuredHeap []*StructuredQueueItem

func (h structuredHeap) Len() int { return len(h) }
func (h structuredHeap) Less(i, j int) bool {
	if h[i].Priority != h[j].Priority {
		return h[i].Priority > h[j].Priority
	}
	return h[i].seq < h[j].seq
}
func (h structuredHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *structuredHeap) Push(x any)   { *h = append(*h, x.(*StructuredQueueItem)) }
func (h *structuredHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // avoid leak
	*h = old[:n-1]
	return item
}

// StructuredQueue 是单个会话的插队队列。并发安全。
//
// 两个 lane（都是 heap）：high（优先级 ≥ 100）与 low（普通 10 / 后台 0）。
// 出队严格优先 high；连续 8 条 high 后从 low 让位一条 —— 两个 lane 都是
// O(log n)，因此入队/出队与队列长度无关（不自测 O(n) 防饿死扫描）。
type StructuredQueue struct {
	mu              sync.Mutex
	high            structuredHeap
	low             structuredHeap
	seq             uint64
	consecutiveHigh int
}

// NewStructuredQueue 创建一个空队列。
func NewStructuredQueue() *StructuredQueue { return &StructuredQueue{} }

// Push 入队。Priority < 0 视为后台（0）。返回入队后的总长度。
func (q *StructuredQueue) Push(item *StructuredQueueItem) int {
	if item == nil {
		return 0
	}
	if item.Priority < 0 {
		item.Priority = StructuredPriorityBackground
	}
	if item.EnqueuedAt.IsZero() {
		item.EnqueuedAt = time.Now()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	item.seq = q.seq
	if item.Priority >= StructuredPriorityHigh {
		heap.Push(&q.high, item)
	} else {
		heap.Push(&q.low, item)
	}
	return len(q.high) + len(q.low)
}

// Pop 取出下一条要注入的消息。防饿死：连续 8 条高优先级后，若 low 非空，
// 本轮强制取一条 low（low 内部按 normal(10) > background(0) 排序）。
func (q *StructuredQueue) Pop() *StructuredQueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.consecutiveHigh >= structuredStarvationLimit && len(q.low) > 0 {
		q.consecutiveHigh = 0
		return heap.Pop(&q.low).(*StructuredQueueItem)
	}
	if len(q.high) > 0 {
		q.consecutiveHigh++
		return heap.Pop(&q.high).(*StructuredQueueItem)
	}
	q.consecutiveHigh = 0
	if len(q.low) == 0 {
		return nil
	}
	return heap.Pop(&q.low).(*StructuredQueueItem)
}

// Len 返回当前队列长度。
func (q *StructuredQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.high) + len(q.low)
}

// ---------------------------------------------------------------------------
// Agent 侧：会话级队列注册表 + 投递
// ---------------------------------------------------------------------------

// structuredQueue 返回（必要时创建）某会话的插队队列。
func (a *Agent) structuredQueue(sessionKey string) *StructuredQueue {
	if v, ok := a.structuredQueues.Load(sessionKey); ok {
		return v.(*StructuredQueue)
	}
	q := NewStructuredQueue()
	actual, _ := a.structuredQueues.LoadOrStore(sessionKey, q)
	return actual.(*StructuredQueue)
}

// peekStructuredQueue 只读地取某会话的插队队列；不存在返回 nil（不创建）。
func (a *Agent) peekStructuredQueue(sessionKey string) *StructuredQueue {
	v, ok := a.structuredQueues.Load(sessionKey)
	if !ok {
		return nil
	}
	q, _ := v.(*StructuredQueue)
	return q
}

// StructuredQueueLen 返回某会话待注入的结构化消息数量（诊断/测试）。
func (a *Agent) StructuredQueueLen(channelName, chatID string) int {
	v, ok := a.structuredQueues.Load(qualifyChatID(channelName, chatID))
	if !ok {
		return 0
	}
	return v.(*StructuredQueue).Len()
}

// DeliverStructuredMessage 把一条结构化消息投递给目标会话：
//   - 该会话正在跑 turn（busy）→ 入插队队列，引擎在下一个 iteration 边界注入；
//   - 空闲 → 作为普通入站消息开启新 turn（历史里已有结构化行，不会丢）。
//
// 返回是否已受理（false = 会话不存在/参数非法）。
// SessionBusy 报告该物理会话当前是否有 Run 在执行。
//
// 用途：结构化回流（卡片批复）在 busy 与 idle 两种状态下**只能有一种表示**——
// busy 走 structured queue（notice 携带内容），idle 走「落库 + 空注入」。
// 两者同时发生就会出现用户实测的重复投递（2026-11-15）。
func (a *Agent) SessionBusy(channelName, chatID string) bool {
	if channelName == "" || chatID == "" {
		return false
	}
	if st, ok := a.bgSessionStates.Load(qualifyChatID(channelName, chatID)); ok {
		if ss, ok := st.(*bgSessionState); ok && ss != nil {
			return ss.busy.Load()
		}
	}
	return false
}

func (a *Agent) DeliverStructuredMessage(item *StructuredQueueItem) bool {
	if item == nil || item.Channel == "" || item.ChatID == "" {
		return false
	}
	key := qualifyChatID(item.Channel, item.ChatID)
	if st, ok := a.bgSessionStates.Load(key); ok {
		if ss, ok := st.(*bgSessionState); ok && ss.busy.Load() {
			a.structuredQueue(key).Push(item)
			// best-effort 唤醒空闲等待中的 worker（busy 时由 Run 的 drain 消费）。
			select {
			case ss.notifyCh <- struct{}{}:
			default:
			}
			return true
		}
	}
	metadata := map[string]string{
		"structured_kind":     item.Kind,
		"priority":            strconv.Itoa(item.Priority),
		"structured_delivery": "true",
	}
	if item.CorrelationID != "" {
		metadata["correlation_id"] = item.CorrelationID
	}
	// 结构化行已经在会话库中（调用方通过 persist 先落库）——空 content +
	// structured_delivery 让 processMessage 跳过再次落 user 行。
	a.injectInboundWithMetadata(item.Channel, item.ChatID, item.SenderID, "", metadata)
	return true
}

// DeliverStructuredMessagePersisting 是结构化回流（卡片批复/回答）的**唯一入口**。
//
// 为什么需要它：落库与投递本来是两步调用，而"要不要落库"取决于目标会话忙不忙。
// 两步分离时，调用方无条件落库 + 投递在 busy 时又带内容 ⇒ 同一事件被表示两遍
// （busy 那一轮看到 notice，idle 的下一轮从历史里再读一次；2026-11-15 用户实测）。
//
// 这里把"落库"作为回调收进来，由同一个 busy/idle 分支决定：
//   - busy  → 只入队（notice 自带内容），**不落库**；
//   - idle  → 先落库（历史行即唯一可读副本），再空内容注入；
//   - idle 且落库失败 → 退化为"携带内容投递"，绝不丢消息。
func (a *Agent) DeliverStructuredMessagePersisting(item *StructuredQueueItem, persist func() error) bool {
	if item == nil || item.Channel == "" || item.ChatID == "" {
		return false
	}
	key := qualifyChatID(item.Channel, item.ChatID)
	if st, ok := a.bgSessionStates.Load(key); ok {
		if ss, ok := st.(*bgSessionState); ok && ss != nil && ss.busy.Load() {
			a.structuredQueue(key).Push(item)
			select {
			case ss.notifyCh <- struct{}{}:
			default:
			}
			return true
		}
	}

	content := item.Content // 仅在落库失败/未提供 persist 时内联
	if persist != nil {
		if err := persist(); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"channel": item.Channel, "chat_id": item.ChatID, "correlation_id": item.CorrelationID,
			}).Warn("structured notice persist failed; delivering content inline")
		} else {
			content = ""
		}
	}
	metadata := map[string]string{
		"structured_kind":     item.Kind,
		"priority":            strconv.Itoa(item.Priority),
		"structured_delivery": "true",
	}
	if item.CorrelationID != "" {
		metadata["correlation_id"] = item.CorrelationID
	}
	a.injectInboundWithMetadata(item.Channel, item.ChatID, item.SenderID, content, metadata)
	return true
}

// flushStructuredQueueAsUserMessage 把会话**已 idle** 时队列里的残留转成一条普通
// user 消息，立即起一个 turn。
//
// 为什么必须要有这一步：插队队列只在 Run 的 iteration 边界（wireBgNotificationDrain）
// 被消费。如果消息在 busy 期入队、而该 Run 在下一个边界之前就结束了，队列残留就
// 没有任何人消费 —— 表现为「idle 时不投递，要等下一次 busy 才变成 user msg」
// （2026-11-16 用户实测）。Run 结束后由 drainAndProcessNotifications 调用本函数兜底。
//
// 语义要点：这里**不带** structured_delivery（否则 processMessage 会跳过落 user 行），
// 也不再加结构化元数据 —— 内容直接内联，按普通用户消息落库并触发新 turn。
func (a *Agent) flushStructuredQueueAsUserMessage(sessionKey string) int {
	q := a.peekStructuredQueue(sessionKey)
	if q == nil || q.Len() == 0 {
		return 0
	}
	var parts []string
	var first *StructuredQueueItem
	n := 0
	for {
		item := q.Pop()
		if item == nil {
			break
		}
		n++
		if first == nil {
			first = item
		}
		if c := strings.TrimSpace(item.Content); c != "" {
			parts = append(parts, c)
		}
	}
	if first == nil || len(parts) == 0 {
		return n
	}
	content := strings.Join(parts, "\n\n---\n\n")
	log.WithFields(log.Fields{
		"channel": first.Channel, "chat_id": first.ChatID,
		"count": n, "correlation_id": first.CorrelationID,
	}).Info("flushing queued structured messages as a user message (session went idle before drain)")
	a.injectInboundWithMetadata(first.Channel, first.ChatID, first.SenderID, content, nil)
	return n
}

// drainStructuredQueue 在 iteration 边界把队列中的消息转换成注入通知。
// 顺序 = 优先队列出队顺序（高优先级 + 防饿死让位）。
func (a *Agent) drainStructuredQueue(sessionKey string) []tools.BgNotification {
	v, ok := a.structuredQueues.Load(sessionKey)
	if !ok {
		return nil
	}
	q := v.(*StructuredQueue)
	var out []tools.BgNotification
	for {
		item := q.Pop()
		if item == nil {
			break
		}
		out = append(out, &tools.StructuredNotice{
			Key:           sessionKey,
			Sid:           item.SenderID,
			Kind:          item.Kind,
			CorrelationID: item.CorrelationID,
			Content:       item.Content,
			Priority:      item.Priority,
		})
	}
	return out
}
