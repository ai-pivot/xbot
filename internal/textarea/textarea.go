// Package textarea provides a multi-line text input component for Bubble Tea
// applications.
package textarea

import (
	"image/color"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"xbot/internal/textarea/memoization"
	"xbot/internal/textarea/runeutil"

	"charm.land/bubbles/v2/cursor"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	rw "github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
	"golang.design/x/clipboard"
)

const (
	minHeight        = 1
	defaultHeight    = 6
	defaultWidth     = 40
	defaultCharLimit = 0 // no limit
	defaultMaxHeight = 99
	defaultMaxWidth  = 500

	// XXX: in v2, make max lines dynamic and default max lines configurable.
	maxLines = 10000

	// wrapCacheCap is the LRU capacity for the soft-wrap memoization cache.
	// Each entry stores the wrapped result for one (line content, width) pair.
	// A typical textarea has at most a few dozen logical lines, each at one
	// width, so 128 is more than sufficient while keeping memory bounded.
	wrapCacheCap = 128
)

// Internal messages for clipboard operations.
type (
	pasteMsg    string
	pasteErrMsg struct{ error }
)

// KeyMap is the key bindings for different actions within the textarea.
type KeyMap struct {
	CharacterBackward       key.Binding
	CharacterForward        key.Binding
	DeleteAfterCursor       key.Binding
	DeleteBeforeCursor      key.Binding
	DeleteCharacterBackward key.Binding
	DeleteCharacterForward  key.Binding
	DeleteWordBackward      key.Binding
	DeleteWordForward       key.Binding
	InsertNewline           key.Binding
	LineEnd                 key.Binding
	LineNext                key.Binding
	LinePrevious            key.Binding
	LineStart               key.Binding
	PageUp                  key.Binding
	PageDown                key.Binding
	Paste                   key.Binding
	WordBackward            key.Binding
	WordForward             key.Binding
	InputBegin              key.Binding
	InputEnd                key.Binding

	UppercaseWordForward  key.Binding
	LowercaseWordForward  key.Binding
	CapitalizeWordForward key.Binding

	TransposeCharacterBackward key.Binding
}

// DefaultKeyMap returns the default set of key bindings for navigating and acting
// upon the textarea.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		CharacterForward:        key.NewBinding(key.WithKeys("right", "ctrl+f"), key.WithHelp("right", "character forward")),
		CharacterBackward:       key.NewBinding(key.WithKeys("left", "ctrl+b"), key.WithHelp("left", "character backward")),
		WordForward:             key.NewBinding(key.WithKeys("ctrl+right", "alt+right", "alt+f"), key.WithHelp("ctrl+right", "word forward")),
		WordBackward:            key.NewBinding(key.WithKeys("ctrl+left", "alt+left", "alt+b"), key.WithHelp("ctrl+left", "word backward")),
		LineNext:                key.NewBinding(key.WithKeys("down", "ctrl+n"), key.WithHelp("down", "next line")),
		LinePrevious:            key.NewBinding(key.WithKeys("up", "ctrl+p"), key.WithHelp("up", "previous line")),
		DeleteWordBackward:      key.NewBinding(key.WithKeys("alt+backspace", "ctrl+w"), key.WithHelp("alt+backspace", "delete word backward")),
		DeleteWordForward:       key.NewBinding(key.WithKeys("alt+delete", "alt+d"), key.WithHelp("alt+delete", "delete word forward")),
		DeleteAfterCursor:       key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "delete after cursor")),
		DeleteBeforeCursor:      key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "delete before cursor")),
		InsertNewline:           key.NewBinding(key.WithKeys("enter", "ctrl+m"), key.WithHelp("enter", "insert newline")),
		DeleteCharacterBackward: key.NewBinding(key.WithKeys("backspace", "ctrl+h"), key.WithHelp("backspace", "delete character backward")),
		DeleteCharacterForward:  key.NewBinding(key.WithKeys("delete", "ctrl+d"), key.WithHelp("delete", "delete character forward")),
		LineStart:               key.NewBinding(key.WithKeys("home", "ctrl+a"), key.WithHelp("home", "line start")),
		LineEnd:                 key.NewBinding(key.WithKeys("end", "ctrl+e"), key.WithHelp("end", "line end")),
		PageUp:                  key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown:                key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "page down")),
		Paste:                   key.NewBinding(key.WithKeys("ctrl+v"), key.WithHelp("ctrl+v", "paste")),
		InputBegin:              key.NewBinding(key.WithKeys("alt+<", "ctrl+home"), key.WithHelp("alt+<", "input begin")),
		InputEnd:                key.NewBinding(key.WithKeys("alt+>", "ctrl+end"), key.WithHelp("alt+>", "input end")),

		CapitalizeWordForward: key.NewBinding(key.WithKeys("alt+c"), key.WithHelp("alt+c", "capitalize word forward")),
		LowercaseWordForward:  key.NewBinding(key.WithKeys("alt+l"), key.WithHelp("alt+l", "lowercase word forward")),
		UppercaseWordForward:  key.NewBinding(key.WithKeys("alt+u"), key.WithHelp("alt+u", "uppercase word forward")),

		TransposeCharacterBackward: key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "transpose character backward")),
	}
}

