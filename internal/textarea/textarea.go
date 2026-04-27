// Package textarea provides a multi-line text input component for Bubble Tea
// applications.
package textarea

import (
	"crypto/sha256"
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"charm.land/bubbles/v2/cursor"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-ego/gse"
	rw "github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
	"xbot/internal/textarea/memoization"
	"xbot/internal/textarea/runeutil"
)

// cjkSeg holds the shared CJK word segmenter, initialized lazily.
var (
	cjkSeg          gse.Segmenter
	cjkSegAvailable bool
	cjkSegOnce      sync.Once
)

func getCJKSegmenter() *gse.Segmenter {
	cjkSegOnce.Do(func() {
		var err error
		cjkSeg, err = gse.NewEmbed()
		if err == nil {
			cjkSegAvailable = true
		}
	})
	if !cjkSegAvailable {
		return nil
	}
	return &cjkSeg
}

// cjkBoundary is a word boundary in a line of text, in rune positions.
type cjkBoundary struct {
	start, end int // [start, end) in rune offsets
}

// cjkWordBoundaries returns the segmented word boundaries for a line
// using gse's CutSearch mode (DAG-based, correct for isolated CJK words).
// Returns nil if the segmenter is unavailable (fall back to single-char).
func cjkWordBoundaries(line []rune) []cjkBoundary {
	seg := getCJKSegmenter()
	if seg == nil {
		return nil
	}
	text := string(line)
	words := seg.CutSearch(text, true)
	boundaries := make([]cjkBoundary, 0, len(words))
	pos := 0
	for _, word := range words {
		wordRunes := []rune(word)
		wordLen := len(wordRunes)
		if wordLen > 0 {
			boundaries = append(boundaries, cjkBoundary{pos, pos + wordLen})
			pos += wordLen
		}
	}
	return boundaries
}

