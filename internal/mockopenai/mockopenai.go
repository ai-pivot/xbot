// Package mockopenai provides a fake OpenAI chat.completions server that
// streams a scripted sequence of chunks over real HTTP + SSE, and records the
// request bodies it receives. It is used by integration tests that exercise the
// REAL OpenAI client (HTTP + SSE parsing) end-to-end, instead of mocking the
// LLMClient interface directly.
package mockopenai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Chunk is one OpenAI chat.completions SSE chunk's worth of content.
type Chunk struct {
	Content          string // delta.content
	ReasoningContent string // delta.reasoning_content (DeepSeek/GLM thinking)
	ToolCalls        []ToolCall
	FinishReason     string
	Usage            *Usage
}

type ToolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Server is a fake OpenAI chat.completions endpoint.
type Server struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests [][]byte
}

// NewServer starts an httptest server that replies to POST */chat/completions
// with the given chunk sequence as an SSE stream, followed by [DONE].
func NewServer(t *testing.T, chunks []Chunk) *Server {
	t.Helper()
	s := &Server{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, body)
		s.mu.Unlock()

		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher := w.(http.Flusher)

		for _, c := range chunks {
			data, err := json.Marshal(buildChunk(c))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// URL returns the server's base URL (no trailing slash).
func (s *Server) URL() string { return s.srv.URL }

// Requests returns a copy of all request bodies received so far.
func (s *Server) Requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.requests))
	copy(out, s.requests)
	return out
}

func buildChunk(c Chunk) map[string]any {
	delta := map[string]any{}
	if c.Content != "" {
		delta["content"] = c.Content
	}
	if c.ReasoningContent != "" {
		delta["reasoning_content"] = c.ReasoningContent
	}
	if len(c.ToolCalls) > 0 {
		toolCalls := make([]map[string]any, 0, len(c.ToolCalls))
		for _, tc := range c.ToolCalls {
			toolCalls = append(toolCalls, map[string]any{
				"index": tc.Index,
				"id":    tc.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		delta["tool_calls"] = toolCalls
	}

	choice := map[string]any{
		"index":         0,
		"delta":         delta,
		"finish_reason": nil,
	}
	if c.FinishReason != "" {
		choice["finish_reason"] = c.FinishReason
	}

	chunk := map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion.chunk",
		"created": 1700000000,
		"model":   "mock-model",
		"choices": []any{choice},
	}
	if c.Usage != nil {
		chunk["usage"] = c.Usage
	}
	return chunk
}
