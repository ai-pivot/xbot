package textarea

import (
	"slices"
	"testing"

	"charm.land/bubbles/v2/key"
)

func TestWrapCJK(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		width  int
		expect int
	}{
		{"CJK wraps at character boundary", "你好世界测试", 6, 2},
		{"CJK with space wraps normally", "你好 世界", 8, 2},
		{"CJK fits exactly", "你好", 4, 1},
		{"Mixed CJK and Latin wraps correctly", "Hello你好World", 10, 2},
		{"Latin word wrapping preserved", "Hello World", 8, 2},
		{"Empty input", "", 10, 1},
		{"Single CJK char", "你", 10, 1},
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
	input := "你好 世"
	width := 10
	result := wrap([]rune(input), width)
	if len(result) != 1 {
		t.Errorf("wrap(%q, %d) returned %d lines, expected 1",
			input, width, len(result))
	}
}

// TestWordNavigationCJK tests word navigation with CJK characters.
// CJK characters are individual word boundaries (single-char granularity).
// Non-boundary chars (Latin, punctuation) group together between boundaries.
//
// Input: "Hello 你好World 测试 end"
// Indices: H(0)e(1)l(2)l(3)o(4) ' '(5) 你(6)好(7)W(8)o(9)r(10)l(11)d(12) ' '(13) 测(14)试(15) ' '(16) e(17)n(18)d(19)
func TestWordNavigationCJK(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("Hello 你好World 测试 end")

	tests := []struct {
		name     string
		startCol int
		expected int
		forward  bool
	}{
		// wordRight
		{"right: skip Hello", 0, 5, true},
		{"right: skip space + 你", 5, 7, true},
		{"right: skip 好 + World", 7, 13, true},
		{"right: skip space + 测", 13, 15, true},
		{"right: skip 试", 15, 16, true},
		{"right: skip space + end", 16, 20, true},
		{"right: at end stays", 20, 20, true},
		// wordLeft
		{"left: skip end", 20, 17, false},
		{"left: skip space + 试", 17, 15, false},
		{"left: skip 测", 15, 14, false},
		{"left: skip space + World + 好", 14, 8, false},
		{"left: skip 你", 8, 7, false},
		{"left: skip space + Hello", 7, 6, false},
		{"left: skip 你", 6, 0, false},
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

// TestDeleteWordCJK tests delete with CJK characters.
// Input: "Hello你好测试" → H(0)e(1)l(2)l(3)o(4)你(5)好(6)测(7)试(8)
func TestDeleteWordCJK(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("Hello你好测试")
	m.SetCursorColumn(9)

	// Delete "试"
	m.deleteWordLeft()
	if got := m.Value(); got != "Hello你好测" {
		t.Errorf("after deleteWordLeft (试): got %q, want %q", got, "Hello你好测")
	}

	// Delete "测"
	m.deleteWordLeft()
	if got := m.Value(); got != "Hello你好" {
		t.Errorf("after deleteWordLeft (测): got %q, want %q", got, "Hello你好")
	}

	// Delete "好"
	m.deleteWordLeft()
	if got := m.Value(); got != "Hello你" {
		t.Errorf("after deleteWordLeft (好): got %q, want %q", got, "Hello你")
	}

	// Delete "Hello你" — '你' is a boundary but tokenLeft retreats past it into Latin text
	m.deleteWordLeft()
	if got := m.Value(); got != "" {
		t.Errorf("after deleteWordLeft (Hello你): got %q, want %q", got, "")
	}
}

func TestCtrlArrowKeyBindings(t *testing.T) {
	km := DefaultKeyMap()

	assertHasKey := func(t *testing.T, binding key.Binding, want string) {
		t.Helper()
		if slices.Contains(binding.Keys(), want) {
			return
		}
		t.Errorf("binding keys %v should include %q", binding.Keys(), want)
	}

	assertHasKey(t, km.WordForward, "ctrl+right")
	assertHasKey(t, km.WordBackward, "ctrl+left")
	assertHasKey(t, km.WordForward, "alt+right")
	assertHasKey(t, km.WordBackward, "alt+left")
}

func TestIsCJK(t *testing.T) {
	tests := []struct {
		name  string
		r     rune
		isCJK bool
	}{
		{"Han", '一', true},
		{"Han ext", '中', true},
		{"Hangul", '가', true},
		{"Hiragana", 'あ', true},
		{"Katakana", 'ア', true},
		{"CJK ExtA", '㐀', true},
		{"Hangul syllable", '한', true},
		{"ASCII letter", 'A', false},
		{"ASCII digit", '5', false},
		{"ASCII space", ' ', false},
		{"Fullwidth A", 'Ａ', false},
		{"CJK punct", '。', false},
		{"Ideographic space", '　', false},
		{"Emoji", '😀', false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCJK(tt.r)
			if got != tt.isCJK {
				t.Errorf("isCJK(U+%04X %q) = %v, want %v", tt.r, string(tt.r), got, tt.isCJK)
			}
		})
	}
}

func TestIsWordBoundary(t *testing.T) {
	tests := []struct {
		name     string
		r        rune
		boundary bool
	}{
		{"Space", ' ', true},
		{"Tab", '\t', true},
		{"CJK Han", '一', true},
		{"CJK Katakana", 'ア', true},
		{"ASCII letter", 'a', false},
		{"ASCII digit", '5', false},
		{"Underscore", '_', false},
		{"Punctuation dot", '.', false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWordBoundary(tt.r)
			if got != tt.boundary {
				t.Errorf("isWordBoundary(U+%04X %q) = %v, want %v", tt.r, string(tt.r), got, tt.boundary)
			}
		})
	}
}

