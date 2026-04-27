package textarea

import (
	"testing"

	"charm.land/bubbles/v2/key"
)

func TestWrapCJK(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		width  int
		expect int // expected number of visual lines
	}{
		{
			name:   "CJK wraps at character boundary",
			input:  "你好世界测试",
			width:  6,
			expect: 2,
		},
		{
			name:   "CJK with space wraps normally",
			input:  "你好 世界",
			width:  8,
			expect: 2,
		},
		{
			name:   "CJK fits exactly",
			input:  "你好",
			width:  4,
			expect: 1,
		},
		{
			name:   "Mixed CJK and Latin wraps correctly",
			input:  "Hello你好World",
			width:  10,
			expect: 2,
		},
		{
			name:   "Latin word wrapping preserved",
			input:  "Hello World",
			width:  8,
			expect: 2,
		},
		{
			name:   "Empty input",
			input:  "",
			width:  10,
			expect: 1,
		},
		{
			name:   "Single CJK char",
			input:  "你",
			width:  10,
			expect: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wrap([]rune(tt.input), tt.width)
			if len(result) != tt.expect {
				t.Errorf("wrap(%q, %d) returned %d lines, expected %d",
					tt.input, tt.width, len(result), tt.expect)
				for i, line := range result {
					t.Errorf("  line %d: %q", i, string(line))
				}
			}
		})
	}
}

func TestWrapCJKNoSpaceHardWrap(t *testing.T) {
	// CJK text with a space should NOT hard-wrap at the space position
	// when the content fits on the line.
	input := "你好 世"
	width := 10
	result := wrap([]rune(input), width)
	if len(result) != 1 {
		t.Errorf("wrap(%q, %d) returned %d lines, expected 1",
			input, width, len(result))
		for i, line := range result {
			t.Errorf("  line %d: %q", i, string(line))
		}
	}
}

func TestWordNavigationCJK(t *testing.T) {
	// Indices: H(0)e(1)l(2)l(3)o(4) ' '(5) 你(6)好(7)W(8)o(9)r(10)l(11)d(12) ' '(13)测(14)试(15) ' '(16)e(17)n(18)d(19)
	m := New()
	m.SetWidth(40)
	m.SetValue("Hello 你好World 测试 end")

	tests := []struct {
		name     string
		startCol int
		expected int
		forward  bool
	}{
		// wordRight: gse segments CJK words ("你好" as one, "测试" as one)
		{"right: skip Hello", 0, 5, true},
		{"right: skip space + 你好(gse)", 5, 8, true},
		{"right: skip World", 8, 13, true},
		{"right: skip space + 测试(gse)", 13, 16, true},
		{"right: skip space + end", 16, 20, true},
		{"right: at end stays", 20, 20, true},
		// wordLeft: gse segments CJK words
		{"left: skip end", 20, 17, false},
		{"left: skip space + 测试(gse)", 17, 14, false},
		{"left: skip World", 14, 8, false},
		{"left: skip 你好(gse)", 8, 6, false},
		{"left: skip space + Hello", 6, 0, false},
		{"left: at start stays", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetCursorColumn(tt.startCol)
			if tt.forward {
				m.wordRight()
			} else {
				m.wordLeft()
			}
			if m.col != tt.expected {
				t.Errorf("from col %d, %s → col %d, expected %d",
					tt.startCol,
					map[bool]string{true: "wordRight", false: "wordLeft"}[tt.forward],
					m.col, tt.expected)
			}
		})
	}
}

