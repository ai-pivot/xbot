package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The push endpoints use writeJSON, which already wraps the value in the
// {ok,data,error} envelope. Passing {"ok":true,"data":...} into writeJSON
// produces a second nesting (data.data) and the web client correctly reports
// "WebPush disabled" because cfg.webpush is undefined. Guard the flat shape.
func TestPushConfigResponseShape(t *testing.T) {
	wc := &WebChannel{callbacks: WebCallbacks{
		PushConfig: func() map[string]any {
			return map[string]any{"webpush": map[string]any{
				"enabled":    true,
				"public_key": "test-key",
			}}
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/api/push/config", strings.NewReader("{}"))
	req = req.WithContext(contextWithSenderID(req.Context(), "web-1"))
	rec := httptest.NewRecorder()

	wc.handlePushConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		t.Fatalf("data is not an object: %s", rec.Body.String())
	}
	if _, nested := data["data"]; nested {
		t.Fatalf("response is double-wrapped (data.data): %s", rec.Body.String())
	}
	wp, _ := data["webpush"].(map[string]any)
	if wp["enabled"] != true || wp["public_key"] != "test-key" {
		t.Fatalf("webpush = %#v, want enabled/public_key", wp)
	}
}

func TestPushTestResponseShape(t *testing.T) {
	wc := &WebChannel{callbacks: WebCallbacks{
		PushTest: func(string) (int, error) { return 2, nil },
	}}
	req := httptest.NewRequest(http.MethodPost, "/api/push/test", strings.NewReader("{}"))
	req = req.WithContext(contextWithSenderID(req.Context(), "web-1"))
	rec := httptest.NewRecorder()

	wc.handlePushTest(rec, req)

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		t.Fatalf("data is not an object: %s", rec.Body.String())
	}
	if _, nested := data["data"]; nested {
		t.Fatalf("response is double-wrapped (data.data): %s", rec.Body.String())
	}
	if data["sent"] != float64(2) {
		t.Fatalf("sent = %#v, want 2 (%s)", data["sent"], rec.Body.String())
	}
}
