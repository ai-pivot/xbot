package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// Bug 1（2026-10-22 05:26 生产 panic / exit status 2）复现用例。
//
// 现场日志签名（铁证）：
//   TTL 清理 → "SQLite database closed" + "WAL file DISAPPEARED ... where=close"
//   → "Tenant deleted" / "Tenant session destroyed"
//   → "WAL state ... where=open"   ← 关闭之后又被 open（use-after-close）
//   → supervisor: exited (exit status 2)
//
// 根因链：destroyInteractiveSession 关库/删文件的同时，并发投递路径仍持有该
// 会话（stale 引用），继续在「已 close 的库」上跑完 turn 并回写 —— 回写失败
// 只是脏数据/异常，真正的致命点在于没有任何墓碑阻止"库已关→又被重新打开/使用"。
//
// 修复：interactiveAgent.destroyed 原子墓碑（CAS 夺权，销毁幂等）+ 所有取用
// 路径的墓碑检查 + session 层"已销毁租户拒绝重开"。
// ---------------------------------------------------------------------------

// blockingLLM 的 Generate 会阻塞到 release 关闭 —— 用来把 Run 精确停在
// "turn 正在跑、会话库还活着" 的窗口里，再从另一个 goroutine 销毁会话。
type blockingLLM struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingLLM() *blockingLLM {
	return &blockingLLM{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingLLM) Generate(ctx context.Context, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return &llm.LLMResponse{Content: "late reply after destroy"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingLLM) ListModels() []string { return nil }

// TestSendRacingDestroy_EndsWithExplicitDestroyedError 是本 bug 的**确定复现**：
//
//	① send 进入 Run（LLM 阻塞在 Generate）—— 会话库仍活着；
//	② 另一个 goroutine 销毁该会话（TTL/unload/级联取消的真实形态）；
//	③ 放行 LLM，Run 收尾并尝试回写。
//
// 修复前：回写在**已 close 的库**上 AppendMessage → ia.lastError 是一条裸的
// sqlite 错误（"database connection is closed"），前端只看到半截 turn。
// 修复后：墓碑生效 → 回写被跳过，lastError 明确说"session was destroyed"。
func TestSendRacingDestroy_EndsWithExplicitDestroyedError(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	key := "cli:/w/reviewer:race1"

	llmStub := newBlockingLLM()
	ia := &interactiveAgent{
		roleName:     "reviewer",
		instance:     "race1",
		systemPrompt: llm.NewSystemMessage("You are a reviewer."),
		messages:     []llm.ChatMessage{llm.NewUserMessage("first")},
		lastUsed:     time.Now(),
		cfg: &RunConfig{
			LLMClient: llmStub,
			Model:     "test-model",
			Tools:     newTestRegistry(),
			Channel:   "agent",
			ChatID:    key,
			Session:   sess,
		},
	}
	a.interactiveSubAgents.Store(key, ia)

	out, err := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
		Channel: "cli", ChatID: "/w", SenderID: "u1", ChatType: "p2p",
		Content:  "racing message",
		Metadata: map[string]string{"instance_id": "race1"},
	})
	if err != nil {
		t.Fatalf("SendToInteractiveSession: %v", err)
	}
	if out != nil && out.Error != nil {
		t.Fatalf("send rejected before the race even started: %v", out.Error)
	}

	// ② Run 已进入 LLM 调用 —— 此刻销毁会话（与生产 TTL 清理同一条路径）。
	select {
	case <-llmStub.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never reached the LLM call")
	}
	a.destroyInteractiveSession(key)

	// ③ 放行 LLM，让 Run 走完收尾/回写。
	close(llmStub.release)
	if !waitForCondition(5*time.Second, func() bool {
		ia.mu.Lock()
		defer ia.mu.Unlock()
		return !ia.running
	}) {
		t.Fatal("Run did not finish after destroy")
	}

	ia.mu.Lock()
	lastErr := ia.lastError
	ia.mu.Unlock()

	if lastErr == "" {
		t.Fatalf("BUG: Run finished after its session was destroyed and reported NO error — the turn was written into (or silently dropped by) a destroyed session")
	}
	if !strings.Contains(lastErr, "destroyed") {
		t.Fatalf("BUG (use-after-close): lastError = %q — the Run kept using the closed session DB instead of a tombstone; want an explicit \"... was destroyed\" error", lastErr)
	}
	if _, still := a.interactiveSubAgents.Load(key); still {
		t.Error("destroyed session is still registered")
	}
}

