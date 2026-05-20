package channel

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// mathBlockRe matches $$...$$ display math blocks (dotall, non-greedy).
var mathBlockRe = regexp.MustCompile(`(?s)\$\$\s*(.*?)\s*\$\$`)

// mathInlineRe matches $...$ inline math.
// Broad match; validated in replacement via looksLikeMath.
var mathInlineRe = regexp.MustCompile(`\$([^\$\n]+?)\$`)

// renderMathBlocks pre-processes markdown content containing LaTeX math
// expressions, converting them to Unicode/plain-text representations that
// the terminal can display. Follows the same pre-processing pattern as
// renderMermaidBlocks.
func renderMathBlocks(content string, maxW int) string {
	// Phase 1: block math → code block
	content = mathBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := mathBlockRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		src := strings.TrimSpace(sub[1])
		if src == "" {
			return match
		}
		rendered := latexToUnicode(src)
		if maxW > 0 {
			rendered = truncateMathLines(rendered, maxW)
		}
		return "```\n" + rendered + "\n```"
	})

	// Phase 2: inline math → Unicode text
	content = mathInlineRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := mathInlineRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		src := strings.TrimSpace(sub[1])
		if src == "" || !looksLikeMath(src) {
			return match
		}
		return latexToUnicode(src)
	})

	return content
}

// truncateMathLines truncates each line to maxW display columns.
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

// hasMath detects whether content contains LaTeX math expressions.
func hasMath(content string) bool {
	return mathBlockRe.MatchString(content) || mathInlineRe.MatchString(content)
}

// looksLikeMath returns true if the content between $ delimiters looks like
// a LaTeX math expression rather than plain text with dollar signs (currency).
var mathIndicatorRe = regexp.MustCompile(`\\[a-zA-Z]|\^|_|[{}]`)

func looksLikeMath(s string) bool {
	return mathIndicatorRe.MatchString(s)
}

// =====================================================================
// latexToUnicode — core LaTeX → Unicode converter
// =====================================================================

func latexToUnicode(src string) string {
	out := src

	// 0. Strip LaTeX environments early: \begin{cases}, \end{aligned}, etc.
	out = envRe.ReplaceAllString(out, "")

	// 1. Line breaks: \\ → newline (before symbol replacement eats them)
	out = lineBreakRe.ReplaceAllString(out, "\n")

	// 2. Unwrap text-style commands that take a braced argument:
	//    \text{prime} → prime,  \mathrm{x} → x
	//    Must happen BEFORE subscript/superscript so the content is plain text.
	out = unwrapTextCommands(out)

	// 3. Named Greek letters (longest-first)
	out = replaceLongestFirst(out, greekLetters)

	// 4. Named operators + delimiters merged into one longest-first pass.
	//    CRITICAL: \left[ (7 chars) must beat \le (3 chars).
	//    When separate, operators ran first and \le ate the start of \left.
	out = replaceLongestFirst(out, mergedOperators)

	// 5. Named arrows
	out = replaceLongestFirst(out, arrows)

	// 6. Whitespace commands
	out = strings.ReplaceAll(out, `\,`, " ")
	out = strings.ReplaceAll(out, `\;`, " ")
	out = strings.ReplaceAll(out, `\quad`, "  ")
	out = strings.ReplaceAll(out, `\qquad`, "    ")
	out = strings.ReplaceAll(out, `\ `, " ")
	out = strings.ReplaceAll(out, `~`, " ")

	// 7. Structural constructs — brace-aware parsing
	out = renderFractions(out)
	out = renderSquareRoots(out)
	out = renderOver(out)

	// 8. Superscripts / subscripts — brace-aware
	out = renderSuperscripts(out)
	out = renderSubscripts(out)

	// 9. Strip remaining bare text-style commands (no braces left)
	for _, cmd := range []string{
		`\text`, `\mathrm`, `\mathbf`, `\mathit`, `\mathcal`,
		`\mathbb`, `\mathfrak`, `\mathsf`, `\mathtt`,
		`\operatorname`, `\textbf`, `\textit`,
		`\displaystyle`, `\textstyle`, `\scriptstyle`,
		`\limits`, `\nolimits`,
	} {
		out = strings.ReplaceAll(out, cmd, "")
	}

	// 10. Remove leftover braces iteratively (innermost first)
	out = removeBraces(out)

	// 11. Remaining \cmd → strip backslash
	out = unknownCmdRe.ReplaceAllString(out, "$1")

	return strings.TrimSpace(out)
}

