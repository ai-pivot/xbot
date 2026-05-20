package channel

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// mathBlockRe matches $$...$$ display math blocks (dotall, non-greedy).
var mathBlockRe = regexp.MustCompile(`(?s)\$\$\s*(.*?)\s*\$\$`)

// mathInlineRe matches $...$ inline math.
// Requirements for a valid match:
//   - Content must contain at least one backslash command (\xx) or
//     a superscript/subscript (^, _) or a Unicode math indicator.
//   - This prevents matching currency like "$10 and $20".
//   - Content must not span multiple lines.
//
// Since Go RE2 lacks lookbehind, we use a simple pattern and validate
// content quality in the replacement function.
var mathInlineRe = regexp.MustCompile(`\$([^\$\n]+?)\$`)

// renderMathBlocks pre-processes markdown content containing LaTeX math
// expressions, converting them to Unicode/plain-text representations that
// the terminal can display. It follows the same pre-processing pattern as
// renderMermaidBlocks.
//
// Block math ($$...$$) is rendered into a ``` code block so glamour
// preserves whitespace alignment. Inline math ($...$) is rendered inline
// with Unicode symbols.
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

// latexToUnicode converts a LaTeX math expression to a Unicode/plain-text
// representation suitable for terminal display. It handles the most common
// constructs: Greek letters, operators, sub/superscripts, fractions,
// square roots, summations, integrals, and basic formatting.
func latexToUnicode(src string) string {
	// Apply transformations in order — order matters for nested constructs.
	out := src

	// 1. Named Greek letters: \alpha → α, \beta → β, etc.
	out = replaceLongestFirst(out, greekLetters)

	// 2. Named operators: \sum → ∑, \int → ∫, etc.
	//    Must be longest-first to avoid \in matching before \int, \leq before \le, etc.
	out = replaceLongestFirst(out, operators)

	// 3. Named arrows: \rightarrow → →, etc.
	out = replaceLongestFirst(out, arrows)

	// 4. Bracket delimiters (multi-char sequences)
	out = strings.ReplaceAll(out, `\left(`, "(")
	out = strings.ReplaceAll(out, `\right)`, ")")
	out = strings.ReplaceAll(out, `\left[`, "[")
	out = strings.ReplaceAll(out, `\right]`, "]")
	out = strings.ReplaceAll(out, `\left\{`, "{")
	out = strings.ReplaceAll(out, `\right\}`, "}")
	out = strings.ReplaceAll(out, `\left|`, "|")
	out = strings.ReplaceAll(out, `\right|`, "|")
	out = strings.ReplaceAll(out, `\bigl(`, "(")
	out = strings.ReplaceAll(out, `\bigr)`, ")")
	out = strings.ReplaceAll(out, `\Bigl(`, "(")
	out = strings.ReplaceAll(out, `\Bigr)`, ")")
	out = strings.ReplaceAll(out, `\left.`, "")
	out = strings.ReplaceAll(out, `\right.`, "")

	// 5. Whitespace commands
	out = strings.ReplaceAll(out, `\,`, " ")
	out = strings.ReplaceAll(out, `\;`, " ")
	out = strings.ReplaceAll(out, `\quad`, "  ")
	out = strings.ReplaceAll(out, `\qquad`, "    ")
	out = strings.ReplaceAll(out, `\ `, " ")
	out = strings.ReplaceAll(out, `~`, " ")

	// 6. Structural constructs
	out = renderFractions(out)
	out = renderSquareRoots(out)

	// 7. Superscripts: ^{...} and single-char ^x
	out = renderSuperscripts(out)

	// 8. Subscripts: _{...} and single-char _x
	out = renderSubscripts(out)

	// 9. Cleanup text commands
	out = strings.ReplaceAll(out, `\text{`, "")
	out = strings.ReplaceAll(out, `\mathrm{`, "")
	out = strings.ReplaceAll(out, `\mathbf{`, "")
	out = strings.ReplaceAll(out, `\mathit{`, "")
	out = strings.ReplaceAll(out, `\mathcal{`, "")
	out = strings.ReplaceAll(out, `\operatorname{`, "")
	out = strings.ReplaceAll(out, `\text`, "")
	out = strings.ReplaceAll(out, `\mathrm`, "")
	out = strings.ReplaceAll(out, `\mathbf`, "")
	out = strings.ReplaceAll(out, `\displaystyle`, "")
	out = strings.ReplaceAll(out, `\limits`, "")

	// 10. Remove leftover braces from processed constructs
	out = removeBraces(out)

	// 11. Remaining \cmd that we didn't handle → keep as-is (strip backslash)
	out = unknownCmdRe.ReplaceAllString(out, "$1")

	return strings.TrimSpace(out)
}

