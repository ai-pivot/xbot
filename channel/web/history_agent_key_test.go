package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ch "xbot/channel"
)

// A sub-agent history key that does not exist must be a LOUD 404, never a silent
// 200 with zero messages (the frontend then renders "this session is empty").
// Before the fix, isUnknownSession short-circuited on `strings.Contains(chatID, ":")`
// — every agent key contains ':' — so ANY typo'd/prefixed key returned 200/empty.
func TestAgentHistoryUnknownKeyIsNotFound(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channelName, chatID string) bool {
		return channelName == "agent" && chatID == "web:chat_1/taizi:inst-1"
	}
	wc.callbacks.HistorySnapshot = func(senderID string, sel SessionSelector, limit int, beforeID int64) (HistorySnapshot, error) {
		return HistorySnapshot{Messages: []ch.HistoryMessage{{Role: "assistant", Content: "x"}}}, nil
	}
	server := httptest.NewServer(wc.newServeMux())
	t.Cleanup(server.Close)
	cookie := loginTestAdmin(t, server.URL)

	status, body := postHistory(t, server.URL, cookie, "web:chat_1/taizi:typo-9")
	if status != http.StatusNotFound {
		t.Fatalf("unknown agent key status = %d (body=%s), want 404 — an empty 200 masks real errors", status, body)
	}
}

// The exact map key works, and the legacy `agent:`-prefixed form must resolve to
// the SAME session instead of returning an empty history.
func TestAgentHistoryAcceptsPrefixedKey(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	const key = "web:chat_1/taizi:inst-1"
	wc.callbacks.SessionExists = func(channelName, chatID string) bool {
		return channelName == "agent" && chatID == key
	}
	var seen []string
	wc.callbacks.HistorySnapshot = func(senderID string, sel SessionSelector, limit int, beforeID int64) (HistorySnapshot, error) {
		seen = append(seen, sel.Channel+"|"+sel.ChatID)
		return HistorySnapshot{Messages: []ch.HistoryMessage{{Role: "assistant", Content: "reply"}}}, nil
	}
	server := httptest.NewServer(wc.newServeMux())
	t.Cleanup(server.Close)
	cookie := loginTestAdmin(t, server.URL)

	status, body := postHistory(t, server.URL, cookie, key)
	if status != http.StatusOK || !strings.Contains(body, `"reply"`) {
		t.Fatalf("exact key status=%d body=%s, want 200 with messages", status, body)
	}
	status, body = postHistory(t, server.URL, cookie, "agent:"+key)
	if status != http.StatusOK || !strings.Contains(body, `"reply"`) {
		t.Fatalf("prefixed key status=%d body=%s, want 200 with the same messages", status, body)
	}
	for _, s := range seen {
		if s != "agent|"+key {
			t.Fatalf("history resolved to %q, want agent|%s (prefixed form must canonicalize)", s, key)
		}
	}
}

// Nested sub-agents may legitimately own a map key that itself starts with
// "agent:" — exact match must win over prefix stripping.
func TestAgentHistoryPrefersExactNestedKey(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	const nested = "agent:web:chat_1/parent:p/child:c"
	wc.callbacks.SessionExists = func(channelName, chatID string) bool {
		return channelName == "agent" && (chatID == nested || chatID == "web:chat_1/parent:p/child:c")
	}
	var got string
	wc.callbacks.HistorySnapshot = func(senderID string, sel SessionSelector, limit int, beforeID int64) (HistorySnapshot, error) {
		got = sel.ChatID
		return HistorySnapshot{}, nil
	}
	server := httptest.NewServer(wc.newServeMux())
	t.Cleanup(server.Close)
	cookie := loginTestAdmin(t, server.URL)

	if status, body := postHistory(t, server.URL, cookie, nested); status != http.StatusOK {
		t.Fatalf("nested key status=%d body=%s", status, body)
	}
	if got != nested {
		t.Fatalf("resolved %q, want the exact nested key %q", got, nested)
	}
}

func postHistory(t *testing.T, baseURL string, cookie *http.Cookie, chatID string) (int, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"channel": "agent", "chat_id": chatID, "limit": 30})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/history", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}
