package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// snapshotToolCalls returns an ordered copy of the accumulated tool call deltas.
// Used by the onToolCall streaming callback to notify UI of tool names as
// soon as they arrive (before arguments finish streaming).
func snapshotToolCalls(toolCalls map[int]*ToolCallDelta) []ToolCallDelta {
	if len(toolCalls) == 0 {
		return nil
	}
	maxIdx := -1
	for idx := range toolCalls {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	result := make([]ToolCallDelta, 0, maxIdx+1)
	for i := 0; i <= maxIdx; i++ {
		if tc, ok := toolCalls[i]; ok {
			result = append(result, *tc)
		}
	}
	return result
}

// orderedToolCalls converts the map-based tool call accumulation into an
// ordered slice, sorted by stream index.
func orderedToolCalls(toolCalls map[int]*ToolCallDelta) []ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	maxIdx := -1
	for idx := range toolCalls {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	result := make([]ToolCall, 0, len(toolCalls))
	for i := 0; i <= maxIdx; i++ {
		tc, ok := toolCalls[i]
		if !ok {
			continue
		}
		result = append(result, ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}
	return result
}

// safeCallback invokes f with a panic recovery guard and context cancellation check.
func safeCallback(ctx context.Context, f func(string), s string) {
	if f == nil {
		return
	}
	func() {
		defer func() { recover() }()
		select {
		case <-ctx.Done():
			return
		default:
		}
		f(s)
	}()
}

// CollectStream collects all events from a stream channel and assembles them into a single LLMResponse.
// It handles content, reasoning content, tool calls (accumulating deltas by index), usage, and finish reason.
// Returns an error if the stream emits an EventError or if ctx is cancelled during collection.
func CollectStream(ctx context.Context, eventCh <-chan StreamEvent) (*LLMResponse, error) {
	return CollectStreamWithCallback(ctx, eventCh, nil, nil, nil)
}

// CollectStreamWithCallback is like CollectStream but calls onContent with the
// accumulated text content after each EventContent delta. Optionally calls
// onReasoning with accumulated reasoning content after each EventReasoningContent
// delta (for real-time thinking/reasoning display). Optionally calls onToolCall
// with the current snapshot of tool calls whenever a new tool name arrives in
// the stream — this enables early tool detection (showing "generating tool X"
// before arguments finish streaming, similar to Cursor). EventError handling
// is identical to CollectStream (returns partial content).
func CollectStreamWithCallback(ctx context.Context, eventCh <-chan StreamEvent, onContent func(content string), onReasoning func(content string), onToolCall func(toolCalls []ToolCallDelta)) (*LLMResponse, error) {
	var resp LLMResponse
	var content strings.Builder
	var reasoningContent strings.Builder
	toolCalls := make(map[int]*ToolCallDelta) // index → accumulated delta

	// Idle timeout: if no chunk arrives for this duration, the stream is considered
	// hung and we return an error. This replaces the old approach of using ctx deadline
	// as a total stream timeout, which incorrectly killed actively-streaming responses
	// that simply took longer than the deadline to generate all output.
	const streamIdleTimeout = 120 * time.Second
	idleTimer := time.NewTimer(streamIdleTimeout)
	defer idleTimer.Stop()

	for {
		// Explicit context check BEFORE the select. Go's select is pseudo-random
		// when multiple cases are ready. If ctx is canceled AND the event channel
		// has buffered data, the select may pick eventCh over ctx.Done(), causing
		// the stream to complete normally without detecting the cancellation.
		// This check guarantees ctx.Err() is seen on every iteration.
		if err := ctx.Err(); err != nil {
			resp.Content = content.String()
			resp.ReasoningContent = reasoningContent.String()
			resp.ToolCalls = orderedToolCalls(toolCalls)
			return &resp, err
		}
		select {
		case <-ctx.Done():
			// Caller cancelled or their total deadline exceeded.
			// Return partial content accumulated so far.
			resp.Content = content.String()
			resp.ReasoningContent = reasoningContent.String()
			resp.ToolCalls = orderedToolCalls(toolCalls)
			return &resp, ctx.Err()

		case <-idleTimer.C:
			// No chunk received for streamIdleTimeout — stream is hung.
			resp.Content = content.String()
			resp.ReasoningContent = reasoningContent.String()
			resp.ToolCalls = orderedToolCalls(toolCalls)
			return &resp, fmt.Errorf("stream idle timeout after %v: %w", streamIdleTimeout, context.DeadlineExceeded)

		case ev, ok := <-eventCh:
			if !ok {
				// Channel closed normally — stream completed.
				resp.Content = content.String()
				resp.ReasoningContent = reasoningContent.String()
				resp.ToolCalls = orderedToolCalls(toolCalls)

				// Infer finish_reason from actual response data.
				// Some providers send "stop" instead of "tool_calls" even when tool_calls are present.
				if resp.FinishReason == "" && len(resp.ToolCalls) > 0 {
					resp.FinishReason = FinishReasonToolCalls
				}
				return &resp, nil
			}

			// Reset idle timer — we received a chunk, so the stream is alive.
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(streamIdleTimeout)

			switch ev.Type {
			case EventContent:
				content.WriteString(ev.Content)
				safeCallback(ctx, onContent, content.String())
			case EventReasoningContent:
				reasoningContent.WriteString(ev.ReasoningContent)
				safeCallback(ctx, onReasoning, reasoningContent.String())
			case EventToolCall:
				if ev.ToolCall == nil {
					continue
				}
				idx := ev.ToolCall.Index
				tc := toolCalls[idx]
				if tc == nil {
					tc = &ToolCallDelta{Index: idx}
					toolCalls[idx] = tc
				}
				if ev.ToolCall.ID != "" {
					tc.ID = ev.ToolCall.ID
				}
				if ev.ToolCall.Name != "" {
					tc.Name = ev.ToolCall.Name
				}
				tc.Arguments += ev.ToolCall.Arguments
				// Fire callback on every tool call event (name arrival + argument
				// progress). Originally fired only on name arrival for early tool
				// detection. Now also fires on argument deltas so the UI can show
				// real-time argument generation progress (e.g. "42 chars").
				// The TUI's ~100ms tick rate naturally coalesces high-frequency
				// deltas — no explicit throttle needed here.
				if onToolCall != nil {
					func() {
						defer func() { recover() }()
						select {
						case <-ctx.Done():
							return
						default:
						}
						onToolCall(snapshotToolCalls(toolCalls))
					}()
				}
			case EventUsage:
				if ev.Usage != nil {
					resp.Usage = *ev.Usage
				}
			case EventDone:
				if ev.FinishReason != "" {
					resp.FinishReason = ev.FinishReason
				}
			case EventError:
				if ev.Error != "" {
					// Return partial content accumulated so far instead of nil,
					// so the engine can display what was received before the stream broke.
					resp.Content = content.String()
					resp.ReasoningContent = reasoningContent.String()
					resp.ToolCalls = orderedToolCalls(toolCalls)
					return &resp, fmt.Errorf("stream error: %s", ev.Error)
				}
			}
		}
	}
}
