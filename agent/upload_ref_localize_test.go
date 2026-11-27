package agent

// Stored tool-role content can carry an upload image reference (user
// interjection / synthetic pair). Those messages never go through the multimodal
// layer, so the raw reference reaches the model — and a same-origin
// "/api/files/download?key=…" URL is unconsumable (P0 user report 2026-11-27).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/llm"
)

func TestLocalizeUploadRefsInToolMessages(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	key := "uploads/4/inject.png"
	local := filepath.Join(home, "uploads", key)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := "/api/files/download?key=uploads%2F4%2Finject.png&inline=1"

	history := []llm.ChatMessage{
		{Role: "user", Content: "![shot](" + ref + ")"},       // handled by the multimodal layer
		{Role: "tool", Content: "插话：![shot](" + ref + ") 继续"}, // must be localized here
		{Role: "tool", Content: "no images here"},             // untouched
		{Role: "assistant", Content: "ok!"},                   // untouched
	}
	localizeUploadRefsInToolMessages(history)

	if history[0].Content != "![shot]("+ref+")" {
		t.Fatalf("user messages are the multimodal layer's job, got %q", history[0].Content)
	}
	if strings.Contains(history[1].Content, "/api/files/download") {
		t.Fatalf("tool content must not keep the unconsumable reference: %q", history[1].Content)
	}
	if !strings.Contains(history[1].Content, local) || !strings.Contains(history[1].Content, "继续") {
		t.Fatalf("tool content must carry the local path and keep surrounding text: %q", history[1].Content)
	}
	if history[2].Content != "no images here" || history[3].Content != "ok!" {
		t.Fatalf("unrelated messages must be untouched: %+v", history[2:])
	}
}
