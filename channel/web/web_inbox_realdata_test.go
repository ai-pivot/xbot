package web

// web_inbox_realdata_test.go — /api/inbox 对**真实 sqlite 数据**的端到端读数。
//
// 判据 c（派发单 G-20261029-N1）：卡片落到用户会话后，收件箱接口必须能查到该条，
// 且 unanswered=1。这里不打算桩：写入走真实的 sqlite.SessionService（与生产
// 同一张 session_messages 表、同一套 ListInbox/CountInbox），读出走真实的
// handleInbox（httptest 记录请求/响应）。
//
// 跑法（把证据写到指定目录，不设则只打印）：
//   N1_EVIDENCE_DIR=/path/to/evidence go test ./channel/web/ -run InboxRealData -v

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

func toProtocolStructuredForTest(m sqlite.StructuredMessage) protocol.StructuredMessage {
	payload := map[string]any{}
	envelope := map[string]any{}
	if m.StructuredJSON != "" {
		_ = json.Unmarshal([]byte(m.StructuredJSON), &envelope)
		if p, ok := envelope["payload"].(map[string]any); ok {
			payload = p
		}
	}
	return protocol.StructuredMessage{
		MessageID: m.ID,
		Kind:      m.Kind,
		Answered:  m.Answered,
		Cancelled: m.Cancelled,
		Text:      m.Content,
		Payload:   payload,
		Sender:    protocol.SenderInfo{Type: m.SenderType, ID: m.SenderID, Name: m.SenderName},
		CreatedAt: m.CreatedAt,
		Envelope:  envelope,
	}
}

func writeEvidence(t *testing.T, name, content string) {
	t.Helper()
	dir := os.Getenv("N1_EVIDENCE_DIR")
	if dir == "" {
		t.Logf("%s:\n%s", name, content)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir evidence: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write evidence %s: %v", name, err)
	}
}

func TestInboxRealDataReturnsCardAndCount(t *testing.T) {
	db := newTestDB(t)
	svc := sqlite.NewSessionService(db)

	// 用户看板卡片：kind=report，pending（answered=0/cancelled=0）。
	cardID, created, err := svc.AppendStructuredMessage(0, sqlite.StructuredMessage{
		ChatID:         "web-1",
		Kind:           sqlite.StructuredKindReport,
		Content:        "N1 后端闭环：卡片已入库",
		Direction:      "in",
		SenderType:     "agent",
		SenderID:       "agent:main/court.shoufu",
		SenderName:     "agent:main/court.shoufu",
		StructuredJSON: `{"schema":"xbot.message/v1","kind":"report","payload":{"summary":"一句话结论"},"origin":{"session":"web:web-1/court.shoufu:main","agent":"agent:main/court.shoufu"}}`,
		IdempotencyKey: "n1-http-1",
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

	req := authedAPIRequest(http.MethodGet, "/api/inbox", nil)
	rec := httptest.NewRecorder()
	wc.handleInbox(rec, req)

	body := rec.Body.String()
	evidence := "REQUEST: GET /api/inbox (sender=web-1, current session=web:web-1)\n" +
		"STATUS: " + http.StatusText(rec.Code) + " (" + itoa(rec.Code) + ")\n" +
		"BODY: " + body + "\n"
	writeEvidence(t, "inbox.log", evidence)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, body)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Items      []protocol.StructuredMessage `json:"items"`
			Unanswered int                          `json:"unanswered"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if !env.OK {
		t.Fatalf("ok=false: %s", body)
	}
	resp := env.Data
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d, want 1: %s", len(resp.Items), body)
	}
	if resp.Unanswered != 1 {
		t.Fatalf("unanswered = %d, want 1: %s", resp.Unanswered, body)
	}
	got := resp.Items[0]
	if got.MessageID != cardID || got.Kind != "report" {
		t.Fatalf("item = %+v, want id=%d kind=report", got, cardID)
	}
	if origin, _ := got.Envelope["origin"].(map[string]any); origin == nil {
		t.Fatalf("card must expose its origin to the board: %s", body)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