// sliceChannel 统计 subagent_stopped(removed) 事件，并用 arrive/release 制造
// 一个"所有销毁者都已进入"的屏障（见 TestDestroyInteractiveSession_IsIdempotent）。
type sliceChannel struct {
	mu       sync.Mutex
	removed  int
	arrive   chan struct{}
	release  chan struct{}
	maxWait  time.Duration
	arrivedN int
	target   int
}

func (c *sliceChannel) Name() string                               { return "test-slice" }
func (c *sliceChannel) Start() error                               { return nil }
func (c *sliceChannel) Stop()                                      {}
func (c *sliceChannel) Send(_ channel.OutboundMsg) (string, error) { return "", nil }

func (c *sliceChannel) SendSessionState(ev protocol.SessionEvent) {
	if ev.Action == "subagent_stopped" && ev.Removed {
		c.mu.Lock()
		c.removed++
		c.arrivedN++
		reached := c.arrivedN >= c.target
		c.mu.Unlock()
		select {
		case c.arrive <- struct{}{}:
		default:
		}
		if !reached {
			// 等其他人（修复后只有 1 个会来 —— 用上限兜底，绝不永久阻塞）。
			select {
			case <-c.release:
			case <-time.After(c.maxWait):
			}
		}
	}
}

func (c *sliceChannel) removedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removed
}

// TestDestroyInteractiveSession_IsIdempotent 钉死"销毁必须幂等"：
// N 个 goroutine 同时销毁同一个会话，删除事件只能发一次（= 只有一次真正的
// DestroySession / 一次 close），不许二次 close 直接把并发重建的库关掉。
//
// 修复前：每个 goroutine 都会走完 emit → 计数 = N（红）。
// 修复后：CAS 只有一个赢家 → 计数 = 1（绿）。
// 变异验证：去掉 markDestroyed 的 CAS 早退 → 立刻回到 N（见 evidence/）。
func TestDestroyInteractiveSession_IsIdempotent(t *testing.T) {
	const destroyers = 8

	mt, sess := newAgentHistorySession(t)
	ch := &sliceChannel{
		arrive:  make(chan struct{}, destroyers),
		release: make(chan struct{}),
		maxWait: 250 * time.Millisecond,
		target:  destroyers,
	}
	a := &Agent{multiSession: mt}
	a.channelRange = func(fn func(string, channel.Channel) bool) {
		fn("web", ch)
	}
	key := "web:web-4/proj.xbot.dev:xbot-dev-route"
	ia := &interactiveAgent{
		roleName:     "proj.xbot.dev",
		instance:     "xbot-dev-route",
		systemPrompt: llm.NewSystemMessage("sys"),
		lastUsed:     time.Now(),
		cfg: &RunConfig{
			LLMClient: &mockLLM{}, Model: "m", Tools: newTestRegistry(),
			Channel: "agent", ChatID: key, Session: sess,
		},
	}
	a.interactiveSubAgents.Store(key, ia)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < destroyers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a.destroyInteractiveSession(key)
		}()
	}
	close(start)
	// 等第一个销毁者进入 emit（它在 barrier 里等人），然后放行，避免空转。
	select {
	case <-ch.arrive:
	case <-time.After(2 * time.Second):
		t.Fatal("no destroyer ever emitted subagent_stopped")
	}
	close(ch.release)
	wg.Wait()

	if got := ch.removedCount(); got != 1 {
		t.Fatalf("subagent_stopped(removed) emitted %d times for %d concurrent destroys, want exactly 1 — destroy is NOT idempotent (a second run would close the re-created session DB again: use-after-close)", got, destroyers)
	}
	if _, still := a.interactiveSubAgents.Load(key); still {
		t.Error("session entry survived the destroy")
	}
}

