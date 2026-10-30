package web

// web_inbox_unanswered_test.go — N1 断言②：`/api/inbox` 必须能在 approve 前后
// 给出**正确的未处理计数**（不是"接口返回 200 就算过"）。
//
// 判据：
//   a) approve 前：GET /api/inbox 的 data.unanswered >= 1，且卡片在 items 里
//      （answered=false）；
//   b) approve 后：同一个接口 data.unanswered == 0，items 为空；
//   c) 存储侧同步收敛：卡片行 answered=1。
//
// 这里不桩存储：写入/推进都走真实 sqlite.SessionService（与生产同一张
// session_messages），读出走真实 handleInbox / handleInboxResolve（httptest）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

func TestInboxUnansweredHttpBeforeAndAfterApprove(t *testing.T) {
	db := newTestDB(t)
	svc := sqlite.NewSessionService(db)

	const cardText = "N1 断言②：approve 前后 unanswered 必须收敛"
	cardID, created, err := svc.AppendStructuredMessage(0, sqlite.StructuredMessage{
		ChatID:         "web-1",
		RecordType:     sqlite.HistoryRecordNotice,
		Kind:           sqlite.StructuredKindReport,
		Content:        cardText,
		Direction:      "in",
		SenderType:     "agent",
		SenderID:       "agent:main/court.shoufu",
		SenderName:     "agent:main/court.shoufu",
		StructuredJSON: `{"schema":"xbot.message/v1","kind":"report","payload":{"summary":"待批"},"origin":{"session":"web:web-1/court.shoufu:main","agent":"agent:main/court.shoufu"},"silent":true}`,
		IdempotencyKey: "n1-http-unanswered-1",
		CreatedAt:      time.Now(),
	})
	if err != nil {
		t.Fatalf("AppendStructuredMessage: %v", err)
	}
	if !created {
		t.Fatal("first insert reported duplicate")
	}

	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredInbox = func(_ SessionSelector, cursor int64, limit int) ([]protocol.StructuredMessage, bool, int64, error) {
		rows, more, next, err := svc.ListInbox(0, cursor, limit)
		if err != nil {
			return nil, false, 0, err
		}
		out := make([]protocol.StructuredMessage, 0, len(rows))
		for _, r := range rows {
			out = append(out, toProtocolStructuredForTest(r))
		}
		return out, more, next, nil
	}
	wc.callbacks.StructuredCount = func(SessionSelector) (int, error) { return svc.CountInbox(0) }
	wc.callbacks.StructuredResolve = func(senderID string, _ SessionSelector, input protocol.StructuredResolveInput) (*protocol.StructuredResolveResult, error) {
		status, ok := sqlite.NoticeStatusOfAction(input.Action)
		if !ok {
			t.Fatalf("unexpected action %q", input.Action)
		}
		idem := input.IdempotencyKey
		if idem == "" {
			idem = "notice:" + strconv.FormatInt(input.MessageID, 10) + ":" + input.Action
		}
		res, err := svc.ResolveNotice(0, "web-1", input.MessageID, input.Action,
			"用户处理了卡片", "{}", idem, senderID, senderID)
		if err != nil {
			return nil, err
		}
		out := &protocol.StructuredResolveResult{
			MessageID: input.MessageID, ResolutionID: res.WrittenID,
			Status: status, Duplicate: !res.Created,
		}
		if res.Target != nil {
			if env := map[string]any{}; res.Target.StructuredJSON != "" {
				_ = json.Unmarshal([]byte(res.Target.StructuredJSON), &env)
				if o, ok := env["origin"].(map[string]any); ok {
					out.Origin, _ = o["session"].(string)
				}
			}
		}
		return out, nil
	}

	readInbox := func(stage string) (int, []protocol.StructuredMessage) {
		t.Helper()
		req := authedAPIRequest(http.MethodGet, "/api/inbox", nil)
		rec := httptest.NewRecorder()
		wc.handleInbox(rec, req)
		body := rec.Body.String()
		writeEvidence(t, "inbox-unanswered-"+stage+".log",
			"REQUEST: GET /api/inbox\nSTATUS: "+itoa(rec.Code)+"\nBODY: "+body+"\n")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", stage, rec.Code, body)
		}
		var env struct {
			OK   bool `json:"ok"`
			Data struct {
				Items      []protocol.StructuredMessage `json:"items"`
				Unanswered int                          `json:"unanswered"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: decode: %v (%s)", stage, err, body)
		}
		if !env.OK {
			t.Fatalf("%s: ok=false: %s", stage, body)
		}
		return env.Data.Unanswered, env.Data.Items
	}

	// ── a) approve 前 ────────────────────────────────────────────────────────
	before, items := readInbox("before")
	if before < 1 {
		t.Fatalf("approve 前 answered unanswered = %d, want >= 1（卡片必须可查）", before)
	}
	if len(items) != 1 || items[0].MessageID != cardID {
		t.Fatalf("approve 前 items = %+v, want exactly the card #%d", items, cardID)
	}
	if items[0].Answered || items[0].Cancelled {
		t.Fatalf("approve 前卡片必须 pending: %+v", items[0])
	}

	// ── approve（真实 handler + 真实 CAS）───────────────────────────────────
	rec := httptest.NewRecorder()
	wc.handleInboxResolve(rec, authedAPIRequest(http.MethodPost, "/api/inbox/resolve",
		[]byte(`{"message_id":`+strconv.FormatInt(cardID, 10)+`,"action":"approve","comment":"准"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", rec.Code, rec.Body.String())
	}

	// ── b) approve 后 ────────────────────────────────────────────────────────
	after, remaining := readInbox("after")
	if after != 0 {
		t.Fatalf("approve 后 unanswered = %d, want 0", after)
	}
	if len(remaining) != 0 {
		t.Fatalf("approve 后 items = %d, want 0: %+v", len(remaining), remaining)
	}

	// ── c) 存储侧收敛 ────────────────────────────────────────────────────────
	row, err := svc.GetStructuredMessageByID(0, cardID)
	if err != nil {
		t.Fatalf("GetStructuredMessageByID: %v", err)
	}
	if !row.Answered || row.Cancelled {
		t.Fatalf("卡片行 answered=%v cancelled=%v, want answered:true cancelled:false", row.Answered, row.Cancelled)
	}
}