func TestDeleteWordCJK(t *testing.T) {
	m := New()
	m.SetWidth(40)

	m.SetValue("Hello你好测试")
	m.SetCursorColumn(len("Hello你好测试"))

	// Delete "测试" (CJK word, gse segmented)
	m.deleteWordLeft()
	if got := m.Value(); got != "Hello你好" {
		t.Errorf("after deleteWordLeft (测试): got %q, want %q", got, "Hello你好")
	}

	// Delete "你好" (CJK word, gse segmented)
	m.deleteWordLeft()
	if got := m.Value(); got != "Hello" {
		t.Errorf("after deleteWordLeft (你好): got %q, want %q", got, "Hello")
	}

	// Delete "Hello" (Latin word)
	m.deleteWordLeft()
	if got := m.Value(); got != "" {
		t.Errorf("after deleteWordLeft (Hello): got %q, want %q", got, "")
	}
}

func TestCtrlArrowKeyBindings(t *testing.T) {
	km := DefaultKeyMap()

	assertHasKey := func(t *testing.T, binding key.Binding, want string) {
		t.Helper()
		for _, k := range binding.Keys() {
			if k == want {
				return
			}
		}
		t.Errorf("binding keys %v should include %q", binding.Keys(), want)
	}

	assertHasKey(t, km.WordForward, "ctrl+right")
	assertHasKey(t, km.WordBackward, "ctrl+left")
	// Original alt bindings should still work
	assertHasKey(t, km.WordForward, "alt+right")
	assertHasKey(t, km.WordBackward, "alt+left")
}

// TestIsCJK validates that isCJK correctly identifies CJK scripts and rejects
// non-CJK characters including fullwidth Latin, punctuation, and plain ASCII.
func TestIsCJK(t *testing.T) {
	tests := []struct {
		name  string
		r     rune
		isCJK bool
	}{
		// CJK characters that should be detected
		{"Han (Chinese)", '一', true},         // 一
		{"Han (Chinese) ext", '中', true},     // 中
		{"Hangul (Korean)", '가', true},       // 가
		{"Hiragana (Japanese)", 'あ', true},   // あ
		{"Katakana (Japanese)", 'ア', true},   // ア
		{"Katakana ext phonetic", 'ㇰ', true}, // ㇰ
		{"CJK ExtA", '㐀', true},              // 㐀
		{"CJK compat ideograph", '豈', true},  // 豈
		{"CJK radical", '⺀', true},           // ⺀
		{"Kangxi radical", '⼀', true},        // ⼀
		{"Hangul syllable", '한', true},       // 한

		// Characters that should NOT be detected as CJK
		{"ASCII letter", 'A', false},
		{"ASCII digit", '5', false},
		{"ASCII space", ' ', false},
		{"Newline", '\n', false},
		{"Fullwidth A", 'Ａ', false},           // Ａ — semantically Latin
		{"Fullwidth digit", '１', false},       // １ — semantically digit
		{"CJK Symbols and Punct", '。', false}, // 。— punctuation
		{"Ideographic space", '　', false},     // 　 — whitespace
		{"Latin é", 'é', false},
		{"Cyrillic", 'А', false},
		{"Emoji", '😀', false}, // 😀
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCJK(tt.r)
			if got != tt.isCJK {
				t.Errorf("isCJK(U+%04X %q) = %v, want %v",
					tt.r, string(tt.r), got, tt.isCJK)
			}
		})
	}
}

// TestIsWordBoundary validates that isWordBoundary correctly identifies
// word boundaries (whitespace or CJK characters).
func TestIsWordBoundary(t *testing.T) {
	tests := []struct {
		name     string
		r        rune
		boundary bool
	}{
		{"Space is boundary", ' ', true},
		{"Tab is boundary", '\t', true},
		{"Newline is boundary", '\n', true},
		{"CJK Han is boundary", '一', true},      // 一
		{"CJK Katakana is boundary", 'ア', true}, // ア
		{"ASCII letter is NOT boundary", 'a', false},
		{"ASCII digit is NOT boundary", '5', false},
		{"Underscore is NOT boundary", '_', false},
		{"Punctuation dot is NOT boundary", '.', false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWordBoundary(tt.r)
			if got != tt.boundary {
				t.Errorf("isWordBoundary(U+%04X %q) = %v, want %v",
					tt.r, string(tt.r), got, tt.boundary)
			}
		})
	}
}