const (
	minHeight        = 1
	defaultHeight    = 6
	defaultWidth     = 40
	defaultCharLimit = 0 // no limit
	defaultMaxHeight = 99
	defaultMaxWidth  = 500

	// XXX: in v2, make max lines dynamic and default max lines configurable.
	maxLines = 10000
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

func (s StyleState) computedCursorLine() lipgloss.Style {
	return s.CursorLine.Inherit(s.Base).Inline(true)
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

// Hash returns a hash of the line.
func (w line) Hash() string {
	v := fmt.Sprintf("%s:%d", string(w.runes), w.width)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(v)))
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

	// cjkWordCache holds segmented word boundaries for the current line.
	// Nil if not yet segmented or segmenter unavailable.
	cjkWordCache []cjkBoundary
	// cjkWordCacheLine is the line text this cache was built for.
	// When the line changes, the cache is invalidated (set to "").
	cjkWordCacheLine string
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
		cache:                memoization.NewMemoCache[line, [][]rune](maxLines),
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

	// Pre-warm the CJK segmenter in the background so the first
	// Ctrl+Arrow keypress doesn't block on dictionary loading.
	go getCJKSegmenter()

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
func (m *Model) SetValue(s string) {
	m.Reset()
	m.InsertString(s)
	m.recalculateHeight()
}

// InsertString inserts a string at the cursor position.
func (m *Model) InsertString(s string) {
	m.insertRunesFromUserInput([]rune(s))
	m.recalculateHeight()
}

// InsertRune inserts a rune at the cursor position.
func (m *Model) InsertRune(r rune) {
	m.insertRunesFromUserInput([]rune{r})
	m.recalculateHeight()
}

// insertRunesFromUserInput inserts runes at the current cursor position.
func (m *Model) insertRunesFromUserInput(runes []rune) {
	// Clean up any special characters in the input provided by the
	// clipboard. This avoids bugs due to e.g. tab characters and
	// whatnot.
	runes = m.san().Sanitize(runes)

	if m.CharLimit > 0 {
		availSpace := m.CharLimit - m.Length()
		// If the char limit's been reached, cancel.
		if availSpace <= 0 {
			return
		}
		// If there's not enough space to paste the whole thing cut the pasted
		// runes down so they'll fit.
		if availSpace < len(runes) {
			runes = runes[:availSpace]
		}
	}

	// Split the input into lines.
	var lines [][]rune
	lstart := 0
	for i := range runes {
		if runes[i] == '\n' {
			// Queue a line to become a new row in the text area below.
			// Beware to clamp the max capacity of the slice, to ensure no
			// data from different rows get overwritten when later edits
			// will modify this line.
			lines = append(lines, runes[lstart:i:i])
			lstart = i + 1
		}
	}
	if lstart <= len(runes) {
		// The last line did not end with a newline character.
		// Take it now.
		lines = append(lines, runes[lstart:])
	}

	// Obey the maximum line limit.
	if maxLines > 0 && len(m.value)+len(lines)-1 > maxLines {
		allowedHeight := max(0, maxLines-len(m.value)+1)
		lines = lines[:allowedHeight]
	}

	// Obey MaxContentHeight in visual rows when set.
	if m.MaxContentHeight > 0 {
		budget := m.MaxContentHeight - m.totalVisualLines()
		// Trim lines from the end until we fit within the budget.
		for len(lines) > 1 && m.visualLinesForInsert(lines) > budget {
			lines = lines[:len(lines)-1]
		}
		if m.visualLinesForInsert(lines) > budget {
			return
		}
	}

	if len(lines) == 0 {
		// Nothing left to insert.
		return
	}

	// Save the remainder of the original line at the current
	// cursor position.
	tail := make([]rune, len(m.value[m.row][m.col:]))
	copy(tail, m.value[m.row][m.col:])

	// Paste the first line at the current cursor position.
	m.value[m.row] = append(m.value[m.row][:m.col], lines[0]...)
	m.col += len(lines[0])

	if numExtraLines := len(lines) - 1; numExtraLines > 0 {
		// Add the new lines.
		// We try to reuse the slice if there's already space.
		var newGrid [][]rune
		if cap(m.value) >= len(m.value)+numExtraLines {
			// Can reuse the extra space.
			newGrid = m.value[:len(m.value)+numExtraLines]
		} else {
			// No space left; need a new slice.
			newGrid = make([][]rune, len(m.value)+numExtraLines)
			copy(newGrid, m.value[:m.row+1])
		}
		// Add all the rows that were after the cursor in the original
		// grid at the end of the new grid.
		copy(newGrid[m.row+1+numExtraLines:], m.value[m.row+1:])
		m.value = newGrid
		// Insert all the new lines in the middle.
		for _, l := range lines[1:] {
			m.row++
			m.value[m.row] = l
			m.col = len(l)
		}
	}

	// Finally add the tail at the end of the last line inserted.
	m.value[m.row] = append(m.value[m.row], tail...)

	m.SetCursorColumn(m.col)
}

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
func (m Model) Line() int {
	return m.row
}

// Column returns the 0-indexed column position of the cursor.
func (m Model) Column() int {
	return m.col
}

// ScrollYOffset returns the Y offset (top row) index of the current view, which
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
func (m *Model) setCursorLineRelative(delta int) {
	if delta == 0 {
		return
	}

	li := m.LineInfo()
	charOffset := max(m.lastCharOffset, li.CharOffset)
	m.lastCharOffset = charOffset

	// Without trailing spaces in the grid, StartColumn+Width gives the first
	// character of the next visual line, and StartColumn-1 steps back across
	// a wrap boundary.
	const trailingStep = 1

	if delta > 0 { //nolint:nestif
		// Moving down.
		for range delta {
			if li.RowOffset+1 >= li.Height && m.row < len(m.value)-1 {
				m.row++
				m.col = 0
			} else {
				// Move the cursor to the start of the next virtual line.
				m.col = min(li.StartColumn+li.Width, len(m.value[m.row])-1)
			}
			li = m.LineInfo()
		}
	} else {
		// Moving up.
		for range -delta {
			if li.RowOffset <= 0 && m.row > 0 {
				m.row--
				m.col = len(m.value[m.row])
			} else {
				// Move the cursor to the end of the previous line.
				m.col = li.StartColumn - trailingStep
			}
			li = m.LineInfo()
		}
	}

	nli := m.LineInfo()
	m.col = nli.StartColumn

	if nli.Width <= 0 {
		m.repositionView()
		return
	}

	offset := 0
	for offset < charOffset {
		if m.row >= len(m.value) || m.col >= len(m.value[m.row]) || offset >= nli.CharWidth-1 {
			break
		}
		offset += rw.RuneWidth(m.value[m.row][m.col])
		m.col++
	}
	m.repositionView()
}

// CursorDown moves the cursor down by one line.
func (m *Model) CursorDown() {
	m.setCursorLineRelative(1)
}

// CursorUp moves the cursor up by one line.
func (m *Model) CursorUp() {
	m.setCursorLineRelative(-1)
}

// SetCursorColumn moves the cursor to the given position. If the position is
// out of bounds the cursor will be moved to the start or end accordingly.
func (m *Model) SetCursorColumn(col int) {
	m.col = clamp(col, 0, len(m.value[m.row]))
	// Any time that we move the cursor horizontally we need to reset the last
	// offset so that the horizontal position when navigating is adjusted.
	m.lastCharOffset = 0
}

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

// cjkWordBounds returns the segmented CJK word boundaries for the current
// line, recomputing the cache when the line text changes. Returns nil if
// the segmenter is unavailable.
func (m *Model) cjkWordBounds() []cjkBoundary {
	line := string(m.value[m.row])
	if m.cjkWordCacheLine != line {
		m.cjkWordCache = cjkWordBoundaries(m.value[m.row])
		m.cjkWordCacheLine = line
	}
	return m.cjkWordCache
}

// tokenRight returns the column after skipping one gse token to the right
// from the given column. It skips spaces first, then jumps to the end of the
// current token. Returns col unchanged if gse is unavailable or at end of line.
func (m *Model) tokenRight(col int) int {
	line := m.value[m.row]
	// Skip spaces forward
	for col < len(line) && unicode.IsSpace(line[col]) {
		col++
	}
	if col >= len(line) {
		return col
	}
	// Find the token containing col and jump to its end
	bounds := m.cjkWordBounds()
	if bounds != nil {
		for _, b := range bounds {
			if b.start <= col && col < b.end {
				return b.end
			}
		}
		// col is at a gap between tokens (shouldn't happen), move to next token start
		for _, b := range bounds {
			if b.start > col {
				return b.start
			}
		}
		return len(line)
	}
	// Fallback: no segmenter, use character-by-character with isWordBoundary
	for col < len(line) {
		r := line[col]
		if isWordBoundary(r) {
			break
		}
		col++
	}
	return col
}

// tokenLeft returns the column after skipping one gse token to the left
// from the given column. It skips spaces first, then jumps to the start of the
// previous token. Returns col unchanged if gse is unavailable or at start of line.
func (m *Model) tokenLeft(col int) int {
	line := m.value[m.row]
	// Skip spaces backward
	for col > 0 && unicode.IsSpace(line[col-1]) {
		col--
	}
	if col <= 0 {
		return 0
	}
	// Find the token containing col-1 and jump to its start
	bounds := m.cjkWordBounds()
	if bounds != nil {
		for _, b := range bounds {
			if b.start <= col-1 && col-1 < b.end {
				return b.start
			}
		}
		// col-1 is at a gap (shouldn't happen), find preceding token
		prev := 0
		for _, b := range bounds {
			if b.end <= col-1 {
				prev = b.start
			} else {
				break
			}
		}
		return prev
	}
	// Fallback: no segmenter, use character-by-character with isWordBoundary
	for col > 0 {
		prev := line[col-1]
		if isWordBoundary(prev) {
			break
		}
		col--
	}
	return col
}

// Word returns the word at the cursor position.
//
// Uses gse token boundaries when available for accurate CJK + punctuation
// identification. Falls back to isWordBoundary (whitespace or CJK characters)
// when the segmenter is unavailable.
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

	// Try gse token-based lookup
	bounds := m.cjkWordBounds()
	for _, b := range bounds {
		if b.start <= col && col < b.end {
			return string(line[b.start:b.end])
		}
	}

	// Fallback: no segmenter or col not in any token, use isWordBoundary
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
func (m *Model) deleteBeforeCursor() {
	m.value[m.row] = m.value[m.row][m.col:]
	m.SetCursorColumn(0)
}

// deleteAfterCursor deletes all text after the cursor. Returns whether or not
// the cursor blink should be reset. If input is masked delete everything after
// the cursor so as not to reveal word breaks in the masked input.
func (m *Model) deleteAfterCursor() {
	m.value[m.row] = m.value[m.row][:m.col]
	m.SetCursorColumn(len(m.value[m.row]))
}

// transposeLeft exchanges the runes at the cursor and immediately
// before. No-op if the cursor is at the beginning of the line.  If
// the cursor is not at the end of the line yet, moves the cursor to
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
// Uses gse token boundaries when available for accurate CJK+ punctuation navigation.
func (m *Model) deleteWordLeft() {
	if m.col == 0 || len(m.value[m.row]) == 0 {
		return
	}

	oldCol := m.col
	m.SetCursorColumn(m.tokenLeft(m.col))

	if oldCol > len(m.value[m.row]) {
		m.value[m.row] = m.value[m.row][:m.col]
	} else {
		m.value[m.row] = append(m.value[m.row][:m.col], m.value[m.row][oldCol:]...)
	}
}

// deleteWordRight deletes the word right to the cursor.
// Uses gse token boundaries when available for accurate CJK+ punctuation navigation.
func (m *Model) deleteWordRight() {
	if m.col >= len(m.value[m.row]) || len(m.value[m.row]) == 0 {
		return
	}

	oldCol := m.col
	endCol := m.tokenRight(m.col)

	if endCol > len(m.value[m.row]) {
		m.value[m.row] = m.value[m.row][:oldCol]
	} else {
		m.value[m.row] = append(m.value[m.row][:oldCol], m.value[m.row][endCol:]...)
	}

	m.SetCursorColumn(oldCol)
}

// characterRight moves the cursor one character to the right.
func (m *Model) characterRight() {
	if m.col < len(m.value[m.row]) {
		m.SetCursorColumn(m.col + 1)
	} else {
		if m.row < len(m.value)-1 {
			m.row++
			m.CursorStart()
		}
	}
}

// characterLeft moves the cursor one character to the left.
// If insideLine is set, the cursor is moved to the last
// character in the previous line, instead of one past that.
func (m *Model) characterLeft(insideLine bool) {
	if m.col == 0 && m.row != 0 {
		m.row--
		m.CursorEnd()
		if !insideLine {
			return
		}
	}
	if m.col > 0 {
		m.SetCursorColumn(m.col - 1)
	}
}

// wordLeft moves the cursor one word to the left.
// Uses gse token boundaries when available; falls back to isWordBoundary
// (whitespace or CJK characters) when the segmenter is unavailable.
func (m *Model) wordLeft() {
	m.SetCursorColumn(m.tokenLeft(m.col))
}

// wordRight moves the cursor one word to the right.
// Uses gse token boundaries when available; falls back to isWordBoundary
// (whitespace or CJK characters) when the segmenter is unavailable.
func (m *Model) wordRight() {
	m.doWordRight(func(int, int) { /* nothing */ })
}

// doWordRight moves the cursor one word to the right, invoking fn for each
// character traversed. The fn callback receives the character index within the
// word and the absolute column position. This enables word-transform operations
// (uppercase, lowercase, capitalize) to modify characters as the cursor moves.
//
// Uses gse token boundaries when available for accurate CJK + punctuation
// navigation. Falls back to isWordBoundary when the segmenter is unavailable.
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

	// Try gse token-based navigation
	bounds := m.cjkWordBounds()
	if bounds != nil {
		for _, b := range bounds {
			if b.start <= col && col < b.end {
				// Walk through the token character by character, calling fn
				for i := b.start; i < b.end; i++ {
					fn(i-b.start, i)
				}
				m.SetCursorColumn(b.end)
				return
			}
		}
		// col is at a gap (shouldn't happen with CutSearch), just advance
		m.SetCursorColumn(col + 1)
		return
	}

	// Fallback: no segmenter, use character-by-character with isWordBoundary
	charIdx := 0
	for m.col < len(line) {
		r := line[m.col]
		if isWordBoundary(r) {
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
func (m Model) LineInfo() LineInfo {
	grid := m.memoizedWrap(m.value[m.row], m.width)

	// Find out which line we are currently on. This can be determined by the
	// m.col and counting the number of runes that we need to skip.
	var counter int
	for i, line := range grid {
		// We've found the line that we are on
		if counter+len(line) == m.col && i+1 < len(grid) {
			// We wrap around to the next line if we are at the end of the
			// previous line so that we can be at the very beginning of the row
			return LineInfo{
				CharOffset:   0,
				ColumnOffset: 0,
				Height:       len(grid),
				RowOffset:    i + 1,
				StartColumn:  m.col,
				Width:        len(grid[i+1]),
				CharWidth:    uniseg.StringWidth(string(line)),
			}
		}

		if counter+len(line) >= m.col {
			return LineInfo{
				CharOffset:   uniseg.StringWidth(string(line[:max(0, m.col-counter)])),
				ColumnOffset: m.col - counter,
				Height:       len(grid),
				RowOffset:    i,
				StartColumn:  counter,
				Width:        len(line),
				CharWidth:    uniseg.StringWidth(string(line)),
			}
		}

		counter += len(line)
	}
	return LineInfo{}
}

// repositionView repositions the view of the viewport based on the defined
// scrolling behavior.
func (m *Model) repositionView() {
	minimum := m.viewport.YOffset()
	maximum := minimum + m.viewport.Height() - 1
	if row := m.cursorLineNumber(); row < minimum {
		m.viewport.ScrollUp(minimum - row)
	} else if row > maximum {
		m.viewport.ScrollDown(row - maximum)
	}
}

// Width returns the width of the textarea.
func (m Model) Width() int {
	return m.width
}

// MoveToBegin moves the cursor to the beginning of the input.
func (m *Model) MoveToBegin() {
	m.row = 0
	m.SetCursorColumn(0)
	m.repositionView()
}

// MoveToEnd moves the cursor to the end of the input.
func (m *Model) MoveToEnd() {
	m.row = len(m.value) - 1
	m.SetCursorColumn(len(m.value[m.row]))
	m.repositionView()
}

// PageUp moves the cursor up by one page. First call snaps to the first visible
// line, subsequent calls move up by a full page.
func (m *Model) PageUp() {
	// If not on the first visible line, snap to it.
	if offset := m.viewport.YOffset() - m.cursorLineNumber(); offset < 0 {
		m.setCursorLineRelative(offset)
		return
	}

	// Already on first visible line, move up by a full page.
	m.setCursorLineRelative(-m.height)
}

// PageDown moves the cursor down by one page. First call snaps to the last
// visible line, subsequent calls move down by a full page.
func (m *Model) PageDown() {
	// If not on the last visible line, snap to it.
	if offset := m.cursorLineNumber() - m.viewport.YOffset(); offset < m.height-1 {
		m.setCursorLineRelative(m.height - 1 - offset)
		return
	}

	// Already on last visible line, move down by a full page.
	m.setCursorLineRelative(m.height)
}

// SetWidth sets the width of the textarea to fit exactly within the given width.
// This means that the textarea will account for the width of the prompt and
// whether or not line numbers are being shown.
//
// Ensure that SetWidth is called after setting the Prompt and ShowLineNumbers,
// It is important that the width of the textarea be exactly the given width
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
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.focus {
		m.virtualCursor.Blur()
		return m, nil
	}

	// Used to determine if the cursor should blink.
	oldRow, oldCol := m.cursorLineNumber(), m.col

	var cmds []tea.Cmd

	if m.value[m.row] == nil {
		m.value[m.row] = make([]rune, 0)
	}

	if m.MaxHeight > 0 && m.MaxHeight != m.cache.Capacity() {
		m.cache = memoization.NewMemoCache[line, [][]rune](m.MaxHeight)
	}

	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.insertRunesFromUserInput([]rune(msg.Content))
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.KeyMap.DeleteAfterCursor):
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			if m.col >= len(m.value[m.row]) {
				m.mergeLineBelow(m.row)
				break
			}
			m.deleteAfterCursor()
		case key.Matches(msg, m.KeyMap.DeleteBeforeCursor):
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			if m.col <= 0 {
				m.mergeLineAbove(m.row)
				break
			}
			m.deleteBeforeCursor()
		case key.Matches(msg, m.KeyMap.DeleteCharacterBackward):
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			if m.col <= 0 {
				m.mergeLineAbove(m.row)
				break
			}
			if len(m.value[m.row]) > 0 {
				m.value[m.row] = append(m.value[m.row][:max(0, m.col-1)], m.value[m.row][m.col:]...)
				if m.col > 0 {
					m.SetCursorColumn(m.col - 1)
				}
			}
		case key.Matches(msg, m.KeyMap.DeleteCharacterForward):
			if len(m.value[m.row]) > 0 && m.col < len(m.value[m.row]) {
				m.value[m.row] = slices.Delete(m.value[m.row], m.col, m.col+1)
			}
			if m.col >= len(m.value[m.row]) {
				m.mergeLineBelow(m.row)
				break
			}
		case key.Matches(msg, m.KeyMap.DeleteWordBackward):
			if m.col <= 0 {
				m.mergeLineAbove(m.row)
				break
			}
			m.deleteWordLeft()
		case key.Matches(msg, m.KeyMap.DeleteWordForward):
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			if m.col >= len(m.value[m.row]) {
				m.mergeLineBelow(m.row)
				break
			}
			m.deleteWordRight()
		case key.Matches(msg, m.KeyMap.InsertNewline):
			if m.atContentLimit() {
				return m, nil
			}
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			m.splitLine(m.row, m.col)
		case key.Matches(msg, m.KeyMap.LineEnd):
			m.CursorEnd()
		case key.Matches(msg, m.KeyMap.LineStart):
			m.CursorStart()
		case key.Matches(msg, m.KeyMap.CharacterForward):
			m.characterRight()
		case key.Matches(msg, m.KeyMap.LineNext):
			m.CursorDown()
		case key.Matches(msg, m.KeyMap.WordForward):
			m.wordRight()
		case key.Matches(msg, m.KeyMap.Paste):
			return m, Paste
		case key.Matches(msg, m.KeyMap.CharacterBackward):
			m.characterLeft(false /* insideLine */)
		case key.Matches(msg, m.KeyMap.LinePrevious):
			m.CursorUp()
		case key.Matches(msg, m.KeyMap.WordBackward):
			m.wordLeft()
		case key.Matches(msg, m.KeyMap.InputBegin):
			m.MoveToBegin()
		case key.Matches(msg, m.KeyMap.InputEnd):
			m.MoveToEnd()
		case key.Matches(msg, m.KeyMap.PageUp):
			m.PageUp()
		case key.Matches(msg, m.KeyMap.PageDown):
			m.PageDown()
		case key.Matches(msg, m.KeyMap.LowercaseWordForward):
			m.lowercaseRight()
		case key.Matches(msg, m.KeyMap.UppercaseWordForward):
			m.uppercaseRight()
		case key.Matches(msg, m.KeyMap.CapitalizeWordForward):
			m.capitalizeRight()
		case key.Matches(msg, m.KeyMap.TransposeCharacterBackward):
			m.transposeLeft()

		default:
			m.insertRunesFromUserInput([]rune(msg.Text))
		}

	case pasteMsg:
		m.insertRunesFromUserInput([]rune(msg))

	case pasteErrMsg:
		m.Err = msg
	}

	m.recalculateHeight()

	// Make sure we set the content of the viewport before updating it.
	view := m.view()
	m.viewport.SetContent(view)
	vp, cmd := m.viewport.Update(msg)
	m.viewport = &vp
	cmds = append(cmds, cmd)

	if m.useVirtualCursor {
		m.virtualCursor, cmd = m.virtualCursor.Update(msg)

		// If the cursor has moved, reset the blink state. This is a small UX
		// nuance that makes cursor movement obvious and feel snappy.
		newRow, newCol := m.cursorLineNumber(), m.col
		if (newRow != oldRow || newCol != oldCol) && m.virtualCursor.Mode() == cursor.CursorBlink {
			m.virtualCursor.IsBlinked = false
			cmd = m.virtualCursor.Blink()
		}
		cmds = append(cmds, cmd)
	}

	m.repositionView()

	return m, tea.Batch(cmds...)
}

