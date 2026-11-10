package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xbot/protocol"
)

// stateTestChannel builds a WebChannel with the state callbacks wired, sharing
// the same frame path (per-chat seq + ring) as production.
func stateTestChannel() *WebChannel {
	wc := newSeqTestChannel()
	wc.userCurrentSession = make(map[string]SessionSelector)
	return wc
}

// serveStateRequest executes one request against the real handlers.
func serveStateRequest(t *testing.T, wc *WebChannel, target string) *httptest.ResponseRecorder {
	t.Helper()
	if wc.sessions == nil {
		wc.sessions = make(map[string]sessionInfo)
		wc.sessions["__state_test__"] = sessionInfo{
			userID:   1,
			username: "qa",
			expires:  time.Now().Add(time.Hour),
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/iteration", wc.authMiddleware(wc.handleStateIteration))
	mux.HandleFunc("/api/turn", wc.authMiddleware(wc.handleStateTurn))
	mux.HandleFunc("/api/session-state", wc.authMiddleware(wc.handleSessionState))
	rec := httptest.NewRecorder()
	req := authedAPIRequest(http.MethodGet, target, nil)
	req.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: "__state_test__"})
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

// TestStateIterationEndpoint — G-3 helper: an existing iteration returns its
// authoritative block; a non-existent one is answered with the explicit
// `absent` marker (the only legal answer for a skipped iteration number — the
// client uses it to confirm a legal jump instead of guessing).
func TestStateIterationEndpoint(t *testing.T) {
	wc := stateTestChannel()
	wc.callbacks.IterationBlock = func(channelName, chatID string, turnID uint64, iteration int) (*protocol.IterationBlock, error) {
		if channelName != "web" || chatID != "chat-a" || turnID != 7 || iteration != 2 {
			return nil, nil
		}
		return &protocol.IterationBlock{
			TurnID: 7, Iteration: 2, Content: "done",
			Tools: []protocol.ToolProgress{{Name: "Shell", CallID: "call-7-2-0", Iteration: 2, Status: "done"}},
		}, nil
	}

	rec := serveStateRequest(t, wc, "/api/iteration?chat=chat-a&turn=7&iter=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("existing iteration: status %d body %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ok"] != true {
		t.Fatalf("ok = %v", body["ok"])
	}
	block, _ := body["block"].(map[string]any)
	if block == nil || block["iteration"].(float64) != 2 || block["content"] != "done" {
		t.Fatalf("unexpected block: %v", body["block"])
	}

	rec = serveStateRequest(t, wc, "/api/iteration?chat=chat-a&turn=7&iter=9")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("absent iteration: status %d body %s", rec.Code, rec.Body.String())
	}
	body = decodeBody(t, rec)
	if body["ok"] != false || body["absent"] != true {
		t.Fatalf("absent must be explicit ({ok:false, absent:true}), got %v", body)
	}

	// Missing/invalid parameters are client errors, never "absent".
	for _, target := range []string{
		"/api/iteration?chat=chat-a&turn=7",
		"/api/iteration?chat=chat-a&turn=7&iter=0",
		"/api/iteration?chat=chat-a&turn=x&iter=1",
	} {
		rec = serveStateRequest(t, wc, target)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", target, rec.Code)
		}
	}
}