// replaceLongestFirst replaces all keys in m with values,
// processing longest keys first to prevent partial matches.
func replaceLongestFirst(s string, m map[string]string) string {
	keys := sortedKeysByLenDesc(m)
	for _, k := range keys {
		s = strings.ReplaceAll(s, k, m[k])
	}
	return s
}

func sortedKeysByLenDesc(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// insertion sort by length descending
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && len(keys[j]) > len(keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// =====================================================================
// Brace-aware structural parsers
// =====================================================================

// envRe strips \begin{xxx} and \end{xxx} environment markers.
var envRe = regexp.MustCompile(`\\(?:begin|end)\{[^}]*}`)

// lineBreakRe matches LaTeX line breaks \\ (but not \{ or other escapes).
// Must handle \\ at end of line and mid-expression.
var lineBreakRe = regexp.MustCompile(`\\\\\s*`)

// textCmdRe matches \text{content}, \mathrm{content}, etc. — unwraps the
// braced argument, keeping only the inner text. This prevents subscript
// conversion from mangling words like "prime" into ₚᵣᵢₘₑ.
var textCmdRe = regexp.MustCompile(`\\(?:text|mathrm|mathbf|mathit|mathcal|mathbb|mathfrak|mathsf|mathtt|operatorname|textbf|textit)\{([^{}]*)}`)

func unwrapTextCommands(s string) string {
	return textCmdRe.ReplaceAllString(s, "$1")
}

// extractBraced finds the content inside {…} starting at position pos.
// Handles nested braces correctly. Returns the inner content and the
// end position (index after closing }). Returns ("", pos) if no match.
func extractBraced(s string, pos int) (content string, end int) {
	if pos >= len(s) || s[pos] != '{' {
		return "", pos
	}
	depth := 1
	i := pos + 1
	for i < len(s) && depth > 0 {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		i++
	}
	if depth != 0 {
		return "", pos // unmatched brace
	}
	return s[pos+1 : i-1], i
}

// renderFractions converts \frac{num}{den} and \dfrac{num}{den} to num/den.
// Uses brace-aware parsing to handle nested braces correctly.
func renderFractions(s string) string {
	for {
		idx := fracCmdIndex(s)
		if idx < 0 {
			break
		}
		// Find the opening brace of numerator
		numStart := skipSpaces(s, idx)
		if numStart >= len(s) || s[numStart] != '{' {
			break
		}
		num, afterNum := extractBraced(s, numStart)
		if afterNum == numStart {
			break
		}
		denStart := skipSpaces(s, afterNum)
		if denStart >= len(s) || s[denStart] != '{' {
			break
		}
		den, afterDen := extractBraced(s, denStart)
		if afterDen == denStart {
			break
		}

		cmdStart := fracCmdStart(s, idx)
		replacement := num + "/" + den
		s = s[:cmdStart] + replacement + s[afterDen:]
	}
	return s
}

// fracCmdIndex finds the index of '{' right after \frac or \dfrac.
func fracCmdIndex(s string) int {
	for _, prefix := range []string{`\dfrac{`, `\frac{`} {
		if i := strings.Index(s, prefix); i >= 0 {
			return i + len(prefix) - 1 // index of '{'
		}
	}
	return -1
}

func fracCmdStart(s string, braceIdx int) int {
	// Walk back from braceIdx to find the \ that starts \frac or \dfrac
	sub := s[:braceIdx+1]
	for _, prefix := range []string{`\dfrac{`, `\frac{`} {
		if strings.HasSuffix(sub, prefix) {
			return len(sub) - len(prefix)
		}
	}
	return 0
}

func skipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// renderSquareRoots converts \sqrt{x} → √x and \sqrt[n]{x} → ⁿ√x.
// Brace-aware for nested content.
func renderSquareRoots(s string) string {
	for {
		idx := strings.Index(s, `\sqrt`)
		if idx < 0 {
			break
		}
		pos := idx + len(`\sqrt`)
		var indexStr string

		// Optional [n] index
		if pos < len(s) && s[pos] == '[' {
			endBracket := strings.IndexByte(s[pos:], ']')
			if endBracket > 0 {
				indexStr = s[pos+1 : pos+endBracket]
				// Convert index to superscript
				indexStr = toSuperscript(indexStr)
				pos = pos + endBracket + 1
			}
		}

		pos = skipSpaces(s, pos)
		if pos >= len(s) || s[pos] != '{' {
			break
		}
		content, afterContent := extractBraced(s, pos)
		if afterContent == pos {
			break
		}

		replacement := indexStr + "√" + content
		s = s[:idx] + replacement + s[afterContent:]
	}
	return s
}

// renderOver converts \over (LaTeX infix fraction: {a \over b} → a/b).
func renderOver(s string) string {
	return overRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := overRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		return strings.TrimSpace(sub[1]) + "/" + strings.TrimSpace(sub[2])
	})
}

