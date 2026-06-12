package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"xbot/protocol"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

func (m *cliModel) renderRewindResultBlock() string {
	if m.rewindResult == nil {
		return ""
	}
	r := m.rewindResult

	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(m.styles.ProgressDone.Bold(true).Render("  Rewind complete"))
	sb.WriteString("\n")

	if len(r.Restored) > 0 {
		fmt.Fprintf(&sb, "  Files restored: %d\n", len(r.Restored))
		for _, f := range r.Restored {
			sb.WriteString(m.styles.TextMutedSt.Render(fmt.Sprintf("    %s\n", f)))
		}
	}
	if len(r.CreatedDel) > 0 {
		fmt.Fprintf(&sb, "  Files deleted: %d\n", len(r.CreatedDel))
		for _, f := range r.CreatedDel {
			sb.WriteString(m.styles.TextMutedSt.Render(fmt.Sprintf("    %s\n", f)))
		}
	}
	if len(r.Errors) > 0 {
		for _, e := range r.Errors {
			sb.WriteString(m.styles.ProgressError.Render(fmt.Sprintf("  Error: %s\n", e)))
		}
	}

	return sb.String()
}

// tickerCmd is deprecated — ticker is now driven by cliTickMsg.
// Kept for reference only.
// func tickerCmd() tea.Cmd {
// 	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
// 		return tickerTickMsg{}
// 	})
// }

// renderToolHint renders plugin-provided or built-in hint content.
// For ```diff code blocks, renders with line numbers and theme backgrounds (crush-style).
// For other markdown, renders with glamour.
// maxLines caps diff line rendering (0 = unlimited).
func (m *cliModel) renderToolHint(md string, maxW, maxLines int) (string, error) {
	if md == "" {
		return "", nil
	}
	// Diff: use provided width (no guide prefix, fills full width like crush)
	if strings.Contains(md, "```diff") {
		w := maxW
		if w < 40 {
			w = 40
		}
		return renderDiffStyled(md, w, maxLines), nil
	}
	// Non-diff markdown: render with glamour
	rendered, err := m.renderer.Render(md)
	if err != nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextSecondary)).Render(md), nil
	}
	return strings.TrimSpace(rendered), nil
}

// renderToolBody renders tool-specific body content below the tool line.
// Dispatches to specialized renderers based on tool name (crush-style per-tool rendering).
// Returns empty string if no body content should be shown.
func (m *cliModel) renderToolBody(tool protocol.ToolProgress, maxW int) string {
	if tool.Status == "error" {
		return "" // errors shown in the tool line itself
	}
	t := *currentTheme
	switch tool.Name {
	case "Read":
		return m.renderReadBody(tool, maxW, t)
	case "Shell":
		return m.renderShellBody(tool, maxW, t)
	case "Grep":
		return m.renderGrepBody(tool, maxW, t)
	case "Glob":
		return m.renderGlobBody(tool, maxW, t)
	}
	return ""
}

const toolBodyMaxLines = 10

// highlightCode performs Chroma syntax highlighting on code content.
// filePath is used to select the lexer; falls back to plain text if no match.
// Each token is rendered with lipgloss including the background color (crush xchroma approach).
// Returns a slice of highlighted lines (split by \n).
func highlightCode(content string, filePath string) []string {
	lexer := lexers.Match(filePath)
	if lexer == nil {
		lexer = lexers.Analyse(content)
	}
	if lexer == nil {
		return nil // no match → caller uses plain rendering
	}
	lexer = chroma.Coalesce(lexer)

	it, err := lexer.Tokenise(nil, content)
	if err != nil {
		return nil
	}

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	// Walk tokens, format each with lipgloss (foreground only, no background), split by newline
	var lineBuf strings.Builder
	var result []string

	for _, tok := range it.Tokens() {
		if tok == chroma.EOF {
			break
		}
		entry := style.Get(tok.Type)
		s := lipgloss.NewStyle()
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

		val := tok.Value
		for val != "" {
			nl := strings.IndexByte(val, '\n')
			if nl < 0 {
				lineBuf.WriteString(s.Render(val))
				break
			}
			if nl > 0 {
				lineBuf.WriteString(s.Render(val[:nl]))
			}
			result = append(result, lineBuf.String())
			lineBuf.Reset()
			val = val[nl+1:]
		}
	}
	if lineBuf.Len() > 0 {
		result = append(result, lineBuf.String())
	}
	return result
}

