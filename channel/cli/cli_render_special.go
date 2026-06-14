package cli

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
	"github.com/pgavlin/mermaid-ascii/pkg/render"
)

// ──────────────────────────────────────────────────────────────────────
// Mermaid diagram rendering
// ──────────────────────────────────────────────────────────────────────

// mermaidBlockRe matches ```mermaid ... ``` code blocks.
var mermaidBlockRe = regexp.MustCompile("(?s)```mermaid\\s*\n(.*?)```")

// renderMermaidBlocks replaces all ```mermaid code blocks in markdown content
// with their ASCII/Unicode art representation. maxW is the maximum output
// width in display columns (0 = no constraint). When maxW > 0, it is passed
// to mermaid-ascii as TargetWidth so the diagram re-layouts to fit, with a
// fallback truncation for any lines that still exceed maxW.
func renderMermaidBlocks(content string, maxW int) string {
	return mermaidBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := mermaidBlockRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		src := strings.TrimSpace(sub[1])
		if src == "" {
			return match
		}

		cfg := diagram.DefaultConfig()
		if maxW > 0 {
			cfg.TargetWidth = maxW
		}

		output, err := render.Render(src, cfg)
		if err != nil {
			return match
		}

		// Fallback: truncate any lines that still exceed maxW after re-layout.
		if maxW > 0 {
			lines := strings.Split(output, "\n")
			for i, line := range lines {
				line = strings.TrimRight(line, " \t")
				if ansi.StringWidth(line) > maxW {
					lines[i] = truncateStringWidth(line, maxW)
				} else {
					lines[i] = line
				}
			}
			output = strings.Join(lines, "\n")
		}

		return "```\n" + output + "\n```"
	})
}

// truncateStringWidth truncates a plain-text string (no ANSI) to maxW display
// columns, handling wide runes (CJK, box-drawing) correctly.
func truncateStringWidth(s string, maxW int) string {
	var buf strings.Builder
	w := 0
	for _, r := range s {
		rw := ansi.StringWidth(string(r))
		if w+rw > maxW {
			break
		}
		buf.WriteRune(r)
		w += rw
	}
	return buf.String()
}

// ──────────────────────────────────────────────────────────────────────
// LaTeX math rendering
// ──────────────────────────────────────────────────────────────────────

// ─── Markdown-level extraction (regex, unavoidable) ───

var (
	mathBlockRe     = regexp.MustCompile(`(?s)\$\$\s*(.*?)\s*\$\$`)
	mathInlineRe    = regexp.MustCompile(`\$([^\$\n]+?)\$`)
	mathIndicatorRe = regexp.MustCompile(`\\[a-zA-Z]|\^|_|[{}]`)
	multiSpaceRe    = regexp.MustCompile(` {3,}`)
)

// renderMathBlocks pre-processes markdown, converting LaTeX math blocks/inline
// to Unicode via renderLaTeX. Block math wraps in code fences for glamour.
func renderMathBlocks(content string, maxW int) string {
	content = mathBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := mathBlockRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		src := strings.TrimSpace(sub[1])
		if src == "" {
			return match
		}
		rendered := renderLaTeX(src)
		if maxW > 0 {
			rendered = truncateMathLines(rendered, maxW)
		}
		return "```\n" + rendered + "\n```"
	})

	content = mathInlineRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := mathInlineRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		src := strings.TrimSpace(sub[1])
		if src == "" || !looksLikeMath(src) {
			return match
		}
		return renderLaTeX(src)
	})
	return content
}

func hasMath(content string) bool {
	return mathBlockRe.MatchString(content) || mathInlineRe.MatchString(content)
}
func looksLikeMath(s string) bool { return mathIndicatorRe.MatchString(s) }
func truncateMathLines(s string, maxW int) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, " \t")
		if ansi.StringWidth(line) > maxW {
			lines[i] = truncateStringWidth(line, maxW)
		} else {
			lines[i] = line
		}
	}
	return strings.Join(lines, "\n")
}