// replaceLongestFirst replaces all keys in the map with their values,
// processing longest keys first to prevent partial matches (e.g. \int before \in).
func replaceLongestFirst(s string, m map[string]string) string {
	// Sort keys by length descending
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort by length descending
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && len(keys[j]) > len(keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	for _, k := range keys {
		s = strings.ReplaceAll(s, k, m[k])
	}
	return s
}

// ---------- Greek Letters ----------

var greekLetters = map[string]string{
	`\alpha`:      "α",
	`\beta`:       "β",
	`\gamma`:      "γ",
	`\delta`:      "δ",
	`\epsilon`:    "ε",
	`\varepsilon`: "ε",
	`\zeta`:       "ζ",
	`\eta`:        "η",
	`\theta`:      "θ",
	`\vartheta`:   "ϑ",
	`\iota`:       "ι",
	`\kappa`:      "κ",
	`\lambda`:     "λ",
	`\mu`:         "μ",
	`\nu`:         "ν",
	`\xi`:         "ξ",
	`\pi`:         "π",
	`\varpi`:      "ϖ",
	`\rho`:        "ρ",
	`\sigma`:      "σ",
	`\tau`:        "τ",
	`\upsilon`:    "υ",
	`\phi`:        "φ",
	`\varphi`:     "φ",
	`\chi`:        "χ",
	`\psi`:        "ψ",
	`\omega`:      "ω",
	// Uppercase
	`\Gamma`:   "Γ",
	`\Delta`:   "Δ",
	`\Theta`:   "Θ",
	`\Lambda`:  "Λ",
	`\Xi`:      "Ξ",
	`\Pi`:      "Π",
	`\Sigma`:   "Σ",
	`\Upsilon`: "Υ",
	`\Phi`:     "Φ",
	`\Psi`:     "Ψ",
	`\Omega`:   "Ω",
}

// ---------- Operators ----------

var operators = map[string]string{
	`\sum`:        "∑",
	`\prod`:       "∏",
	`\coprod`:     "∐",
	`\oint`:       "∮",
	`\iiint`:      "∭",
	`\iint`:       "∬",
	`\int`:        "∫",
	`\partial`:    "∂",
	`\nabla`:      "∇",
	`\infty`:      "∞",
	`\pm`:         "±",
	`\mp`:         "∓",
	`\times`:      "×",
	`\div`:        "÷",
	`\cdot`:       "·",
	`\circ`:       "∘",
	`\bullet`:     "•",
	`\star`:       "★",
	`\approx`:     "≈",
	`\neq`:        "≠",
	`\leq`:        "≤",
	`\geq`:        "≥",
	`\le`:         "≤",
	`\ge`:         "≥",
	`\ll`:         "≪",
	`\gg`:         "≫",
	`\equiv`:      "≡",
	`\sim`:        "∼",
	`\simeq`:      "≃",
	`\propto`:     "∝",
	`\perp`:       "⊥",
	`\parallel`:   "∥",
	`\angle`:      "∠",
	`\triangle`:   "△",
	`\square`:     "□",
	`\subseteq`:   "⊆",
	`\supseteq`:   "⊇",
	`\subset`:     "⊂",
	`\supset`:     "⊃",
	`\notin`:      "∉",
	`\in`:         "∈",
	`\cup`:        "∪",
	`\cap`:        "∩",
	`\emptyset`:   "∅",
	`\varnothing`: "∅",
	`\forall`:     "∀",
	`\exists`:     "∃",
	`\neg`:        "¬",
	`\land`:       "∧",
	`\lor`:        "∨",
	`\oplus`:      "⊕",
	`\otimes`:     "⊗",
	`\odot`:       "⊙",
	`\hbar`:       "ℏ",
	`\ell`:        "ℓ",
	`\Re`:         "ℜ",
	`\Im`:         "ℑ",
	`\aleph`:      "ℵ",
	`\wp`:         "℘",
	`\prime`:      "′",
	`\dagger`:     "†",
	`\ddagger`:    "‡",
	`\cdots`:      "⋯",
	`\ldots`:      "…",
	`\vdots`:      "⋮",
	`\ddots`:      "⋱",
	`\%`:          "%",
	`\&`:          "&",
	`\#`:          "#",
	`\_`:          "_",
	`\{`:          "{",
	`\}`:          "}",
}

// ---------- Arrows ----------

var arrows = map[string]string{
	`\rightarrow`:     "→",
	`\to`:             "→",
	`\leftarrow`:      "←",
	`\gets`:           "←",
	`\leftrightarrow`: "↔",
	`\Rightarrow`:     "⇒",
	`\Leftarrow`:      "⇐",
	`\Leftrightarrow`: "⇔",
	`\uparrow`:        "↑",
	`\downarrow`:      "↓",
	`\updownarrow`:    "↕",
	`\Uparrow`:        "⇑",
	`\Downarrow`:      "⇓",
	`\Updownarrow`:    "⇕",
	`\mapsto`:         "↦",
	`\hookrightarrow`: "↪",
	`\nearrow`:        "↗",
	`\searrow`:        "↘",
	`\swarrow`:        "↙",
	`\nwarrow`:        "↖",
	`\rightharpoonup`: "⇀",
	`\leftharpoonup`:  "↼",
}

// ---------- Superscript / Subscript ----------

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

// fracRe matches \frac{num}{den} or \dfrac{num}{den}.
var fracRe = regexp.MustCompile(`\\(?:d)?frac\{([^}]*)}\{([^}]*)}`)

// sqrtRe matches \sqrt[n]{content} or \sqrt{content}.
var sqrtRe = regexp.MustCompile(`\\sqrt(?:\[(\w+)\])?\{([^}]*)}`)

// supRe matches ^{content} (braced multi-char superscript).
var supBraceRe = regexp.MustCompile(`\^{([^}]+)}`)

// supSingleRe matches ^x (single-char superscript, not a brace).
var supSingleRe = regexp.MustCompile(`\^([^\\{_\s])`)

// subBraceRe matches _{content} (braced multi-char subscript).
var subBraceRe = regexp.MustCompile(`_{([^}]+)}`)

// subSingleRe matches _x (single-char subscript, not a brace).
var subSingleRe = regexp.MustCompile(`_([^\\{^\s])`)

// unknownCmdRe matches remaining \word commands.
var unknownCmdRe = regexp.MustCompile(`\\([a-zA-Z]+)`)

// renderFractions converts \frac{a}{b} → a/b.
func renderFractions(s string) string {
	return fracRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := fracRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		num := strings.TrimSpace(sub[1])
		den := strings.TrimSpace(sub[2])
		if num == "" || den == "" {
			return match
		}
		return num + "/" + den
	})
}

