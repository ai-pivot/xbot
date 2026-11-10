package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the real SDK error shape as well as the shared predicate: a provider
// rejection must terminate before the SDK/retry wrapper can resend the request.
func TestRetryLLMRegionRestrictionHTTP(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"unsupported_country_region_territory","message":"Country, region, or territory not supported","type":"request_forbidden"}}`))
	}))
	defer upstream.Close()
	client := NewOpenAILLM(OpenAIConfig{BaseURL: upstream.URL + "/v1", APIKey: "test-only", DefaultModel: "test-model"})
	wrapped := NewRetryLLM(client, RetryConfig{Attempts: 5, Delay: time.Millisecond, MaxDelay: time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := wrapped.Generate(ctx, "test-model", []ChatMessage{NewUserMessage("test")}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "unsupported_country_region_territory") {
		t.Fatalf("error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP attempts = %d, want 1", got)
	}
}
