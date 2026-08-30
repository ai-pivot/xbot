package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"xbot/protocol"
)

// failingWriter fails every Write — used to simulate a broken SSE stream
// (the batch write/flush fails partway).
type failingWriter struct{}

func (failingWriter) Header() http.Header         { return http.Header{} }
func (failingWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }
func (failingWriter) WriteHeader(status int)      {}

// TestSSEBatchResolvedAskUserCursorAdvancesOnlyAfterFlush verifies the
// writeSSEBatch cursor contract ("lastSentSeq advances only after the flush
// succeeds"): a resolved AskUser consumed mid-batch must NOT advance
// client.lastSentSeq by itself. If the batch later fails (write error below
// the resolved event), the cursor must stay at the last flushed seq so a
// reconnect replays from there — the OLD code advanced the cursor at the
// resolved event (line: client.lastSentSeq = msg.Seq), permanently skipping
// every event between the last flush and the resolved seq.
func TestSSEBatchResolvedAskUserCursorAdvancesOnlyAfterFlush(t *testing.T) {
	// Batch: [resolved ask_user (seq 11), text (seq 12)]. The text write
	// fails (failingWriter) AFTER the resolved event was consumed.
	wc := &WebChannel{}
	wc.callbacks.WithPendingAskUser = func(ch, chatID string, fn func(*protocol.ProgressEvent) bool) bool {
		return false // resolved — no pending
	}
	client := &Client{w: failingWriter{}, sseEncWriter: failingWriter{}}
	batch := []protocol.WSMessage{
		{Type: protocol.MsgTypeAskUser, Seq: 11, ChatID: "chat-1", Progress: &protocol.ProgressEvent{RequestID: "req-1"}},
		{Type: protocol.MsgTypeText, Seq: 12, ChatID: "chat-1"},
	}

	err := wc.writeSSEBatch(context.Background(), client, batch)
	if err == nil {
		t.Fatal("batch write must fail with a failing writer")
	}
	if client.lastSentSeq != 0 {
		t.Fatalf("failed batch advanced lastSentSeq to %d — the cursor must only advance after a successful flush (stays 0)", client.lastSentSeq)
	}

	// Success path: a resolved AskUser alone in a flushed batch is consumed
	// and advances the cursor to its seq (committed at batch end).
	recorder := httptest.NewRecorder()
	okClient := &Client{w: recorder, flusher: recorder, sseEncWriter: recorder}
	if err := wc.writeSSEBatch(context.Background(), okClient, batch[:1]); err != nil {
		t.Fatal(err)
	}
	if okClient.lastSentSeq != 11 {
		t.Fatalf("flushed resolved ask_user must advance lastSentSeq to 11, got %d", okClient.lastSentSeq)
	}
}