func (m *Model) view() string {
	if len(m.Value()) == 0 && m.row == 0 && m.col == 0 && m.Placeholder != "" {
		return m.placeholderView()
	}
	m.virtualCursor.TextStyle = m.activeStyle().computedCursorLine()

	var (
		s        strings.Builder
		style    lipgloss.Style
		newLines int

		lineInfo = m.LineInfo()
		styles   = m.activeStyle()
	)

	displayLine := 0
	for l, line := range m.value {
		wrappedLines := m.memoizedWrap(line, m.width)

		if m.row == l {
			style = styles.computedCursorLine()
		} else {
			style = styles.computedText()
		}

		for wl, wrappedLine := range wrappedLines {
			prompt := m.promptView(displayLine)
			prompt = styles.computedPrompt().Render(prompt)
			s.WriteString(style.Render(prompt))
			displayLine++

			if m.ShowLineNumbers {
				if wl == 0 { // logical line
					s.WriteString(m.lineNumberView(l+1, m.row == l))
				} else { // soft-wrapped continuation
					s.WriteString(m.lineNumberView(-1, m.row == l))
				}
			}

			strwidth := uniseg.StringWidth(string(wrappedLine))
			padding := m.width - strwidth
			if m.row == l && lineInfo.RowOffset == wl {
				co := lineInfo.ColumnOffset
				s.WriteString(style.Render(string(wrappedLine[:co])))
				if co >= len(wrappedLine) {
					// Cursor is at or beyond the last visible character.
					// Render an empty-cursor placeholder so the cursor sits
					// *after* the last glyph instead of highlighting it.
					m.virtualCursor.SetChar(" ")
					s.WriteString(m.virtualCursor.View())
				} else {
					m.virtualCursor.SetChar(string(wrappedLine[co]))
					s.WriteString(style.Render(m.virtualCursor.View()))
					s.WriteString(style.Render(string(wrappedLine[co+1:])))
				}
			} else {
				s.WriteString(style.Render(string(wrappedLine)))
			}
			s.WriteString(style.Render(strings.Repeat(" ", max(0, padding))))
			s.WriteRune('\n')
			newLines++
		}
	}

	// Always show at least `m.Height` lines at all times.
	// To do this we can simply pad out a few extra new lines in the view.
	for range m.height {
		s.WriteString(m.promptView(displayLine))
		displayLine++

		// Write end of buffer content
		leftGutter := string(m.EndOfBufferCharacter)
		rightGapWidth := m.Width() - uniseg.StringWidth(leftGutter)
		rightGap := strings.Repeat(" ", max(0, rightGapWidth))
		s.WriteString(styles.computedEndOfBuffer().Render(leftGutter + rightGap))
		s.WriteRune('\n')
	}

	return s.String()
}