// LineInfo is a helper for keeping track of line information regarding
// soft-wrapped lines.
type LineInfo struct {
	// Width is the number of columns in the line.
	Width int

	// CharWidth is the number of characters in the line to account for
	// double-width runes.
	CharWidth int

	// Height is the number of rows in the line.
	Height int

	// StartColumn is the index of the first column of the line.
	StartColumn int

	// ColumnOffset is the number of columns that the cursor is offset from the
	// start of the line.
	ColumnOffset int

	// RowOffset is the number of rows that the cursor is offset from the start
	// of the line.
	RowOffset int

	// CharOffset is the number of characters that the cursor is offset
	// from the start of the line. This will generally be equivalent to
	// ColumnOffset, but will be different there are double-width runes before
	// the cursor.
	CharOffset int
}

// PromptInfo is a struct that can be used to store information about the
// prompt.
type PromptInfo struct {
	LineNumber int
	Focused    bool
}

// CursorStyle is the style for real and virtual cursors.
type CursorStyle struct {
	// Style styles the cursor block.
	//
	// For real cursors, the foreground color set here will be used as the
	// cursor color.
	Color color.Color

	// Shape is the cursor shape. The following shapes are available:
	//
	// - tea.CursorBlock
	// - tea.CursorUnderline
	// - tea.CursorBar
	//
	// This is only used for real cursors.
	Shape tea.CursorShape

	// CursorBlink determines whether or not the cursor should blink.
	Blink bool

	// BlinkSpeed is the speed at which the virtual cursor blinks. This has no
	// effect on real cursors as well as no effect if the cursor is set not to
	// [CursorBlink].
	//
	// By default, the blink speed is set to about 500ms.
	BlinkSpeed time.Duration
}

// Styles are the styles for the textarea, separated into focused and blurred
// states. The appropriate styles will be chosen based on the focus state of
// the textarea.
type Styles struct {
	Focused StyleState
	Blurred StyleState
	Cursor  CursorStyle
}

// StyleState that will be applied to the text area.
//
// StyleState can be applied to focused and unfocused states to change the styles
// depending on the focus state.
//
// For an introduction to styling with Lip Gloss see:
// https://github.com/charmbracelet/lipgloss
type StyleState struct {
	Base             lipgloss.Style
	Text             lipgloss.Style
	LineNumber       lipgloss.Style
	CursorLineNumber lipgloss.Style
	CursorLine       lipgloss.Style
	EndOfBuffer      lipgloss.Style
	Placeholder      lipgloss.Style
	Prompt           lipgloss.Style
}

func (s StyleState) computedCursorLineNumber() lipgloss.Style {
	return s.CursorLineNumber.
		Inherit(s.CursorLine).
		Inherit(s.Base).
		Inline(true)
}

func (s StyleState) computedEndOfBuffer() lipgloss.Style {
	return s.EndOfBuffer.Inherit(s.Base).Inline(true)
}

func (s StyleState) computedLineNumber() lipgloss.Style {
	return s.LineNumber.Inherit(s.Base).Inline(true)
}

func (s StyleState) computedPlaceholder() lipgloss.Style {
	return s.Placeholder.Inherit(s.Base).Inline(true)
}

func (s StyleState) computedPrompt() lipgloss.Style {
	return s.Prompt.Inherit(s.Base).Inline(true)
}

func (s StyleState) computedText() lipgloss.Style {
	return s.Text.Inherit(s.Base).Inline(true)
}

// line is the input to the text wrapping function. This is stored in a struct
// so that it can be hashed and memoized.
type line struct {
	runes []rune
	width int
}