var overRe = regexp.MustCompile(`\{([^{}]*)\s*\\over\s*([^{}]*)}`)

// =====================================================================
// Superscript / Subscript
// =====================================================================

var superscriptDigits = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
	'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
	'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾',
	'n': 'ⁿ', 'i': 'ⁱ',
}

var subscriptDigits = map[rune]rune{
	'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
	'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
	'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎',
	'a': 'ₐ', 'e': 'ₑ', 'o': 'ₒ', 'x': 'ₓ',
	'h': 'ₕ', 'k': 'ₖ', 'l': 'ₗ', 'm': 'ₘ', 'n': 'ₙ',
	'p': 'ₚ', 's': 'ₛ', 't': 'ₜ',
	'i': 'ᵢ', 'j': 'ⱼ', 'r': 'ᵣ', 'u': 'ᵤ', 'v': 'ᵥ',
}

// renderSuperscripts converts ^{...} and ^x to superscript Unicode.
// Brace-aware for nested content like ^{i\pi}.
func renderSuperscripts(s string) string {
	// Braced: ^{...}
	s = renderPowOrSub(s, '^', toSuperscript)
	// Single-char: ^x (not followed by { or \)
	s = renderPowOrSubSingle(s, '^', toSuperscript)
	return s
}

// renderSubscripts converts _{...} and _x to subscript Unicode.
func renderSubscripts(s string) string {
	// Braced: _{...}
	s = renderPowOrSub(s, '_', toSubscript)
	// Single-char: _x
	s = renderPowOrSubSingle(s, '_', toSubscript)
	return s
}

// renderPowOrSub handles ^{...} or _{...} with brace-aware parsing.
func renderPowOrSub(s string, marker byte, convert func(string) string) string {
	for {
		idx := indexOfMarkerBrace(s, marker)
		if idx < 0 {
			break
		}
		braceStart := idx + 1
		if braceStart >= len(s) || s[braceStart] != '{' {
			break
		}
		content, afterContent := extractBraced(s, braceStart)
		if afterContent == braceStart {
			break
		}
		replacement := convert(content)
		s = s[:idx] + replacement + s[afterContent:]
	}
	return s
}

// renderPowOrSubSingle handles ^x or _x (single char, no braces).
var singlePowRe = regexp.MustCompile(`\^([^\\{_\s])`)
var singleSubRe = regexp.MustCompile(`_([^\\{^\s])`)

func renderPowOrSubSingle(s string, marker byte, convert func(string) string) string {
	var re *regexp.Regexp
	if marker == '^' {
		re = singlePowRe
	} else {
		re = singleSubRe
	}
	return re.ReplaceAllStringFunc(s, func(match string) string {
		sub := re.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		return convert(sub[1])
	})
}

func indexOfMarkerBrace(s string, marker byte) int {
	for i := 0; i < len(s)-1; i++ {
		if s[i] == marker && i+1 < len(s) && s[i+1] == '{' {
			return i
		}
	}
	return -1
}