// View renders the text area in its current state.
func (m Model) View() string {
	// XXX: This is a workaround for the case where the viewport hasn't
	// been initialized yet like during the initial render. In that case,
	// we need to render the view again because Update hasn't been called
	// yet to set the content of the viewport.
	m.viewport.SetContent(m.view())
	view := m.viewport.View()
	styles := m.activeStyle()
	return styles.Base.Render(view)
}

// promptView renders a single line of the prompt.
func (m Model) promptView(displayLine int) (prompt string) {
	prompt = m.Prompt
	if m.promptFunc == nil {
		return prompt
	}
	prompt = m.promptFunc(PromptInfo{
		LineNumber: displayLine,
		Focused:    m.focus,
	})
	width := lipgloss.Width(prompt)
	if width < m.promptWidth {
		prompt = fmt.Sprintf("%*s%s", m.promptWidth-width, "", prompt)
	}

	return m.activeStyle().computedPrompt().Render(prompt)
}

// lineNumberView renders the line number.
//
// If the argument is less than 0, a space styled as a line number is returned
// instead. Such cases are used for soft-wrapped lines.
//
// The second argument indicates whether this line number is for a 'cursorline'
// line number.
func (m Model) lineNumberView(n int, isCursorLine bool) (str string) {
	if !m.ShowLineNumbers {
		return ""
	}

	if n <= 0 {
		str = " "
	} else {
		str = strconv.Itoa(n)
	}

	// XXX: is textStyle really necessary here?
	textStyle := m.activeStyle().computedText()
	lineNumberStyle := m.activeStyle().computedLineNumber()
	if isCursorLine {
		textStyle = m.activeStyle().computedCursorLine()
		lineNumberStyle = m.activeStyle().computedCursorLineNumber()
	}

	// Format line number dynamically based on the maximum number of lines.
	digits := len(strconv.Itoa(m.MaxHeight))
	str = fmt.Sprintf(" %*v ", digits, str)

	return textStyle.Render(lineNumberStyle.Render(str))
}