// Hash returns a hash of the line using FNV-1a for fast, zero-allocation
// memoization keys. This is intentionally non-cryptographic — we only need
// collision resistance for the LRU cache, and FNV-1a provides ample
// bit spread for short UI text lines.
func (w line) Hash() string {
	// Inline FNV-1a over the rune data + width to avoid string/[]byte allocations.
	var h uint64 = 14695981039346656037 // offset64
	for _, r := range w.runes {
		v := uint32(r)
		for range 4 {
			h ^= uint64(v & 0xFF)
			h *= 1099511628211 // prime64
			v >>= 8
		}
	}
	// Mix in width to distinguish same content at different widths.
	v := uint64(w.width)
	for range 8 {
		h ^= v & 0xFF
		h *= 1099511628211
		v >>= 8
	}
	return strconv.FormatUint(h, 36) // base-36 compact string
}

// Model is the Bubble Tea model for this text area element.
type Model struct {
	Err error

	// General settings.
	cache *memoization.MemoCache[line, [][]rune]

	// Prompt is printed at the beginning of each line.
	//
	// When changing the value of Prompt after the model has been
	// initialized, ensure that SetWidth() gets called afterwards.
	//
	// See also [SetPromptFunc] for a dynamic prompt.
	Prompt string

	// Placeholder is the text displayed when the user
	// hasn't entered anything yet.
	Placeholder string

	// ShowLineNumbers, if enabled, causes line numbers to be printed
	// after the prompt.
	ShowLineNumbers bool

	// EndOfBufferCharacter is displayed at the end of the input.
	EndOfBufferCharacter rune

	// KeyMap encodes the keybindings recognized by the widget.
	KeyMap KeyMap

	// virtualCursor manages the virtual cursor.
	virtualCursor cursor.Model

	// CharLimit is the maximum number of characters this input element will
	// accept. If 0 or less, there's no limit.
	CharLimit int

	// MaxHeight is the maximum height of the text area in rows. If 0 or less,
	// there's no limit.
	MaxHeight int

	// MaxWidth is the maximum width of the text area in columns. If 0 or less,
	// there's no limit.
	MaxWidth int

	// DynamicHeight, when true, causes the textarea to automatically grow
	// and shrink its height to fit the content. The height is clamped between
	// MinHeight and MaxHeight.
	DynamicHeight bool

	// MinHeight is the minimum height of the text area in rows when
	// DynamicHeight is enabled. If 0 or less, defaults to 1.
	MinHeight int

	// MaxContentHeight is the maximum content height in visual rows
	// (accounting for soft wraps). When set (> 0), input is blocked once
	// the total visual lines reach this limit, while MaxHeight controls
	// only the visible viewport height. When 0, the content guard falls
	// back to the legacy MaxHeight behavior (blocking at MaxHeight
	// logical lines) for backward compatibility.
	MaxContentHeight int

	// Styling. Styles are defined in [Styles]. Use [SetStyles] and [GetStyles]
	// to work with this value publicly.
	styles Styles

	// useVirtualCursor determines whether or not to use the virtual cursor.
	// Use [SetVirtualCursor] and [VirtualCursor] to work with this this
	// value publicly.
	useVirtualCursor bool

	// If promptFunc is set, it replaces Prompt as a generator for
	// prompt strings at the beginning of each line.
	promptFunc func(PromptInfo) string

	// promptWidth is the width of the prompt.
	promptWidth int

	// width is the maximum number of characters that can be displayed at once.
	// If 0 or less this setting is ignored.
	width int

	// height is the maximum number of lines that can be displayed at once. It
	// essentially treats the text field like a vertically scrolling viewport
	// if there are more lines than the permitted height.
	height int

	// Underlying text value.
	value [][]rune

	// focus indicates whether user input focus should be on this input
	// component. When false, ignore keyboard input and hide the cursor.
	focus bool

	// Cursor column.
	col int

	// Cursor row.
	row int

	// Last character offset, used to maintain state when the cursor is moved
	// vertically such that we can maintain the same navigating position.
	lastCharOffset int

	// viewport is the vertically-scrollable viewport of the multi-line text
	// input.
	viewport *viewport.Model

	// rune sanitizer for input.
	rsan runeutil.Sanitizer
}

// New creates a new model with default settings.
func New() Model {
	vp := viewport.New()
	vp.KeyMap = viewport.KeyMap{}
	cur := cursor.New()

	styles := DefaultDarkStyles()

	m := Model{
		CharLimit:            defaultCharLimit,
		MaxHeight:            defaultMaxHeight,
		MaxWidth:             defaultMaxWidth,
		Prompt:               lipgloss.ThickBorder().Left + " ",
		styles:               styles,
		cache:                memoization.NewMemoCache[line, [][]rune](wrapCacheCap),
		EndOfBufferCharacter: ' ',
		ShowLineNumbers:      true,
		useVirtualCursor:     true,
		virtualCursor:        cur,
		KeyMap:               DefaultKeyMap(),

		value: make([][]rune, minHeight, maxLines),
		focus: false,
		col:   0,
		row:   0,

		viewport: &vp,
	}

	m.SetHeight(defaultHeight)
	m.SetWidth(defaultWidth)

	return m
}

