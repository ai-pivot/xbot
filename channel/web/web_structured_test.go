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

// P1：批红失败必须映射为 409/404，而不是 200。
func TestDecisionRepeatVerdictMapsTo409(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredAppend = func(string, SessionSelector, protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
		return nil, false, sqlite.ErrAskUserAnswered
	}
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message",
		[]byte(`{"kind":"decision","reply_to":"6","idempotency_key":"k2","payload":{"verdict":"approve"}}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already answered") {
		t.Fatalf("body = %s, want already answered", rec.Body.String())
	}
}

func TestDecisionMissingTargetMapsTo404(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredAppend = func(string, SessionSelector, protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
		return nil, false, sqlite.ErrStructuredNotFound
	}
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message",
		[]byte(`{"kind":"decision","reply_to":"9999","payload":{"verdict":"approve"}}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestDecisionWithoutTargetRejected400(t *testing.T) {
	wc := newStructuredTestChannel(t)
	called := false
	wc.callbacks.StructuredAppend = func(string, SessionSelector, protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
		called = true
		return &protocol.StructuredMessage{MessageID: 1}, false, nil
	}
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message",
		[]byte(`{"kind":"decision","payload":{"verdict":"approve"}}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("decision without reply_to must not reach storage")
	}
}

// P2：kind=answer 走 /api/message 时必须用 reply_to 寻址，并复用同一套 409/404 映射。
func TestMessageKindAnswerRequiresReplyTo(t *testing.T) {
	wc := newStructuredTestChannel(t)
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message",
		[]byte(`{"kind":"answer","payload":{"answers":[{"question_id":"q1","option_ids":["a"]}]}}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "reply_to") {
		t.Fatalf("body = %s, want hint about reply_to / /api/answer", rec.Body.String())
	}
}

func TestMessageKindAnswerRepeatMapsTo409(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredAppend = func(string, SessionSelector, protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
		return nil, false, sqlite.ErrAskUserAnswered
	}
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message",
		[]byte(`{"kind":"answer","reply_to":"1","idempotency_key":"a2","payload":{"answers":[{"question_id":"q1","option_ids":["a"]}]}}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestMessageKindAnswerMissingTargetMapsTo404(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredAppend = func(string, SessionSelector, protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
		return nil, false, sqlite.ErrStructuredNotFound
	}
	rec := httptest.NewRecorder()
	wc.handleMessage(rec, authedAPIRequest(http.MethodPost, "/api/message",
		[]byte(`{"kind":"answer","reply_to":"404","payload":{"answers":[{"question_id":"q1","option_ids":["a"]}]}}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