// ─── Public entry ───

func renderLaTeX(src string) string {
	p := &parser{input: []rune(src)}
	raw := p.parseTop()
	raw = cleanSpaces(raw)
	raw = alignLines(raw)
	raw = renderEnvironments(raw)
	return strings.TrimRight(raw, "\n")
}

func cleanSpaces(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		// Skip ENV marker lines
		if strings.ContainsRune(line, '\x01') {
			continue
		}
		line = multiSpaceRe.ReplaceAllString(line, "  ")
		line = strings.TrimRight(line, " \t")
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// ─── Environment rendering (brackets + side-by-side) ───

// envBlock represents a parsed block between ENV markers or plain text.
type envBlock struct {
	lines []string
	env   string // "bmatrix", "pmatrix", "" for plain text
}

// envBrackets maps environment names to bracket pairs.
var envBrackets = map[string][2]string{
	"bmatrix": {"[", "]"},
	"pmatrix": {"(", ")"},
	"vmatrix": {"|", "|"},
	"Vmatrix": {"‖", "‖"},
	"Bmatrix": {"{", "}"},
}

// renderEnvironments processes ENV markers: adds brackets to matrix blocks
// and renders adjacent blocks side-by-side.
func renderEnvironments(s string) string {
	if !strings.ContainsRune(s, '\x01') {
		return s
	}
	lines := strings.Split(s, "\n")
	blocks := parseBlocks(lines)
	if len(blocks) == 0 {
		return s
	}

	// Add brackets to env blocks
	for i := range blocks {
		if bk, ok := envBrackets[blocks[i].env]; ok {
			left, right := bk[0], bk[1]
			for j := range blocks[i].lines {
				blocks[i].lines[j] = left + " " + blocks[i].lines[j] + " " + right
			}
		}
	}

	// Render all adjacent blocks side-by-side
	return renderRow(blocks)
}

// parseBlocks splits lines into blocks using ENV markers.
func parseBlocks(lines []string) []envBlock {
	var blocks []envBlock
	var current *envBlock

	for _, line := range lines {
		if strings.ContainsRune(line, '\x01') {
			if strings.HasPrefix(line, "\x01ENV:") {
				envName := line[strings.IndexByte(line, ':')+1:]
				if idx := strings.IndexByte(envName, '\x02'); idx >= 0 {
					envName = envName[:idx]
				}
				current = &envBlock{env: envName}
			} else if strings.HasPrefix(line, "\x01/ENV:") {
				if current != nil && len(current.lines) > 0 {
					blocks = append(blocks, *current)
				}
				current = nil
			}
			continue
		}
		if current != nil {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				current.lines = append(current.lines, line)
			}
		} else if strings.TrimSpace(line) != "" {
			blocks = append(blocks, envBlock{lines: []string{line}})
		}
	}
	if current != nil && len(current.lines) > 0 {
		blocks = append(blocks, *current)
	}
	return blocks
}

// renderRow renders blocks side-by-side, centering shorter blocks vertically.
func renderRow(blocks []envBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	if len(blocks) == 1 {
		return strings.Join(blocks[0].lines, "\n")
	}

	// Find max height
	maxH := 0
	for _, b := range blocks {
		if len(b.lines) > maxH {
			maxH = len(b.lines)
		}
	}

	// Compute display width of each block (max line width)
	widths := make([]int, len(blocks))
	for i, b := range blocks {
		for _, line := range b.lines {
			w := ansi.StringWidth(line)
			if w > widths[i] {
				widths[i] = w
			}
		}
	}

	// Pad each block vertically (center) and horizontally (to block width)
	padded := make([][]string, len(blocks))
	for i, b := range blocks {
		topPad := (maxH - len(b.lines)) / 2
		var paddedLines []string
		blank := strings.Repeat(" ", widths[i])
		for j := 0; j < topPad; j++ {
			paddedLines = append(paddedLines, blank)
		}
		for _, line := range b.lines {
			w := ansi.StringWidth(line)
			if w < widths[i] {
				line += strings.Repeat(" ", widths[i]-w)
			}
			paddedLines = append(paddedLines, line)
		}
		for j := topPad + len(b.lines); j < maxH; j++ {
			paddedLines = append(paddedLines, blank)
		}
		padded[i] = paddedLines
	}

	// Interleave lines from all blocks
	var out strings.Builder
	for row := 0; row < maxH; row++ {
		if row > 0 {
			out.WriteRune('\n')
		}
		for col := 0; col < len(blocks); col++ {
			out.WriteString(padded[col][row])
			if col < len(blocks)-1 {
				out.WriteString(" ")
			}
		}
	}
	return out.String()
}

