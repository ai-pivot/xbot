package web

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"xbot/protocol"
)

// Real HTTP subscription with the exact production subagent identity. The
// engine's restore/send regression covers generation; this covers its delivery.
func TestSSESubAgentProgressAndStreamUseAgentRoute(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	const key = "web:chat_9C218CD94A75/taizi:dpsk-perf-1"
	wc.callbacks.SessionExists = func(ch, id string) bool { return ch == "agent" && id == key }
	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	query := url.Values{"chat_id": {key}, "channel": {"agent"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/sse?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status: %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	wc.SendProgress(key, &protocol.ProgressEvent{ChatID: "agent:" + key, TurnID: 7, Iteration: 1, Phase: "thinking"})
	progress := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeProgress, 1)
	if progress.Progress == nil || progress.Progress.TurnID != 7 || progress.Progress.ChatID != "agent:"+key {
		t.Fatalf("lost progress identity: %#v", progress.Progress)
	}
	wc.SendProgress(key, &protocol.ProgressEvent{ChatID: "agent:" + key, TurnID: 7, Iteration: 1, StreamContent: "live content"})
	// Iteration-bearing stream frames keep the progress_structured envelope;
	// the frontend normalizer extracts the stream payload without losing iter.
	stream := assertSSEMessage(t, readSSEEvent(t, reader), protocol.MsgTypeProgress, 2)
	if stream.Progress == nil || stream.Progress.StreamContent != "live content" || stream.Progress.TurnID != 7 {
		t.Fatalf("lost stream payload: %#v", stream.Progress)
	}
	if got := wc.getEventStream(sessionRouteKey("web", key)).lastSeq(); got != 0 {
		t.Fatalf("agent progress leaked into web route: seq=%d", got)
	}
}
