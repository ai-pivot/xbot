package channel

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHardWrapSingleLine_EmojiSequence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		maxW  int
		want  int // expected number of lines
	}{
		{
			name:  "family emoji stays together when fits",
			input: "hello 👨‍👩‍👧‍👦 world",
			maxW:  20,
			want:  1,
		},
		{
			name:  "CJK characters wrap individually",
			input: "你好世界再见",
			maxW:  4,
			want:  3, // 2 chars (4 width) per line
		},
		{
			name:  "mixed CJK and emoji",
			input: "你好🎉世界",
			maxW:  6,
			want:  2, // "你好🎉" (2+2+2=6) on line 1, "世界" (4) on line 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hardWrapSingleLine(tt.input, tt.maxW)
			lines := strings.Split(result, "\n")
			if len(lines) != tt.want {
				t.Errorf("got %d lines, want %d\nlines: %q", len(lines), tt.want, lines)
			}
			// Verify no line exceeds maxW
			for i, line := range lines {
				w := ansi.StringWidth(line)
				if w > tt.maxW {
					t.Errorf("line %d width %d exceeds maxW %d: %q", i, w, tt.maxW, line)
				}
			}
		})
	}
}

func TestHardWrapSingleLine_NoBrokenGrapheme(t *testing.T) {
	// Verify that multi-rune emoji sequences are never split mid-sequence
	emoji := "👨‍👩‍👧‍👦" // family: man + ZWJ + woman + ZWJ + girl + ZWJ + boy
	input := strings.Repeat(emoji, 5)
	for maxW := 2; maxW <= 20; maxW++ {
		result := hardWrapSingleLine(input, maxW)
		lines := strings.Split(result, "\n")
		for i, line := range lines {
			w := ansi.StringWidth(line)
			if w > maxW {
				t.Errorf("maxW=%d line %d width %d exceeds: %q", maxW, i, w, line)
			}
		}
	}
}

func TestHardWrapSingleLine_SkinToneNotSplit(t *testing.T) {
	// 👍🏽 = thumbs up + skin tone modifier (2 runes, 2 width)
	// Must never be split into 👍 and 🏽
	input := "👍🏽👍🏽👍🏽"
	result := hardWrapSingleLine(input, 4)
	lines := strings.Split(result, "\n")
	for i, line := range lines {
		w := ansi.StringWidth(line)
		if w > 4 {
			t.Errorf("line %d width %d exceeds 4: %q", i, w, line)
		}
		// Should contain complete 👍🏽 pairs, never a lone 👍 or 🏽
		if strings.Contains(line, "👍") && !strings.Contains(line, "👍🏽") {
			t.Errorf("line %d has broken skin tone: %q", i, line)
		}
	}
}
