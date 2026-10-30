package serverapp

import (
	"testing"

	"github.com/google/uuid"

	"xbot/channel/web"
	"xbot/protocol"
)

// TestAskUserCorrelationId* 是 D-07 的实质判据（不启服务、不走 HTTP）：
// 服务端创建 ask_user 时必须自己发「回答钥匙」correlation_id，否则
// /api/answer 对空值一律 400，提问在结构上不可回答。
//
// 反证（删掉 ensureAskCorrelationID 的生成分支后本组必红）：
//   - TestAskUserCorrelationIdGeneratedWhenMissing 会拿到空串；
//   - TestAskUserCorrelationIdEnvelopeMatchesStored 拿不到统一值。

func askUserInput(corr string) protocol.StructuredMessageInput {
	return protocol.StructuredMessageInput{
		Kind:          "ask_user",
		CorrelationID: corr,
		Payload: map[string]any{
			"questions": []any{map[string]any{"id": "q1", "question": "选个颜色", "type": "single"}},
		},
	}
}

func TestAskUserCorrelationIdGeneratedWhenMissing(t *testing.T) {
	in := ensureAskCorrelationID(askUserInput(""))
	if in.CorrelationID == "" {
		t.Fatal("ask_user 缺 correlation_id 时服务端未生成（/api/answer 将一律 400，提问不可回答）")
	}
	if len(in.CorrelationID) < 5 || in.CorrelationID[:4] != "ask-" {
		t.Fatalf("correlation_id 形状应为 ask-<uuid>，实际 %q", in.CorrelationID)
	}
	if _, err := uuid.Parse(in.CorrelationID[4:]); err != nil {
		t.Fatalf("correlation_id 的 uuid 部分不可解析：%q: %v", in.CorrelationID, err)
	}
}

func TestAskUserCorrelationIdPreservedWhenProvided(t *testing.T) {
	const given = "ask-client-supplied-1"
	in := ensureAskCorrelationID(askUserInput(given))
	if in.CorrelationID != given {
		t.Fatalf("客户端显式给的 correlation_id 必须原样保留（幂等重投/导入依赖它），得到 %q", in.CorrelationID)
	}
}

func TestAskUserCorrelationIdUniquePerAsk(t *testing.T) {
	a := ensureAskCorrelationID(askUserInput(""))
	b := ensureAskCorrelationID(askUserInput(""))
	if a.CorrelationID == b.CorrelationID {
		t.Fatalf("两次创建拿到同一个 correlation_id %q —— 回答会串到别的提问上", a.CorrelationID)
	}
}

func TestAskUserCorrelationIdNotAppliedToOtherKinds(t *testing.T) {
	for _, kind := range []string{"report", "decision", "text", "command"} {
		in := ensureAskCorrelationID(protocol.StructuredMessageInput{Kind: kind})
		if in.CorrelationID != "" {
			t.Fatalf("kind=%s 不应被补发 correlation_id，得到 %q", kind, in.CorrelationID)
		}
	}
}

// TestAskUserCorrelationIdEnvelopeMatchesStored 锁住「envelope 与 message 同值」：
// 两者都从同一个 input 派生，前端读 message.correlation_id、/api/answer 用
// envelope 里的值，任何一侧漂移都会让回答 400/404。
func TestAskUserCorrelationIdEnvelopeMatchesStored(t *testing.T) {
	in := ensureAskCorrelationID(askUserInput(""))
	sel := web.SessionSelector{Channel: "web", ChatID: "web-1"}
	env := buildStructuredEnvelope("web-1", sel, in)

	envCorr, _ := env["correlation_id"].(string)
	if envCorr == "" {
		t.Fatal("envelope.correlation_id 为空（前端拿到的提问没有回答钥匙）")
	}
	if envCorr != in.CorrelationID {
		t.Fatalf("envelope.correlation_id=%q 与落库用的 correlation_id=%q 不一致", envCorr, in.CorrelationID)
	}
	if structuredContent(in.Kind, in.Payload, in.Text) == "" {
		t.Fatal("ask_user 的正文渲染为空（用户看不到问的是什么）")
	}
}

// TestAskUserCorrelationIdThroughAppendFn 是 D-07 的**入口级**判据：走真实
// appendFn（/api/message 与 SendMessage 工具共用的落库入口），不是直接调
// ensureAskCorrelationID —— 只有从这里驱动，删掉 appendFn 里那行调用才会红。
func TestAskUserCorrelationIdThroughAppendFn(t *testing.T) {
	ag := newTestAgentForCWD(t)
	appendFn, _, _, _, _, _ := structuredCallbacks(ag)

	sel := web.SessionSelector{Channel: "web", ChatID: "chat-entry"}
	msg, _, err := appendFn("web-1", sel, protocol.StructuredMessageInput{
		Kind: "ask_user",
		Payload: map[string]any{
			"questions": []any{map[string]any{"id": "q1", "question": "选个颜色", "type": "single"}},
		},
	})
	if err != nil {
		t.Fatalf("appendFn 落库失败：%v", err)
	}
	if msg == nil {
		t.Fatal("appendFn 返回 nil 消息")
	}
	if msg.CorrelationID == "" {
		t.Fatal("appendFn 落库的 ask_user 没有 correlation_id：入口侧没发钥匙，/api/answer 必然 400")
	}
	if envCorr, _ := msg.Envelope["correlation_id"].(string); envCorr != msg.CorrelationID {
		t.Fatalf("envelope.correlation_id=%q 与 message.correlation_id=%q 不一致", envCorr, msg.CorrelationID)
	}
}
