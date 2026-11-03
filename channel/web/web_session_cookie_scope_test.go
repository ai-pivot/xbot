package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xbot/bus"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newScopedChannel builds a channel with an explicit instance identity so these
// tests never depend on the ambient process environment (XBOT_HOME /
// XBOT_INSTANCE_ID) that other tests may be tinkering with.
func newScopedChannel(t *testing.T, instanceID string, port int) *WebChannel {
	t.Helper()
	wc := NewWebChannel(WebChannelConfig{
		Host:       "127.0.0.1",
		Port:       port,
		InstanceID: instanceID,
	}, bus.NewMessageBus())
	t.Cleanup(wc.Stop)
	return wc
}

// newScopedServer exposes just the two things these tests need: an authenticated
// probe endpoint (the authMiddleware path) and logout.
func newScopedServer(t *testing.T, wc *WebChannel) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/logout", wc.handleLogout)
	mux.HandleFunc("/api/probe", wc.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func seedSession(wc *WebChannel, token string, userID int) {
	wc.sessionsMu.Lock()
	wc.sessions[token] = sessionInfo{
		userID:   userID,
		username: "tester",
		expires:  time.Now().Add(webSessionMaxAge),
	}
	wc.sessionsMu.Unlock()
}

func hasSession(wc *WebChannel, token string) bool {
	wc.sessionsMu.RLock()
	defer wc.sessionsMu.RUnlock()
	_, ok := wc.sessions[token]
	return ok
}

func probe(t *testing.T, srv *httptest.Server, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// ---------------------------------------------------------------------------
// ① Different identities ⇒ different cookie names
// ---------------------------------------------------------------------------

func TestSessionCookieName_DiffersPerInstanceIdentity(t *testing.T) {
	a := webSessionCookieNameFor("smith")
	b := webSessionCookieNameFor("daidai")

	if a == b {
		t.Fatalf("two instances derived the SAME cookie name %q — they will evict each other", a)
	}
	for _, name := range []string{a, b} {
		if !strings.HasPrefix(name, webSessionCookieName+"_") {
			t.Errorf("name %q is not derived from %q", name, webSessionCookieName)
		}
		if !isCookieNameToken(name) {
			t.Errorf("name %q is not a valid RFC 6265 token", name)
		}
	}
	// Deterministic, never random: the same identity must always map to the
	// same name, or every restart would invalidate every browser cookie.
	if again := webSessionCookieNameFor("smith"); again != a {
		t.Errorf("derivation is not deterministic: %q then %q", a, again)
	}
	// Unusable identity ⇒ legacy name (login/startup must never fail).
	if got := webSessionCookieNameFor(""); got != webSessionCookieName {
		t.Errorf("empty identity = %q, want legacy %q", got, webSessionCookieName)
	}
}

// isCookieNameToken reports whether s is a non-empty RFC 6265 token.
func isCookieNameToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '!', r == '#', r == '$', r == '%', r == '&', r == '\'', r == '*',
			r == '+', r == '-', r == '.', r == '^', r == '_', r == '`', r == '|', r == '~':
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// ② Legacy cookie still authenticates and is migrated onto the scoped name
// ---------------------------------------------------------------------------

func TestSessionCookie_LegacyCookieMigratesToScopedName(t *testing.T) {
	wc := newScopedChannel(t, "smith", 8080)
	srv := newScopedServer(t, wc)

	scoped := wc.sessionCookieName()
	if scoped != webSessionCookieNameFor("smith") {
		t.Fatalf("scoped name = %q, want %q", scoped, webSessionCookieNameFor("smith"))
	}

	const token = "legacy-token-1"
	seedSession(wc, token, 7)

	// Browser still carries the pre-upgrade, unscoped cookie.
	resp := probe(t, srv, &http.Cookie{Name: webSessionCookieName, Value: token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy cookie must still authenticate (no forced re-login): got %d", resp.StatusCode)
	}

	var migrated *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == scoped {
			migrated = c
		}
	}
	if migrated == nil {
		t.Fatalf("no migration Set-Cookie for %q on the response (new cookies: %v)", scoped, cookieNames(resp.Cookies()))
	}
	if migrated.Value != token {
		t.Fatalf("migration must keep the token: got %q want %q", migrated.Value, token)
	}
	if migrated.Path != "/" || !migrated.HttpOnly || migrated.SameSite != http.SameSiteLaxMode {
		t.Fatalf("migration cookie attributes drifted: path=%q httpOnly=%v sameSite=%v",
			migrated.Path, migrated.HttpOnly, migrated.SameSite)
	}
	if migrated.MaxAge != int(webSessionMaxAge.Seconds()) {
		t.Fatalf("migration cookie MaxAge = %d, want %d", migrated.MaxAge, int(webSessionMaxAge.Seconds()))
	}
	// The migration deliberately does NOT delete the legacy cookie: Set-Cookie
	// can only expire a name it explicitly targets, and clearing it here would
	// log out any other instance that still uses it. So the browser keeps BOTH
	// cookies until logout.
	for _, c := range resp.Cookies() {
		if c.Name == webSessionCookieName && c.MaxAge == -1 {
			t.Fatalf("migration must not expire the legacy cookie (that is logout's job)")
		}
	}

	// Second request carries both cookies (the real browser state right after
	// migration): it must resolve through the scoped branch and therefore must
	// NOT issue the migration Set-Cookie again — otherwise every single request
	// would keep re-issuing it forever.
	resp2 := probe(t, srv,
		&http.Cookie{Name: scoped, Value: token},
		&http.Cookie{Name: webSessionCookieName, Value: token},
	)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("post-migration request with both cookies = %d, want 200", resp2.StatusCode)
	}
	for _, c := range resp2.Cookies() {
		if c.Name == scoped {
			t.Fatalf("scoped request re-issued the migration cookie (%q) — legacy branch still winning", scoped)
		}
	}
}

func cookieNames(cookies []*http.Cookie) []string {
	names := make([]string, 0, len(cookies))
	for _, c := range cookies {
		names = append(names, c.Name)
	}
	return names
}

// ---------------------------------------------------------------------------
// ③ A cookie minted by ANOTHER instance must never authenticate here
// ---------------------------------------------------------------------------

func TestSessionCookie_OtherInstanceCookieRejected(t *testing.T) {
	wcA := newScopedChannel(t, "smith", 8080)
	wcB := newScopedChannel(t, "daidai", 8081)

	nameA, nameB := wcA.sessionCookieName(), wcB.sessionCookieName()
	if nameA == nameB {
		t.Fatalf("instances share cookie name %q — the eviction bug is back", nameA)
	}

	// The token is deliberately ALSO present in A's session map: isolation must
	// come from the cookie NAME, not from A happening to not know the token.
	// (Before the fix both instances read "xbot_session", so B's cookie
	// authenticated on A whenever the token was known there.)
	const shared = "token-known-to-both"
	seedSession(wcA, shared, 7)
	seedSession(wcB, shared, 7)

	srvA := newScopedServer(t, wcA)
	if resp := probe(t, srvA, &http.Cookie{Name: nameB, Value: shared}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie minted by instance B authenticated on instance A: got %d, want 401", resp.StatusCode)
	}
	// Sanity: A's own cookie does authenticate, so the 401 above is about the
	// name and not a broken session map.
	if resp := probe(t, srvA, &http.Cookie{Name: nameA, Value: shared}); resp.StatusCode != http.StatusOK {
		t.Fatalf("instance A rejected its own cookie: got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// ④ Logout clears BOTH names and drops both tokens
// ---------------------------------------------------------------------------

func TestSessionCookie_LogoutClearsBothNames(t *testing.T) {
	wc := newScopedChannel(t, "smith", 8080)
	srv := newScopedServer(t, wc)
	scoped := wc.sessionCookieName()

	const newToken, oldToken = "token-new-name", "token-old-name"
	seedSession(wc, newToken, 7)
	seedSession(wc, oldToken, 7)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: scoped, Value: newToken})
	req.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: oldToken})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", resp.StatusCode)
	}

	cleared := map[string]bool{}
	for _, c := range resp.Cookies() {
		if c.MaxAge == -1 {
			cleared[c.Name] = true
		}
	}
	for _, want := range []string{scoped, webSessionCookieName} {
		if !cleared[want] {
			t.Errorf("logout did not clear cookie %q (cleared: %v)", want, cookieNames(resp.Cookies()))
		}
	}

	if hasSession(wc, newToken) {
		t.Error("scoped cookie's token survived logout")
	}
	if hasSession(wc, oldToken) {
		t.Error("legacy cookie's token survived logout — the old cookie can resurrect the session")
	}
}

