package agent

// subagent_path_test.go — 2026-11-13 P0（用户报「子代理会话在 Web 上收不到任何 SSE /
// busy 状态不对」，会话 `agent:web:chat_9C218CD94A75/taizi:dpsk-perf-1`）。
//
// 线上证据（xbot-2026-11-13.log，06:01:50，32 次）：
//   ERROR {"bad_key":"/taizi","count":32,
//          "msg":"bg notification key unresolvable and no matching parent session;
//                 keeping for main session"}
//
// 根因：`parentAgentID` 是父 agent 的路径根，**主会话（用户直接对话）spawn 子代理时
// 它是空的**；旧实现无条件拼 `parentAgentID + "/" + roleName` ⇒ subAgentID = "/taizi"
// ⇒ drainAndProcessNotifications 见「无冒号」走 agent 路径兜底 ⇒ Trim("/") 后单段
// ⇒ 取不到父 role ⇒ 子代理的完成/状态通知全部落进主会话，子代理会话永远收不到 idle。
//
// 本测守护 subAgentPath 的这个不变量：**任何情况下都不产出以 "/" 开头的畸形 ID**。

import (
	"strings"
	"testing"
)

func TestSubAgentPath_NoLeadingSlashWhenRootEmpty(t *testing.T) {
	got := subAgentPath("", "taizi")
	if got != "taizi" {
		t.Fatalf("subAgentPath(\"\", \"taizi\") = %q, want \"taizi\".\n"+
			"  A leading \"/\" makes this value look like an AgentID path but parse to a\n"+
			"  single segment, so bg notifications fall back to the main session and the\n"+
			"  sub-agent session never receives idle (2026-11-13, 32x ERROR bad_key=\"/taizi\").",
			got)
	}
	if strings.HasPrefix(got, "/") {
		t.Fatalf("subAgentPath must never start with \"/\", got %q", got)
	}
}

func TestSubAgentPath_NestedKeepsFullChain(t *testing.T) {
	got := subAgentPath("main/court.shoufu", "court.bingbu")
	if got != "main/court.shoufu/court.bingbu" {
		t.Fatalf("nested = %q, want \"main/court.shoufu/court.bingbu\"", got)
	}
}

// TestSubAgentPath_TopLevelKeyIsSingleSegment 单段 key 必须能被识别为「顶层子代理」，
// 即 Trim("/")+Split 后 len==1 —— deliverNotificationsByAgentPath 依赖这一点区分
// 「父就是主会话」（正常）与「父 role 找不到」（真异常）。
func TestSubAgentPath_TopLevelKeyIsSingleSegment(t *testing.T) {
	key := subAgentPath("", "taizi")
	segs := strings.Split(strings.Trim(key, "/"), "/")
	if len(segs) != 1 {
		t.Fatalf("top-level key %q splits into %d segments %v, want 1", key, len(segs), segs)
	}
}
