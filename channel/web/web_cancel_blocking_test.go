package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xbot/bus"
)

// TestCancelBlocksUntilTurnEnds — POST /api/cancel 必须**阻塞**到被取消的这一轮
// 真正收尾才返回（用户 2026-10-22：「cancel 只要 api 返回了一定必须已经取消成功，
// 它是阻塞的」）。
//
// 变异判别：把 handleCancel 里的等待去掉（改回"发完信号就 200"），本测试会在
// 断言 elapsed >= 250ms 处变红。
func TestCancelBlocksUntilTurnEnds(t *testing.T) {
	db := newTestDB(t)
	msgBus := bus.NewMessageBus()
	msgBus.EnableDeliveryAcknowledgement()
	wc := NewWebChannel(WebChannelConfig{Store: newTestStorage(t, db)}, msgBus)
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	if _, err := db.Conn().Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "web", "web-1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	runDone := make(chan struct{})
	wc.callbacks.ActiveRunDone = func(channel, chatID string) (<-chan struct{}, bool) {
		if channel != "web" || chatID != "web-1" {
			t.Errorf("ActiveRunDone(%q,%q) — want the resolved session", channel, chatID)
		}
		return runDone, true
	}

	const turnTeardown = 300 * time.Millisecond
	go func() {
		cancelMsg := <-msgBus.Inbound
		if cancelMsg.Content != "/cancel" {
			t.Errorf("expected /cancel inbound, got %q", cancelMsg.Content)
		}
		if cancelMsg.DeliveryAck != nil {
			cancelMsg.DeliveryAck <- bus.DeliveryResult{}
		}
		// The turn keeps running for a while after the signal (a slow tool is
		// still being torn down) — exactly the window the API used to lie about.
		time.Sleep(turnTeardown)
		close(runDone)
	}()

	recorder := httptest.NewRecorder()
	started := time.Now()
	wc.handleCancel(recorder, authedAPIRequest(http.MethodPost, "/api/cancel", []byte(`{"chat_id":"web-1"}`)))
	elapsed := time.Since(started)

	if recorder.Code != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if elapsed < turnTeardown {
		t.Fatalf("POST /api/cancel returned after %v, before the turn ended (%v) — it must BLOCK until the cancellation took effect", elapsed, turnTeardown)
	}

	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Cancelled bool   `json:"cancelled"`
			WaitedMS  int64  `json:"waited_ms"`
			ChatID    string `json:"chat_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, recorder.Body.String())
	}
	if !envelope.OK || !envelope.Data.Cancelled {
		t.Errorf("ok/cancelled = %v/%v in %s", envelope.OK, envelope.Data.Cancelled, recorder.Body.String())
	}
	if envelope.Data.WaitedMS < turnTeardown.Milliseconds() {
		t.Errorf("waited_ms = %d, want >= %d (the response must report the real wait)", envelope.Data.WaitedMS, turnTeardown.Milliseconds())
	}
}

// TestCancelReportsTimeoutInsteadOfLying — 5s 内没结束就返回 504 cancel_timeout，
// 绝不谎报成功。
func TestCancelReportsTimeoutInsteadOfLying(t *testing.T) {
	db := newTestDB(t)
	msgBus := bus.NewMessageBus()
	msgBus.EnableDeliveryAcknowledgement()
	wc := NewWebChannel(WebChannelConfig{Store: newTestStorage(t, db)}, msgBus)
	wc.cancelWait = 100 * time.Millisecond // test-only bound; production default is 5s
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	if _, err := db.Conn().Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "web", "web-1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	never := make(chan struct{}) // never closed: the turn refuses to stop
	wc.callbacks.ActiveRunDone = func(string, string) (<-chan struct{}, bool) { return never, true }

	go func() {
		cancelMsg := <-msgBus.Inbound
		if cancelMsg.DeliveryAck != nil {
			cancelMsg.DeliveryAck <- bus.DeliveryResult{}
		}
	}()

	recorder := httptest.NewRecorder()
	wc.handleCancel(recorder, authedAPIRequest(http.MethodPost, "/api/cancel", []byte(`{"chat_id":"web-1"}`)))

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 cancel_timeout: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			Cancelled bool  `json:"cancelled"`
			WaitedMS  int64 `json:"waited_ms"`
			TimeoutMS int64 `json:"timeout_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if envelope.OK || envelope.Error.Code != "cancel_timeout" || envelope.Data.Cancelled {
		t.Fatalf("body = %s, want ok=false error.code=cancel_timeout cancelled=false", recorder.Body.String())
	}
	if !strings.Contains(envelope.Error.Message, "cancel") {
		t.Errorf("error message %q should name the cancel timeout", envelope.Error.Message)
	}
	if envelope.Data.WaitedMS < 100 || envelope.Data.TimeoutMS != 100 {
		t.Errorf("waited_ms=%d timeout_ms=%d, want waited_ms>=100 timeout_ms=100", envelope.Data.WaitedMS, envelope.Data.TimeoutMS)
	}
}

// TestCancelWithoutActiveTurnReturnsImmediately — 没有活跃轮次时立即成功（保持现状）。
func TestCancelWithoutActiveTurnReturnsImmediately(t *testing.T) {
	db := newTestDB(t)
	msgBus := bus.NewMessageBus()
	msgBus.EnableDeliveryAcknowledgement()
	wc := NewWebChannel(WebChannelConfig{Store: newTestStorage(t, db)}, msgBus)
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	if _, err := db.Conn().Exec("INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)", "web", "web-1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	wc.callbacks.ActiveRunDone = func(string, string) (<-chan struct{}, bool) { return nil, false }

	go func() {
		cancelMsg := <-msgBus.Inbound
		if cancelMsg.DeliveryAck != nil {
			cancelMsg.DeliveryAck <- bus.DeliveryResult{}
		}
	}()

	recorder := httptest.NewRecorder()
	started := time.Now()
	wc.handleCancel(recorder, authedAPIRequest(http.MethodPost, "/api/cancel", []byte(`{"chat_id":"web-1"}`)))
	elapsed := time.Since(started)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if elapsed > 90*time.Millisecond {
		t.Fatalf("waited %v with no active turn — must return immediately", elapsed)
	}
}