// placeholderView returns the prompt and placeholder, if any.
func (m Model) placeholderView() string {
	var (
		s      strings.Builder
		p      = m.Placeholder
		styles = m.activeStyle()
	)
	// word wrap lines
	pwordwrap := ansi.Wordwrap(p, m.width, "")
	// hard wrap lines (handles lines that could not be word wrapped)
	pwrap := ansi.Hardwrap(pwordwrap, m.width, true)
	// split string by new lines
	plines := strings.Split(strings.TrimSpace(pwrap), "\n")

	for i := range m.height {
		isLineNumber := len(plines) > i

		lineStyle := styles.computedPlaceholder()
		if len(plines) > i {
			lineStyle = styles.computedCursorLine()
		}

		// render prompt
		prompt := m.promptView(i)
		prompt = styles.computedPrompt().Render(prompt)
		s.WriteString(lineStyle.Render(prompt))

		// when show line numbers enabled:
		// - render line number for only the cursor line
		// - indent other placeholder lines
		// this is consistent with vim with line numbers enabled
		if m.ShowLineNumbers {
			var ln int

			switch {
			case i == 0:
				ln = i + 1
				fallthrough
			case len(plines) > i:
				s.WriteString(m.lineNumberView(ln, isLineNumber))
			default:
			}
		}

		switch {
		// first line
		case i == 0:
			// first character of first line as cursor with character
			m.virtualCursor.TextStyle = styles.computedPlaceholder()

			ch, rest, _, _ := uniseg.FirstGraphemeClusterInString(plines[0], 0)
			m.virtualCursor.SetChar(ch)
			s.WriteString(lineStyle.Render(m.virtualCursor.View()))

			// the rest of the first line
			s.WriteString(lineStyle.Render(styles.computedPlaceholder().Render(rest)))

			// extend the first line with spaces to fill the width, so that
			// the entire line is filled when cursorline is enabled.
			gap := strings.Repeat(" ", max(0, m.width-lipgloss.Width(plines[0])))
			s.WriteString(lineStyle.Render(gap))
		// remaining lines
		case len(plines) > i:
			// current line placeholder text
			if len(plines) > i {
				placeholderLine := plines[i]
				gap := strings.Repeat(" ", max(0, m.width-uniseg.StringWidth(plines[i])))
				s.WriteString(lineStyle.Render(placeholderLine + gap))
			}
		default:
			// end of line buffer character
			eob := styles.computedEndOfBuffer().Render(string(m.EndOfBufferCharacter))
			s.WriteString(eob)
		}

		// terminate with new line
		s.WriteRune('\n')
	}

	m.viewport.SetContent(s.String())
	return styles.Base.Render(m.viewport.View())
}

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

