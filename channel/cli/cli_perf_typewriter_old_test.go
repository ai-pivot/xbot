package cli

import (
	"fmt"
	"testing"

	"xbot/protocol"
)

// These benchmarks simulate the OLD behavior (before deferred rendering):
// every call to liveIterationBlocks with changed content triggers glamour.
// We force this by clearing liveContentRendered (which makes needRender=true
// via the first check: liveContentRendered == "").

// BenchmarkFastAPIMixedOld: 2 stream arrivals + 1 typewriter tick.
// OLD: each stream arrival triggers glamour (2 glamour calls).
// NEW (deferred): stream arrivals skip glamour if typewriter is behind (0-1 glamour).
func BenchmarkFastAPIMixedOld(b *testing.B) {
	model := setupStreamingModelWithIters(10)
	baseContent := "Analyzing the code structure for step %d with **important** patterns.\n\n```go\nfunc process(data []byte) error {\n    return nil\n}\n```\n\nDone!"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// 2 stream arrivals — OLD: each triggers glamour
		model.progressState.current = &protocol.ProgressEvent{
			Iteration:     11,
			Phase:         "running",
			StreamContent: fmt.Sprintf(baseContent, i*2),
			ChatID:        "cli:/test",
		}
		model.rc.liveContentRendered = "" // force glamour (old behavior)
		model.updateStreamingOnly()

		model.progressState.current.StreamContent = fmt.Sprintf(baseContent, i*2+1)
		model.rc.liveContentRendered = "" // force glamour (old behavior)
		model.updateStreamingOnly()

		// 1 typewriter tick — content unchanged → cache hit in both old and new
		totalRunes := len([]rune(model.progressState.current.StreamContent))
		model.progressState.twVisible = totalRunes / 3
		model.updateStreamingOnly()
	}
}

// BenchmarkMediumAPIMixedOld: 1 stream + 1 typewriter tick.
func BenchmarkMediumAPIMixedOld(b *testing.B) {
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
		model.rc.liveContentRendered = "" // force glamour (old behavior)
		model.updateStreamingOnly()

		totalRunes := len([]rune(model.progressState.current.StreamContent))
		model.progressState.twVisible = totalRunes / 2
		model.updateStreamingOnly() // cache hit
	}
}

// BenchmarkSlowAPIMixedOld: 1 stream + 9 typewriter ticks.
func BenchmarkSlowAPIMixedOld(b *testing.B) {
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
		model.rc.liveContentRendered = "" // force glamour (old behavior)
		model.updateStreamingOnly()

		totalRunes := len([]rune(model.progressState.current.StreamContent))
		for t := 1; t <= 9; t++ {
			model.progressState.twVisible = totalRunes * t / 10
			model.updateStreamingOnly() // cache hit
		}
	}
}
