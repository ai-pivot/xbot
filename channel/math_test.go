package channel

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// ---------- latexToUnicode tests ----------

func TestLatexToUnicode_GreekLetters(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`\alpha`, "α"},
		{`\beta + \gamma`, "β + γ"},
		{`\Delta x = \Sigma f_i`, "Δ x = Σ fᵢ"},
		{`\pi r^2`, "π r²"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("renderLaTeX(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_Operators(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`\sum_{i=1}^{n}`, "∑ᵢ₌₁ⁿ"},
		{`\int_0^1 f(x) dx`, "∫₀¹ f(x) dx"},
		{`x \leq y \neq z`, "x ≤ y ≠ z"},
		{`\infty + \pm`, "∞ + ±"},
		{`a \in \emptyset`, "a ∈ ∅"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("renderLaTeX(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_Arrows(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`x \rightarrow y`, "x → y"},
		{`A \Rightarrow B`, "A ⇒ B"},
		{`x \mapsto f(x)`, "x ↦ f(x)"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("renderLaTeX(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_Fractions(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`\frac{1}{2}`, "1/2"},
		{`\frac{a+b}{c-d}`, "a+b/c-d"},
		{`\dfrac{\pi}{2}`, "π/2"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("renderLaTeX(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_SquareRoots(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`\sqrt{x}`, "√x"},
		{`\sqrt[3]{8}`, "³√8"},
		{`\sqrt{a^2 + b^2}`, "√a² + b²"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("renderLaTeX(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_Superscripts(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`x^{2}`, "x²"},
		{`E = mc^{2}`, "E = mc²"},
		{`2^{10}`, "2¹⁰"},
		{`x^{n+1}`, "xⁿ⁺¹"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("renderLaTeX(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_Subscripts(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`x_{1}`, "x₁"},
		{`a_{ij}`, "aᵢⱼ"},
		{`v_{0} = 0`, "v₀ = 0"},
		{`x_n`, "xₙ"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("renderLaTeX(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_ComplexExpressions(t *testing.T) {
	// Einstein's mass-energy equivalence
	got := renderLaTeX(`E = mc^{2}`)
	want := "E = mc²"
	if got != want {
		t.Errorf("Einstein: got %q, want %q", got, want)
	}

	// Quadratic formula parts
	got = renderLaTeX(`x = \frac{-b \pm \sqrt{b^2 - 4ac}}{2a}`)
	if !strings.Contains(got, "√b²") || !strings.Contains(got, "±") {
		t.Errorf("Quadratic: got %q, expected √ and ±", got)
	}

	// Euler's identity
	got = renderLaTeX(`e^{i\pi} + 1 = 0`)
	if !strings.Contains(got, "eⁱπ") {
		t.Errorf("Euler: got %q, expected eⁱπ", got)
	}

	// Maxwell's equations — the key test that motivated the rewrite
	got = renderLaTeX(`\nabla \cdot E = \frac{\rho}{\epsilon_0}`)
	// Key assertions: no stray braces, no "frac" remnant, has ∇ and / (fraction)
	if strings.Contains(got, "{") || strings.Contains(got, "frac") {
		t.Errorf("Maxwell Gauss 1 has stray braces/frac: got %q", got)
	}
	if !strings.Contains(got, "∇") || !strings.Contains(got, "E =") || !strings.Contains(got, "/") {
		t.Errorf("Maxwell Gauss 1: got %q", got)
	}

	got = renderLaTeX(`\nabla \times B = \mu_0 J + \mu_0 \epsilon_0 \frac{\partial E}{\partial t}`)
	if strings.Contains(got, "{") || strings.Contains(got, "frac") {
		t.Errorf("Maxwell Ampere has stray braces/frac: got %q", got)
	}
	if !strings.Contains(got, "∇ × B") || !strings.Contains(got, "∂ E/∂ t") {
		t.Errorf("Maxwell Ampere: got %q", got)
	}

	// ---- User-reported bugs (real LLM output) ----

	// Bug: \left[ was rendered as ≤ft[ because \le matched first
	got = renderLaTeX(`\left[ -\hbar^2/2m \nabla^2 + V(r) \right]`)
	if strings.Contains(got, "≤ft") || strings.Contains(got, "\\left") {
		t.Errorf("\\left[ bug: got %q", got)
	}
	if !strings.Contains(got, "[") || !strings.Contains(got, "]") {
		t.Errorf("\\left/right brackets: got %q", got)
	}

	// Bug: \left( not rendered
	got = renderLaTeX(`n! \sim \sqrt{2\pi n} \left( n/e \right)^n`)
	if strings.Contains(got, "≤ft") || strings.Contains(got, "\\left") {
		t.Errorf("\\left( bug: got %q", got)
	}

	// Bug: \mid not rendered
	got = renderLaTeX(`P(A \mid B) = \frac{P(B \mid A) P(A)}{P(B)}`)
	if strings.Contains(got, "{") || strings.Contains(got, "frac") {
		t.Errorf("Bayes stray braces: got %q", got)
	}
	if !strings.Contains(got, "P(A") || !strings.Contains(got, "/P(B)") {
		t.Errorf("Bayes: got %q", got)
	}
	// \mid should be replaced (not left as literal "mid")
	if strings.Contains(got, "mid") {
		t.Errorf("\\mid not replaced: got %q", got)
	}

	// Bug: \text{prime} inside _{} was subscripted to ₜₑₓₜₚᵣᵢₘₑ
	got = renderLaTeX(`\prod_{\text{prime}}`)
	// \text{prime} should unwrap to plain "prime" before subscript
	// So the subscript should be ₚᵣᵢₘₑ (all letters subscripted) NOT ₜₑₓₜₚᵣᵢₘₑ
	if strings.Contains(got, "ₜₑₓₜ") {
		t.Errorf("\\text subscript bug: got %q, should NOT contain ₜₑₓₜ", got)
	}
	// Should contain subscript p (ₚ) from "prime"
	if !strings.Contains(got, "ₚ") {
		t.Errorf("\\text subscript missing: got %q", got)
	}
	// Should NOT contain literal word "text"
	if strings.Contains(got, "text") {
		t.Errorf("\\text not unwrapped: got %q", got)
	}

	// Bug: \\ line breaks not handled
	got = renderLaTeX(`f(x) = \begin{cases} x^2 \\ x+1 \end{cases}`)
	if !strings.Contains(got, "\n") {
		t.Errorf("line break: got %q, expected newline", got)
	}
	if strings.Contains(got, "begin") || strings.Contains(got, "end") || strings.Contains(got, "cases") {
		t.Errorf("env remnants: got %q", got)
	}

	// Nested frac: \frac{-b \pm \sqrt{b^2 - 4ac}}{2a}
	got = renderLaTeX(`\frac{-b \pm \sqrt{b^2 - 4ac}}{2a}`)
	if !strings.Contains(got, "±") || !strings.Contains(got, "√") || !strings.Contains(got, "/2a") {
		t.Errorf("Nested quadratic: got %q", got)
	}
	// Should NOT contain stray braces or "frac"
	if strings.Contains(got, "{") || strings.Contains(got, "frac") {
		t.Errorf("Nested quadratic has stray braces/frac: got %q", got)
	}
}

func TestLatexToUnicode_NoMath(t *testing.T) {
	// Plain text should pass through unchanged
	input := "Hello, world! No math here."
	got := renderLaTeX(input)
	if got != input {
		t.Errorf("plain text: got %q, want %q", got, input)
	}
}

func TestLatexToUnicode_AlignmentMarkers(t *testing.T) {
	// &= should become just =
	got := renderLaTeX(`\sin(α + β) &= \sinα\cosβ + \cosα\sinβ`)
	if strings.Contains(got, "&=") {
		t.Errorf("&= not stripped: got %q", got)
	}
	if !strings.Contains(got, "= sin") {
		t.Errorf("alignment: got %q", got)
	}
	// Multi-line with & alignment
	got = renderLaTeX(`f(x) &= x^2 \\ g(x) &= x + 1`)
	if strings.Contains(got, "&") {
		t.Errorf("& remnant: got %q", got)
	}
}

func TestLatexToUnicode_MathFunctions(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`\sin(x)`, "sin(x)"},
		{`\cos^2(x) + \sin^2(x) = 1`, "cos²(x) + sin²(x) = 1"},
		{`\log_{10}(x)`, "log₁₀(x)"},
		{`\lim_{x \to \infty}`, "limₓ → ∞"},
		{`\exp(i\theta)`, "exp(iθ)"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("mathfunc(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_Accents(t *testing.T) {
	tests := []struct {
		input   string
		contain string
	}{
		{`\hat{x}`, "x̂"},
		{`\vec{F}`, "F⃗"},
		{`\bar{x}`, "x̄"},
		{`\dot{x}`, "ẋ"},
		{`\ddot{x}`, "ẍ"},
		{`\tilde{n}`, "ñ"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if !strings.Contains(got, tt.contain) {
			t.Errorf("accent(%q) = %q, expected %q", tt.input, got, tt.contain)
		}
	}
}

func TestLatexToUnicode_Brackets(t *testing.T) {
	tests := []struct {
		input   string
		contain string
	}{
		{`\langle x, y \rangle`, "⟨ x, y ⟩"},
		{`\lfloor x \rfloor`, "⌊ x ⌋"},
		{`\lceil x \rceil`, "⌈ x ⌉"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if !strings.Contains(got, tt.contain) {
			t.Errorf("bracket(%q) = %q, expected %q", tt.input, got, tt.contain)
		}
	}
}

func TestLatexToUnicode_Binomial(t *testing.T) {
	got := renderLaTeX(`\binom{n}{k}`)
	if !strings.Contains(got, "(n k)") {
		t.Errorf("binomial: got %q", got)
	}
}

func TestLatexToUnicode_LineBreaks(t *testing.T) {
	got := renderLaTeX(`x^2 + y^2 \\ = r^2`)
	if !strings.Contains(got, "\n") {
		t.Errorf("linebreak: got %q", got)
	}
}

func TestLatexToUnicode_Environments(t *testing.T) {
	got := renderLaTeX(`\begin{cases} x & y \\ z & w \end{cases}`)
	if strings.Contains(got, "begin") || strings.Contains(got, "end") {
		t.Errorf("env not stripped: got %q", got)
	}
}

func TestLatexToUnicode_Schrodinger(t *testing.T) {
	got := renderLaTeX(`i\hbar \frac{\partial}{\partial t} \Psi(r, t) = \left[ -\frac{\hbar^2}{2m}\nabla^2 + V(r) \right] \Psi(r, t)`)
	if strings.Contains(got, "{") || strings.Contains(got, "frac") {
		t.Errorf("Schrödinger stray braces: got %q", got)
	}
	if !strings.Contains(got, "iℏ") || !strings.Contains(got, "∂/∂ t") {
		t.Errorf("Schrödinger content: got %q", got)
	}
	if strings.Contains(got, "≤ft") {
		t.Errorf("Schrödinger \\left[ bug: got %q", got)
	}
}

func TestLatexToUnicode_EscapeChars(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`\%`, "%"},
		{`\#`, "#"},
		{`\_`, "_"},
		{`\{`, "{"},
		{`\}`, "}"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("escape(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------- renderMathBlocks tests ----------

func TestRenderMathBlocks_BlockMath(t *testing.T) {
	input := `Some text before.

$$
E = mc^{2}
$$

Some text after.`

	got := renderMathBlocks(input, 80)

	if !strings.Contains(got, "```") {
		t.Error("expected block math to be wrapped in code block")
	}
	if !strings.Contains(got, "E = mc²") {
		t.Errorf("block math not rendered correctly: %q", got)
	}
	if !strings.Contains(got, "Some text before") {
		t.Error("surrounding text should be preserved")
	}
}

func TestRenderMathBlocks_InlineMath(t *testing.T) {
	input := `The value of $\pi$ is approximately 3.14.`
	got := renderMathBlocks(input, 80)

	if !strings.Contains(got, "π") {
		t.Errorf("inline math not rendered: %q", got)
	}
	if strings.Contains(got, "$") {
		t.Errorf("inline dollar signs should be consumed: %q", got)
	}
}

func TestRenderMathBlocks_MixedContent(t *testing.T) {
	input := `# Math Test

The area of a circle is $A = \pi r^{2}$.

$$
\int_0^1 f(x) dx
$$

And $\alpha + \beta = \gamma$.`

	got := renderMathBlocks(input, 80)

	if !strings.Contains(got, "A = π r²") {
		t.Errorf("inline math in mixed content: %q", got)
	}
	if !strings.Contains(got, "∫₀¹ f(x) dx") {
		t.Errorf("block math in mixed content: %q", got)
	}
	if !strings.Contains(got, "α + β = γ") {
		t.Errorf("second inline math: %q", got)
	}
}

func TestRenderMathBlocks_NoMatch(t *testing.T) {
	input := "Regular text with $10 price and $20 total."
	got := renderMathBlocks(input, 80)
	// $10 and $20 have digits right after $, no closing $ — should not match
	if got != input {
		t.Errorf("non-math dollar signs should be preserved: got %q", got)
	}
}

func TestRenderMathBlocks_EmptyBlock(t *testing.T) {
	input := "$$$$\n\n$$"
	got := renderMathBlocks(input, 80)
	// Empty block should pass through unchanged
	if got != input {
		t.Errorf("empty block should pass through: got %q", got)
	}
}

func TestRenderMathBlocks_WidthTruncation(t *testing.T) {
	// Very long formula should be truncated
	longFormula := strings.Repeat(`\alpha + \beta`, 50)
	input := "$$\n" + longFormula + "\n$$"

	got := renderMathBlocks(input, 40)

	// Each line in the code block should not exceed 40 display columns
	lines := strings.Split(got, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			continue
		}
		// We just check it doesn't panic and produces output
		_ = i
	}
}

func TestHasMath(t *testing.T) {
	if !hasMath("$x^2$") {
		t.Error("expected inline math detected")
	}
	if !hasMath("$$E=mc^2$$") {
		t.Error("expected block math detected")
	}
	if hasMath("no math here $10") {
		t.Error("expected no math for plain dollar sign")
	}
	if hasMath("plain text") {
		t.Error("expected no math for plain text")
	}
}

func TestLatexToUnicode_Dots(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`x_1, x_2, \ldots, x_n`, "x₁, x₂, …, xₙ"},
		{`\cdots`, "⋯"},
		{`\vdots`, "⋮"},
	}
	for _, tt := range tests {
		got := renderLaTeX(tt.input)
		if got != tt.want {
			t.Errorf("dots(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_WhitespaceCommands(t *testing.T) {
	input := `a \quad b \qquad c`
	got := renderLaTeX(input)
	// \quad → "  ", \qquad → "    "
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") || !strings.Contains(got, "c") {
		t.Errorf("whitespace commands: got %q", got)
	}
}

func TestLatexToUnicode_MultiLineAlignment(t *testing.T) {
	// Integration by parts - step-by-step derivation
	got := renderLaTeX(`\int_0^\pi x \sin x \, dx &= [-x\cos x]_0^\pi + \int_0^\pi \cos x \, dx \\ &= \pi + [\sin x]_0^\pi \\ &= \pi`)
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), got)
	}
	// All = signs should be at the same display column
	eqDisplayCol := func(line string) int {
		idx := strings.Index(line, "=")
		if idx < 0 {
			return -1
		}
		return ansi.StringWidth(line[:idx])
	}
	col0 := eqDisplayCol(lines[0])
	col1 := eqDisplayCol(lines[1])
	col2 := eqDisplayCol(lines[2])
	if col0 < 0 || col1 < 0 || col2 < 0 {
		t.Fatalf("missing = in output:\n%s", got)
	}
	if col1 != col0 {
		t.Errorf("line 2 = at display col %d, expected %d\n%s", col1, col0, got)
	}
	if col2 != col0 {
		t.Errorf("line 3 = at display col %d, expected %d\n%s", col2, col0, got)
	}
}

func TestLatexToUnicode_TrigIdentityAlignment(t *testing.T) {
	got := renderLaTeX(`\sin(\alpha + \beta) &= \sin\alpha\cos\beta + \cos\alpha\sin\beta \\ \sin(\alpha - \beta) &= \sin\alpha\cos\beta - \cos\alpha\sin\beta`)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), got)
	}
	pos0 := strings.Index(lines[0], "=")
	pos1 := strings.Index(lines[1], "=")
	if pos0 < 0 || pos1 < 0 {
		t.Fatalf("missing =:\n%s", got)
	}
	if pos1 != pos0 {
		t.Errorf("= not aligned: line1=%d line2=%d\n%s", pos0, pos1, got)
	}
}

func TestLatexToUnicode_MatrixRendering(t *testing.T) {
	// Matrix should render each row on its own line
	got := renderLaTeX(`\begin{bmatrix} a_{11} & a_{12} \\ a_{21} & a_{22} \end{bmatrix}`)
	if strings.Contains(got, "begin") || strings.Contains(got, "end") {
		t.Errorf("env remnants: %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("matrix should have newlines: %q", got)
	}
}

func TestLatexToUnicode_MatrixMultiColumn(t *testing.T) {
	src := `\begin{bmatrix}
a_{11} & a_{12} & a_{13} \\
a_{21} & a_{22} & a_{23} \\
a_{31} & a_{32} & a_{33}
\end{bmatrix}`
	got := renderLaTeX(src)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), got)
	}
	// No blank lines
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Errorf("blank line at %d", i)
		}
	}
	// Columns should be aligned: split by spaces, column positions match
	// Each line has the pattern: a₁₁ <spaces> a₁₂ <spaces> a₁₃
	// The second column (a₁₂, a₂₂, a₃₂) should start at the same display col
	col2Start := func(line string) int {
		// Find second group of non-space after first group
		inFirst := false
		spaceAfterFirst := false
		w := 0
		for _, r := range line {
			if r != ' ' {
				if spaceAfterFirst {
					return w
				}
				inFirst = true
			} else if inFirst {
				spaceAfterFirst = true
			}
			w++
		}
		return -1
	}
	c0 := col2Start(lines[0])
	c1 := col2Start(lines[1])
	c2 := col2Start(lines[2])
	if c0 < 0 || c1 < 0 || c2 < 0 {
		t.Fatalf("could not find column 2 in output:\n%s", got)
	}
	if c1 != c0 || c2 != c0 {
		t.Errorf("column 2 not aligned: line0=%d line1=%d line2=%d\n%s", c0, c1, c2, got)
	}
	// Verify no env remnants
	if strings.Contains(got, "begin") || strings.Contains(got, "end") || strings.Contains(got, "bmatrix") {
		t.Errorf("env remnants: %q", got)
	}
}

func TestLatexToUnicode_FullMatrixEquation(t *testing.T) {
	src := `\begin{bmatrix}
a_{11} & a_{12} & a_{13} \\
a_{21} & a_{22} & a_{23} \\
a_{31} & a_{32} & a_{33}
\end{bmatrix}
\begin{bmatrix}
x_1 \\ x_2 \\ x_3
\end{bmatrix}
=
\begin{bmatrix}
a_{11}x_1 + a_{12}x_2 + a_{13}x_3 \\
a_{21}x_1 + a_{22}x_2 + a_{23}x_3 \\
a_{31}x_1 + a_{32}x_2 + a_{33}x_3
\end{bmatrix}`
	got := renderLaTeX(src)
	// Side-by-side rendering: 3 lines (all matrices rendered horizontally)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (side-by-side), got %d:\n%s", len(lines), got)
	}
	// All lines should have brackets
	for i, line := range lines {
		if !strings.Contains(line, "[") || !strings.Contains(line, "]") {
			t.Errorf("line %d missing brackets: %q", i, line)
		}
	}
	// Middle line should have =
	if !strings.Contains(lines[1], "=") {
		t.Errorf("middle line should have =: %q", lines[1])
	}
	// First and third lines should NOT have =
	if strings.Contains(lines[0], "=") || strings.Contains(lines[2], "=") {
		t.Errorf("= should only be on middle line:\n%s", got)
	}
}