// DefaultStyles returns the default styles for focused and blurred states for
// the textarea.
func DefaultStyles(isDark bool) Styles {
	lightDark := lipgloss.LightDark(isDark)

	var s Styles
	s.Focused = StyleState{
		Base:             lipgloss.NewStyle(),
		CursorLine:       lipgloss.NewStyle().Background(lightDark(lipgloss.Color("255"), lipgloss.Color("0"))),
		CursorLineNumber: lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("240"), lipgloss.Color("240"))),
		EndOfBuffer:      lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("254"), lipgloss.Color("0"))),
		LineNumber:       lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("249"), lipgloss.Color("7"))),
		Placeholder:      lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Prompt:           lipgloss.NewStyle().Foreground(lipgloss.Color("7")),
		Text:             lipgloss.NewStyle(),
	}
	s.Blurred = StyleState{
		Base:             lipgloss.NewStyle(),
		CursorLine:       lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("245"), lipgloss.Color("7"))),
		CursorLineNumber: lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("249"), lipgloss.Color("7"))),
		EndOfBuffer:      lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("254"), lipgloss.Color("0"))),
		LineNumber:       lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("249"), lipgloss.Color("7"))),
		Placeholder:      lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Prompt:           lipgloss.NewStyle().Foreground(lipgloss.Color("7")),
		Text:             lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("245"), lipgloss.Color("7"))),
	}
	s.Cursor = CursorStyle{
		Color: lipgloss.Color("7"),
		Shape: tea.CursorBlock,
		Blink: true,
	}
	return s
}

// DefaultLightStyles returns the default styles for a light background.
func DefaultLightStyles() Styles {
	return DefaultStyles(false)
}

// DefaultDarkStyles returns the default styles for a dark background.
func DefaultDarkStyles() Styles {
	return DefaultStyles(true)
}

// Styles returns the current styles for the textarea.
func (m Model) Styles() Styles {
	return m.styles
}

// SetStyles updates styling for the textarea.
func (m *Model) SetStyles(s Styles) {
	m.styles = s
	m.updateVirtualCursorStyle()
}

// VirtualCursor returns whether or not the virtual cursor is enabled.
func (m Model) VirtualCursor() bool {
	return m.useVirtualCursor
}

// SetVirtualCursor sets whether or not to use the virtual cursor.
func (m *Model) SetVirtualCursor(v bool) {
	m.useVirtualCursor = v
	m.updateVirtualCursorStyle()
}

// updateVirtualCursorStyle sets styling on the virtual cursor based on the
// textarea's style settings.
func (m *Model) updateVirtualCursorStyle() {
	if !m.useVirtualCursor {
		m.virtualCursor.SetMode(cursor.CursorHide)
		return
	}

	m.virtualCursor.Style = lipgloss.NewStyle().Foreground(m.styles.Cursor.Color)

	// By default, the blink speed of the cursor is set to a default
	// internally.
	if m.styles.Cursor.Blink {
		if m.styles.Cursor.BlinkSpeed > 0 {
			m.virtualCursor.BlinkSpeed = m.styles.Cursor.BlinkSpeed
		}
		m.virtualCursor.SetMode(cursor.CursorBlink)
		return
	}
	m.virtualCursor.SetMode(cursor.CursorStatic)
}

// SetValue sets the value of the text input.
// InsertString inserts a string at the cursor position.
// InsertRune inserts a rune at the cursor position.
// insertRunesFromUserInput inserts runes at the current cursor position.
// Value returns the value of the text input.
func (m Model) Value() string {
	if m.value == nil {
		return ""
	}

	var v strings.Builder
	for _, l := range m.value {
		v.WriteString(string(l))
		v.WriteByte('\n')
	}

	return strings.TrimSuffix(v.String(), "\n")
}

// Length returns the number of characters currently in the text input.
func (m *Model) Length() int {
	var l int
	for _, row := range m.value {
		l += uniseg.StringWidth(string(row))
	}
	// We add len(m.value) to include the newline characters.
	return l + len(m.value) - 1
}