// alignLines pads columns so that &-separated cells align in each group.
// The parser emits \x00 for each & alignment marker. Lines may have
// multiple \x00 markers (matrix columns). Each column is padded to
// the max display-width of that column across the group.
// Lines containing \x01 (ENV markers) are skipped.
func alignLines(s string) string {
	if !strings.ContainsRune(s, '\x00') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); {
		// Skip lines without \x00 or with ENV markers
		if !strings.ContainsRune(lines[i], '\x00') || strings.ContainsRune(lines[i], '\x01') {
			i++
			continue
		}
		start := i
		for i < len(lines) && strings.ContainsRune(lines[i], '\x00') && !strings.ContainsRune(lines[i], '\x01') {
			i++
		}
		alignGroup(lines, start, i)
	}
	return strings.Join(lines, "\n")
}

// alignGroup pads a contiguous group of lines [start, end) so that
// each \x00-delimited column is aligned by display width.
func alignGroup(lines []string, start, end int) {
	// Split each line into columns by \x00
	cols := make([][]string, end-start)
	maxCols := 0
	for j := start; j < end; j++ {
		parts := strings.Split(lines[j], "\x00")
		cols[j-start] = parts
		if len(parts) > maxCols {
			maxCols = len(parts)
		}
	}
	// For each column index, find max display width
	colWidths := make([]int, maxCols)
	for c := 0; c < maxCols; c++ {
		for j := range cols {
			if c < len(cols[j]) {
				w := ansi.StringWidth(cols[j][c])
				if w > colWidths[c] {
					colWidths[c] = w
				}
			}
		}
	}
	// Rebuild each line with padding
	for j := start; j < end; j++ {
		var buf strings.Builder
		parts := cols[j-start]
		for c, part := range parts {
			if c > 0 {
				buf.WriteString(" ") // column separator space
			}
			buf.WriteString(part)
			if c < len(parts)-1 {
				// Pad to column width
				w := ansi.StringWidth(part)
				if w < colWidths[c] {
					buf.WriteString(strings.Repeat(" ", colWidths[c]-w))
				}
			}
		}
		lines[j] = buf.String()
	}
}

// ─── Recursive-descent parser ───

type parser struct {
	input []rune
	pos   int
}

func (p *parser) peek() rune {
	if p.pos >= len(p.input) {
		return -1
	}
	return p.input[p.pos]
}

func (p *parser) next() rune {
	if p.pos >= len(p.input) {
		return -1
	}
	ch := p.input[p.pos]
	p.pos++
	return ch
}

func (p *parser) skipSpaces() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) readAlpha() string {
	start := p.pos
	for p.pos < len(p.input) && isAlpha(p.input[p.pos]) {
		p.pos++
	}
	return string(p.input[start:p.pos])
}