// renderReadBody renders Read tool output as code with line numbers and syntax highlighting.
// The Read tool output (Detail/Summary) already has line numbers in format "%*d\t<code>".
// We parse those to extract pure code, highlight it with Chroma, then render with our own line numbers.
func (m *cliModel) renderReadBody(tool protocol.ToolProgress, maxW int, t cliTheme) string {
	content := tool.Detail
	if content == "" {
		content = tool.Summary
	}
	if content == "" {
		return ""
	}

	// Parse args for file path
	filePath := ""
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(tool.Args), &args) == nil {
		filePath = args.Path
	}

	// Read tool output has line numbers: "%*d\t<code>"
	// Parse to extract line numbers and pure code
	rawLines := strings.Split(content, "\n")
	if len(rawLines) == 0 || (len(rawLines) == 1 && rawLines[0] == "") {
		return ""
	}

	type parsedLine struct {
		num  int
		code string
	}
	var parsed []parsedLine

	for _, line := range rawLines {
		matches := readLineNumRe.FindStringSubmatch(line)
		if matches != nil {
			num, _ := strconv.Atoi(matches[1])
			parsed = append(parsed, parsedLine{num: num, code: matches[2]})
		}
		// Skip non-matching lines (e.g. truncation messages)
	}

	if len(parsed) == 0 {
		return "" // no parseable content
	}

	totalLines := len(parsed)
	displayParsed := parsed
	if len(displayParsed) > toolBodyMaxLines {
		displayParsed = displayParsed[:toolBodyMaxLines]
	}

	// Try Chroma highlighting on pure code
	pureCode := make([]string, len(parsed))
	for i, p := range parsed {
		pureCode[i] = p.code
	}
	pureCodeStr := strings.Join(pureCode, "\n")
	hlLines := highlightCode(pureCodeStr, filePath)

	// Layout calculations
	maxLineNum := parsed[totalLines-1].num
	digits := numDigits(maxLineNum)
	numFmt := fmt.Sprintf("%%%dd ", digits)
	lineNumW := digits + 1

	fgLineNum := lipgloss.Color(t.TextMuted)

	codeW := maxW - lineNumW
	if codeW < 10 {
		codeW = 10
	}

	var sb strings.Builder
	for i, p := range displayParsed {
		lineNumText := fmt.Sprintf(numFmt, p.num)
		lineNumText = strings.ReplaceAll(lineNumText, " ", "\u00a0")
		lineNum := lipgloss.NewStyle().Foreground(fgLineNum).Render(lineNumText)

		var codeLine string
		if hlLines != nil && i < len(hlLines) {
			codeLine = ansi.Truncate(hlLines[i], codeW, "")
		} else {
			codeLine = lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextPrimary)).
				Render(ansi.Truncate(p.code, codeW, ""))
		}
		sb.WriteString(lineNum + codeLine)
		sb.WriteString("\n")
	}
	if totalLines > toolBodyMaxLines {
		hidden := totalLines - toolBodyMaxLines
		sb.WriteString(lipgloss.NewStyle().Foreground(fgLineNum).
			Width(maxW).Render(fmt.Sprintf("  ... %d more lines", hidden)))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderShellBody renders Shell tool output with command indicator.
func (m *cliModel) renderShellBody(tool protocol.ToolProgress, maxW int, t cliTheme) string {
	content := tool.Detail
	if content == "" {
		content = tool.Summary
	}
	if content == "" {
		return ""
	}
	// Parse args for command
	command := ""
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(tool.Args), &args) == nil {
		command = args.Command
	}

	fgPrompt := lipgloss.Color(t.TextMuted)

	var sb strings.Builder

	// Show command
	if command != "" {
		commandLine := "$ " + ansi.Truncate(command, maxW-5, "") + "..."
		for _, wl := range strings.Split(hardWrapRunes(commandLine, maxW), "\n") {
			sb.WriteString(lipgloss.NewStyle().Foreground(fgPrompt).Render(wl))
			sb.WriteString("\n")
		}
	}

	// Show output (wrapped to fit)
	// Progress bars (tqdm etc.) use \r to overwrite the same line.
	// When captured as output, \r-embedded lines confuse the terminal:
	// \r moves cursor to column 0, overwriting the guide prefix.
	// sanitizeOutputLine handles \r stripping and ANSI removal.
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	displayLines := lines
	if len(displayLines) > toolBodyMaxLines {
		displayLines = displayLines[:toolBodyMaxLines]
	}
	outputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextPrimary))
	for _, line := range displayLines {
		line = sanitizeOutputLine(line)
		// Skip empty lines after sanitization (fully overwritten frames)
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, wl := range strings.Split(hardWrapRunes(line, maxW), "\n") {
			sb.WriteString(outputStyle.Render(wl))
			sb.WriteString("\n")
		}
	}
	if totalLines > toolBodyMaxLines {
		hidden := totalLines - toolBodyMaxLines
		sb.WriteString(lipgloss.NewStyle().Foreground(fgPrompt).
			Width(maxW).Render(fmt.Sprintf("  ... %d more lines", hidden)))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderGrepBody renders Grep tool output with highlighted matches.
func (m *cliModel) renderGrepBody(tool protocol.ToolProgress, maxW int, t cliTheme) string {
	content := tool.Detail
	if content == "" {
		content = tool.Summary
	}
	if content == "" {
		return ""
	}
	fgMeta := lipgloss.Color(t.TextMuted)

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	displayLines := lines
	if len(displayLines) > toolBodyMaxLines {
		displayLines = displayLines[:toolBodyMaxLines]
	}

	var sb strings.Builder
	grepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextPrimary))
	for _, line := range displayLines {
		for _, wl := range strings.Split(hardWrapRunes(line, maxW), "\n") {
			sb.WriteString(grepStyle.Render(wl))
			sb.WriteString("\n")
		}
	}
	if totalLines > toolBodyMaxLines {
		hidden := totalLines - toolBodyMaxLines
		sb.WriteString(lipgloss.NewStyle().Foreground(fgMeta).
			Width(maxW).Render(fmt.Sprintf("  ... %d more matches", hidden)))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderGlobBody renders Glob tool output as a file list.
func (m *cliModel) renderGlobBody(tool protocol.ToolProgress, maxW int, t cliTheme) string {
	content := tool.Detail
	if content == "" {
		content = tool.Summary
	}
	if content == "" {
		return ""
	}
	fgFile := lipgloss.Color(t.TextPrimary)
	fgMeta := lipgloss.Color(t.TextMuted)

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	displayLines := lines
	if len(displayLines) > toolBodyMaxLines {
		displayLines = displayLines[:toolBodyMaxLines]
	}

	var sb strings.Builder
	indentStyle := lipgloss.NewStyle().Foreground(fgMeta)
	fileStyle := lipgloss.NewStyle().Foreground(fgFile)
	for _, line := range displayLines {
		// Account for "  " indent: wrap file path to maxW-2, then prepend indent.
		for _, wl := range strings.Split(hardWrapRunes(line, maxW-2), "\n") {
			sb.WriteString(indentStyle.Render("  "))
			sb.WriteString(fileStyle.Render(wl))
			sb.WriteString("\n")
		}
	}
	if totalLines > toolBodyMaxLines {
		hidden := totalLines - toolBodyMaxLines
		sb.WriteString(lipgloss.NewStyle().Foreground(fgMeta).
			Render(fmt.Sprintf("  ... %d more files", hidden)))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