func (m Model) memoizedWrap(runes []rune, width int) [][]rune {
	input := line{runes: runes, width: width}
	if v, ok := m.cache.Get(input); ok {
		return v
	}
	v := wrap(runes, width)
	m.cache.Set(input, v)
	return v
}

// cursorLineNumber returns the line number that the cursor is on.
// This accounts for soft wrapped lines.
func (m Model) cursorLineNumber() int {
	line := 0
	for i := range m.row {
		// Calculate the number of lines that the current line will be split
		// into.
		line += len(m.memoizedWrap(m.value[i], m.width))
	}
	line += m.LineInfo().RowOffset
	return line
}

// totalVisualLines returns the total number of display lines across all
// logical lines, accounting for soft wraps.
func (m *Model) totalVisualLines() int {
	n := 0
	for _, line := range m.value {
		n += len(m.memoizedWrap(line, m.width))
	}
	return n
}

// recalculateHeight recomputes and applies the textarea height based on
// content when DynamicHeight is enabled. It is a no-op otherwise.
func (m *Model) recalculateHeight() {
	if !m.DynamicHeight {
		return
	}
	minH := max(m.MinHeight, minHeight)
	total := m.totalVisualLines()
	h := max(total, minH)
	if m.MaxHeight > 0 {
		h = min(h, m.MaxHeight)
	}
	if maxOffset := total - h; m.viewport.YOffset() > maxOffset {
		m.viewport.SetYOffset(max(0, maxOffset))
	}
	m.SetHeight(h)
}

