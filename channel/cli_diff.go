package channel

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// ---------------------------------------------------------------------------
// Package-level compiled regexps (compiled once, not per-call)
// ---------------------------------------------------------------------------

var (
	readLineNumRe = regexp.MustCompile(`^\s*(\d+)\t(.*)$`)
	diffHunkRe    = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

func numDigits(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		n /= 10
		d++
	}
	return d
}

// padBgRight appends background-colored non-breaking spaces after content.
// Do NOT use ordinary spaces for terminal background fills: some renderers/terminals
// trim or don't paint trailing regular spaces at EOL. NBSP is visually blank but
// remains an actual cell, so the background is painted and selectable.
func padBgRight(content string, bgHex string, targetWidth int) string {
	visualW := lipgloss.Width(content)
	pad := targetWidth - visualW
	if pad <= 0 {
		return content
	}
	padding := lipgloss.NewStyle().Background(lipgloss.Color(bgHex)).Render(strings.Repeat("\u00a0", pad))
	return content + padding
}

// expandTabs replaces tab characters with spaces, respecting ANSI escape
// sequences so that escape codes don't affect tab-stop calculation.
func expandTabs(s string, tabWidth int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			b.WriteRune(r)
			continue
		}
		if inEscape {
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		if r == '\t' {
			spaces := tabWidth - (col % tabWidth)
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		} else {
			b.WriteRune(r)
			col += uniseg.StringWidth(string(r))
		}
	}
	return b.String()
}

func renderBgLine(content string, fgHex string, bgHex string, targetWidth int) string {
	content = ansi.Truncate(content, targetWidth, "")
	st := lipgloss.NewStyle().Background(lipgloss.Color(bgHex))
	if fgHex != "" {
		st = st.Foreground(lipgloss.Color(fgHex))
	}
	return padBgRight(st.Render(content), bgHex, targetWidth)
}