func isAlpha(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// parseTop is the entry: parse until EOF.
func (p *parser) parseTop() string { return p.parse(-1) }

// parse reads input until stop rune is found (consumed) or EOF.
// stop < 0 means no stop (parse to EOF).
func (p *parser) parse(stop rune) string {
	var buf strings.Builder
	for p.pos < len(p.input) {
		ch := p.peek()
		if stop >= 0 && ch == stop {
			p.pos++ // consume stop
			return buf.String()
		}
		switch ch {
		case '\\':
			buf.WriteString(p.parseEscape())
		case '^':
			p.pos++
			buf.WriteString(toSuperscript(p.parseArg()))
		case '_':
			p.pos++
			buf.WriteString(toSubscript(p.parseArg()))
		case '{':
			p.pos++ // skip {
			buf.WriteString(p.parse('}'))
		case '}':
			return buf.String() // don't consume; caller handles
		case '&':
			p.pos++
			buf.WriteRune('\x00') // alignment marker
		default:
			buf.WriteRune(p.next())
		}
	}
	return buf.String()
}

// parseArg reads a single argument: {content} or a single char/command.
func (p *parser) parseArg() string {
	p.skipSpaces()
	if p.pos >= len(p.input) {
		return ""
	}
	if p.peek() == '{' {
		p.pos++ // skip {
		return p.parse('}')
	}
	if p.peek() == '\\' {
		return p.parseEscape()
	}
	return string(p.next())
}

// parseEscape handles a \command or escaped character.
func (p *parser) parseEscape() string {
	p.pos++ // skip \

	// \\ → newline, also consume trailing source newline
	if p.pos < len(p.input) && p.input[p.pos] == '\\' {
		p.pos++
		p.skipSpaces()
		if p.pos < len(p.input) && p.input[p.pos] == '\n' {
			p.pos++
		}
		return "\n"
	}

	// Non-alpha escape: \{ \} \% \_ etc
	if p.pos >= len(p.input) || !isAlpha(p.peek()) {
		ch := p.next()
		switch ch {
		case ' ', ',', ';', '!':
			return " "
		}
		return string(ch)
	}

	name := p.readAlpha()

	// --- Structural commands ---
	switch name {
	case "frac", "dfrac":
		num := p.parseArg()
		den := p.parseArg()
		return num + "/" + den
	case "sqrt":
		idx := ""
		if p.pos < len(p.input) && p.peek() == '[' {
			p.pos++ // skip [
			idx = p.readUntilRune(']')
		}
		content := p.parseArg()
		if idx != "" {
			return toSuperscript(idx) + "√" + content
		}
		return "√" + content
	case "binom", "tbinom":
		n := p.parseArg()
		k := p.parseArg()
		return "(" + n + " " + k + ")"
	case "over":
		return "/" // best-effort for {a \over b}
	}

	// --- Accents ---
	if combining, ok := accentMap[name]; ok {
		content := p.parseArg()
		runes := []rune(content)
		if len(runes) == 0 {
			return ""
		}
		var buf strings.Builder
		buf.WriteRune(runes[0])
		buf.WriteString(combining)
		for _, r := range runes[1:] {
			buf.WriteRune(r)
		}
		return buf.String()
	}

	// --- Text-unwrap commands ---
	if textCmds[name] {
		return p.parseArg()
	}

	// --- Delimiter commands ---
	switch name {
	case "left", "right":
		p.skipSpaces()
		if p.pos < len(p.input) {
			if p.peek() == '\\' {
				p.pos++ // skip \
				if p.pos < len(p.input) {
					ch := p.next()
					return string(ch) // \left\{ → {
				}
				return ""
			}
			ch := p.next()
			if ch == '.' {
				return ""
			}
			return string(ch)
		}
		return ""
	case "bigl", "bigr", "Bigl", "Bigr", "big", "Big", "bigg", "Bigg":
		p.skipSpaces()
		if p.pos < len(p.input) {
			return string(p.next())
		}
		return ""
	case "begin", "end":
		isEnd := name == "end"
		p.skipSpaces()
		envName := ""
		if p.pos < len(p.input) && p.peek() == '{' {
			p.pos++
			envName = p.readUntilRune('}')
		}
		// Consume trailing newline to avoid blank lines
		if p.pos < len(p.input) && p.input[p.pos] == '\n' {
			p.pos++
		}
		prefix := "\n\x01ENV:"
		if isEnd {
			prefix = "\n\x01/ENV:"
		}
		return prefix + envName + "\x02\n"
	}

	// --- Symbol lookup ---
	key := name // lookup without backslash prefix; table keys have no \
	if sym, ok := symbols[key]; ok {
		return sym
	}

	// --- Math functions ---
	if mathFuncs[name] {
		return name
	}

	// Unknown: strip backslash, keep name
	return name
}

func (p *parser) readUntilRune(stop rune) string {
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != stop {
		p.pos++
	}
	result := string(p.input[start:p.pos])
	if p.pos < len(p.input) {
		p.pos++ // consume stop
	}
	return result
}

// ─── Superscript / Subscript ───

var supMap = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
	'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
	'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾',
	'n': 'ⁿ', 'i': 'ⁱ',
}

