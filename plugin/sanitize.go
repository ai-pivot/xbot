package plugin

import "regexp"

// ANSI escape sequence pattern for sanitization.
// Matches CSI sequences (\x1b[...), OSC sequences (\x1b]...), and other escape codes.
var ansiEscapeRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b\][^\x1b]*\x1b\\|\x1b[PX^_].*?\x1b\\|\x1b.`)

// controlCharRegex matches non-printable control characters (except \t, \n).
var controlCharRegex = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

// SanitizeWidgetOutput strips ANSI escape sequences and non-printable control
// characters from a widget output string. This is applied as a safety measure
// for gRPC/WASM runtime plugins before their output reaches the terminal.
// Native plugins are trusted and skip sanitization (they use WidgetSpan API).
func SanitizeWidgetOutput(input string) string {
	// Strip ANSI escape sequences
	cleaned := ansiEscapeRegex.ReplaceAllString(input, "")
	// Strip control characters (keep \t and \n)
	cleaned = controlCharRegex.ReplaceAllString(cleaned, "")
	// Trim to max length
	const maxLen = 200
	runes := []rune(cleaned)
	if len(runes) > maxLen {
		cleaned = string(runes[:maxLen-3]) + "..."
	}
	return cleaned
}

// SanitizeSpans applies sanitization to all span texts. Used as a defense-in-depth
// measure for gRPC/WASM runtimes.
func SanitizeSpans(spans []WidgetSpan) []WidgetSpan {
	result := make([]WidgetSpan, len(spans))
	for i, sp := range spans {
		result[i] = WidgetSpan{
			Text:  SanitizeWidgetOutput(sp.Text),
			Style: sp.Style,
		}
	}
	return result
}
