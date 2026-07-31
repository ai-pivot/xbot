package cli

import (
	"fmt"
	"testing"

	"xbot/protocol"
)

// BenchmarkTypewriterCacheHit measures the cost of a typewriter tick where
// StreamContent has NOT changed (cache hit — glamour skipped).
// This is the hot path: between stream arrivals, multiple ticks render
// the same content. With caching, only the first runs glamour.
func BenchmarkTypewriterCacheHit(b *testing.B) {
	model := setupStreamingModelWithIters(10)

	model.progressState.current = &protocol.ProgressEvent{
		Iteration:              11,
		Phase:                  "running",
		StreamContent:          "I am now analyzing the results and thinking about the next step in this complex task that requires careful consideration of multiple factors.",
		ReasoningStreamContent: "Let me think about this carefully. The user wants me to analyze the code and find the root cause of the performance issue.",
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go build ./...", Status: "running", Iteration: 11},
		},
		ChatID: "cli:/test",
	}

	// First call: cache miss (glamour runs, populates cache).
	model.updateStreamingOnly()

	// Set typewriter to partially visible (simulates mid-typewriter).
	totalRunes := len([]rune(model.progressState.current.StreamContent))
	model.progressState.twVisible = totalRunes / 2
	model.progressState.rwVisible = len([]rune(model.progressState.current.ReasoningStreamContent)) / 2

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Each iteration: content unchanged, only twVisible advances by 1.
		model.progressState.twVisible++
		if model.progressState.twVisible > totalRunes {
			model.progressState.twVisible = 1
		}
		model.updateStreamingOnly()
	}
}

// BenchmarkTypewriterCacheMiss measures the cost when StreamContent CHANGES
// (cache miss — glamour runs). This happens on every stream arrival.
func BenchmarkTypewriterCacheMiss(b *testing.B) {
	model := setupStreamingModelWithIters(10)

	contents := make([]string, 100)
	for i := range contents {
		contents[i] = fmt.Sprintf("Analyzing step %d: the code structure shows **important** patterns that need attention.\n\n```go\nfunc process(data []byte) error {\n    return nil\n}\n```", i)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		model.progressState.current = &protocol.ProgressEvent{
			Iteration:     11,
			Phase:         "running",
			StreamContent: contents[i%len(contents)],
			ChatID:        "cli:/test",
		}
		model.updateStreamingOnly()
	}
}

// BenchmarkTypewriterNoCache simulates the OLD behavior (before optimization):
// glamour.Render() runs on every tick regardless of content change.
func BenchmarkTypewriterNoCache(b *testing.B) {
	model := setupStreamingModelWithIters(10)

	model.progressState.current = &protocol.ProgressEvent{
		Iteration:     11,
		Phase:         "running",
		StreamContent: "I am now analyzing the results and thinking about the next step in this complex task that requires careful consideration of multiple factors.",
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go build ./...", Status: "running", Iteration: 11},
		},
		ChatID: "cli:/test",
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		model.rc.liveContentKey = "" // force cache miss (old behavior)
		model.updateStreamingOnly()
	}
}

// BenchmarkFastAPIMixed simulates a fast API (20ms/chunk) with typewriter
// ticks at 50ms. Each iteration = 2 stream arrivals + 1 typewriter tick.
func BenchmarkFastAPIMixed(b *testing.B) {
	model := setupStreamingModelWithIters(10)
	baseContent := "Analyzing the code structure for step %d with **important** patterns.\n\n```go\nfunc process(data []byte) error {\n    return nil\n}\n```\n\nDone!"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		model.progressState.current = &protocol.ProgressEvent{
			Iteration:     11,
			Phase:         "running",
			StreamContent: fmt.Sprintf(baseContent, i*2),
			ChatID:        "cli:/test",
		}
		model.updateStreamingOnly()

		model.progressState.current.StreamContent = fmt.Sprintf(baseContent, i*2+1)
		model.updateStreamingOnly()

		totalRunes := len([]rune(model.progressState.current.StreamContent))
		model.progressState.twVisible = totalRunes / 3
		model.updateStreamingOnly()
	}
}

// BenchmarkMediumAPIMixed simulates a medium API (100ms/chunk).
// 1 stream arrival + 1 typewriter tick.
func BenchmarkMediumAPIMixed(b *testing.B) {
	model := setupStreamingModelWithIters(10)
	baseContent := "Analyzing the code structure for step %d with **important** patterns.\n\n```go\nfunc process(data []byte) error {\n    return nil\n}\n```\n\nDone!"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		model.progressState.current = &protocol.ProgressEvent{
			Iteration:     11,
			Phase:         "running",
			StreamContent: fmt.Sprintf(baseContent, i),
			ChatID:        "cli:/test",
		}
		model.updateStreamingOnly()

		totalRunes := len([]rune(model.progressState.current.StreamContent))
		model.progressState.twVisible = totalRunes / 2
		model.updateStreamingOnly()
	}
}

// BenchmarkSlowAPIMixed simulates a slow API (500ms/chunk).
// 1 stream arrival + 9 typewriter ticks.
func BenchmarkSlowAPIMixed(b *testing.B) {
	model := setupStreamingModelWithIters(10)
	baseContent := "Analyzing the code structure for step %d with **important** patterns.\n\n```go\nfunc process(data []byte) error {\n    return nil\n}\n```\n\nDone!"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		model.progressState.current = &protocol.ProgressEvent{
			Iteration:     11,
			Phase:         "running",
			StreamContent: fmt.Sprintf(baseContent, i),
			ChatID:        "cli:/test",
		}
		model.updateStreamingOnly()

		totalRunes := len([]rune(model.progressState.current.StreamContent))
		for t := 1; t <= 9; t++ {
			model.progressState.twVisible = totalRunes * t / 10
			model.updateStreamingOnly()
		}
	}
}