// TestStateIterationEndpointSurfacesError — a backend read failure must be a
// 500, not a silent "absent" (that would make the client believe a committed
// iteration never existed).
func TestStateIterationEndpointSurfacesError(t *testing.T) {
	wc := stateTestChannel()
	wc.callbacks.IterationBlock = func(string, string, uint64, int) (*protocol.IterationBlock, error) {
		return nil, errors.New("db down")
	}
	rec := serveStateRequest(t, wc, "/api/iteration?chat=chat-a&turn=1&iter=1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestStateTurnEndpoint — pullTurn returns the turn's blocks and its closing
// record; an unknown turn is `absent`.
func TestStateTurnEndpoint(t *testing.T) {
	wc := stateTestChannel()
	wc.callbacks.TurnSnapshot = func(channelName, chatID string, turnID uint64) (*protocol.TurnSnapshot, error) {
		if turnID != 4 {
			return nil, nil
		}
		return &protocol.TurnSnapshot{
			TurnID: 4,
			Blocks: []protocol.IterationBlock{
				{TurnID: 4, Iteration: 1, Content: "a", Tools: []protocol.ToolProgress{}},
				{TurnID: 4, Iteration: 2, Content: "b", Tools: []protocol.ToolProgress{}},
			},
			Final: &protocol.TurnFinalFrame{TurnID: 4, Content: "b"},
		}, nil
	}

	rec := serveStateRequest(t, wc, "/api/turn?chat=chat-a&turn=4")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	blocks, _ := body["blocks"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %v", body["blocks"])
	}
	if final, _ := body["final"].(map[string]any); final == nil || final["content"] != "b" {
		t.Fatalf("final = %v", body["final"])
	}

	rec = serveStateRequest(t, wc, "/api/turn?chat=chat-a&turn=99")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown turn: status %d body %s", rec.Code, rec.Body.String())
	}
}

// TestStateTurnEndpointNeverFabricatesFinal — a turn with no closing record must
// omit `final` entirely (the client then knows the turn is unfinished), rather
// than receive a fabricated one.
func TestStateTurnEndpointNeverFabricatesFinal(t *testing.T) {
	wc := stateTestChannel()
	wc.callbacks.TurnSnapshot = func(string, string, uint64) (*protocol.TurnSnapshot, error) {
		return &protocol.TurnSnapshot{
			TurnID: 5,
			Blocks: []protocol.IterationBlock{{TurnID: 5, Iteration: 1, Content: "x", Tools: []protocol.ToolProgress{}}},
		}, nil
	}
	rec := serveStateRequest(t, wc, "/api/turn?chat=chat-a&turn=5")
	body := decodeBody(t, rec)
	if _, carried := body["final"]; carried {
		t.Fatalf("final must be omitted when the turn is not closed, got %v", body["final"])
	}
}

// TestSessionStateEndpointStampsEpochAndSeq — the snapshot must be stamped with
// (epoch, seq) where seq is the chat's CURRENT watermark, so a client can accept
// it as a baseline and apply only newer frames (P6 / C-12).
func TestSessionStateEndpointStampsEpochAndSeq(t *testing.T) {
	wc := stateTestChannel()
	// Two frames on the chat before the read → watermark 2.
	publishProgress(wc, "web", "chat-s")
	publishProgress(wc, "web", "chat-s")

	wc.callbacks.SessionState = func(channelName, chatID string) (*protocol.SessionStateSnapshot, error) {
		return &protocol.SessionStateSnapshot{
			Turns: []protocol.SessionStateTurn{{TurnID: 1, Blocks: []protocol.IterationBlock{
				{TurnID: 1, Iteration: 1, Content: "hello", Tools: []protocol.ToolProgress{}},
			}}},
			Sess: protocol.SessionStateSession{Running: false},
		}, nil
	}

	rec := serveStateRequest(t, wc, "/api/session-state?chat=chat-s")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ok"] != true {
		t.Fatalf("ok = %v", body["ok"])
	}
	if body["epoch"] != ServerEpoch() {
		t.Fatalf("epoch = %v, want %q", body["epoch"], ServerEpoch())
	}
	if seq, _ := body["seq"].(float64); uint64(seq) != 2 {
		t.Fatalf("seq = %v, want the chat watermark 2", body["seq"])
	}
	if _, ok := body["live"]; !ok {
		t.Fatal("live must be present (null when there is no in-flight iteration)")
	}
	if body["live"] != nil {
		t.Fatalf("live = %v, want null", body["live"])
	}
	if sess, _ := body["session"].(map[string]any); sess == nil {
		t.Fatalf("session = %v", body["session"])
	}
}

// TestSessionStateEndpointRereadsOnConcurrentFrame — two-phase read (C-12): if a
// frame is published while the (slow) state assembly runs, the snapshot may be
// stale, so the handler re-reads. It also never blocks frame publishing: the
// state callback runs OUTSIDE the per-chat counter lock (asserted by publishing
// from inside the callback — a lock held across it would deadlock).
func TestSessionStateEndpointRereadsOnConcurrentFrame(t *testing.T) {
	wc := stateTestChannel()
	calls := 0
	wc.callbacks.SessionState = func(channelName, chatID string) (*protocol.SessionStateSnapshot, error) {
		calls++
		if calls == 1 {
			// Simulate a frame landing while the DB read is in flight. This must
			// NOT deadlock (the read holds no frame lock).
			publishProgress(wc, "web", "chat-race")
		}
		return &protocol.SessionStateSnapshot{Sess: protocol.SessionStateSession{}}, nil
	}

	rec := serveStateRequest(t, wc, "/api/session-state?chat=chat-race")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if calls < 2 {
		t.Fatalf("state callback ran %d time(s) — a concurrent frame must trigger a re-read", calls)
	}
	body := decodeBody(t, rec)
	if seq, _ := body["seq"].(float64); uint64(seq) != 1 {
		t.Fatalf("seq = %v, want the post-frame watermark 1", body["seq"])
	}
}