// TestDeleteWordRightCJK tests forward word deletion with CJK characters (gse segmented).
func TestDeleteWordRightCJK(t *testing.T) {
	m := New()
	m.SetWidth(40)

	// Start at position 0, delete forward through CJK text
	m.SetValue("Hello你好测试")
	m.SetCursorColumn(0)

	// Delete "Hello" (Latin word)
	m.deleteWordRight()
	if got := m.Value(); got != "你好测试" {
		t.Errorf("after deleteWordRight: got %q, want %q", got, "你好测试")
	}

	// Delete "你好" (CJK word, gse segmented as one)
	m.deleteWordRight()
	if got := m.Value(); got != "测试" {
		t.Errorf("after deleteWordRight: got %q, want %q", got, "测试")
	}

	// Delete "测试" (CJK word, gse segmented as one)
	m.deleteWordRight()
	if got := m.Value(); got != "" {
		t.Errorf("after deleteWordRight: got %q, want %q", got, "")
	}
}

// TestWrapCJKEdgeCases tests wrap() edge cases for CJK text.
func TestWrapCJKEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		width       int
		expectLines int
	}{
		{
			name:        "CJK wider than width forces per-char wrap",
			input:       "你好世界",
			width:       2,
			expectLines: 4, // each CJK char on its own line (2 columns each)
		},
		{
			name:        "Very long Latin word wraps at character boundary",
			input:       "abcdefghijklmnopqrstuvwxyz",
			width:       10,
			expectLines: 3,
		},
		{
			name:        "CJK with punctuation (non-CJK non-space treated as Latin word)",
			input:       "你好.世界",
			width:       6,
			expectLines: 2,
		},
		{
			name:        "Width 1 with CJK (each char needs 2 cols)",
			input:       "你好",
			width:       1,
			expectLines: 2, // each char on its own line (too wide for 1 col)
		},
		{
			name:        "Multiple spaces between CJK",
			input:       "你好  世界",
			width:       10,
			expectLines: 1,
		},
		{
			name:        "All spaces",
			input:       "    ",
			width:       2,
			expectLines: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wrap([]rune(tt.input), tt.width)
			if len(result) != tt.expectLines {
				t.Errorf("wrap(%q, %d) returned %d lines, expected %d",
					tt.input, tt.width, len(result), tt.expectLines)
				for i, line := range result {
					t.Errorf("  line %d: %q (width=%d)", i, string(line),
						len([]rune(string(line))))
				}
			}
		})
	}
}

// TestWordCJK tests the Word() method with CJK characters.
// Word() examines the character to the left of the cursor (col-1) and returns
// the word that character belongs to. CJK characters are individual words.
//
// For input "Hello 你好World测试 end":
//
//	Index: H(0) e(1) l(2) l(3) o(4) ' '(5) 你(6) 好(7) W(8) o(9) r(10) l(11) d(12) 测(13) 试(14) ' '(15) e(16) n(17) d(18)
func TestWordCJK(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("Hello 你好World测试 end")

	tests := []struct {
		name     string
		col      int
		expected string
	}{
		// col=0: col-1=-1 → no char to left → ""
		{"At col 0 (no char to left)", 0, ""},
		// col=1: col-1=0='H' → part of "Hello"
		{"At col 1 (left=H)", 1, "Hello"},
		// col=5: col-1=4='o' → part of "Hello"
		{"At col 5 (left=o)", 5, "Hello"},
		// col=6: col-1=5=' ' → space → ""
		{"At col 6 (left=space)", 6, ""},
		// col=7: col-1=6='你' → gse CJK word → "你好"
		{"At col 7 (left=你)", 7, "你好"},
		// col=8: col-1=7='好' → gse CJK word → "你好"
		{"At col 8 (left=好)", 8, "你好"},
		// col=9: col-1=8='W' → part of "World"
		{"At col 9 (left=W)", 9, "World"},
		// col=13: col-1=12='d' → part of "World"
		{"At col 13 (left=d)", 13, "World"},
		// col=14: col-1=13='测' → gse CJK word → "测试"
		{"At col 14 (left=测)", 14, "测试"},
		// col=15: col-1=14='试' → gse CJK word → "测试"
		{"At col 15 (left=试)", 15, "测试"},
		// col=16: col-1=15=' ' → space → ""
		{"At col 16 (left=space)", 16, ""},
		// col=17: col-1=16='e' → part of "end"
		{"At col 17 (left=e)", 17, "end"},
		// col=19 (beyond end): col-1=18='d' → part of "end"
		{"At col 19 (end of line)", 19, "end"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetCursorColumn(tt.col)
			got := m.Word()
			if got != tt.expected {
				t.Errorf("Word() at col %d = %q, want %q", tt.col, got, tt.expected)
			}
		})
	}
}

