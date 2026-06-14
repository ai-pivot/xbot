//go:build !noui

package cli

import (
	"reflect"
	"unsafe"

	"charm.land/bubbles/v2/viewport"
)

var (
	viewportLinesOffset            uintptr
	viewportLongestLineWidthOffset uintptr
)

func init() {
	// Compute field offsets via reflect at init time.
	// This avoids hardcoding offsets that would break if viewport.Model changes.
	t := reflect.TypeOf(viewport.Model{})
	if f, ok := t.FieldByName("lines"); ok {
		viewportLinesOffset = f.Offset
	}
	if f, ok := t.FieldByName("longestLineWidth"); ok {
		viewportLongestLineWidthOffset = f.Offset
	}
}

// viewportSetLinesBypassMaxWidth sets viewport lines and longestLineWidth
// directly, bypassing the expensive maxLineWidth() call inside
// viewport.Model.SetContentLines. That function calls ansi.StringWidth on
// every line to find the widest — with ~9MB of content this accounts for
// ~49% of CPU during 100ms ticks (pprof 2026-05-23). Since we already wrap
// lines to chatWidth, we know the max width from our own wrap pass.
//
// The caller guarantees lines contain no embedded \r or \n (they've already
// been split and wrapped), so we skip the ContainsAny scan that was eating
// another ~13% CPU.
func viewportSetLinesBypassMaxWidth(vp *viewport.Model, lines []string, maxW int) {
	// Normalize empty content
	if len(lines) == 1 && len(lines[0]) == 0 {
		setViewportLines(vp, nil)
		setViewportLongestLineWidth(vp, 0)
		vp.ClearHighlights()
		return
	}

	// Caller guarantees no embedded \r\n — skip the scan entirely.
	setViewportLines(vp, lines)
	setViewportLongestLineWidth(vp, maxW)
	vp.ClearHighlights()
}

// setViewportLines sets the unexported 'lines' field of viewport.Model.
func setViewportLines(vp *viewport.Model, lines []string) {
	ptr := (*[]string)(unsafe.Pointer(uintptr(unsafe.Pointer(vp)) + viewportLinesOffset))
	*ptr = lines
}

// setViewportLongestLineWidth sets the unexported 'longestLineWidth' field.
func setViewportLongestLineWidth(vp *viewport.Model, w int) {
	ptr := (*int)(unsafe.Pointer(uintptr(unsafe.Pointer(vp)) + viewportLongestLineWidthOffset))
	*ptr = w
}
