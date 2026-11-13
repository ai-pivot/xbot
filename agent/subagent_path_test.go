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
	if got != "main/taizi" {
		t.Fatalf("subAgentPath(\"\", \"taizi\") = %q, want \"main/taizi\".\n"+
			"  A stable main root keeps top-level subagents distinguishable from the main Agent,\n"+
			"  while avoiding the old malformed leading-slash key.", got)
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

// A top-level child must retain the main root segment because buildToolContext
// identifies subagents by the presence of an AgentID path separator.
func TestSubAgentPath_TopLevelKeyHasAgentRoot(t *testing.T) {
	key := subAgentPath("", "taizi")
	segs := strings.Split(strings.Trim(key, "/"), "/")
	if len(segs) != 2 || segs[0] != "main" || segs[1] != "taizi" {
		t.Fatalf("top-level key %q splits into %v, want [main taizi]", key, segs)
	}
}
