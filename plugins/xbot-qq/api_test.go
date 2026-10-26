package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubTokens 是可控的 TokenSource：Invalidate 后换代（tok-1 → tok-2 …）。
type stubTokens struct {
	mu     sync.Mutex
	gen    int
	calls  int
	revoke int
}

func (s *stubTokens) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return fmt.Sprintf("tok-%d", s.gen+1), nil
}

func (s *stubTokens) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.revoke++
}

func (s *stubTokens) Counts() (calls, revokes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.revoke
}

// TestSendC2CRequestShape 断言私聊发送的 URL / 鉴权头 / body 三件事。
func TestSendC2CRequestShape(t *testing.T) {
	var got struct {
		path   string
		auth   string
		method string
		ctype  string
		body   messageBody
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.method = r.Method
		got.ctype = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"m-1","timestamp":"2026-10-26T12:00:00Z"}`))
	}))
	defer srv.Close()

	api := NewAPIClient(&stubTokens{})
	api.baseURL = srv.URL

	msg, err := api.SendC2C(context.Background(), "USER_OPENID", "hello", "MSG_ANCHOR", 7)
	if err != nil {
		t.Fatalf("SendC2C: %v", err)
	}
	if msg.ID != "m-1" {
		t.Fatalf("msg id = %q, want m-1", msg.ID)
	}
	if got.path != "/v2/users/USER_OPENID/messages" {
		t.Fatalf("path = %q", got.path)
	}
	if got.method != http.MethodPost {
		t.Fatalf("method = %q, want POST", got.method)
	}
	if got.auth != "QQBot tok-1" {
		t.Fatalf("Authorization = %q, want QQBot tok-1", got.auth)
	}
	if got.ctype != "application/json" {
		t.Fatalf("Content-Type = %q", got.ctype)
	}
	if got.body.Content != "hello" || got.body.MsgType != MsgTypeText ||
		got.body.MsgID != "MSG_ANCHOR" || got.body.MsgSeq != 7 {
		t.Fatalf("body = %+v", got.body)
	}
}

// TestSendGroupRequestShape 断言群聊发送走 /v2/groups/{group_openid}/messages。
func TestSendGroupRequestShape(t *testing.T) {
	var path, auth string
	var body messageBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"id":"m-2"}`))
	}))
	defer srv.Close()

	api := NewAPIClient(&stubTokens{})
	api.baseURL = srv.URL
	if _, err := api.SendGroup(context.Background(), "GROUP_OPENID", "hi", "", 1); err != nil {
		t.Fatalf("SendGroup: %v", err)
	}
	if path != "/v2/groups/GROUP_OPENID/messages" {
		t.Fatalf("path = %q", path)
	}
	if auth != "QQBot tok-1" {
		t.Fatalf("auth = %q", auth)
	}
	if body.MsgID != "" || body.MsgSeq != 1 || body.MsgType != MsgTypeText {
		t.Fatalf("body = %+v (proactive message must omit msg_id)", body)
	}
}

// TestGatewayURL 断言 /gateway 的路径与解析。
func TestGatewayURL(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"url":"wss://api.sgroup.qq.com/websocket/"}`))
	}))
	defer srv.Close()

	api := NewAPIClient(&stubTokens{})
	api.baseURL = srv.URL
	got, err := api.GatewayURL(context.Background())
	if err != nil {
		t.Fatalf("GatewayURL: %v", err)
	}
	if got != "wss://api.sgroup.qq.com/websocket/" {
		t.Fatalf("url = %q", got)
	}
	if path != "/gateway" || auth != "QQBot tok-1" {
		t.Fatalf("path=%q auth=%q", path, auth)
	}
}

// TestGatewayURLEmptyIsError 空 url 必须报错（连 "" 会变成难查的 dial 错误）。
func TestGatewayURLEmptyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	api := NewAPIClient(&stubTokens{})
	api.baseURL = srv.URL
	if _, err := api.GatewayURL(context.Background()); err == nil {
		t.Fatal("GatewayURL = nil error, want error for empty url")
	}
}

// TestUnauthorizedRefetchesTokenAndRetries 401 → Invalidate → 用新 token 重试一次。
//
// 这是"过期后请求会自动重取"的硬证据：第二次请求的 Authorization 必须换成了
// tok-2，且总请求数为 2、Invalidate 恰好 1 次。
func TestUnauthorizedRefetchesTokenAndRetries(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		auths = append(auths, auth)
		mu.Unlock()
		if auth == "QQBot tok-1" {
			http.Error(w, `{"message":"token expired"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"m-3"}`))
	}))
	defer srv.Close()

	tokens := &stubTokens{}
	api := NewAPIClient(tokens)
	api.baseURL = srv.URL

	if _, err := api.SendC2C(context.Background(), "U", "hi", "", 1); err != nil {
		t.Fatalf("SendC2C: %v", err)
	}
	calls, revokes := tokens.Counts()
	if calls != 2 {
		t.Fatalf("token calls = %d, want 2 (refetch after 401)", calls)
	}
	if revokes != 1 {
		t.Fatalf("invalidations = %d, want 1", revokes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 2 || auths[0] != "QQBot tok-1" || auths[1] != "QQBot tok-2" {
		t.Fatalf("auth headers = %v, want [QQBot tok-1, QQBot tok-2]", auths)
	}
}

// TestUnauthorizedRetriesOnlyOnce 连续 401 不能无限重试。
func TestUnauthorizedRetriesOnlyOnce(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	api := NewAPIClient(&stubTokens{})
	api.baseURL = srv.URL
	_, err := api.SendC2C(context.Background(), "U", "hi", "", 1)
	if err == nil {
		t.Fatal("want error for persistent 401")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want APIError 401", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (one retry only)", requests)
	}
}

// TestAPIErrorIncludesBody 非 2xx 的错误要带响应体，方便对照官方错误码。
func TestAPIErrorIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":40034001,"message":"invalid content"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	api := NewAPIClient(&stubTokens{})
	api.baseURL = srv.URL
	_, err := api.SendGroup(context.Background(), "G", "hi", "", 1)
	if err == nil || !strings.Contains(err.Error(), "40034001") {
		t.Fatalf("err = %v, want body with error code", err)
	}
}

// TestSendEscapesOpenID openid 里出现特殊字符时不能破坏 URL。
func TestSendEscapesOpenID(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"id":"m"}`))
	}))
	defer srv.Close()

	api := NewAPIClient(&stubTokens{})
	api.baseURL = srv.URL
	if _, err := api.SendC2C(context.Background(), "a/b", "hi", "", 1); err != nil {
		t.Fatalf("SendC2C: %v", err)
	}
	if path != "/v2/users/a%2Fb/messages" {
		t.Fatalf("escaped path = %q", path)
	}
}