// ---------------------------------------------------------------------------
// ⑤ The migration read is an exact legacy-name read, never a prefix match
// ---------------------------------------------------------------------------

func TestSessionCookie_LegacyReadIsExactNameOnly(t *testing.T) {
	wc := newScopedChannel(t, "smith", 8080)
	srv := newScopedServer(t, wc)

	const token = "token-5"
	seedSession(wc, token, 7)

	for _, foreign := range []string{
		webSessionCookieNameFor("daidai"), // another instance's scoped name
		wc.sessionCookieName() + "_extra", // near-miss on our own prefix
		webSessionCookieName + "_",        // dangling prefix
	} {
		if foreign == wc.sessionCookieName() {
			continue
		}
		resp := probe(t, srv, &http.Cookie{Name: foreign, Value: token})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("cookie %q (not this instance's name) authenticated: got %d, want 401", foreign, resp.StatusCode)
		}
	}
}

// ---------------------------------------------------------------------------
// ⑥ Same XBOT_HOME, different ports ⇒ SAME name (identity, not port)
// ---------------------------------------------------------------------------

func TestSessionCookie_SameHomeDifferentPortsShareName(t *testing.T) {
	t.Setenv("XBOT_HOME", "/tmp/p03-home-smith")
	t.Setenv("XBOT_INSTANCE_ID", "")

	a := NewWebChannel(WebChannelConfig{Host: "127.0.0.1", Port: 8080}, bus.NewMessageBus())
	t.Cleanup(a.Stop)
	b := NewWebChannel(WebChannelConfig{Host: "127.0.0.1", Port: 8081}, bus.NewMessageBus())
	t.Cleanup(b.Stop)

	nameA, nameB := a.sessionCookieName(), b.sessionCookieName()
	if nameA != nameB {
		t.Fatalf("same XBOT_HOME on ports 8080/8081 derived %q vs %q — that is port-derived, not identity-derived", nameA, nameB)
	}
	if want := webSessionCookieNameFor("p03-home-smith"); nameA != want {
		t.Fatalf("derived name = %q, want %q (from XBOT_HOME basename)", nameA, want)
	}
}