func toSuperscript(s string) string {
	var buf strings.Builder
	for _, r := range s {
		if sr, ok := superscriptDigits[r]; ok {
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
		if sr, ok := subscriptDigits[r]; ok {
			buf.WriteRune(sr)
		} else {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// =====================================================================
// Brace cleanup
// =====================================================================

// removeBraces iteratively strips the innermost {content} pairs.
var innerBraceRe = regexp.MustCompile(`\{([^{}]*)}`)

func removeBraces(s string) string {
	for i := 0; i < 10; i++ {
		next := innerBraceRe.ReplaceAllString(s, "$1")
		if next == s {
			break
		}
		s = next
	}
	return s
}

// =====================================================================
// Symbol tables
// =====================================================================

var unknownCmdRe = regexp.MustCompile(`\\([a-zA-Z]+)`)

var greekLetters = map[string]string{
	`\alpha`: "α", `\beta`: "β", `\gamma`: "γ", `\delta`: "δ",
	`\epsilon`: "ε", `\varepsilon`: "ε", `\zeta`: "ζ", `\eta`: "η",
	`\theta`: "θ", `\vartheta`: "ϑ", `\iota`: "ι", `\kappa`: "κ",
	`\lambda`: "λ", `\mu`: "μ", `\nu`: "ν", `\xi`: "ξ",
	`\pi`: "π", `\varpi`: "ϖ", `\rho`: "ρ", `\sigma`: "σ",
	`\tau`: "τ", `\upsilon`: "υ", `\phi`: "φ", `\varphi`: "φ",
	`\chi`: "χ", `\psi`: "ψ", `\omega`: "ω",
	`\Gamma`: "Γ", `\Delta`: "Δ", `\Theta`: "Θ", `\Lambda`: "Λ",
	`\Xi`: "Ξ", `\Pi`: "Π", `\Sigma`: "Σ", `\Upsilon`: "Υ",
	`\Phi`: "Φ", `\Psi`: "Ψ", `\Omega`: "Ω",
}

// mergedOperators = operators + delimiters merged into a single map
// so that longest-first replacement handles \left[ (7ch) > \le (3ch).
var mergedOperators = map[string]string{
	// Delimiters (longer keys first implicitly via longest-first sort)
	`\left\{`: "{", `\right\}`: "}",
	`\left(`: "(", `\right)`: ")",
	`\left[`: "[", `\right]`: "]",
	`\left|`: "|", `\right|`: "|",
	`\left.`: "", `\right.`: "",
	`\bigl(`: "(", `\bigr)`: ")",
	`\Bigl(`: "(", `\Bigr)`: ")",
	// Core operators
	`\sum`: "∑", `\prod`: "∏", `\coprod`: "∐",
	`\oint`: "∮", `\iiint`: "∭", `\iint`: "∬", `\int`: "∫",
	`\partial`: "∂", `\nabla`: "∇", `\infty`: "∞",
	`\pm`: "±", `\mp`: "∓", `\times`: "×", `\div`: "÷",
	`\cdot`: "·", `\circ`: "∘", `\bullet`: "•", `\star`: "★",
	`\approx`: "≈", `\neq`: "≠", `\leq`: "≤", `\geq`: "≥",
	`\le`: "≤", `\ge`: "≥", `\ll`: "≪", `\gg`: "≫",
	`\equiv`: "≡", `\sim`: "∼", `\simeq`: "≃", `\propto`: "∝",
	`\perp`: "⊥", `\parallel`: "∥", `\angle`: "∠",
	`\triangle`: "△", `\square`: "□",
	`\subseteq`: "⊆", `\supseteq`: "⊇", `\subset`: "⊂", `\supset`: "⊃",
	`\notin`: "∉", `\in`: "∈",
	`\cup`: "∪", `\cap`: "∩", `\emptyset`: "∅", `\varnothing`: "∅",
	`\forall`: "∀", `\exists`: "∃", `\neg`: "¬", `\land`: "∧", `\lor`: "∨",
	`\oplus`: "⊕", `\otimes`: "⊗", `\odot`: "⊙",
	`\hbar`: "ℏ", `\ell`: "ℓ", `\Re`: "ℜ", `\Im`: "ℑ",
	`\aleph`: "ℵ", `\wp`: "℘", `\prime`: "′",
	`\dagger`: "†", `\ddagger`: "‡",
	`\cdots`: "⋯", `\ldots`: "…", `\vdots`: "⋮", `\ddots`: "⋱",
	// Symbols previously missing
	`\mid`: "∣", `\vert`: "|", `\Vert`: "‖",
	`\big`: "", `\Big`: "", `\bigg`: "", `\Bigg`: "",
	// Escaped special chars
	`\%`: "%", `\&`: "&", `\#`: "#", `\_`: "_",
	`\{`: "{", `\}`: "}",
}

var arrows = map[string]string{
	`\rightarrow`: "→", `\to`: "→", `\leftarrow`: "←", `\gets`: "←",
	`\leftrightarrow`: "↔",
	`\Rightarrow`:     "⇒", `\Leftarrow`: "⇐", `\Leftrightarrow`: "⇔",
	`\uparrow`: "↑", `\downarrow`: "↓", `\updownarrow`: "↕",
	`\Uparrow`: "⇑", `\Downarrow`: "⇓", `\Updownarrow`: "⇕",
	`\mapsto`: "↦", `\hookrightarrow`: "↪",
	`\nearrow`: "↗", `\searrow`: "↘", `\swarrow`: "↙", `\nwarrow`: "↖",
	`\rightharpoonup`: "⇀", `\leftharpoonup`: "↼",
}