// renderDiffStyled renders diff content with syntax highlighting, line numbers
// and theme semantic backgrounds. Uses the same approach as crush's xchroma:
// Chroma tokens are formatted with lipgloss including the diff background color,
// so ANSI codes are always correct without manual escape management.
// maxLines caps the number of diff lines rendered (0 = unlimited).
func renderDiffStyled(md string, maxW, maxLines int) string {
	if maxW < 40 {
		maxW = 40
	}
	t := currentTheme
	lines := strings.Split(md, "\n")

	// Extract diff content from ```diff ... ``` block
	var diffLines []string
	inDiff := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inDiff && trimmed == "```diff" {
			inDiff = true
			continue
		}
		if inDiff && trimmed == "```" {
			break
		}
		if inDiff {
			diffLines = append(diffLines, line)
		}
	}
	if len(diffLines) == 0 {
		trimmed := strings.TrimSpace(md)
		if trimmed == "" {
			return ""
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextSecondary)).Render(trimmed)
	}

	// Cap diff lines before expensive chroma tokenisation.
	if maxLines > 0 && len(diffLines) > maxLines {
		diffLines = diffLines[:maxLines]
	}

	// --- Extract file path for syntax highlighting ---
	filePath := ""
	for _, line := range diffLines {
		if strings.HasPrefix(line, "--- a/") {
			filePath = line[6:]
		} else if strings.HasPrefix(line, "--- /dev/null") {
			filePath = ""
		}
		if strings.HasPrefix(line, "+++ b/") {
			if filePath == "" {
				filePath = line[6:]
			}
			break
		}
	}

	// --- Theme colors ---
	bgAdd := lipgloss.Color(t.SuccessBg)
	bgDel := lipgloss.Color(t.ErrorBg)
	fgAdd := lipgloss.Color(t.Success)
	fgDel := lipgloss.Color(t.Error)
	fgMeta := lipgloss.Color(t.TextMuted)

	// --- Syntax highlighting per line type (crush xchroma approach) ---
	// Highlight code with background baked in via lipgloss, so ANSI codes are clean.
	// Context lines use empty bg → transparent (no background color).
	highlightMap := diffHighlightLines(diffLines, filePath, t.SuccessBg, t.ErrorBg, "")

	oldLine := 0
	newLine := 0
	lineNumDigits := 3

	for _, line := range diffLines {
		if matches := diffHunkRe.FindStringSubmatch(line); matches != nil {
			os, _ := strconv.Atoi(matches[1])
			oc, _ := strconv.Atoi(matches[2])
			if oc == 0 {
				oc = 1
			}
			ns, _ := strconv.Atoi(matches[3])
			nc, _ := strconv.Atoi(matches[4])
			if nc == 0 {
				nc = 1
			}
			maxNum := os + oc
			if ns+nc > maxNum {
				maxNum = ns + nc
			}
			d := numDigits(maxNum)
			if d > lineNumDigits {
				lineNumDigits = d
			}
		}
	}

	numFmt := fmt.Sprintf("%%%dd", lineNumDigits)
	lineNumColW := lineNumDigits*2 + 2
	codeW := maxW - lineNumColW - 2
	if codeW < 10 {
		codeW = 10
	}

	lineNumStyleAdd := lipgloss.NewStyle().Foreground(fgMeta).Background(bgAdd)
	lineNumStyleDel := lipgloss.NewStyle().Foreground(fgMeta).Background(bgDel)
	lineNumStyleCtx := lipgloss.NewStyle().Foreground(fgMeta)
	symStyleAdd := lipgloss.NewStyle().Background(bgAdd).Foreground(fgAdd)
	symStyleDel := lipgloss.NewStyle().Background(bgDel).Foreground(fgDel)

	var sb strings.Builder
	for i, line := range diffLines {
		if strings.HasPrefix(line, `\ `) {
			continue
		}

		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			sb.WriteString(lipgloss.NewStyle().Foreground(fgMeta).Faint(true).Render(ansi.Truncate(line, maxW, "")))

		case strings.HasPrefix(line, "@@"):
			if matches := diffHunkRe.FindStringSubmatch(line); matches != nil {
				oldLine, _ = strconv.Atoi(matches[1])
				newLine, _ = strconv.Atoi(matches[3])
			}
			sb.WriteString(renderBgLine(line, t.Info, t.Border, maxW))

		case strings.HasPrefix(line, "+"):
			code := line[1:]
			if hl, ok := highlightMap[i]; ok {
				code = lipgloss.NewStyle().Background(bgAdd).Render(hl)
			} else {
				code = lipgloss.NewStyle().Background(bgAdd).Render(code)
			}
			code = expandTabs(code, 4)
			code = ansi.Truncate(code, codeW, "")
			oldNum := strings.Repeat("\u00a0", lineNumDigits)
			newNum := fmt.Sprintf(numFmt, newLine)
			lineNums := lineNumStyleAdd.Render(oldNum + " " + newNum + " ")
			sym := symStyleAdd.Render("+ ")
			codeStyled := padBgRight(code, t.SuccessBg, codeW)
			sb.WriteString(lineNums + sym + codeStyled)
			newLine++

		case strings.HasPrefix(line, "-"):
			code := line[1:]
			if hl, ok := highlightMap[i]; ok {
				code = lipgloss.NewStyle().Background(bgDel).Render(hl)
			} else {
				code = lipgloss.NewStyle().Background(bgDel).Render(code)
			}
			code = expandTabs(code, 4)
			code = ansi.Truncate(code, codeW, "")
			oldNum := fmt.Sprintf(numFmt, oldLine)
			newNum := strings.Repeat("\u00a0", lineNumDigits)
			lineNums := lineNumStyleDel.Render(oldNum + " " + newNum + " ")
			sym := symStyleDel.Render("- ")
			codeStyled := padBgRight(code, t.ErrorBg, codeW)
			sb.WriteString(lineNums + sym + codeStyled)
			oldLine++

		default:
			code := ""
			if len(line) > 0 {
				code = line[1:] // strip diff prefix char (space for context lines)
			}
			if hl, ok := highlightMap[i]; ok {
				code = hl
			}
			code = expandTabs(code, 4)
			code = ansi.Truncate(code, codeW, "")
			oldNum := fmt.Sprintf(numFmt, oldLine)
			newNum := fmt.Sprintf(numFmt, newLine)
			lineNums := lineNumStyleCtx.Render(oldNum + " " + newNum + " ")
			sb.WriteString(lineNums + "  " + code)
			oldLine++
			newLine++
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// diffHighlightLines performs Chroma-based syntax highlighting on code lines within a diff.
// Uses crush's xchroma approach: each token is rendered via lipgloss with the diff background
// color baked in, producing clean ANSI output that lipgloss can measure/Width correctly.
// Returns a map from diff line index to highlighted code content (plain code, no +/- prefix).
func diffHighlightLines(diffLines []string, filePath string, bgAdd, bgDel, bgCtx string) map[int]string {
	lexer := lexers.Match(filePath)
	if lexer == nil {
		return nil
	}
	lexer = chroma.Coalesce(lexer)

	// Collect code spans with their line types
	type codeSpan struct {
		diffIdx int
		code    string
		bgHex   string
	}
	var spans []codeSpan
	var joined strings.Builder
	for i, line := range diffLines {
		code := ""
		bg := bgCtx
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "@@"):
			continue
		case strings.HasPrefix(line, "+"):
			code = line[1:]
			bg = bgAdd
		case strings.HasPrefix(line, "-"):
			code = line[1:]
			bg = bgDel
		default:
			if len(line) > 0 {
				code = line[1:] // strip diff prefix space for context lines
			}
		}
		if code != "" {
			spans = append(spans, codeSpan{diffIdx: i, code: code, bgHex: bg})
			joined.WriteString(code)
			joined.WriteString("\n")
		}
	}

	if joined.Len() == 0 {
		return nil
	}

	it, err := lexer.Tokenise(nil, joined.String())
	if err != nil {
		return nil
	}

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	// Walk tokens, split by newline, format each token with lipgloss + background
	result := make(map[int]string, len(spans))
	var lineBuf strings.Builder
	spanIdx := 0

	flushLine := func() {
		if spanIdx < len(spans) {
			result[spans[spanIdx].diffIdx] = lineBuf.String()
		}
		lineBuf.Reset()
		spanIdx++
	}

	formatWithBg := func(tok chroma.Token, bgHex string) {
		entry := style.Get(tok.Type)
		s := lipgloss.NewStyle()
		if bgHex != "" {
			s = s.Background(lipgloss.Color(bgHex))
		}
		if entry.Bold == chroma.Yes {
			s = s.Bold(true)
		}
		if entry.Italic == chroma.Yes {
			s = s.Italic(true)
		}
		if entry.Underline == chroma.Yes {
			s = s.Underline(true)
		}
		if entry.Colour.IsSet() {
			s = s.Foreground(lipgloss.Color(entry.Colour.String()))
		}
		lineBuf.WriteString(s.Render(tok.Value))
	}

	for _, tok := range it.Tokens() {
		if tok == chroma.EOF {
			break
		}
		val := tok.Value
		for val != "" {
			nl := strings.IndexByte(val, '\n')
			if nl < 0 {
				if spanIdx < len(spans) {
					formatWithBg(chroma.Token{Type: tok.Type, Value: val}, spans[spanIdx].bgHex)
				}
				break
			}
			if nl > 0 {
				if spanIdx < len(spans) {
					formatWithBg(chroma.Token{Type: tok.Type, Value: val[:nl]}, spans[spanIdx].bgHex)
				}
			}
			flushLine()
			val = val[nl+1:]
		}
	}

	if lineBuf.Len() > 0 && spanIdx < len(spans) {
		result[spans[spanIdx].diffIdx] = lineBuf.String()
	}

	return result
}