// LineCount returns the number of lines that are currently in the text input.
func (m *Model) LineCount() int {
	return len(m.value)
}

// Line returns the 0-indexed row position of the cursor.
// Column returns the 0-indexed column position of the cursor.
// can be used to calculate the current scroll position.
func (m Model) ScrollYOffset() int {
	return m.viewport.YOffset()
}

// ScrollPercent returns the amount of the textarea that is currently scrolled
// through, clamped between 0 and 1.
func (m Model) ScrollPercent() float64 {
	return m.viewport.ScrollPercent()
}

// setCursorLineRelative moves the cursor by the given number of lines. Negative
// values move the cursor up, positive values move the cursor down.
// CursorDown moves the cursor down by one line.
// CursorUp moves the cursor up by one line.
// out of bounds the cursor will be moved to the start or end accordingly.
// CursorStart moves the cursor to the start of the input field.
func (m *Model) CursorStart() {
	m.SetCursorColumn(0)
}

// CursorEnd moves the cursor to the end of the input field.
func (m *Model) CursorEnd() {
	m.SetCursorColumn(len(m.value[m.row]))
}

// Focused returns the focus state on the model.
func (m Model) Focused() bool {
	return m.focus
}

// activeStyle returns the appropriate set of styles to use depending on
// whether the textarea is focused or blurred.
func (m Model) activeStyle() *StyleState {
	if m.focus {
		return &m.styles.Focused
	}
	return &m.styles.Blurred
}

// Focus sets the focus state on the model. When the model is in focus it can
// receive keyboard input and the cursor will be hidden.
func (m *Model) Focus() tea.Cmd {
	m.focus = true
	return m.virtualCursor.Focus()
}

// Blur removes the focus state on the model. When the model is blurred it can
// not receive keyboard input and the cursor will be hidden.
func (m *Model) Blur() {
	m.focus = false
	m.virtualCursor.Blur()
}

// Reset sets the input to its default state with no input.
func (m *Model) Reset() {
	m.value = make([][]rune, minHeight, maxLines)
	m.col = 0
	m.row = 0
	m.viewport.GotoTop()
	m.SetCursorColumn(0)
	m.recalculateHeight()
}

// Click positions the cursor based on a mouse click at terminal column x.
// x is the terminal column (0-based), relative to the textarea content area.
// This is a simplified version that positions the cursor on the current line.
// For full (x, y) click support, use ClickAt instead.
// (both 0-based, relative to the textarea content area).
// Always advances at least one non-space character.
func (m *Model) tokenRight(col int) int {
	line := m.value[m.row]
	// Skip spaces forward
	for col < len(line) && unicode.IsSpace(line[col]) {
		col++
	}
	if col >= len(line) {
		return col
	}
	// Advance at least one character, then stop at word boundary
	col++
	for col < len(line) {
		r := line[col]
		if isWordBoundary(r) {
			break
		}
		col++
	}
	return col
}

// tokenLeft returns the column after skipping one word/token to the left
// from the given column. It skips spaces first, then retreats through
// non-boundary characters using isWordBoundary.
func (m *Model) tokenLeft(col int) int {
	line := m.value[m.row]
	// Skip spaces backward
	for col > 0 && unicode.IsSpace(line[col-1]) {
		col--
	}
	if col <= 0 {
		return 0
	}
	// Retreat through non-boundary characters.
	// On the first step, always retreat at least one rune even if it is a word
	// boundary, so CJK navigation works (CJK chars are boundaries but we want
	// to skip at least one character).
	origCol := col
	for col > 0 {
		prev := line[col-1]
		if isWordBoundary(prev) && col != origCol {
			break
		}
		col--
	}

	return col
}

// Word returns the word at the cursor position.
//
// Uses isWordBoundary (whitespace or CJK characters) to identify word edges.
// CJK characters are treated as individual words (single-character granularity).
// Returns an empty string if the cursor is on whitespace, beyond the line end,
// or at position 0.
func (m *Model) Word() string {
	line := m.value[m.row]
	col := m.col - 1

	if col < 0 {
		return ""
	}

	// If cursor is beyond the line, return empty string
	if col >= len(line) {
		return ""
	}

	// If cursor is on a space, return empty string
	if unicode.IsSpace(line[col]) {
		return ""
	}

	// If cursor is on a CJK character, return just that character.
	// CJK characters are word boundaries and each forms its own "word".
	if isCJK(line[col]) {
		return string(line[col])
	}

	// Use isWordBoundary to find word edges for non-CJK, non-space text
	start := col
	for start > 0 && !isWordBoundary(line[start-1]) {
		start--
	}

	end := col + 1
	for end < len(line) && !isWordBoundary(line[end]) {
		end++
	}

	return string(line[start:end])
}

