package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xbot/bus"
	"xbot/protocol"
)

func authedAPIRequest(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	ctx := contextWithSenderID(contextWithUserID(req.Context(), 1), "web-1")
	return req.WithContext(ctx)
}

func decodeAPIResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func setTestCurrentSession(wc *WebChannel, sel SessionSelector) {
	wc.userCurrentSessionMu.Lock()
	defer wc.userCurrentSessionMu.Unlock()
	wc.userCurrentSession["web-1"] = sel
}

func TestHistoryEndpointReturnsWebSnapshot(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "cli", ChatID: "cli-chat"})
	wc.SetCallbacks(WebCallbacks{
		HistorySnapshot: func(senderID string, sel SessionSelector) (HistorySnapshot, error) {
			if senderID != "web-1" {
				t.Fatalf("unexpected senderID %q", senderID)
			}
			if sel.Channel != "cli" || sel.ChatID != "cli-chat" {
				t.Fatalf("unexpected selector %#v", sel)
			}
			return HistorySnapshot{
				Messages: []protocol.HistoryMessage{{
					Role:      "user",
					Content:   "hello",
					Timestamp: time.Unix(1, 0),
				}},
				Processing: true,
				ActiveProgress: &protocol.ProgressEvent{
					Phase: "thinking",
				},
			}, nil
		},
	})

	rec := httptest.NewRecorder()
	wc.handleHistory(rec, authedAPIRequest(http.MethodGet, "/api/history", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeAPIResponse(t, rec)
	if out["ok"] != true || out["processing"] != true {
		t.Fatalf("unexpected history response: %#v", out)
	}
	if out["chat_id"] != "cli-chat" || out["channel"] != "cli" {
		t.Fatalf("history must echo resolved session, got %#v", out)
	}
	msgs, ok := out["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected one history message, got %#v", out["messages"])
	}
}

func TestWebOnlyRestEndpointsUseCallbacks(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "chat-a"})
	var setCWDDir string
	wc.SetCallbacks(WebCallbacks{
		GetCWD: func(senderID string, sel SessionSelector) (string, error) {
			if sel.ChatID != "chat-a" {
				t.Fatalf("unexpected cwd selector %#v", sel)
			}
			return "/repo", nil
		},
		SetCWD: func(senderID string, sel SessionSelector, dir string) error {
			if sel.ChatID != "chat-a" {
				t.Fatalf("unexpected set cwd selector %#v", sel)
			}
			setCWDDir = dir
			return nil
		},
		CronTasks: func(senderID string, sel SessionSelector) (any, error) {
			return []map[string]any{{"id": "cron-1"}}, nil
		},
		BackgroundTasks: func(senderID string, sel SessionSelector) (any, error) {
			return []map[string]any{{"id": "bg-1"}}, nil
		},
		CommandList: func(senderID string) ([]CommandInfo, error) {
			return []CommandInfo{{Name: "help", Description: "show help"}}, nil
		},
		RewindHistory: func(senderID string, sel SessionSelector, cutoff time.Time) (RewindHistoryResult, error) {
			if cutoff.UnixMilli() != 1700000000000 {
				t.Fatalf("unexpected cutoff %v", cutoff)
			}
			return RewindHistoryResult{
				Draft: "redo",
				RewindResult: &protocol.RewindResult{
					Restored: []string{"a"},
				},
			}, nil
		},
	})

	rec := httptest.NewRecorder()
	wc.handleCWD(rec, authedAPIRequest(http.MethodGet, "/api/cwd", nil))
	out := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || out["dir"] != "/repo" {
		t.Fatalf("unexpected cwd response: %d %#v", rec.Code, out)
	}

	rec = httptest.NewRecorder()
	wc.handleCWD(rec, authedAPIRequest(http.MethodPut, "/api/cwd", []byte(`{"dir":"/next"}`)))
	out = decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || setCWDDir != "/next" || out["dir"] != "/next" {
		t.Fatalf("unexpected set cwd response: %d %#v dir=%q", rec.Code, out, setCWDDir)
	}

	rec = httptest.NewRecorder()
	wc.handleTasks(rec, authedAPIRequest(http.MethodGet, "/api/tasks", nil))
	out = decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || len(out["tasks"].([]any)) != 1 {
		t.Fatalf("unexpected tasks response: %d %#v", rec.Code, out)
	}

	rec = httptest.NewRecorder()
	wc.handleBackgroundTasks(rec, authedAPIRequest(http.MethodGet, "/api/background-tasks", nil))
	out = decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || len(out["tasks"].([]any)) != 1 {
		t.Fatalf("unexpected background tasks response: %d %#v", rec.Code, out)
	}

	rec = httptest.NewRecorder()
	wc.handleCommands(rec, authedAPIRequest(http.MethodGet, "/api/commands", nil))
	out = decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || len(out["commands"].([]any)) != 1 {
		t.Fatalf("unexpected commands response: %d %#v", rec.Code, out)
	}

	rec = httptest.NewRecorder()
	wc.handleHistoryRewind(rec, authedAPIRequest(http.MethodPost, "/api/history/rewind", []byte(`{"cutoff_ms":1700000000000}`)))
	out = decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || out["draft"] != "redo" {
		t.Fatalf("unexpected rewind response: %d %#v", rec.Code, out)
	}
}

func TestWebOnlyRestEndpointsRejectWrongMethods(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	tests := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		target  string
	}{
		{"history", wc.handleHistory, http.MethodPost, "/api/history"},
		{"tasks", wc.handleTasks, http.MethodPost, "/api/tasks"},
		{"background", wc.handleBackgroundTasks, http.MethodPost, "/api/background-tasks"},
		{"commands", wc.handleCommands, http.MethodPost, "/api/commands"},
		{"rewind", wc.handleHistoryRewind, http.MethodGet, "/api/history/rewind"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, authedAPIRequest(tc.method, tc.target, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCanAccessAgentSessionUsesTenantAndWebParentOwnership(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "agent", "web:web-2/review:1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "agent", "cli:/repo:Agent-main/review:1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	if !wc.canAccessSession(contextWithUserID(context.Background(), 2), 2, "web-2", "agent", "web:web-2/review:1") {
		t.Fatal("web user should access SubAgent under their default web session")
	}
	if wc.canAccessSession(contextWithUserID(context.Background(), 3), 3, "web-3", "agent", "web:web-2/review:1") {
		t.Fatal("different web user must not access another user's SubAgent")
	}
	if !wc.canAccessSession(contextWithUserID(context.Background(), 1), 1, "web-1", "agent", "cli:/repo:Agent-main/review:1") {
		t.Fatal("admin web user should access existing cli-backed SubAgent")
	}
	if wc.canAccessSession(contextWithUserID(context.Background(), 1), 1, "web-1", "agent", "cli:/repo:Agent-main/missing:1") {
		t.Fatal("admin access still requires an existing agent tenant")
	}
}
