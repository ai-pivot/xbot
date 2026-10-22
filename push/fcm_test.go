package push

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"xbot/config"
	"xbot/storage"
)

func newTestServiceAccount(t *testing.T, tokenURL string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemStr := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	sa := map[string]any{
		"project_id":   "test-proj",
		"client_email": "svc@test.iam.gserviceaccount.com",
		"private_key":  string(pemStr),
		"token_uri":    tokenURL,
	}
	raw, _ := json.Marshal(sa)
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write sa: %v", err)
	}
	return path
}

func TestFCMTokenAndSend(t *testing.T) {
	var tokenCalls, sendCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			atomic.AddInt32(&tokenCalls, 1)
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if got := r.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				t.Errorf("grant_type = %q", got)
			}
			assertion := r.Form.Get("assertion")
			if len(strings.Split(assertion, ".")) != 3 {
				t.Errorf("assertion is not a 3-part JWT: %q", assertion)
			}
			_, _ = w.Write([]byte(`{"access_token":"tok-1","expires_in":3600}`))
		case "/send":
			atomic.AddInt32(&sendCalls, 1)
			if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
				t.Errorf("Authorization = %q", got)
			}
			var body struct {
				Message struct {
					Token   string            `json:"token"`
					Data    map[string]string `json:"data"`
					Android struct {
						Priority    string `json:"priority"`
						CollapseKey string `json:"collapse_key"`
					} `json:"android"`
				} `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if body.Message.Token != "device-token" {
				t.Errorf("token = %q", body.Message.Token)
			}
			if body.Message.Data["kind"] != "ask_user" || body.Message.Data["chat_id"] != "chat-1" {
				t.Errorf("data = %+v", body.Message.Data)
			}
			if body.Message.Android.Priority != "HIGH" {
				t.Errorf("priority = %q", body.Message.Android.Priority)
			}
			_, _ = w.Write([]byte(`{"name":"projects/test/messages/1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	saPath := newTestServiceAccount(t, srv.URL+"/token")
	p, err := NewFCM(config.FCMConfig{
		Enable: true, ProjectID: "test-proj", CredentialsFile: saPath, Mode: "data",
	})
	if err != nil {
		t.Fatalf("NewFCM: %v", err)
	}
	p.sendURL = srv.URL + "/send"
	p.http = srv.Client()

	ev := Event{
		Kind: KindAskUser, Channel: "web", ChatID: "chat-1", Title: "t", Body: "b",
		RequestID: "req-1", HighPriority: true, CollapseKey: "ask:req-1", TS: 1,
	}
	if err := p.Send(context.Background(), storage.PushTarget{Provider: "fcm", Platform: "android", Address: "device-token"}, ev); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if tokenCalls != 1 || sendCalls != 1 {
		t.Fatalf("calls: token=%d send=%d, want 1/1", tokenCalls, sendCalls)
	}
}

func TestFCMRefreshesOn401(t *testing.T) {
	var tokenCalls, sendCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			n := atomic.AddInt32(&tokenCalls, 1)
			_, _ = w.Write([]byte(`{"access_token":"tok-` + string(rune('0'+n)) + `","expires_in":3600}`))
		case "/send":
			n := atomic.AddInt32(&sendCalls, 1)
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"status":"UNAUTHENTICATED"}}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	p, err := NewFCM(config.FCMConfig{
		Enable: true, ProjectID: "test-proj",
		CredentialsFile: newTestServiceAccount(t, srv.URL+"/token"), Mode: "data",
	})
	if err != nil {
		t.Fatalf("NewFCM: %v", err)
	}
	p.sendURL = srv.URL + "/send"
	p.http = srv.Client()

	err = p.Send(context.Background(), storage.PushTarget{Provider: "fcm", Platform: "android", Address: "t"},
		Event{Kind: KindTest, ChatID: "c", TS: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if tokenCalls != 2 || sendCalls != 2 {
		t.Fatalf("calls: token=%d send=%d, want 2/2", tokenCalls, sendCalls)
	}
}

func TestClassifyFCM(t *testing.T) {
	cases := []struct {
		status int
		kind   ErrorKind
	}{
		{200, 0}, // nil handled separately
		{401, Auth},
		{403, Auth},
		{404, Permanent},
		{400, Permanent},
		{429, Transient},
		{500, Transient},
		{503, Transient},
	}
	for _, c := range cases {
		err := classifyFCM(c.status, []byte("x"))
		if c.status == 200 {
			if err != nil {
				t.Errorf("200: %v", err)
			}
			continue
		}
		if got := ErrorKindOf(err); got != c.kind {
			t.Errorf("status %d: kind %v, want %v", c.status, got, c.kind)
		}
	}
}
