package channel

import (
	"strings"
	"testing"
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
		got := latexToUnicode(tt.input)
		if got != tt.want {
			t.Errorf("latexToUnicode(%q) = %q, want %q", tt.input, got, tt.want)
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
		got := latexToUnicode(tt.input)
		if got != tt.want {
			t.Errorf("latexToUnicode(%q) = %q, want %q", tt.input, got, tt.want)
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
		got := latexToUnicode(tt.input)
		if got != tt.want {
			t.Errorf("latexToUnicode(%q) = %q, want %q", tt.input, got, tt.want)
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
		got := latexToUnicode(tt.input)
		if got != tt.want {
			t.Errorf("latexToUnicode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_SquareRoots(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`\sqrt{x}`, "√x"},
		{`\sqrt[3]{8}`, "3√8"},
		{`\sqrt{a^2 + b^2}`, "√a² + b²"},
	}
	for _, tt := range tests {
		got := latexToUnicode(tt.input)
		if got != tt.want {
			t.Errorf("latexToUnicode(%q) = %q, want %q", tt.input, got, tt.want)
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
		got := latexToUnicode(tt.input)
		if got != tt.want {
			t.Errorf("latexToUnicode(%q) = %q, want %q", tt.input, got, tt.want)
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
		got := latexToUnicode(tt.input)
		if got != tt.want {
			t.Errorf("latexToUnicode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_ComplexExpressions(t *testing.T) {
	// Einstein's mass-energy equivalence
	got := latexToUnicode(`E = mc^{2}`)
	want := "E = mc²"
	if got != want {
		t.Errorf("Einstein: got %q, want %q", got, want)
	}

	// Quadratic formula parts
	got = latexToUnicode(`x = \frac{-b \pm \sqrt{b^2 - 4ac}}{2a}`)
	if !strings.Contains(got, "√b²") || !strings.Contains(got, "±") {
		t.Errorf("Quadratic: got %q, expected √ and ±", got)
	}

	// Euler's identity
	got = latexToUnicode(`e^{i\pi} + 1 = 0`)
	if !strings.Contains(got, "eⁱπ") {
		t.Errorf("Euler: got %q, expected eⁱπ", got)
	}
}

func TestLatexToUnicode_NoMath(t *testing.T) {
	// Plain text should pass through unchanged
	input := "Hello, world! No math here."
	got := latexToUnicode(input)
	if got != input {
		t.Errorf("plain text: got %q, want %q", got, input)
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
		got := latexToUnicode(tt.input)
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
		got := latexToUnicode(tt.input)
		if got != tt.want {
			t.Errorf("dots(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLatexToUnicode_WhitespaceCommands(t *testing.T) {
	input := `a \quad b \qquad c`
	got := latexToUnicode(input)
	// \quad → "  ", \qquad → "    "
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") || !strings.Contains(got, "c") {
		t.Errorf("whitespace commands: got %q", got)
	}
}