// san initializes or retrieves the rune sanitizer.
func (m *Model) san() runeutil.Sanitizer {
	if m.rsan == nil {
		// Textinput has all its input on a single line so collapse
		// newlines/tabs to single spaces.
		m.rsan = runeutil.NewSanitizer()
	}
	return m.rsan
}

// deleteBeforeCursor deletes all text before the cursor. Returns whether or
// not the cursor blink should be reset.
// the cursor so as not to reveal word breaks in the masked input.
// the right.
func (m *Model) transposeLeft() {
	if m.col == 0 || len(m.value[m.row]) < 2 {
		return
	}
	if m.col >= len(m.value[m.row]) {
		m.SetCursorColumn(m.col - 1)
	}
	m.value[m.row][m.col-1], m.value[m.row][m.col] = m.value[m.row][m.col], m.value[m.row][m.col-1]
	if m.col < len(m.value[m.row]) {
		m.SetCursorColumn(m.col + 1)
	}
}

// deleteWordLeft deletes the word left to the cursor.
// deleteWordRight deletes the word right to the cursor.
// characterRight moves the cursor one character to the right.
// character in the previous line, instead of one past that.
// wordLeft moves the cursor one word to the left.
// wordRight moves the cursor one word to the right.
// navigation works at single-character granularity.
func (m *Model) doWordRight(fn func(charIdx int, pos int)) {
	line := m.value[m.row]
	col := m.col

	// Skip spaces forward
	for col < len(line) && unicode.IsSpace(line[col]) {
		fn(0, col)
		m.SetCursorColumn(col + 1)
		col++
	}
	if col >= len(line) {
		return
	}

	// Advance through non-boundary characters using isWordBoundary.
	charIdx := 0
	for m.col < len(line) {
		r := line[m.col]
		if isWordBoundary(r) && charIdx > 0 {
			break
		}
		fn(charIdx, m.col)
		m.SetCursorColumn(m.col + 1)
		charIdx++
	}
}

// uppercaseRight changes the word to the right to uppercase.
func (m *Model) uppercaseRight() {
	m.doWordRight(func(_ int, i int) {
		m.value[m.row][i] = unicode.ToUpper(m.value[m.row][i])
	})
}

// lowercaseRight changes the word to the right to lowercase.
func (m *Model) lowercaseRight() {
	m.doWordRight(func(_ int, i int) {
		m.value[m.row][i] = unicode.ToLower(m.value[m.row][i])
	})
}

// capitalizeRight changes the word to the right to title case.
func (m *Model) capitalizeRight() {
	m.doWordRight(func(charIdx int, i int) {
		if charIdx == 0 {
			m.value[m.row][i] = unicode.ToTitle(m.value[m.row][i])
		}
	})
}

// LineInfo returns the number of characters from the start of the
// (soft-wrapped) line and the (soft-wrapped) line width.
// scrolling behavior.
// Width returns the width of the textarea.
func (m Model) Width() int {
	return m.width
}

// MoveToBegin moves the cursor to the beginning of the input.
// MoveToEnd moves the cursor to the end of the input.
// line, subsequent calls move up by a full page.
// visible line, subsequent calls move down by a full page.
// and no more.
func (m *Model) SetWidth(w int) {
	// Update prompt width only if there is no prompt function as
	// [SetPromptFunc] updates the prompt width when it is called.
	if m.promptFunc == nil {
		// XXX: Do we even need this or can we calculate the prompt width
		// at render time?
		m.promptWidth = uniseg.StringWidth(m.Prompt)
	}

	// Add base style borders and padding to reserved outer width.
	reservedOuter := m.activeStyle().Base.GetHorizontalFrameSize()

	// Add prompt width to reserved inner width.
	reservedInner := m.promptWidth

	// Add line number width to reserved inner width.
	if m.ShowLineNumbers {
		// XXX: this was originally documented as needing "1 cell" but was,
		// in practice, effectively hardcoded to 2 cells. We can, and should,
		// reduce this to one gap and update the tests accordingly.
		const gap = 2

		// Number of digits plus 1 cell for the margin.
		reservedInner += numDigits(m.MaxHeight) + gap
	}

	// Input width must be at least one more than the reserved inner and outer
	// width. This gives us a minimum input width of 1.
	minWidth := reservedInner + reservedOuter + 1
	inputWidth := max(w, minWidth)

	// Input width must be no more than maximum width.
	if m.MaxWidth > 0 {
		inputWidth = min(inputWidth, m.MaxWidth)
	}

	// Since the width of the viewport and input area is dependent on the width of
	// borders, prompt and line numbers, we need to calculate it by subtracting
	// the reserved width from them.

	m.viewport.SetWidth(inputWidth - reservedOuter)
	m.width = inputWidth - reservedOuter - reservedInner
	m.recalculateHeight()
}