var subMap = map[rune]rune{
	'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
	'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
	'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎',
	'a': 'ₐ', 'e': 'ₑ', 'o': 'ₒ', 'x': 'ₓ',
	'h': 'ₕ', 'k': 'ₖ', 'l': 'ₗ', 'm': 'ₘ', 'n': 'ₙ',
	'p': 'ₚ', 's': 'ₛ', 't': 'ₜ',
	'i': 'ᵢ', 'j': 'ⱼ', 'r': 'ᵣ', 'u': 'ᵤ', 'v': 'ᵥ',
}

func toSuperscript(s string) string {
	var buf strings.Builder
	for _, r := range s {
		if sr, ok := supMap[r]; ok {
			buf.WriteRune(sr)
		} else {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

func toSubscript(s string) string {
	var buf strings.Builder
	for _, r := range s {
		if sr, ok := subMap[r]; ok {
			buf.WriteRune(sr)
		} else {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// ─── Accent combining characters ───

var accentMap = map[string]string{
	"hat":            "\u0302", // ̂
	"check":          "\u030C", // ̌
	"breve":          "\u0306", // ̆
	"acute":          "\u0301", // ́
	"grave":          "\u0300", // ̀
	"tilde":          "\u0303", // ̃
	"widehat":        "\u0302",
	"widetilde":      "\u0303",
	"bar":            "\u0304", // ̄
	"overline":       "\u0304",
	"underline":      "\u0332", // ̲
	"vec":            "\u20D7", // ⃗
	"overrightarrow": "\u20D7",
	"overleftarrow":  "\u20D6", // ⃖
	"dot":            "\u0307", // ̇
	"ddot":           "\u0308", // ̈
	"ring":           "\u030A", // ̊
}

// textCmds lists commands that take a braced arg and just unwrap it.
var textCmds = map[string]bool{
	"text": true, "mathrm": true, "mathbf": true, "mathit": true,
	"mathcal": true, "mathbb": true, "mathfrak": true, "mathsf": true,
	"mathtt": true, "operatorname": true, "textbf": true, "textit": true,
}

// mathFuncs lists function names whose backslash is stripped.
var mathFuncs = map[string]bool{
	"sin": true, "cos": true, "tan": true, "cot": true, "sec": true, "csc": true,
	"arcsin": true, "arccos": true, "arctan": true,
	"sinh": true, "cosh": true, "tanh": true, "coth": true,
	"ln": true, "log": true, "exp": true, "lim": true,
	"sup": true, "inf": true, "det": true, "dim": true,
	"ker": true, "deg": true, "gcd": true, "min": true, "max": true,
	"arg": true, "hom": true, "Pr": true, "sgn": true,
	"mod": true, "bmod": true, "pmod": true,
}

// ─── Symbol table (key = name WITHOUT backslash) ───

var symbols = map[string]string{
	// Greek lowercase
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ",
	"epsilon": "ε", "varepsilon": "ε", "zeta": "ζ", "eta": "η",
	"theta": "θ", "vartheta": "ϑ", "iota": "ι", "kappa": "κ",
	"lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ",
	"pi": "π", "varpi": "ϖ", "rho": "ρ", "sigma": "σ",
	"tau": "τ", "upsilon": "υ", "phi": "φ", "varphi": "φ",
	"chi": "χ", "psi": "ψ", "omega": "ω",
	// Greek uppercase
	"Gamma": "Γ", "Delta": "Δ", "Theta": "Θ", "Lambda": "Λ",
	"Xi": "Ξ", "Pi": "Π", "Sigma": "Σ", "Upsilon": "Υ",
	"Phi": "Φ", "Psi": "Ψ", "Omega": "Ω",
	// Operators
	"sum": "∑", "prod": "∏", "coprod": "∐",
	"oint": "∮", "iiint": "∭", "iint": "∬", "int": "∫",
	"partial": "∂", "nabla": "∇", "infty": "∞",
	"pm": "±", "mp": "∓", "times": "×", "div": "÷",
	"cdot": "·", "circ": "∘", "bullet": "•", "star": "★",
	"approx": "≈", "neq": "≠", "leq": "≤", "geq": "≥",
	"le": "≤", "ge": "≥", "ll": "≪", "gg": "≫",
	"equiv": "≡", "sim": "∼", "simeq": "≃", "propto": "∝",
	"perp": "⊥", "parallel": "∥", "angle": "∠",
	"triangle": "△", "square": "□",
	"subseteq": "⊆", "supseteq": "⊇", "subset": "⊂", "supset": "⊃",
	"notin": "∉", "in": "∈",
	"cup": "∪", "cap": "∩", "emptyset": "∅", "varnothing": "∅",
	"forall": "∀", "exists": "∃", "neg": "¬", "land": "∧", "lor": "∨",
	"oplus": "⊕", "otimes": "⊗", "odot": "⊙",
	"hbar": "ℏ", "ell": "ℓ", "Re": "ℜ", "Im": "ℑ",
	"aleph": "ℵ", "wp": "℘", "prime": "′",
	"dagger": "†", "ddagger": "‡",
	"cdots": "⋯", "ldots": "…", "vdots": "⋮", "ddots": "⋱",
	"mid": "∣", "vert": "|", "Vert": "‖",
	"implies": "⟹", "iff": "⟺", "impliedby": "⟸",
	"therefore": "∴", "because": "∵",
	"surd": "√", "cong": "≅", "doteq": "≐",
	"lesssim": "≲", "gtrsim": "≳",
	"prec": "≺", "succ": "≻", "bowtie": "⋈",
	"uplus": "⊎", "setminus": "∖", "wr": "≀",
	"diamond": "◇", "Join": "⋈", "bigcirc": "◯", "amalg": "∐",
	"sharp": "♯", "flat": "♭", "natural": "♮",
	"Box": "□", "Diamond": "◇",
	"clubsuit": "♣", "diamondsuit": "♦", "heartsuit": "♥", "spadesuit": "♠",
	// Brackets
	"langle": "⟨", "rangle": "⟩",
	"lfloor": "⌊", "rfloor": "⌋",
	"lceil": "⌈", "rceil": "⌉",
	"lbrace": "{", "rbrace": "}",
	"lvert": "|", "rvert": "|",
	// Arrows
	"rightarrow": "→", "to": "→", "leftarrow": "←", "gets": "←",
	"leftrightarrow": "↔",
	"Rightarrow":     "⇒", "Leftarrow": "⇐", "Leftrightarrow": "⇔",
	"uparrow": "↑", "downarrow": "↓", "updownarrow": "↕",
	"Uparrow": "⇑", "Downarrow": "⇓", "Updownarrow": "⇕",
	"mapsto": "↦", "hookrightarrow": "↪",
	"nearrow": "↗", "searrow": "↘", "swarrow": "↙", "nwarrow": "↖",
	"rightharpoonup": "⇀", "leftharpoonup": "↼",
	// Display style — strip
	"displaystyle": "", "textstyle": "", "scriptstyle": "",
	"limits": "", "nolimits": "",
}