// TestCursorAtWrapBoundary verifies that the cursor can be positioned at
// the boundary between wrapped visual lines without panicking.
//
// cursor positions exactly at wrap boundaries are mapped to the end of
// the previous visual line, rendered as a space-cursor placeholder.
func TestCursorAtWrapBoundary(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		width     int
		cursorCol int
	}{
		{
			name:      "CJK wrap boundary — cursor at end of first visual line",
			input:     "你好你好", // 4 CJK chars, width 6 → 3 per line
			width:     6,
			cursorCol: 3, // after "你好你", at wrap point
		},
		{
			name:      "CJK wrap boundary — cursor at end of input",
			input:     "你好你好",
			width:     6,
			cursorCol: 4, // after all chars
		},
		{
			name:      "CJK wrap boundary — cursor at end of long line",
			input:     "你好世界测试文字",
			width:     6,
			cursorCol: 3, // first wrap point
		},
		{
			name:      "CJK wrap boundary — cursor at mid wrap point",
			input:     "你好世界测试文字",
			width:     6,
			cursorCol: 6, // second wrap point
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			m.SetWidth(tt.width)
			m.SetValue(tt.input)
			m.SetCursorColumn(tt.cursorCol)

			// View() must not panic (the old code had an index-out-of-range).
			view := m.View()
			if view == "" {
				t.Error("View() returned empty string")
			}

			// Also verify LineInfo is consistent.
			li := m.LineInfo()
			if li.ColumnOffset < 0 {
				t.Errorf("LineInfo().ColumnOffset = %d, want >= 0", li.ColumnOffset)
			}
		})
	}
}

// TestWordNavigationCJKWithPunctuation tests that ctrl+arrow correctly handles
// CJK punctuation (，。！) as separate stops, not merged with adjacent Latin words.
//
// For "你好，世界测试" gse segments: ["你好" "，" "世界" "测试"]
// Boundaries: [{0,2} {2,3} {3,5} {5,7}]
//
//	wordRight: 0→2→3→5→7
//	wordLeft:  7→5→3→2→0
func TestWordNavigationCJKWithPunctuation(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("你好，世界测试")

	tests := []struct {
		name     string
		startCol int
		expected int
		forward  bool
	}{
		// wordRight through punctuation
		{"right: 你好→end", 0, 2, true},
		{"right: ，→end", 2, 3, true},
		{"right: 世界→end", 3, 5, true},
		{"right: 测试→end", 5, 7, true},
		{"right: at end stays", 7, 7, true},
		// wordLeft through punctuation
		{"left: 测试→start", 7, 5, false},
		{"left: 世界→start", 5, 3, false},
		{"left: ，→start", 3, 2, false},
		{"left: 你好→start", 2, 0, false},
		{"left: at start stays", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetCursorColumn(tt.startCol)
			if tt.forward {
				m.wordRight()
			} else {
				m.wordLeft()
			}
			if m.col != tt.expected {
				t.Errorf("from col %d, %s → col %d, expected %d",
					tt.startCol,
					map[bool]string{true: "wordRight", false: "wordLeft"}[tt.forward],
					m.col, tt.expected)
			}
		})
	}
}