// atContentLimit reports whether the textarea has reached its content limit.
// When MaxContentHeight is set (> 0), it checks total visual lines.
// Otherwise it falls back to the legacy MaxHeight logical-line check for
// backward compatibility.
func (m *Model) atContentLimit() bool {
	if m.MaxContentHeight > 0 {
		return m.totalVisualLines() >= m.MaxContentHeight
	}
	return m.MaxHeight > 0 && len(m.value) >= m.MaxHeight
}

// visualLinesForInsert estimates how many additional visual lines would result
// from inserting the given lines at the current cursor position. The first
// element merges into the current line; subsequent elements become new lines.
func (m *Model) visualLinesForInsert(lines [][]rune) int {
	if len(lines) == 0 {
		return 0
	}

	// The current row's visual line count before insertion.
	currentRowVisual := len(m.memoizedWrap(m.value[m.row], m.width))

	// Simulate merging the first paste line into the current row.
	merged := make([]rune, m.col+len(lines[0]))
	copy(merged, m.value[m.row][:m.col])
	copy(merged[m.col:], lines[0])
	if len(lines) == 1 {
		merged = append(merged, m.value[m.row][m.col:]...)
	}
	delta := len(m.memoizedWrap(merged, m.width)) - currentRowVisual

	// Each additional line (beyond the first) is a new logical line.
	// lines[0] is already accounted for in the merged calculation above.
	for i, content := range lines {
		if i == 0 {
			// Skip: already counted in merged delta above.
			// If there's only one line, merged already includes the tail.
			continue
		}
		if i == len(lines)-1 {
			content = append(content, m.value[m.row][m.col:]...)
		}
		delta += len(m.memoizedWrap(content, m.width))
	}

	return delta
}

