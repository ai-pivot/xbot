package agent

import (
	"testing"
	"time"

	"xbot/protocol"
)

// setPendingCreatedAt 直接改写 pending 的登记时间（不经过真实时钟）。
func setPendingCreatedAt(t *testing.T, a *Agent, ch, chatID string, createdAt time.Time) {
	t.Helper()
	value, ok := a.waitingUserSessions.Load(qualifyChatID(ch, chatID))
	if !ok {
		t.Fatalf("pending entry not registered for %s:%s", ch, chatID)
	}
	entry := value.(*pendingAskUserEntry)
	entry.mu.Lock()
	entry.createdAt = createdAt
	entry.mu.Unlock()
}

// TestAskUserTTLBoundaryInjectedClock 用**注入时钟**钉死 AskUser TTL 的纳秒级边界，
// 并保证两条路径是同一语义：
//
//	路径 A：autoResolveStalePendingAskUser —— 到点收回（清 pending + 广播 cancelled/ttl_expired）
//	路径 B：hasBlockingPendingAskUser     —— 到点不再拦住后台通知排空
//
// 三点（全部用注入时钟，全程不 sleep、不读真实时钟 —— 纳秒精度靠真实时钟测不到）：
//
//	t = TTL-1ns  → 必须保留：pending 在、不广播、通知仍被拦（blocking=true）
//	               注：TTL-1ns 点对 `<`→`<=` 不敏感（两值都保留），`<=` 只由 ttl-exact 点抓。
//	t = TTL 整   → 必须收回。注意：= TTL 整按当前 `<` 语义收回；若实现改成 `<=`，
//	               本断言必须跟着改成"保留"，那说明边界语义已变，不许只改测试了事。
//	t = TTL+1ns  → 必须收回
//
// 防什么：TTL 判据的边界/方向漂移（`<` 改 `<=`、把边界往里收 1ns），以及两条路径
// 只改一半导致语义分叉（一条收回、一条仍拦）。
// 不防什么：① TTL 时长本身（30m / env 注入值）是否被改 —— 由 TestAskUserTTLThirtyMinutesReal
// 与 env 用例覆盖；② createdAt 零值语义（视为新鲜）；③ 真实时钟下长时间运行的行为
// （本测试注入时钟，真实时钟路径只由 31 分钟真跑覆盖）。
func TestAskUserTTLBoundaryInjectedClock(t *testing.T) {
	ttl := pendingAskUserTTL // 跟随当前配置，边界相对它算，不改写这个包级变量
	if ttl <= 0 {
		t.Fatalf("pendingAskUserTTL must be positive, got %s", ttl)
	}

	// 固定"现在"：一个与真实时间无关的绝对值，注入后所有 TTL 判断都看它。
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	restore := setAskUserNowForTest(func() time.Time { return base })
	defer restore()

	cases := []struct {
		name     string
		offset   time.Duration // createdAt = base - offset，即 pending 已经存在的时长
		wantKeep bool
	}{
		{"ttl-minus-1ns", ttl - 1, true},
		{"ttl-exact", ttl, false}, // = TTL 整：按 `<` 语义收回；实现改 `<=` 则此断言必须变
		{"ttl-plus-1ns", ttl + 1, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{}
			fake := &fakeResolvedSender{}
			wireResolvedSender(a, fake)

			chatID := "chat-boundary-" + tc.name
			a.setPendingAskUser("web", chatID, &protocol.ProgressEvent{RequestID: "req-" + tc.name})
			setPendingCreatedAt(t, a, "web", chatID, base.Add(-tc.offset))

			// —— 路径 B：通知拦截（不改状态，先测）——
			blocking := a.hasBlockingPendingAskUser("web", chatID)
			if blocking != tc.wantKeep {
				t.Fatalf("hasBlockingPendingAskUser @age=%s (ttl=%s): got blocking=%v, want %v；"+
					"TTL 边界语义在通知拦截路径上不符", tc.offset, ttl, blocking, tc.wantKeep)
			}
			if pending := a.GetPendingAskUser("web", chatID); pending == nil {
				t.Fatalf("hasBlockingPendingAskUser 不应改动状态：@age=%s pending 被删了", tc.offset)
			}

			// —— 路径 A：到点收回 ——
			a.autoResolveStalePendingAskUser("web", chatID)
			pending := a.GetPendingAskUser("web", chatID)
			events := fake.snapshot()

			if tc.wantKeep {
				if pending == nil {
					t.Fatalf("autoResolveStalePendingAskUser @age=%s (< TTL)：pending 应保留，实际被收回（提前收回）", tc.offset)
				}
				if len(events) != 0 {
					t.Fatalf("autoResolveStalePendingAskUser @age=%s (< TTL)：不应广播 ask_user_resolved，实际 %+v", tc.offset, events)
				}
				return
			}

			if pending != nil {
				t.Fatalf("autoResolveStalePendingAskUser @age=%s (>= TTL)：pending 应被收回，实际仍存在（TTL 不是上界）", tc.offset)
			}
			if len(events) != 1 {
				t.Fatalf("autoResolveStalePendingAskUser @age=%s (>= TTL)：应广播 1 条 ask_user_resolved，实际 %d 条 %+v", tc.offset, len(events), events)
			}
			ev := events[0]
			if ev.Reason != "cancelled" || ev.CancelReason != "ttl_expired" {
				t.Fatalf("收回广播契约不符：reason=%q cancel_reason=%q，期望 cancelled/ttl_expired", ev.Reason, ev.CancelReason)
			}
			if ev.RequestID != "req-"+tc.name {
				t.Fatalf("收回广播 request_id=%q，期望 req-%s", ev.RequestID, tc.name)
			}
		})
	}
}

// 同一语义的守护：两条路径对同一个 pending 的判定必须一致（不允许分叉）。
// 这也解释了为什么把 `<` 只改一处会让本测试红。
func TestAskUserTTLBoundaryPathsAgree(t *testing.T) {
	ttl := pendingAskUserTTL
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	restore := setAskUserNowForTest(func() time.Time { return base })
	defer restore()

	for _, offset := range []time.Duration{0, ttl - 1, ttl} {
		a := &Agent{}
		wireResolvedSender(a, &fakeResolvedSender{})
		chatID := "chat-agree"
		a.setPendingAskUser("web", chatID, &protocol.ProgressEvent{RequestID: "req-agree"})
		setPendingCreatedAt(t, a, "web", chatID, base.Add(-offset))

		blockingBefore := a.hasBlockingPendingAskUser("web", chatID)
		a.autoResolveStalePendingAskUser("web", chatID)
		kept := a.GetPendingAskUser("web", chatID) != nil
		if blockingBefore != kept {
			t.Fatalf("@age=%s：通知拦截路径判定 blocking=%v，但收回路径判定 kept=%v —— 两条路径 TTL 语义分叉", offset, blockingBefore, kept)
		}
		a.clearPendingAskUser("web", chatID)
	}
}