// TestInteractiveSessionTTL_Configurable 是 Bug 2 的行为验收：
// 配置 1s（agent.interactive_session_ttl）后会话确实 1s 回收；默认值不变。
func TestInteractiveSessionTTL_Configurable(t *testing.T) {
	// 默认：30m（与硬编码时代一致）。
	if got := (&Agent{}).interactiveSessionTTL(); got != 30*time.Minute {
		t.Fatalf("default interactive session TTL = %s, want 30m (default must not change)", got)
	}

	mt, sess := newAgentHistorySession(t)
	a := &Agent{multiSession: mt, interactiveSessionTTLValue: time.Second}
	if got := a.interactiveSessionTTL(); got != time.Second {
		t.Fatalf("configured TTL = %s, want 1s", got)
	}
	key := "cli:/w/reviewer:ttl1"
	a.interactiveSubAgents.Store(key, &interactiveAgent{
		roleName: "reviewer", instance: "ttl1",
		systemPrompt: llm.NewSystemMessage("sys"),
		lastUsed:     time.Now(),
		cfg: &RunConfig{
			LLMClient: &mockLLM{}, Model: "m", Tools: newTestRegistry(),
			Channel: "agent", ChatID: key, Session: sess,
		},
	})

	// 刚访问过 → 不回收。
	a.cleanupExpiredSessions()
	if _, ok := a.interactiveSubAgents.Load(key); !ok {
		t.Fatal("fresh session was reclaimed before the TTL elapsed")
	}

	// 1s 之后 → 必须回收（TTL 真的生效，不是恒等 30m）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		a.cleanupExpiredSessions()
		if _, ok := a.interactiveSubAgents.Load(key); !ok {
			elapsed := time.Since(deadline.Add(-3 * time.Second))
			if elapsed < 900*time.Millisecond {
				t.Fatalf("session reclaimed after %s with TTL=1s — TTL is not honored", elapsed)
			}
			return
		}
	}
	t.Fatal("BUG: session with TTL=1s was never reclaimed — agent.interactive_session_ttl is not wired")
}

// TestSendToDestroyedSession_ReturnsClearError 是最朴素的一条：会话销毁后再
// 投递必须拿到明确错误（不是 panic、不是往死库里写）。
func TestSendToDestroyedSession_ReturnsClearError(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	key := "cli:/w/reviewer:gone1"
	ia := &interactiveAgent{
		roleName: "reviewer", instance: "gone1",
		systemPrompt: llm.NewSystemMessage("sys"),
		lastUsed:     time.Now(),
		cfg: &RunConfig{
			LLMClient: &mockLLM{}, Model: "m", Tools: newTestRegistry(),
			Channel: "agent", ChatID: key, Session: sess,
		},
	}
	a.interactiveSubAgents.Store(key, ia)
	a.destroyInteractiveSession(key)

	// 直接对 stale ia 走一遍（模拟"Load 到一半被销毁"的调用方）。
	if !ia.isDestroyed() {
		t.Fatal("tombstone was not set by destroyInteractiveSession")
	}
	err := destroyedSessionError(key, ia.roleName, ia.instance)
	if !strings.Contains(err.Error(), "was destroyed") {
		t.Fatalf("error %q must state the session was destroyed", err)
	}

	// 正常寻址路径：会话已不在列表里 → 明确报"没有活着的会话"。
	out, sendErr := a.SendToInteractiveSession(context.Background(), "reviewer", bus.InboundMessage{
		Channel: "cli", ChatID: "/w", SenderID: "u1", ChatType: "p2p",
		Content:  "hello?",
		Metadata: map[string]string{"instance_id": "gone1"},
	})
	if sendErr != nil {
		t.Fatalf("SendToInteractiveSession: %v", sendErr)
	}
	if out == nil || out.Error == nil {
		t.Fatal("BUG: sending to a destroyed session did not return an error")
	}
	if !strings.Contains(out.Error.Error(), "no ") && !strings.Contains(out.Error.Error(), "destroyed") {
		t.Errorf("error %q should be actionable (no active session / destroyed)", out.Error)
	}
	if ia.running {
		t.Error("destroyed session was left in running state")
	}
	_ = fmt.Sprintf("%v", lastErrorOf(ia))
}

func lastErrorOf(ia *interactiveAgent) string {
	ia.mu.Lock()
	defer ia.mu.Unlock()
	return ia.lastError
}