// SetPromptFunc supersedes the Prompt field and sets a dynamic prompt instead.
//
// If the function returns a prompt that is shorter than the specified
// promptWidth, it will be padded to the left. If it returns a prompt that is
// longer, display artifacts may occur; the caller is responsible for computing
// an adequate promptWidth.
func (m *Model) SetPromptFunc(promptWidth int, fn func(PromptInfo) string) {
	m.promptFunc = fn
	m.promptWidth = promptWidth
}

// Height returns the current height of the textarea.
func (m Model) Height() int {
	return m.height
}

// SetHeight sets the height of the textarea.
func (m *Model) SetHeight(h int) {
	if m.MaxHeight > 0 {
		m.height = clamp(h, minHeight, m.MaxHeight)
		m.viewport.SetHeight(clamp(h, minHeight, m.MaxHeight))
	} else {
		m.height = max(h, minHeight)
		m.viewport.SetHeight(max(h, minHeight))
	}

	m.repositionView()
}

// Update is the Bubble Tea update loop.
// View renders the text area in its current state.
// promptView renders a single line of the prompt.
// line number.
// placeholderView returns the prompt and placeholder, if any.
// Blink returns the blink command for the virtual cursor.
func Blink() tea.Msg {
	return cursor.Blink()
}

// Cursor returns a [tea.Cursor] for rendering a real cursor in a Bubble Tea
// program. This requires that [Model.VirtualCursor] is set to false.
//
// Note that you will almost certainly also need to adjust the offset cursor
// position per the textarea's per the textarea's position in the terminal.
//
// Example:
//
//	// In your top-level View function:
//	f := tea.NewFrame(m.textarea.View())
//	f.Cursor = m.textarea.Cursor()
//	f.Cursor.Position.X += offsetX
//	f.Cursor.Position.Y += offsetY
func (m Model) Cursor() *tea.Cursor {
	if m.useVirtualCursor || !m.Focused() {
		return nil
	}

	lineInfo := m.LineInfo()
	w := lipgloss.Width
	baseStyle := m.activeStyle().Base

	xOffset := lineInfo.CharOffset +
		w(m.promptView(0)) +
		w(m.lineNumberView(0, false)) +
		baseStyle.GetMarginLeft() +
		baseStyle.GetPaddingLeft() +
		baseStyle.GetBorderLeftSize()

	yOffset := m.cursorLineNumber() -
		m.viewport.YOffset() +
		baseStyle.GetMarginTop() +
		baseStyle.GetPaddingTop() +
		baseStyle.GetBorderTopSize()

	c := tea.NewCursor(xOffset, yOffset)
	c.Blink = m.styles.Cursor.Blink
	c.Color = m.styles.Cursor.Color
	c.Shape = m.styles.Cursor.Shape
	return c
}

// This accounts for soft wrapped lines.
// This must be kept in sync with the overflow logic in view().
// logical lines, accounting for soft wraps.
// content when DynamicHeight is enabled. It is a no-op otherwise.
// backward compatibility.
// element merges into the current line; subsequent elements become new lines.
// mergeLineBelow merges the current line the cursor is on with the line below.
// mergeLineAbove merges the current line the cursor is on with the line above.
// Paste is a command for pasting from the clipboard into the text input.
func Paste() tea.Msg {
	if err := clipboard.Init(); err != nil {
		return pasteErrMsg{err}
	}
	return pasteMsg(string(clipboard.Read(clipboard.FmtText)))
}