// mergeLineBelow merges the current line the cursor is on with the line below.
func (m *Model) mergeLineBelow(row int) {
	if row >= len(m.value)-1 {
		return
	}

	// To perform a merge, we will need to combine the two lines and then
	m.value[row] = append(m.value[row], m.value[row+1]...)

	// Shift all lines up by one
	for i := row + 1; i < len(m.value)-1; i++ {
		m.value[i] = m.value[i+1]
	}

	// And, remove the last line
	if len(m.value) > 0 {
		m.value = m.value[:len(m.value)-1]
	}
}

// mergeLineAbove merges the current line the cursor is on with the line above.
func (m *Model) mergeLineAbove(row int) {
	if row <= 0 {
		return
	}

	m.col = len(m.value[row-1])
	m.row = m.row - 1

	// To perform a merge, we will need to combine the two lines and then
	m.value[row-1] = append(m.value[row-1], m.value[row]...)

	// Shift all lines up by one
	for i := row; i < len(m.value)-1; i++ {
		m.value[i] = m.value[i+1]
	}

	// And, remove the last line
	if len(m.value) > 0 {
		m.value = m.value[:len(m.value)-1]
	}
}

func (m *Model) splitLine(row, col int) {
	// To perform a split, take the current line and keep the content before
	// the cursor, take the content after the cursor and make it the content of
	// the line underneath, and shift the remaining lines down by one
	head, tailSrc := m.value[row][:col], m.value[row][col:]
	tail := make([]rune, len(tailSrc))
	copy(tail, tailSrc)

	m.value = append(m.value[:row+1], m.value[row:]...)

	m.value[row] = head
	m.value[row+1] = tail

	m.col = 0
	m.row++
}

// Paste is a command for pasting from the clipboard into the text input.
func Paste() tea.Msg {
	str, err := clipboard.ReadAll()
	if err != nil {
		return pasteErrMsg{err}
	}
	return pasteMsg(str)
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
		switch {
		case isCJK(r):
			// CJK characters break individually: each character is a potential
			// line break point. No word accumulation is needed.
			addRune(r)
			i++

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
