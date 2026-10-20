package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

func newStructuredTestChannel(t *testing.T) *WebChannel {
	t.Helper()
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	return wc
}

func TestStructuredMessageRejectsInvalidKind(t *testing.T) {
	wc := newStructuredTestChannel(t)
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message", []byte(`{"kind":"bogus","payload":{}}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestStructuredMessageRejectsOversizePayload(t *testing.T) {
	wc := newStructuredTestChannel(t)
	payload := map[string]any{"text": strings.Repeat("a", 300*1024)}
	raw, _ := json.Marshal(map[string]any{"kind": "text", "payload": payload})
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message", raw))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "256KB") {
		t.Fatalf("body = %s, want 256KB message", rec.Body.String())
	}
}

func TestStructuredMessageDispatchesCallbackAndBroadcasts(t *testing.T) {
	wc := newStructuredTestChannel(t)
	called := false
	wc.callbacks.StructuredAppend = func(senderID string, sel SessionSelector, input protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
		called = true
		if input.Kind != "ask_user" || sel.ChatID != "web-1" || senderID != "web-1" {
			t.Fatalf("unexpected callback args: sender=%s sel=%+v kind=%s", senderID, sel, input.Kind)
		}
		return &protocol.StructuredMessage{MessageID: 42, Kind: input.Kind, Direction: "in"}, false, nil
	}
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message",
		[]byte(`{"kind":"ask_user","payload":{"questions":[{"id":"q1","question":"?"}]},"idempotency_key":"k1"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("StructuredAppend callback not invoked")
	}
	if !strings.Contains(rec.Body.String(), `"message_id":42`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestStructuredAnswerRequiresCorrelation(t *testing.T) {
	wc := newStructuredTestChannel(t)
	rec := httptest.NewRecorder()
	wc.handleStructuredAnswer(rec, authedAPIRequest(http.MethodPost, "/api/answer", []byte(`{"answers":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestStructuredAnswerConflictMapsTo409(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredAnswer = func(string, SessionSelector, protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error) {
		return nil, false, sqlite.ErrAskUserAnswered
	}
	rec := httptest.NewRecorder()
	wc.handleStructuredAnswer(rec, authedAPIRequest(http.MethodPost, "/api/answer",
		[]byte(`{"correlation_id":"cor_1","answers":[]}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestInboxUnknownSession404(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.SessionExists = func(channel, chatID string) bool { return false }
	rec := httptest.NewRecorder()
	wc.handleInbox(rec, authedAPIRequest(http.MethodGet, "/api/inbox?chat_id=chat_deadbeef", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if wc.callbacks.StructuredInbox != nil {
		t.Fatal("inbox callback must not be reached for unknown session")
	}
}

func TestInboxReturnsItemsAndCount(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredInbox = func(sel SessionSelector, cursor int64, limit int) ([]protocol.StructuredMessage, bool, int64, error) {
		return []protocol.StructuredMessage{{MessageID: 7, Kind: "ask_user", Text: "?"}}, false, 7, nil
	}
	wc.callbacks.StructuredCount = func(SessionSelector) (int, error) { return 3, nil }
	rec := httptest.NewRecorder()
	wc.handleInbox(rec, authedAPIRequest(http.MethodGet, "/api/inbox", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items      []protocol.StructuredMessage `json:"items"`
		Unanswered int                          `json:"unanswered"`
	}
	decodeAPIData(t, rec.Body, &body)
	if len(body.Items) != 1 || body.Unanswered != 3 {
		t.Fatalf("body = %+v", body)
	}
}
