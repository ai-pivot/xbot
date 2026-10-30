package web

// web_inbox_resolve_test.go — POST /api/inbox/resolve 的 handler 契约
//（状态码映射与回调接线）：真正的状态机/CAS 由 storage + serverapp 覆盖。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

func TestInboxResolveDispatchesCallback(t *testing.T) {
	wc := newStructuredTestChannel(t)
	var got protocol.StructuredResolveInput
	wc.callbacks.StructuredResolve = func(senderID string, sel SessionSelector, input protocol.StructuredResolveInput) (*protocol.StructuredResolveResult, error) {
		got = input
		if senderID != "web-1" || sel.Channel != "web" || sel.ChatID != "web-1" {
			t.Fatalf("unexpected caller: sender=%s sel=%+v", senderID, sel)
		}
		return &protocol.StructuredResolveResult{
			MessageID: input.MessageID, ResolutionID: 9, Status: "approved", Origin: "web:web-1/a:main",
		}, nil
	}

	rec := httptest.NewRecorder()
	wc.handleInboxResolve(rec, authedAPIRequest(http.MethodPost, "/api/inbox/resolve",
		[]byte(`{"message_id":12,"action":"approve","comment":"准"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got.MessageID != 12 || got.Action != "approve" || got.Comment != "准" {
		t.Fatalf("callback input = %+v", got)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Result protocol.StructuredResolveResult `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK || env.Data.Result.Status != "approved" || env.Data.Result.ResolutionID != 9 {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestInboxResolveRequiresMessageID(t *testing.T) {
	wc := newStructuredTestChannel(t)
	rec := httptest.NewRecorder()
	wc.handleInboxResolve(rec, authedAPIRequest(http.MethodPost, "/api/inbox/resolve", []byte(`{"action":"approve"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestInboxResolveConflictMapsTo409(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredResolve = func(string, SessionSelector, protocol.StructuredResolveInput) (*protocol.StructuredResolveResult, error) {
		return nil, sqlite.ErrAskUserAnswered
	}
	rec := httptest.NewRecorder()
	wc.handleInboxResolve(rec, authedAPIRequest(http.MethodPost, "/api/inbox/resolve",
		[]byte(`{"message_id":12,"action":"approve"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestInboxResolveNotFoundMapsTo404(t *testing.T) {
	wc := newStructuredTestChannel(t)
	wc.callbacks.StructuredResolve = func(string, SessionSelector, protocol.StructuredResolveInput) (*protocol.StructuredResolveResult, error) {
		return nil, sqlite.ErrStructuredNotFound
	}
	rec := httptest.NewRecorder()
	wc.handleInboxResolve(rec, authedAPIRequest(http.MethodPost, "/api/inbox/resolve",
		[]byte(`{"message_id":404,"action":"dismiss"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestInboxResolveMethodNotAllowed(t *testing.T) {
	wc := newStructuredTestChannel(t)
	rec := httptest.NewRecorder()
	wc.handleInboxResolve(rec, authedAPIRequest(http.MethodGet, "/api/inbox/resolve", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
