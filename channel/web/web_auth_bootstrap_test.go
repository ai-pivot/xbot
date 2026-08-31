package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xbot/bus"

	"xbot/config"
)

// postJSON is a small helper: POST body as JSON against a handler.
func postJSON(t *testing.T, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auth", bytes.NewReader(data))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestRegisterInviteOnlyBootstrap verifies the first-user bootstrap flow:
// an invite-only deployment with an EMPTY web_users table accepts exactly ONE
// registration (the operator account); the second registration is rejected
// with 403 — registration closes as soon as the table is non-empty.
func TestRegisterInviteOnlyBootstrap(t *testing.T) {
	db := newTestDB(t)
	msgBus := bus.NewMessageBus()
	wc := NewWebChannel(WebChannelConfig{
		Host: "127.0.0.1", Port: 0, DB: db,
		InviteOnly: true,
	}, msgBus)
	t.Cleanup(wc.Stop)

	// 1. Empty table + invite-only → the FIRST registration succeeds (bootstrap).
	rec := postJSON(t, wc.handleRegister, map[string]string{"username": "operator", "password": "hunter2secure"})
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap first registration: status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var res authResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res.OK {
		t.Fatalf("bootstrap first registration: body=%s err=%v", rec.Body.String(), err)
	}

	// 2. Table non-empty now → the SECOND registration is rejected 403.
	rec2 := postJSON(t, wc.handleRegister, map[string]string{"username": "intruder", "password": "whatever123"})
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("second registration after bootstrap: status=%d, want 403 (invite-only closed)", rec2.Code)
	}

	// 3. The bootstrap account can actually log in (a valid operator was created).
	rec3 := postJSON(t, wc.handleLogin, map[string]string{"username": "operator", "password": "hunter2secure"})
	if rec3.Code != http.StatusOK {
		t.Fatalf("bootstrap account login: status=%d body=%s, want 200", rec3.Code, rec3.Body.String())
	}
}

// TestAuthConfigBootstrapFlag verifies /api/auth/config: the bootstrap flag is
// true only while invite-only AND the account table is empty (the frontend
// uses it to show the first-user wizard instead of the login form).
func TestAuthConfigBootstrapFlag(t *testing.T) {
	db := newTestDB(t)
	msgBus := bus.NewMessageBus()
	wc := NewWebChannel(WebChannelConfig{
		Host: "127.0.0.1", Port: 0, DB: db,
		InviteOnly: true,
	}, msgBus)
	t.Cleanup(wc.Stop)

	// Empty table + invite-only → bootstrap: true.
	rec := postJSON(t, wc.handleAuthConfig, map[string]string{})
	if rec.Code != http.StatusOK {
		t.Fatalf("auth config: status=%d", rec.Code)
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			InviteOnly bool `json:"invite_only"`
			Bootstrap  bool `json:"bootstrap"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("auth config unmarshal: %v body=%s", err, rec.Body.String())
	}
	if !envelope.OK || !envelope.Data.InviteOnly || !envelope.Data.Bootstrap {
		t.Fatalf("empty table + invite-only: ok=%v invite_only=%v bootstrap=%v, want true/true/true (body=%s)",
			envelope.OK, envelope.Data.InviteOnly, envelope.Data.Bootstrap, rec.Body.String())
	}

	// After the first account, bootstrap flips false (normal invite-only).
	if _, err := db.Exec("INSERT INTO web_users (username, password) VALUES ('someone', 'x')"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	rec2 := postJSON(t, wc.handleAuthConfig, map[string]string{})
	if err := json.Unmarshal(rec2.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("auth config unmarshal 2: %v", err)
	}
	if envelope.Data.Bootstrap {
		t.Fatalf("non-empty table: bootstrap=true, want false")
	}
	if !envelope.Data.InviteOnly {
		t.Fatalf("invite_only flag lost: %+v", envelope.Data)
	}
}

// TestConfigInviteOnlyDefaultsTrue verifies the config default: an unset
// invite_only (nil) is TRUE (secure default — web login = operator, an open
// registration would hand operator rights to anyone who reaches the port);
// an explicit false opts back into open registration.
func TestConfigInviteOnlyDefaultsTrue(t *testing.T) {
	if !(config.WebConfig{}).IsInviteOnly() {
		t.Fatal("unset invite_only (nil) must default to TRUE (secure default)")
	}
	f := false
	if (config.WebConfig{InviteOnly: &f}).IsInviteOnly() {
		t.Fatal("explicit false must be respected (open registration)")
	}
	tr := true
	if !(config.WebConfig{InviteOnly: &tr}).IsInviteOnly() {
		t.Fatal("explicit true must stay true")
	}
}