// TestDeleteWordRightCJK tests forward delete with CJK characters.
// Input: "Hello你好测试" → H(0)e(1)l(2)l(3)o(4)你(5)好(6)测(7)试(8)
func TestDeleteWordRightCJK(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("Hello你好测试")
	m.SetCursorColumn(0)

	// Delete "Hello" — tokenRight(0): col++=1, advance through e,l,l,o → stops at '你' boundary → returns 5
	m.deleteWordRight()
	if got := m.Value(); got != "你好测试" {
		t.Errorf("after deleteWordRight (Hello): got %q, want %q", got, "你好测试")
	}

	// Delete "你" — tokenRight(0): col++=1, '好' is boundary → returns 1
	m.deleteWordRight()
	if got := m.Value(); got != "好测试" {
		t.Errorf("after deleteWordRight (你): got %q, want %q", got, "好测试")
	}

	// Delete "好"
	m.deleteWordRight()
	if got := m.Value(); got != "测试" {
		t.Errorf("after deleteWordRight (好): got %q, want %q", got, "测试")
	}

	// Delete "测"
	m.deleteWordRight()
	if got := m.Value(); got != "试" {
		t.Errorf("after deleteWordRight (测): got %q, want %q", got, "试")
	}

	// Delete "试"
	m.deleteWordRight()
	if got := m.Value(); got != "" {
		t.Errorf("after deleteWordRight (试): got %q, want %q", got, "")
	}
}

func TestWrapCJKEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		width       int
		expectLines int
	}{
		{"CJK wider than width", "你好世界", 2, 4},
		{"Long Latin word", "abcdefghijklmnopqrstuvwxyz", 10, 3},
		{"CJK with punctuation", "你好.世界", 6, 2},
		{"Width 1 with CJK", "你好", 1, 2},
		{"Multiple spaces", "你好  世界", 10, 1},
		{"All spaces", "    ", 2, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wrap([]rune(tt.input), tt.width)
			if len(result) != tt.expectLines {
				t.Errorf("wrap(%q, %d) returned %d lines, expected %d",
					tt.input, tt.width, len(result), tt.expectLines)
			}
		})
	}
}

// TestWordCJK tests Word() with CJK characters.
// CJK characters are returned as individual characters by Word().
//
// Input: "Hello 你好World 测试 end"
// Indices: H(0)e(1)l(2)l(3)o(4) ' '(5) 你(6)好(7)W(8)o(9)r(10)l(11)d(12) ' '(13) 测(14)试(15) ' '(16) e(17)n(18)d(19)
func TestWordCJK(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("Hello 你好World 测试 end")

	tests := []struct {
		name     string
		col      int
		expected string
	}{
		{"At col 0 (no char)", 0, ""},
		{"At col 1 (left=H)", 1, "Hello"},
		{"At col 5 (left=o)", 5, "Hello"},
		{"At col 6 (left=space)", 6, ""},
		{"At col 7 (left=你)", 7, "你"},
		{"At col 8 (left=好)", 8, "好"},
		{"At col 9 (left=W)", 9, "World"},
		{"At col 13 (left=d)", 13, "World"},
		{"At col 14 (left=space)", 14, ""},
		{"At col 15 (left=测)", 15, "测"},
		{"At col 16 (left=试)", 16, "试"},
		{"At col 17 (left=space)", 17, ""},
		{"At col 18 (left=e)", 18, "end"},
		{"At col 20 (end of line)", 20, "end"},
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

func TestCursorAtWrapBoundary(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		width     int
		cursorCol int
	}{
		{"first visual line end", "你好你好", 6, 3},
		{"end of input", "你好你好", 6, 4},
		{"long line first wrap", "你好世界测试文字", 6, 3},
		{"long line second wrap", "你好世界测试文字", 6, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			m.SetWidth(tt.width)
			m.SetValue(tt.input)
			m.SetCursorColumn(tt.cursorCol)

			view := m.View()
			if view == "" {
				t.Error("View() returned empty string")
			}
			li := m.LineInfo()
			if li.ColumnOffset < 0 {
				t.Errorf("LineInfo().ColumnOffset = %d, want >= 0", li.ColumnOffset)
			}
		})
	}
}

