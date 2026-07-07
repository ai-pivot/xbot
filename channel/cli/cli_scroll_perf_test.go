package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"

	"xbot/protocol"
)

// makeANSILine creates a realistic styled line with ANSI color codes,
// simulating rendered markdown/code output. Width is capped at w.
func makeANSILine(w int) string {
	// Simulate a guide prefix + styled content (typical rendered message line)
	reset := "\x1b[0m"
	dim := "\x1b[2m"
	green := "\x1b[32m"
	cyan := "\x1b[36m"
	// Build ~w chars of content with embedded ANSI sequences
	content := fmt.Sprintf("%s┊ %s%s  This is rendered content with styling %s%d%s more text%s",
		dim, reset, green, cyan, 42, reset, reset)
	// Pad or truncate to roughly w visible chars
	visW := visibleWidth(content)
	if visW < w {
		content += strings.Repeat(" ", w-visW)
	}
	return content
}

// makeCodeLine creates a syntax-highlighted code line (many ANSI sequences).
func makeCodeLine(w int) string {
	reset := "\x1b[0m"
	keyword := "\x1b[38;5;81m"  // cyan
	str := "\x1b[38;5;114m"     // green
	comment := "\x1b[38;5;245m" // gray
	fn := "\x1b[38;5;153m"      // light blue

	content := fmt.Sprintf("%s  %d%s  %sfunc%s %sprocess%s(%sdata%s %s[]byte%s, %sn%s %sint%s) %serror%s { %s// do stuff %s\"test\"%s%s",
		comment, 1, reset,
		keyword, reset, fn, reset,
		reset, reset, keyword, reset,
		keyword, reset, keyword, reset,
		keyword, reset, comment, str, reset, reset)
	visW := visibleWidth(content)
	if visW < w {
		content += strings.Repeat(" ", w-visW)
	}
	return content
}

func visibleWidth(s string) int {
	// Strip ANSI and count runes — rough approximation of ansi.StringWidth
	cleaned := scrollStripANSI(s)
	return len([]rune(cleaned))
}

func scrollStripANSI(s string) string {
	var b strings.Builder
	inESC := false
	for _, r := range s {
		if r == '\x1b' {
			inESC = true
			continue
		}
		if inESC {
			if r == 'm' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inESC = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// setupIdleModelWithHistory creates an idle (non-streaming) model with N turns of
// history messages, each with realistic styled content. Simulates a long-context
// session where the user scrolls up to browse history.
func setupIdleModelWithHistory(numTurns int) *cliModel {
	model := newCLIModel()
	model.handleResize(120, 50)
	model.channelName = "cli"
	model.chatID = "/test"
	model.splashState.done = true
	model.ready = true
	model.typing = false

	cw := model.chatWidth()

	// Build messages: alternating user/assistant turns
	for t := 0; t < numTurns; t++ {
		// User message
		model.messages = append(model.messages, cliMessage{
			role:      "user",
			content:   fmt.Sprintf("Please analyze step %d and implement the solution", t+1),
			turnID:    uint64(t*2 + 1),
			timestamp: time.Now(),
		})
		// Assistant message with markdown content + tool summary
		assistant := cliMessage{
			role:      "assistant",
			content:   fmt.Sprintf("Looking at step **%d**, here's my analysis:\n\n```go\nfunc process(data []byte) error {\n    return nil\n}\n```\n\nDone!", t+1),
			turnID:    uint64(t*2 + 2),
			timestamp: time.Now(),
		}
		assistant.iterations = []cliIterationSnapshot{{
			Iteration: 1,
			Content:   assistant.content,
			Tools: []protocol.ToolProgress{
				{Name: "Read", Label: fmt.Sprintf("file_%d.go", t+1), Status: "done", Elapsed: 50},
				{Name: "Shell", Label: fmt.Sprintf("go test ./pkg/%d/...", t+1), Status: "done", Elapsed: 200},
			},
		}}
		model.messages = append(model.messages, assistant)
	}

	// Populate render cache (simulate fully rendered viewport)
	model.fullRebuild()

	// Set viewport content via the direct lines path
	if len(model.rc.histLines) > 0 {
		rewindLines := []string{}
		totalLines := len(model.rc.histLines) + len(rewindLines)
		model.rc.allLines = make([]string, totalLines)
		copy(model.rc.allLines, model.rc.histLines)
		model.rc.allLinesHistLen = len(model.rc.histLines)
		model.rc.allLinesGen = model.rc.histGen
		viewportSetLinesBypassMaxWidth(&model.viewport, model.rc.allLines, cw)
	}

	return model
}

// -----------------------------------------------------------------------
// Benchmark 1: viewport.View() with different total line counts.
// Measures the cost of rendering the visible viewport window when
// scrolling through different amounts of content. The YOffset is set
// to the middle of the content to simulate scrolling.
// -----------------------------------------------------------------------
func BenchmarkViewportViewScroll(b *testing.B) {
	for _, n := range []int{100, 500, 2000, 5000} {
		b.Run(fmt.Sprintf("totalLines_%d", n), func(b *testing.B) {
			model := setupIdleModelWithHistory(n / 4) // ~4 lines per turn

			// Scroll to middle
			totalLines := len(model.rc.allLines)
			if totalLines > model.viewport.Height() {
				model.viewport.SetYOffset(totalLines / 2)
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = model.viewport.View()
			}
		})
	}
}

// -----------------------------------------------------------------------
// Benchmark 2: Direct viewport.View() with synthetic ANSI lines.
// Isolates viewport rendering from xbot message system.
// -----------------------------------------------------------------------
func BenchmarkViewportViewANSILines(b *testing.B) {
	for _, config := range []struct {
		name     string
		total    int
		makeLine func(int) string
	}{
		{"plain_100", 100, func(w int) string { return strings.Repeat("x", w) }},
		{"ansi_100", 100, makeANSILine},
		{"code_100", 100, makeCodeLine},
		{"plain_2000", 2000, func(w int) string { return strings.Repeat("x", w) }},
		{"ansi_2000", 2000, makeANSILine},
		{"code_2000", 2000, makeCodeLine},
	} {
		b.Run(config.name, func(b *testing.B) {
			vp := viewport.New(viewport.WithWidth(116), viewport.WithHeight(40))

			lines := make([]string, config.total)
			for i := range lines {
				lines[i] = config.makeLine(110)
			}
			viewportSetLinesBypassMaxWidth(&vp, lines, 110)

			// Scroll to middle
			vp.SetYOffset(config.total / 2)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = vp.View()
			}
		})
	}
}

// -----------------------------------------------------------------------
// Benchmark 3: Full layoutMain() render path (title bar + viewport +
// status + footer + info bar). This is what actually runs on every
// scroll event.
// -----------------------------------------------------------------------
func BenchmarkFullLayoutScroll(b *testing.B) {
	for _, n := range []int{100, 500, 2000} {
		b.Run(fmt.Sprintf("totalLines_%d", n), func(b *testing.B) {
			model := setupIdleModelWithHistory(n / 4)

			// Scroll to middle
			totalLines := len(model.rc.allLines)
			if totalLines > model.viewport.Height() {
				model.viewport.SetYOffset(totalLines / 2)
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = model.View()
			}
		})
	}
}
