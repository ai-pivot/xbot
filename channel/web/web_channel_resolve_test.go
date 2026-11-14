package web

// web_channel_resolve_test.go — 2026-11-13（用户报「非 web channel 的会话，
// 在 Web 上看收不到 SSE，busy 状态也不对」）。
//
// 根因：resolveSSESession / inferAPISessionChannel 对 chatID 的 channel 是**猜**的 ——
// 除了 agent 有 webChatIDLooksLikeSubAgent 单独救出，其余一律 `Channel: "web"`。
// 于是 cli/feishu 会话被挂到 `web\x00chatID` 路由，事件按真实 channel 发往
// `cli\x00chatID` ⇒ 一条都收不到；isUnknownSession 又用这个猜出来的 channel 探测
// 存在性（CLI 会话的 chatID 是文件路径、查 web 租户必为 false）⇒ 直接 404。
//
// 本文件守的是「不猜」这个契约：channel 必须由 (channel, chat_id) 的真实存在性探测得出。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestResolveChannelForChatID_DetectsCLI(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channel, chatID string) bool {
		return tenantExists(t, db, channel, chatID)
	}

	const cliPath = "/home/smith/src/xbot"
	if _, err := db.Conn().Exec(
		"INSERT OR IGNORE INTO tenants (channel, chat_id, created_at, last_active_at) VALUES (?, ?, ?, ?)",
		"cli", cliPath, time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed cli tenant: %v", err)
	}

	if got := wc.resolveChannelForChatID("web-4", cliPath); got != "cli" {
		t.Fatalf("resolveChannelForChatID(%q) = %q, want \"cli\".\n"+
			"  \"web\" here is the 2026-11-13 bug: the session is subscribed to the web route\n"+
			"  while its events are published on the cli route, so nothing is ever delivered.",
			cliPath, got)
	}
}

func TestResolveChannelForChatID_DetectsWeb(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channel, chatID string) bool {
		return tenantExists(t, db, channel, chatID)
	}

	const chatID = "chat_WEB_ONLY"
	if _, err := db.Conn().Exec(
		"INSERT OR IGNORE INTO tenants (channel, chat_id, created_at, last_active_at) VALUES (?, ?, ?, ?)",
		"web", chatID, time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed web tenant: %v", err)
	}

	if got := wc.resolveChannelForChatID("web-4", chatID); got != "web" {
		t.Fatalf("resolveChannelForChatID(%q) = %q, want \"web\"", chatID, got)
	}
}

// TestResolveChannelForChatID_UnknownReturnsEmpty 探测不到时必须返回空，
// 由调用方决定兜底 —— 不许在这里悄悄替用户选一个 channel。
func TestResolveChannelForChatID_UnknownReturnsEmpty(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channel, chatID string) bool { return false }

	if got := wc.resolveChannelForChatID("web-4", "chat_NOWHERE"); got != "" {
		t.Fatalf("resolveChannelForChatID(unknown) = %q, want empty", got)
	}
}

// TestInferAPISessionChannel_CLI REST 读路径同样不许猜成 web。
func TestInferAPISessionChannel_CLI(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channel, chatID string) bool {
		return tenantExists(t, db, channel, chatID)
	}

	const cliPath = "/home/smith/src/xbot-exp"
	if _, err := db.Conn().Exec(
		"INSERT OR IGNORE INTO tenants (channel, chat_id, created_at, last_active_at) VALUES (?, ?, ?, ?)",
		"cli", cliPath, time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed cli tenant: %v", err)
	}

	if got := wc.inferAPISessionChannel("web-4", cliPath); got != "cli" {
		t.Fatalf("inferAPISessionChannel(%q) = %q, want \"cli\"", cliPath, got)
	}
}

// TestResolveSSESession_CLIWithoutChannelParam 入口级：前端不传 channel 时，
// SSE 也必须解析到真实 channel（cli），而不是 web。
func TestResolveSSESession_CLIWithoutChannelParam(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channel, chatID string) bool {
		return tenantExists(t, db, channel, chatID)
	}

	const cliPath = "/home/smith/src/xbot-cli-session"
	if _, err := db.Conn().Exec(
		"INSERT OR IGNORE INTO tenants (channel, chat_id, created_at, last_active_at) VALUES (?, ?, ?, ?)",
		"cli", cliPath, time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed cli tenant: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sse?chat_id="+url.QueryEscape(cliPath), nil)
	rec := httptest.NewRecorder()
	sel, ok := wc.resolveSSESession(rec, req, "web-4", cliPath)
	if !ok {
		t.Fatalf("resolveSSESession rejected a known cli session (status=%d body=%s)",
			rec.Code, rec.Body.String())
	}
	if sel.Channel != "cli" {
		t.Fatalf("resolved channel = %q, want \"cli\" (chat_id=%q).\n"+
			"  \"web\" means the SSE stream subscribes to a route no event ever reaches.",
			sel.Channel, cliPath)
	}
	if sel.ChatID != cliPath {
		t.Fatalf("resolved chatID = %q, want %q", sel.ChatID, cliPath)
	}
}
