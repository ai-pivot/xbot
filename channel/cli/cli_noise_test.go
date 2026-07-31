package cli

import "testing"

func TestIsNoiseModel(t *testing.T) {
	cases := map[string]bool{
		"gpt-image-1":             true,
		"gpt-image-2":             true,
		"gpt-4o-realtime-preview": true,
		"gpt-4o-audio-preview":    true,
		"whisper-1":               true,
		"tts-1":                   true,
		"tts-1-hd":                true,
		"text-embedding-3-small":  true,
		"omni-moderation-latest":  true,
		"gpt-5.2-2025-12-11":      true, // dated snapshot
		"gpt-5.2-pro-2025-12-11":  true,
		// chat-usable models are NOT noise:
		"gpt-5.2":           false,
		"gpt-5.4":           false,
		"deepseek-v4-pro":   false,
		"glm-5.2":           false,
		"kimi-k2.7":         false,
		"claude-opus-4":     false,
		"codex-auto-review": false,
	}
	for model, want := range cases {
		if got := isNoiseModel(model); got != want {
			t.Errorf("isNoiseModel(%q) = %v, want %v", model, got, want)
		}
	}
}