// TestWordNavigationCJKWithPunctuation tests punctuation handling.
// '，' is NOT a word boundary, so it groups with adjacent non-boundary text.
//
// Input: "你好，世界测试"
// Indices: 你(0)好(1)，(2)世(3)界(4)测(5)试(6)
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
		// wordRight (tokenRight from trace)
		{"right: 你→1", 0, 1, true},
		{"right: 好+，→3", 1, 3, true},
		{"right: ，→3", 2, 3, true},
		{"right: 世→4", 3, 4, true},
		{"right: 界→5", 4, 5, true},
		{"right: 测→6", 5, 6, true},
		{"right: 试→7", 6, 7, true},
		{"right: end stays", 7, 7, true},
		// wordLeft (tokenLeft from trace)
		{"left: 试←7", 7, 6, false},
		{"left: 测←6", 6, 5, false},
		{"left: 界←5", 5, 4, false},
		{"left: 世+，←4", 4, 2, false},
		{"left: 好←2", 2, 1, false},
		{"left: 你←1", 1, 0, false},
		{"left: start stays", 0, 0, false},
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

// TestWordNavigationCJKMixedPunctuation tests mixed CJK/Latin/punctuation.
//
// Input: "Hello你好，世界"
// Indices: H(0)e(1)l(2)l(3)o(4)你(5)好(6)，(7)世(8)界(9)
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
		// wordRight from trace
		{"right: Hello→5", 0, 5, true},
		{"right: 你→6", 5, 6, true},
		{"right: 好+，→8", 6, 8, true},
		{"right: ，→8", 7, 8, true},
		{"right: 世→9", 8, 9, true},
		{"right: 界→10", 9, 10, true},
		// wordLeft from trace
		{"left: 界←10", 10, 9, false},
		{"left: 世+，←9", 9, 7, false},
		{"left: 好←7", 7, 6, false},
		{"left: 你+Hello←6", 6, 0, false},
		{"left: start stays", 0, 0, false},
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
// Input: "你好，世界测试"
// Indices: 你(0)好(1)，(2)世(3)界(4)测(5)试(6)
func TestDeleteWordCJKWithPunctuation(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("你好，世界测试")
	m.SetCursorColumn(7)

	// Delete "试"
	m.deleteWordLeft()
	if got := m.Value(); got != "你好，世界测" {
		t.Errorf("after deleteWordLeft (试): got %q, want %q", got, "你好，世界测")
	}

	// Delete "测"
	m.deleteWordLeft()
	if got := m.Value(); got != "你好，世界" {
		t.Errorf("after deleteWordLeft (测): got %q, want %q", got, "你好，世界")
	}

	// Delete "界"
	m.deleteWordLeft()
	if got := m.Value(); got != "你好，世" {
		t.Errorf("after deleteWordLeft (界): got %q, want %q", got, "你好，世")
	}

	// Delete "世+，" — tokenLeft retreats past non-boundary '，' to boundary '好'
	m.deleteWordLeft()
	if got := m.Value(); got != "你好" {
		t.Errorf("after deleteWordLeft (世，): got %q, want %q", got, "你好")
	}

	// Delete "好"
	m.deleteWordLeft()
	if got := m.Value(); got != "你" {
		t.Errorf("after deleteWordLeft (好): got %q, want %q", got, "你")
	}

	// Delete "你"
	m.deleteWordLeft()
	if got := m.Value(); got != "" {
		t.Errorf("after deleteWordLeft (你): got %q, want %q", got, "")
	}
}

// TestDeleteWordRightCJKWithPunctuation tests deleteWordRight with punctuation.
// Input: "你好，世界测试"
func TestDeleteWordRightCJKWithPunctuation(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("你好，世界测试")
	m.SetCursorColumn(0)

	// Delete "你"
	m.deleteWordRight()
	if got := m.Value(); got != "好，世界测试" {
		t.Errorf("after deleteWordRight (你): got %q, want %q", got, "好，世界测试")
	}

	// Delete "好，"
	m.deleteWordRight()
	if got := m.Value(); got != "世界测试" {
		t.Errorf("after deleteWordRight (好，): got %q, want %q", got, "世界测试")
	}

	// Delete "世"
	m.deleteWordRight()
	if got := m.Value(); got != "界测试" {
		t.Errorf("after deleteWordRight (世): got %q, want %q", got, "界测试")
	}

	// Delete "界"
	m.deleteWordRight()
	if got := m.Value(); got != "测试" {
		t.Errorf("after deleteWordRight (界): got %q, want %q", got, "测试")
	}
}

// TestWordCJKWithPunctuation tests Word() with punctuation.
// Input: "你好，世界"
// Indices: 你(0)好(1)，(2)世(3)界(4)
func TestWordCJKWithPunctuation(t *testing.T) {
	m := New()
	m.SetWidth(40)
	m.SetValue("你好，世界")

	tests := []struct {
		name     string
		col      int
		expected string
	}{
		{"你", 1, "你"},
		{"好", 2, "好"},
		{"，", 3, "，"},
		{"世", 4, "世"},
		{"界", 5, "界"},
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