// renderSquareRoots converts \sqrt{x} → √x and \sqrt[n]{x} → ⁿ√x.
func renderSquareRoots(s string) string {
	return sqrtRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := sqrtRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		index := strings.TrimSpace(sub[1])
		content := strings.TrimSpace(sub[2])
		if index != "" {
			return index + "√" + content
		}
		return "√" + content
	})
}

// renderSuperscripts converts ^{2} → ², ^n → ⁿ, etc.
func renderSuperscripts(s string) string {
	// First: braced multi-char ^{content}
	s = supBraceRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := supBraceRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		return toSuperscript(sub[1])
	})
	// Then: single-char ^x
	s = supSingleRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := supSingleRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		return toSuperscript(sub[1])
	})
	return s
}

// renderSubscripts converts _{2} → ₂, _i → ᵢ, etc.
func renderSubscripts(s string) string {
	// First: braced multi-char _{content}
	s = subBraceRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := subBraceRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		return toSubscript(sub[1])
	})
	// Then: single-char _x
	s = subSingleRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := subSingleRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		return toSubscript(sub[1])
	})
	return s
}

// toSuperscript converts a string to superscript Unicode where possible.
// Characters without a superscript equivalent are kept as-is (no fallback prefix).
func toSuperscript(s string) string {
	var buf strings.Builder
	allConverted := true
	for _, r := range s {
		if sr, ok := superscriptDigits[r]; ok {
			buf.WriteRune(sr)
		} else {
			buf.WriteRune(r)
			allConverted = false
		}
	}
	_ = allConverted
	return buf.String()
}

// toSubscript converts a string to subscript Unicode where possible.
// Characters without a subscript equivalent are kept as-is (no fallback prefix).
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

// removeBraces strips remaining { and } that are not needed for display.
// It uses a simple heuristic: remove braces that wrap content without
// any surrounding command context.
func removeBraces(s string) string {
	// Iteratively remove { } pairs. This is conservative — we only
	// remove when the content inside is plain text without nested braces.
	for strings.Contains(s, "{") {
		next := bracePairRe.ReplaceAllStringFunc(s, func(match string) string {
			// Strip the outer braces
			inner := match[1 : len(match)-1]
			if !strings.ContainsAny(inner, "{}") {
				return inner
			}
			return match
		})
		if next == s {
			break // no progress, stop
		}
		s = next
	}
	return s
}

// bracePairRe matches the innermost {content} pair (no nested braces inside).
var bracePairRe = regexp.MustCompile(`\{([^{}]*)}`)

// hasMath detects whether content contains LaTeX math expressions.
func hasMath(content string) bool {
	return mathBlockRe.MatchString(content) || mathInlineRe.MatchString(content)
}

// looksLikeMath returns true if the content between $ delimiters looks like
// a LaTeX math expression rather than plain text with dollar signs (currency).
// Heuristics: contains backslash commands, superscripts (^), subscripts (_),
// or common math operators.
var mathIndicatorRe = regexp.MustCompile(`\\[a-zA-Z]|\^|_|[{}]|\\[+\-=\{\}_#%&]`)

func looksLikeMath(s string) bool {
	return mathIndicatorRe.MatchString(s)
}
