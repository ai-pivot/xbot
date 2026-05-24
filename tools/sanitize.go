package tools

import (
	"regexp"
	"strings"
)

// ansiEscapeRe matches ANSI escape sequences (color codes, cursor movement, etc.)
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?\x07|\x1b[^[\]()]`)

// SanitizeOutputLine sanitizes a single output line for safe display.
// It strips carriage-return overwrites (keeps only the final visual state
// after the last \r, handling progress bars like tqdm) and removes ANSI
// escape sequences (color codes, cursor movement, etc.).
func SanitizeOutputLine(line string) string {
	// Strip carriage-return overwrites: keep only the final visual
	// state (content after the last \r).
	if idx := strings.LastIndex(line, "\r"); idx >= 0 {
		line = line[idx+1:]
	}
	// Strip ANSI escape sequences.
	line = ansiEscapeRe.ReplaceAllString(line, "")
	return line
}

// SanitizeOutputLines splits raw process output into sanitized display lines.
// It handles \r carriage-return overwrites (progress bars), strips ANSI escape
// sequences, and filters out empty lines left after sanitization.
func SanitizeOutputLines(raw string) []string {
	if raw == "" {
		return nil
	}
	rawLines := strings.Split(raw, "\n")
	var result []string
	for _, line := range rawLines {
		line = SanitizeOutputLine(line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		result = append(result, line)
	}
	return result
}

// SanitizeOutput sanitizes multi-line process output into a clean string.
// It processes each line individually (stripping \r overwrites and ANSI escapes),
// filters empty lines, and joins the result with newlines.
func SanitizeOutput(raw string) string {
	lines := SanitizeOutputLines(raw)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}
