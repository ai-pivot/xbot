package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock 是可推进的时钟，用来验证"临近过期提前续期"。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// tokenServer 是假的 getAppAccessToken：每次返回 tok-<n> 并记录请求体。
type tokenServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  int
	bodies []map[string]string
}

func newTokenServer(t *testing.T, expiresIn string) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.mu.Lock()
		ts.calls++
		n := ts.calls
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		ts.bodies = append(ts.bodies, body)
		ts.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-` + strconv.Itoa(n) + `","expires_in":` + expiresIn + `}`))
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *tokenServer) Calls() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.calls
}

func (ts *tokenServer) Bodies() []map[string]string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]map[string]string, len(ts.bodies))
	copy(out, ts.bodies)
	return out
}

// TestTokenProactiveRenewal 证明"临近过期会提前续期"，而不是到期后才换。
func TestTokenProactiveRenewal(t *testing.T) {
	ts := newTokenServer(t, "7200")
	clock := newFakeClock()

	tc := NewTokenClient("102030405", "s3cret")
	tc.tokenURL = ts.srv.URL
	tc.now = clock.Now

	ctx := context.Background()
	tok, err := tc.Token(ctx)
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if tok != "tok-1" || ts.Calls() != 1 {
		t.Fatalf("tok=%q calls=%d, want tok-1/1", tok, ts.Calls())
	}
	if bodies := ts.Bodies(); len(bodies) != 1 || bodies[0]["appId"] != "102030405" || bodies[0]["clientSecret"] != "s3cret" {
		t.Fatalf("unexpected token request body: %v", bodies)
	}

	// 还在安全窗内（7200-60=7140s 之前）：命中缓存，不再换。
	clock.Advance(7139 * time.Second)
	if tok, err = tc.Token(ctx); err != nil || tok != "tok-1" || ts.Calls() != 1 {
		t.Fatalf("cached Token = %q,%v calls=%d, want tok-1,nil,1", tok, err, ts.Calls())
	}

	// 进入 60s 安全窗（>7140s）：必须提前换新。
	clock.Advance(2 * time.Second) // 7141s
	if tok, err = tc.Token(ctx); err != nil || tok != "tok-2" || ts.Calls() != 2 {
		t.Fatalf("renewal Token = %q,%v calls=%d, want tok-2,nil,2", tok, err, ts.Calls())
	}

	// 换完之后又进入新的安全窗内：继续用 tok-2。
	clock.Advance(10 * time.Second)
	if tok, err = tc.Token(ctx); err != nil || tok != "tok-2" || ts.Calls() != 2 {
		t.Fatalf("post-renewal Token = %q,%v calls=%d, want tok-2,nil,2", tok, err, ts.Calls())
	}
}

// TestTokenExpiresInString 兼容 expires_in 为字符串的响应形态。
func TestTokenExpiresInString(t *testing.T) {
	ts := newTokenServer(t, `"3600"`)
	clock := newFakeClock()
	tc := NewTokenClient("102030405", "s3cret")
	tc.tokenURL = ts.srv.URL
	tc.now = clock.Now

	ctx := context.Background()
	if _, err := tc.Token(ctx); err != nil {
		t.Fatalf("Token: %v", err)
	}
	clock.Advance(3541 * time.Second) // 3600-60 之后
	if _, err := tc.Token(ctx); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if ts.Calls() != 2 {
		t.Fatalf("calls=%d, want 2 (string expires_in must drive renewal)", ts.Calls())
	}
}

// TestTokenShortTTLUsesHalfWindow 有效期很短时最多只用一半，避免窗口为负。
func TestTokenShortTTLUsesHalfWindow(t *testing.T) {
	ts := newTokenServer(t, "60")
	clock := newFakeClock()
	tc := NewTokenClient("102030405", "s3cret")
	tc.tokenURL = ts.srv.URL
	tc.now = clock.Now

	ctx := context.Background()
	if _, err := tc.Token(ctx); err != nil {
		t.Fatalf("Token: %v", err)
	}
	clock.Advance(25 * time.Second)
	if _, err := tc.Token(ctx); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if ts.Calls() != 1 {
		t.Fatalf("calls=%d, want 1 (25s < half of 60s)", ts.Calls())
	}
	clock.Advance(10 * time.Second) // 35s > 30s
	if _, err := tc.Token(ctx); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if ts.Calls() != 2 {
		t.Fatalf("calls=%d, want 2 (35s > 60/2)", ts.Calls())
	}
}

// TestTokenInvalidateForcesRefetch Invalidate 后下一次 Token 必须重取。
func TestTokenInvalidateForcesRefetch(t *testing.T) {
	ts := newTokenServer(t, "7200")
	tc := NewTokenClient("102030405", "s3cret")
	tc.tokenURL = ts.srv.URL
	tc.now = newFakeClock().Now

	ctx := context.Background()
	if _, err := tc.Token(ctx); err != nil {
		t.Fatalf("Token: %v", err)
	}
	tc.Invalidate()
	tok, err := tc.Token(ctx)
	if err != nil {
		t.Fatalf("Token after Invalidate: %v", err)
	}
	if tok != "tok-2" || ts.Calls() != 2 {
		t.Fatalf("tok=%q calls=%d, want tok-2/2", tok, ts.Calls())
	}
}

// TestTokenHTTPErrorNotCached 换 token 失败必须报错且不写缓存。
func TestTokenHTTPErrorNotCached(t *testing.T) {
	var fail = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok-ok","expires_in":7200}`))
	}))
	defer srv.Close()

	tc := NewTokenClient("102030405", "s3cret")
	tc.tokenURL = srv.URL

	if _, err := tc.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Token error = %v, want HTTP 500", err)
	}
	fail = false
	if tok, err := tc.Token(context.Background()); err != nil || tok != "tok-ok" {
		t.Fatalf("retry Token = %q,%v", tok, err)
	}
}

// TestTokenEmptyAccessTokenIsError 200 但没 token 也要报错（别把空串带进请求头）。
func TestTokenEmptyAccessTokenIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"expires_in":7200}`))
	}))
	defer srv.Close()

	tc := NewTokenClient("102030405", "s3cret")
	tc.tokenURL = srv.URL
	if _, err := tc.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "empty access_token") {
		t.Fatalf("Token error = %v, want empty access_token", err)
	}
}
