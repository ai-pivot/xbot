package agent

import (
	"container/heap"
	"strconv"
	"sync"
	"time"

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
	// StructuredPriorityHigh 用户明确回答/批红。
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
	// 结构化行已经在会话库中（API 先落库再投递）——空 content + structured_delivery
	// 让 processMessage 跳过再次落 user 行，避免同一答案出现两条用户消息。
	a.injectInboundWithMetadata(item.Channel, item.ChatID, item.SenderID, "", metadata)
	return true
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
