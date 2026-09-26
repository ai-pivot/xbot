package channel

import (
	"strings"
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// compressMarker 构造一个压缩标记行 —— 落库/回放形态：role=user、turn_id=0、
// content 前缀 "[Compacted context]"（见 storage.replayDisplayRecords 与
// agent/compress.go 的 llm.NewUserMessage("[Compacted context]\n\n"+summary)）。
func compressMarker(summary string) llm.ChatMessage {
	return llm.ChatMessage{Role: "user", Content: "[Compacted context]\n\n" + summary}
}

// 复现（P0，2026-09-26 用户报告「发了个继续结果前端显示压缩了」）：
//
// 压缩发生在 turn 3 **中间**（compress 记录的 id 夹在 turn 3 的消息之间），
// 压缩标记行的 turn_id=0。turn 3 之后才有下一个 user 行（turn 4）。旧
// deriveTurnIDs Pass 1（反向扫描）把 turn_id=0 的 user 行绑到**后面最近的
// user 行**的 turn → 标记被绑到 turn 4 ⇒ 前端 historyToReplaced「每 turn 只取
// 第一条 user」⇒ 标记抢占 turn 4 的 user 槽位，**顶掉用户真正发的消息**。
//
// 正确契约：压缩标记是「无 turn 的独立展示行」——绝不参与 turn 绑定，
// 带 standalone + 时间锚点（= 走到它时已知的最新 turn，此处 3），前端据此
// 插回原位（turn 3 之后、turn 4 之前），且**绝不占用任何 turn 的 user 槽位**。
func TestCompactMarker_NotBoundToFollowingTurn(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "turn3 的用户消息", TurnID: 3},
		{Role: "assistant", Content: "turn3 迭代", TurnID: 3},
		compressMarker("context compacted summary"),
		{Role: "assistant", Content: "turn3 继续迭代", TurnID: 3},
		{Role: "user", Content: "继续优化到 7ms，你的上下文无限", TurnID: 4},
		{Role: "assistant", Content: "turn4 迭代", TurnID: 4},
	}

	out := ConvertMessagesToHistory(msgs)

	var marker *HistoryMessage
	for i := range out {
		if strings.HasPrefix(strings.TrimSpace(out[i].Content), "[Compacted context]") {
			marker = &out[i]
			break
		}
	}
	if marker == nil {
		t.Fatalf("压缩标记必须出现在历史里，got %+v", out)
	}
	// ① 绝不绑定到 turn 4（本 bug 的直接症状）——否则前端它会顶掉用户消息。
	if marker.TurnID == 4 {
		t.Fatalf("压缩标记被错误绑定到 turn 4 —— 会顶掉用户的「继续」消息（P0 复现）: %+v", marker)
	}
	if marker.TurnID != 0 {
		t.Fatalf("压缩标记必须保持 turnID=0（无 turn 的独立行），got %d: %+v", marker.TurnID, marker)
	}
	// ② 必须是 standalone + 锚点 = 它所在的 turn（3），前端按锚点插回原位。
	if !marker.Standalone {
		t.Fatalf("压缩标记必须是 standalone（前端据此跳过 bindTurnIDs 绑定）: %+v", marker)
	}
	if marker.AnchorTurnID != 3 {
		t.Fatalf("压缩标记锚点应为 3（发生在 turn 3 中间），got %d: %+v", marker.AnchorTurnID, marker)
	}
}

// 结构化迭代路径（Web 实际走的 ConvertMessagesToHistoryWithIterations）同样必须
// 不把压缩标记绑到后续 turn —— 这条路径有自己的 deriveTurnIDs 调用。
func TestCompactMarker_StructuredPath_NotBoundToFollowingTurn(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "turn3 的用户消息", TurnID: 3},
		{Role: "assistant", Content: "turn3 迭代", TurnID: 3},
		compressMarker("summary"),
		{Role: "assistant", Content: "turn3 继续", TurnID: 3},
		{Role: "user", Content: "继续优化到 7ms", TurnID: 4},
		{Role: "assistant", Content: "turn4 迭代", TurnID: 4},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		3: {{Iteration: 1, Content: "turn3 迭代"}, {Iteration: 2, Content: "turn3 继续"}},
		4: {{Iteration: 1, Content: "turn4 迭代"}},
	}

	out := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	for _, h := range out {
		if strings.HasPrefix(strings.TrimSpace(h.Content), "[Compacted context]") {
			if h.TurnID != 0 {
				t.Fatalf("结构化路径：压缩标记不得绑 turn（会顶掉用户消息），got turnID=%d: %+v", h.TurnID, h)
			}
			if !h.Standalone || h.AnchorTurnID != 3 {
				t.Fatalf("结构化路径：压缩标记必须 standalone + anchor=3，got %+v", h)
			}
			return
		}
	}
	t.Fatalf("压缩标记未出现在结构化历史里: %+v", out)
}
