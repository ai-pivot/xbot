package web

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

// tenantExists 直接查 tenants 表（newTestDB 返回的是标准库 *sql.DB）。
func tenantExists(t *testing.T, db *sql.DB, channel, chatID string) bool {
	t.Helper()
	var id int64
	err := db.QueryRow("SELECT id FROM tenants WHERE channel = ? AND chat_id = ?", channel, chatID).Scan(&id)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("tenant lookup: %v", err)
	}
	return id != 0
}

// TestSSE_UnknownSessionRejectedNotMaterialized —— 幽灵会话根因回归。
//
// 事故：客户端（旧标签页 / 次要设备 / 缓存布局）仍持有【已删除】的 chatID，
// 重连 SSE 时后端读路径直接 GetOrCreateSession → 把 tenant 又重新建出来，
// 于是幽灵会话"删了又生"（用户报告：总是莫名其妙多几个我没有的会话，
// 名字是默认的 chat_XXXX、msgs=0、不在 user_chats）。
//
// 断言：未知会话必须 404，并且【绝不】被 materialize 成 tenant。
func TestSSE_UnknownSessionRejectedNotMaterialized(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)

	// 会话存在性判定（模拟 serverapp 的接线：库里真有才算存在）。
	wc.callbacks.SessionExists = func(channel, chatID string) bool {
		return tenantExists(t, db, channel, chatID)
	}

	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/sse?chat_id=chat_GHOST_SESSION&channel=web", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session must be rejected with 404, got %d", resp.StatusCode)
	}
	if tenantExists(t, db, "web", "chat_GHOST_SESSION") {
		t.Fatal("ghost tenant was materialized — read paths must never create sessions")
	}
}

// TestSSE_DefaultSessionStillAllowed —— 反向保护：隐式默认会话
// (chatID == senderID，如 "web-1") 从未显式创建过，必须仍然放行；
// 否则正常使用会被 404 打断。
func TestSSE_DefaultSessionStillAllowed(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channel, chatID string) bool { return false } // 一律"不存在"

	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	resp := openSSE(t, server.URL, cookie, "web-1", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default session (chatID == senderID) must be allowed, got %d", resp.StatusCode)
	}
}

// TestSessionStatus_UnknownSessionRejected —— REST 读路径同样不得 materialize。
//
// /api/session/status 是前端每次加载都会调的端点；它走 resolveAPISession →
// GetCWD → SetCWD → GetOrCreateSession。旧版在这里也会把已删除会话建回来，
// 所以门控放在 resolveAPISession（所有 REST 端点的统一入口）而不是逐个端点。
func TestSessionStatus_UnknownSessionRejected(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channel, chatID string) bool { return false }
	wc.callbacks.GetCWD = func(senderID string, sel SessionSelector) (string, error) { return "", nil }

	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/session/status",
		strings.NewReader(`{"channel":"web","chat_id":"chat_GHOST_REST"}`))
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

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session must be rejected with 404, got %d", resp.StatusCode)
	}
	if tenantExists(t, db, "web", "chat_GHOST_REST") {
		t.Fatal("REST read path materialized a ghost tenant")
	}
}

// TestSessionStatus_DefaultSessionAllowed —— 反向保护：隐式默认会话仍可用。
func TestSessionStatus_DefaultSessionAllowed(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	wc.callbacks.SessionExists = func(channel, chatID string) bool { return false }
	wc.callbacks.GetCWD = func(senderID string, sel SessionSelector) (string, error) { return "/tmp", nil }

	server := startTestServer(t, wc)
	cookie := loginTestAdmin(t, server.URL)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/session/status",
		strings.NewReader(`{"channel":"web","chat_id":"web-1"}`))
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
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("implicit default session must not be rejected, got %d", resp.StatusCode)
	}
}
