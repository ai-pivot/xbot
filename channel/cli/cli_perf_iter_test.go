package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"
	"time"

	"xbot/protocol"
)

// makeMarkdownIteration creates a realistic iteration snapshot with markdown content.
// Every iteration has Thinking with markdown (code blocks, lists, bold, etc.)
// plus two completed tools — matching real agent work patterns.
func makeMarkdownIteration(n int) cliIterationSnapshot {
	return cliIterationSnapshot{
		Iteration: n,
		Thinking: fmt.Sprintf(`Looking at step **%d**, I need to:

1. Read the relevant files in `+"`pkg/%d`"+`
2. Analyze the data structure
3. Apply the transformation

`+"```"+`go
func process(data []byte, n int) error {
    if len(data) == 0 {
		return fmt.Errorf("empty data at step %%d")
    }
    for i, entry := range data {
		fmt.Printf("[%%d] %%v", i, entry)
    }
    return nil
    }
`+"```"+`

This ensures **correctness** and *efficiency* for the `+"`n=%d`"+` case.`, n, n, n),
		Reasoning: fmt.Sprintf("Let me think about iteration %d. The user wants me to analyze the code carefully and make sure the logic is correct before proceeding to the next step.", n),
		Tools: []protocol.ToolProgress{
			{Name: "Read", Label: fmt.Sprintf("read file_%d.go", n), Status: "done", Elapsed: int64(50 + n%100), Iteration: n},
			{Name: "Shell", Label: fmt.Sprintf("go test ./pkg/%d/...", n), Status: "done", Elapsed: int64(200 + n%500), Iteration: n},
		},
	}
}

// setupStreamingModelWithIters creates a cliModel in streaming mode with n completed
// iterations (each containing markdown content). The render cache is warmed so that
// streamCompletedLines is populated for the n iterations.
func setupStreamingModelWithIters(n int) *cliModel {
	model := newCLIModel()
	model.handleResize(120, 40)
	model.channelName = "cli"
	model.chatID = "/test"
	model.splashState.done = true
	model.agentTurnID = 1
	model.typing = true

	// Add a streaming (partial) assistant message.
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		content:   "",
		isPartial: true,
		turnID:    1,
		timestamp: time.Now(),
	})
	model.streamingMsgIdx = 0

	// Populate n completed iterations with markdown content.
	iters := make([]cliIterationSnapshot, n)
	for i := range iters {
		iters[i] = makeMarkdownIteration(i + 1)
	}
	model.progressState.iterations = iters
	model.progressState.lastIter = n

	// Set live progress (current iteration in progress).
	model.progressState.current = &protocol.ProgressEvent{
		Iteration: n + 1,
		Phase:     "running",
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go build ./...", Status: "running", Iteration: n + 1},
		},
		ChatID: "cli:/test",
	}

	// Initialize histLines (needed for streaming assembly path).
	model.rc.histLines = []string{"[previous turn message content]"}
	model.rc.histMaxW = 40
	model.rc.bumpHistGen()

	// Warm the cache — this populates streamCompletedLines.
	model.updateStreamingOnly()

	return model
}

// -----------------------------------------------------------------------
// Benchmark 1: Tick render cost with different iteration counts.
// Measures updateStreamingOnly() on a tick where NOTHING changed
// (cache hit for completed, same live iteration). This isolates the
// allLines assembly cost which should be O(1) but is currently O(N).
// -----------------------------------------------------------------------
func BenchmarkTickRenderByIterCount(b *testing.B) {
	for _, n := range []int{10, 50, 100, 500, 1000, 2000, 3000} {
		b.Run(fmt.Sprintf("iters_%d", n), func(b *testing.B) {
			model := setupStreamingModelWithIters(n)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				model.updateStreamingOnly()
			}
		})
	}
}

// -----------------------------------------------------------------------
// Benchmark 2: New iteration completion cost.
// Measures the incremental render path when a new iteration is added.
// The cache is tricked back to n iterations each loop so the incremental
// path renders exactly 1 new iteration.
// -----------------------------------------------------------------------
func BenchmarkNewIterRenderByIterCount(b *testing.B) {
	for _, n := range []int{10, 50, 100, 500, 1000, 2000, 3000} {
		b.Run(fmt.Sprintf("iters_%d", n), func(b *testing.B) {
			model := setupStreamingModelWithIters(n)

			// Add the new iteration (n+1) to the model.
			model.progressState.iterations = append(
				model.progressState.iterations, makeMarkdownIteration(n+1))

			// Save the cache state at n iterations for reset.
			savedLines := make([]string, len(model.rc.streamCompletedLines))
			copy(savedLines, model.rc.streamCompletedLines)
			savedCount := model.rc.streamCompletedCount
			savedMaxW := model.rc.streamMaxW

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				// Reset cache to n iterations (forces incremental render of n+1).
				model.rc.streamCompletedLines = savedLines
				model.rc.streamCompletedCount = savedCount
				model.rc.streamMaxW = savedMaxW
				model.updateStreamingOnly()
			}
		})
	}
}

// -----------------------------------------------------------------------
// Benchmark 3: Stream message + tick render.
// Measures the combined cost of receiving a stream-only progress event
// (merged into current, O(1)) + the subsequent tick render (updateStreamingOnly).
// -----------------------------------------------------------------------
func BenchmarkStreamMsgPlusTickByIterCount(b *testing.B) {
	for _, n := range []int{10, 50, 100, 500, 1000, 2000, 3000} {
		b.Run(fmt.Sprintf("iters_%d", n), func(b *testing.B) {
			model := setupStreamingModelWithIters(n)

			streamPayload := &protocol.ProgressEvent{
				Phase:         "",
				Iteration:     0,
				StreamContent: "I am now analyzing the results and thinking about the next step in this complex task...",
				ChatID:        "cli:/test",
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				// Process stream-only event (handleProgressMsg → merge → return).
				model.handleProgressMsg(cliProgressMsg{payload: streamPayload})
				// Tick render.
				model.updateStreamingOnly()
			}
		})
	}
}

// -----------------------------------------------------------------------
// CPU Profile test: writes profiles for 10-iter vs 3000-iter tick renders
// so they can be compared with `go tool pprof`.
//
// Run:
//
//	go test -run=TestCPUIterationProfile -v -cpuprofile=/dev/null ./channel/cli/
//
// The test writes individual profile files to /tmp/ that can be analyzed:
//
//	go tool pprof /tmp/cli_tick_10iters.prof
//	go tool pprof /tmp/cli_tick_3000iters.prof
//
// -----------------------------------------------------------------------
func TestCPUIterationProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CPU profile test in short mode")
	}

	cases := []struct {
		name  string
		iters int
	}{
		{"10iters", 10},
		{"3000iters", 3000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := setupStreamingModelWithIters(tc.iters)

			profPath := filepath.Join(os.TempDir(), fmt.Sprintf("cli_tick_%s.prof", tc.name))
			f, err := os.Create(profPath)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			if err := pprof.StartCPUProfile(f); err != nil {
				t.Fatal(err)
			}

			// Run enough iterations to get a meaningful profile.
			const N = 2000
			for i := 0; i < N; i++ {
				model.updateStreamingOnly()
			}

			pprof.StopCPUProfile()

			// Report per-call timing.
			result := testing.Benchmark(func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					model.updateStreamingOnly()
				}
			})
			t.Logf("%s: %s (N=%d)", tc.name, result.String(), tc.iters)
			t.Logf("CPU profile saved to: %s", profPath)
			t.Logf("Analyze: go tool pprof -top %s", profPath)
		})
	}
}
