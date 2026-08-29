package llm

import (
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

// ---------------------------------------------------------------------------
// TestGetEncodingForModel
// ---------------------------------------------------------------------------

func TestGetEncodingForModel(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		wantEncoding tokenizer.Model
	}{
		// --- Claude models ---
		{"claude-3-opus maps to GPT4", "claude-3-opus", tokenizer.GPT4},
		{"claude-3-sonnet maps to GPT4", "claude-3-sonnet", tokenizer.GPT4},
		{"claude-3-haiku maps to GPT4", "claude-3-haiku", tokenizer.GPT4},
		{"claude-3-5-sonnet maps to GPT4", "claude-3-5-sonnet", tokenizer.GPT4},
		{"claude-3-5-sonnet-20241022 maps to GPT4", "claude-3-5-sonnet-20241022", tokenizer.GPT4},
		{"claude-3-5-haiku maps to GPT4", "claude-3-5-haiku", tokenizer.GPT4},
		{"claude-2 maps to GPT4", "claude-2", tokenizer.GPT4},
		{"claude-sonnet-4-20250514 maps to GPT4", "claude-sonnet-4-20250514", tokenizer.GPT4},
		{"claude-opus-4-20250115 maps to GPT4", "claude-opus-4-20250115", tokenizer.GPT4},

		// --- GPT-4 series ---
		{"gpt-4 maps to GPT4", "gpt-4", tokenizer.GPT4},
		{"gpt-4-turbo maps to GPT4", "gpt-4-turbo", tokenizer.GPT4},
		{"gpt-4o maps to GPT4o", "gpt-4o", tokenizer.GPT4o},
		{"gpt-4o-mini maps to GPT4o", "gpt-4o-mini", tokenizer.GPT4o},

		// --- GPT-3.5 series ---
		{"gpt-3.5-turbo maps to GPT35Turbo", "gpt-3.5-turbo", tokenizer.GPT35Turbo},

		// --- Prefix matching ---
		{"gpt-4o-2024-11-20 prefix-matches gpt-4o", "gpt-4o-2024-11-20", tokenizer.GPT4o},
		{"gpt-4-0123-preview prefix-matches gpt-4", "gpt-4-0123-preview", tokenizer.GPT4},
		{"gpt-3.5-turbo-16k prefix-matches gpt-3.5-turbo", "gpt-3.5-turbo-16k", tokenizer.GPT35Turbo},

		// --- Case insensitivity ---
		{"GPT-4O uppercased still matches", "GPT-4O", tokenizer.GPT4o},
		{"Claude-3-Opus mixed case matches", "Claude-3-Opus", tokenizer.GPT4},

		// --- Unknown model → default (GPT4) ---
		{"unknown model returns default GPT4", "some-unknown-model-xyz", tokenizer.GPT4},

		// --- Empty string → default (GPT4) ---
		{"empty string returns default GPT4", "", tokenizer.GPT4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getEncodingForModel(tt.model)
			if got != tt.wantEncoding {
				t.Errorf("getEncodingForModel(%q) = %v, want %v", tt.model, got, tt.wantEncoding)
			}
		})
	}
}