// ---------------------------------------------------------------------------
// ⑦ Different XBOT_HOME, same port ⇒ DIFFERENT name (not port-derived)
// ---------------------------------------------------------------------------

func TestSessionCookie_DifferentHomesSamePortDiffer(t *testing.T) {
	t.Setenv("XBOT_INSTANCE_ID", "")

	t.Setenv("XBOT_HOME", "/tmp/p03-home-a/smith")
	a := NewWebChannel(WebChannelConfig{Host: "127.0.0.1", Port: 8080}, bus.NewMessageBus())
	t.Cleanup(a.Stop)
	nameA := a.sessionCookieName()

	t.Setenv("XBOT_HOME", "/tmp/p03-home-b/daidai")
	b := NewWebChannel(WebChannelConfig{Host: "127.0.0.1", Port: 8080}, bus.NewMessageBus())
	t.Cleanup(b.Stop)
	nameB := b.sessionCookieName()

	if nameA == nameB {
		t.Fatalf("two different XBOT_HOMEs on the same port derived the SAME name %q — identity is not being used", nameA)
	}
	if nameA != webSessionCookieNameFor("smith") || nameB != webSessionCookieNameFor("daidai") {
		t.Fatalf("names = %q / %q, want %q / %q", nameA, nameB,
			webSessionCookieNameFor("smith"), webSessionCookieNameFor("daidai"))
	}
}

// ---------------------------------------------------------------------------
// sanitizeCookieScopeID (directly callable, as the wiring relies on it)
// ---------------------------------------------------------------------------

func TestSanitizeCookieScopeID(t *testing.T) {
	cases := map[string]string{
		"smith":                   "smith",
		"daidai-2":                "daidai-2",
		"/home/smith/.xbot":       "home_smith__xbot",
		"  spaced id  ":           "spaced_id",
		"实例一":                     "",
		"___":                     "",
		"":                        "",
		"a/b:c d":                 "a_b_c_d",
		strings.Repeat("x", 100):  strings.Repeat("x", maxCookieScopeIDLen),
		strings.Repeat("_", 100):  "",
		strings.Repeat("ab", 100): strings.Repeat("ab", maxCookieScopeIDLen/2),
	}
	for in, want := range cases {
		if got := sanitizeCookieScopeID(in); got != want {
			t.Errorf("sanitizeCookieScopeID(%q) = %q, want %q", in, got, want)
		}
	}
	// A sanitised identity must always yield a usable cookie name.
	for _, id := range []string{"smith", "/home/smith/.xbot", strings.Repeat("y", 200)} {
		if name := webSessionCookieNameFor(id); !isCookieNameToken(name) {
			t.Errorf("webSessionCookieNameFor(%q) = %q, not a cookie token", id, name)
		}
	}
}