// wrap performs CJK-aware line wrapping on a logical line of runes.
//
// Breaking rules:
//   - CJK characters: each character can start a new visual line (no word accumulation)
//   - Latin/other characters: break at word boundaries (whitespace)
//   - Mixed text: transitions between CJK and Latin are handled correctly
//
// Each visual line contains only the characters that fit within the width.
// No trailing spaces are appended. LineInfo uses the grid to map character
// positions to visual row/column coordinates.
//
// Returns at least one visual line.
func wrap(runes []rune, width int) [][]rune {
	if len(runes) == 0 {
		return [][]rune{{}}
	}
	if width <= 0 {
		return [][]rune{slices.Clone(runes)}
	}

	var (
		lines        [][]rune
		currentLine  []rune
		currentWidth int
	)

	// flushLine emits the current visual line and starts a new empty one.
	flushLine := func() {
		lines = append(lines, currentLine)
		currentLine = nil
		currentWidth = 0
	}

	// addRune appends a single rune, flushing first if it would exceed the width.
	addRune := func(r rune) {
		cw := rw.RuneWidth(r)
		if currentWidth+cw > width && currentWidth > 0 {
			flushLine()
		}
		currentLine = append(currentLine, r)
		currentWidth += cw
	}

	i := 0
	for i < len(runes) {
		r := runes[i]

		// Use uniseg to extract grapheme clusters, which keep multi-rune
		// emoji sequences (ZWJ, variation selectors, skin tone modifiers)
		// together as a single unit. This prevents breaking emoji like
		// 👨‍👩‍👧‍👦 into incomplete fragments.
		rest := string(runes[i:])
		cluster, _, _, _ := uniseg.StepString(rest, 0)
		clusterRunes := len([]rune(cluster))

		switch {
		case clusterRunes > 1 || isCJK(r):
			// Grapheme cluster (emoji sequence) or CJK character:
			// treat as a single unit that breaks individually.
			for j := 0; j < clusterRunes && i < len(runes); j++ {
				addRune(runes[i])
				i++
			}

		case unicode.IsSpace(r):
			// Whitespace is a break point but does not force a wrap by itself.
			// Accumulate consecutive spaces into the current line.
			for i < len(runes) && unicode.IsSpace(runes[i]) {
				addRune(runes[i])
				i++
			}

		default:
			// Non-CJK, non-space: collect the entire "word" (consecutive
			// non-boundary characters), then attempt to fit it on the line.
			wordStart := i
			wordWidth := 0
			for i < len(runes) && !isWordBoundary(runes[i]) {
				wordWidth += rw.RuneWidth(runes[i])
				i++
			}
			word := runes[wordStart:i]

			// If the word doesn't fit on the current line, wrap first
			if currentWidth > 0 && currentWidth+wordWidth > width {
				flushLine()
			}

			// Add each character (handles edge case: single word wider than width)
			for _, wr := range word {
				ww := rw.RuneWidth(wr)
				if currentWidth+ww > width && currentWidth > 0 {
					flushLine()
				}
				currentLine = append(currentLine, wr)
				currentWidth += ww
			}
		}
	}

	// Flush any remaining content
	if len(currentLine) > 0 || len(lines) == 0 {
		lines = append(lines, currentLine)
	}

	return lines
}

// numDigits returns the number of digits in an integer.
func numDigits(n int) int {
	if n == 0 {
		return 1
	}
	count := 0
	num := abs(n)
	for num > 0 {
		count++
		num /= 10
	}
	return count
}

func clamp(v, low, high int) int {
	if high < low {
		low, high = high, low
	}
	return min(high, max(low, v))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// isCJK reports whether the rune belongs to a CJK (Chinese, Japanese, Korean)
// script where each character is its own word boundary for navigation and wrapping.
//
// Covered Unicode blocks:
//   - unicode.Han:     CJK Unified Ideographs (including Ext A–I, compatibility
//     ideographs, CJK radicals, Kangxi radicals)
//   - unicode.Hangul:  Korean syllables and Jamo
//   - unicode.Hiragana: Japanese Hiragana (including digraphs and small variants)
//   - unicode.Katakana: Japanese Katakana (including phonetic extensions U+31F0–U+31FF,
//     half-width forms, and digraphs)
//
// Not covered (by design):
//   - Fullwidth Latin/ASCII (U+FF00–U+FFEF): double-width display, but semantically Latin
//   - CJK Compatibility Forms (U+FE30–U+FE4F): punctuation/vertical forms
//   - CJK Symbols and Punctuation (U+3000–U+303F): punctuation, not word characters
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana)
}

// isWordBoundary reports whether r constitutes a word boundary for CJK-aware
// navigation. A word boundary is a whitespace character or a CJK character
// (each CJK character acts as both a word and a boundary).
//
// This is the central predicate used by wordLeft, wordRight, deleteWordLeft,
// deleteWordRight, Word, and wrap to enforce consistent CJK word-breaking rules.
func isWordBoundary(r rune) bool {
	return unicode.IsSpace(r) || isCJK(r)
}