// TestWordNavigationCJKMixedPunctuation tests mixed CJK/Latin/punctuation navigation.
//
// For "Hello你好，世界" gse segments: ["hello" "你好" "，" "世界"]
// Boundaries: [{0,5} {5,7} {7,8} {8,10}]
func TestWordNavigationCJKMixedPunctuation(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("Hello你好，世界")

	tests := []struct {
		name     string
		startCol int
		expected int
		forward  bool
	}{
		{"right: Hello→end", 0, 5, true},
		{"right: 你好→end", 5, 7, true},
		{"right: ，→end", 7, 8, true},
		{"right: 世界→end", 8, 10, true},
		{"left: 世界→start", 10, 8, false},
		{"left: ，→start", 8, 7, false},
		{"left: 你好→start", 7, 5, false},
		{"left: Hello→start", 5, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetCursorColumn(tt.startCol)
			if tt.forward {
				m.wordRight()
			} else {
				m.wordLeft()
			}
			if m.col != tt.expected {
				t.Errorf("from col %d, %s → col %d, expected %d",
					tt.startCol,
					map[bool]string{true: "wordRight", false: "wordLeft"}[tt.forward],
					m.col, tt.expected)
			}
		})
	}
}

// TestDeleteWordCJKWithPunctuation tests delete operations with punctuation.
func TestDeleteWordCJKWithPunctuation(t *testing.T) {
	// deleteWordLeft: cursor after "测试", should delete "测试"
	m := New()
	m.SetWidth(40)
	m.SetValue("你好，世界测试")
	m.SetCursorColumn(len("你好，世界测试"))

	m.deleteWordLeft()
	if got := m.Value(); got != "你好，世界" {
		t.Errorf("after deleteWordLeft (测试): got %q, want %q", got, "你好，世界")
	}

	// Delete "世界"
	m.deleteWordLeft()
	if got := m.Value(); got != "你好，" {
		t.Errorf("after deleteWordLeft (世界): got %q, want %q", got, "你好，")
	}

	// Delete "，"
	m.deleteWordLeft()
	if got := m.Value(); got != "你好" {
		t.Errorf("after deleteWordLeft (，): got %q, want %q", got, "你好")
	}

	// Delete "你好"
	m.deleteWordLeft()
	if got := m.Value(); got != "" {
		t.Errorf("after deleteWordLeft (你好): got %q, want %q", got, "")
	}
}

// TestDeleteWordRightCJKWithPunctuation tests deleteWordRight with punctuation.
func TestDeleteWordRightCJKWithPunctuation(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("你好，世界测试")
	m.SetCursorColumn(0)

	// Delete "你好"
	m.deleteWordRight()
	if got := m.Value(); got != "，世界测试" {
		t.Errorf("after deleteWordRight (你好): got %q, want %q", got, "，世界测试")
	}

	// Delete "，"
	m.deleteWordRight()
	if got := m.Value(); got != "世界测试" {
		t.Errorf("after deleteWordRight (，): got %q, want %q", got, "世界测试")
	}

	// Delete "世界"
	m.deleteWordRight()
	if got := m.Value(); got != "测试" {
		t.Errorf("after deleteWordRight (世界): got %q, want %q", got, "测试")
	}
}

// TestWordCJKWithPunctuation tests Word() returns correct tokens with punctuation.
func TestWordCJKWithPunctuation(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("你好，世界")

	tests := []struct {
		name     string
		col      int
		expected string
	}{
		{"你", 1, "你好"},
		{"好", 2, "你好"},
		{"，", 3, "，"},
		{"世", 4, "世界"},
		{"界", 5, "世界"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetCursorColumn(tt.col)
			got := m.Word()
			if got != tt.expected {
				t.Errorf("Word() at col %d = %q, want %q", tt.col, got, tt.expected)
			}
		})
	}
}
