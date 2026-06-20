package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	log "xbot/logger"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

// ---------------------------------------------------------------------------
// Message conversion: ChatMessage[] → ResponseNewParams
// ---------------------------------------------------------------------------

// toResponsesParams converts xbot ChatMessages into a ResponseNewParams
// suitable for the OpenAI Responses API (POST /v1/responses).
//
// Key differences from Chat Completions:
//   - system messages are extracted into the Instructions field (not in Input)
//   - assistant tool_calls become individual ResponseFunctionToolCallParam items
//   - tool/result messages become ResponseInputItemFunctionCallOutputParam items
//   - reasoning_content from previous assistant turns is passed back as
//     ResponseReasoningItemParam items
func toResponsesParams(model string, messages []ChatMessage, maxTokens int) responses.ResponseNewParams {
	var instructions []string
	inputItems := make([]responses.ResponseInputItemUnionParam, 0, len(messages))

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// Collect system messages as instructions (sent separately from Input)
			if msg.Content != "" {
				instructions = append(instructions, msg.Content)
			}

		case "user":
			// Check for embedded images (data: URLs in markdown image syntax)
			parts := parseEmbeddedImages(msg.Content)
			if len(parts) > 1 {
				// Multi-part message with images
				contentParts := make(responses.ResponseInputMessageContentListParam, 0, len(parts))
				for _, p := range parts {
					switch p.Type {
					case "text":
						contentParts = append(contentParts, responses.ResponseInputContentUnionParam{
							OfInputText: &responses.ResponseInputTextParam{Text: p.Text},
						})
					case "image":
						contentParts = append(contentParts, responses.ResponseInputContentUnionParam{
							OfInputImage: &responses.ResponseInputImageParam{ImageURL: param.Opt[string]{Value: p.URL}},
						})
					}
				}
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role:    responses.EasyInputMessageRoleUser,
						Content: responses.EasyInputMessageContentUnionParam{OfInputItemContentList: contentParts},
					},
				})
			} else {
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role:    responses.EasyInputMessageRoleUser,
						Content: responses.EasyInputMessageContentUnionParam{OfString: param.Opt[string]{Value: msg.Content}},
					},
				})
			}

		case "assistant":
			// If there's reasoning_content, add a reasoning item first
			if msg.ReasoningContent != "" {
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfReasoning: &responses.ResponseReasoningItemParam{
						ID: fmt.Sprintf("rs_%d", len(inputItems)),
						Summary: []responses.ResponseReasoningItemSummaryParam{
							{Text: msg.ReasoningContent},
						},
					},
				})
			}

			// If there are tool calls, add each as a function_call item
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					args := tc.Arguments
					if args == "" {
						args = "{}"
					}
					inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
						OfFunctionCall: &responses.ResponseFunctionToolCallParam{
							Arguments: args,
							CallID:    tc.ID,
							Name:      tc.Name,
						},
					})
				}
			}

			// If there's text content (and not just tool calls), add an assistant message
			if msg.Content != "" || (len(msg.ToolCalls) == 0 && msg.ReasoningContent == "") {
				contentText := msg.Content
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role:    responses.EasyInputMessageRoleAssistant,
						Content: responses.EasyInputMessageContentUnionParam{OfString: param.Opt[string]{Value: contentText}},
					},
				})
			}

		case "tool":
			// Tool result → function_call_output item
			output := msg.Content
			if output == "" {
				output = "{}"
			}
			inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: msg.ToolCallID,
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: param.Opt[string]{Value: output},
					},
				},
			})
		}
	}

	p := responses.ResponseNewParams{
		Model: openai.ResponsesModel(model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: responses.ResponseInputParam(inputItems),
		},
		MaxOutputTokens: param.Opt[int64]{Value: int64(maxTokens)},
		// xbot is stateless — it sends full message history each turn and
		// never uses previous_response_id. Setting store=false avoids
		// unnecessary server-side storage of conversation state.
		Store: param.NewOpt(false),
	}

	if len(instructions) > 0 {
		p.Instructions = param.Opt[string]{Value: strings.Join(instructions, "\n\n")}
	}

	return p
}

// toResponsesTools converts xbot ToolDefinitions into Responses API tool params.
func toResponsesTools(tools []ToolDefinition) []responses.ToolUnionParam {
	result := make([]responses.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		properties := make(map[string]any)
		required := make([]string, 0)
		for _, p := range tool.Parameters() {
			prop := map[string]any{
				"type":        p.Type,
				"description": p.Description,
			}
			if p.Items != nil {
				prop["items"] = p.Items
			}
			properties[p.Name] = prop
			if p.Required {
				required = append(required, p.Name)
			}
		}
		// Sort required array for deterministic JSON serialization.
		// MCP tool parameters come from map iteration (non-deterministic order),
		// which produces different "required" arrays across requests,
		// breaking API-side prefix caching.
		slices.Sort(required)
		result = append(result, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        tool.Name(),
				Description: param.Opt[string]{Value: tool.Description()},
				Parameters: map[string]any{
					"type":       "object",
					"properties": properties,
					"required":   required,
				},
				// Strict defaults to true in the Responses API; set false to
				// match Chat Completions behavior (no strict schema enforcement).
				Strict: param.NewOpt(false),
			},
		})
	}
	return result
}

// buildResponsesReasoning maps xbot thinkingMode to Responses API ReasoningParam.
//
// thinkingMode values:
//   - "" (default): no reasoning param (let API decide)
//   - "enabled": medium effort with auto summary
//   - "disabled": none effort (explicitly disable reasoning)
//   - custom JSON: parsed and mapped to ReasoningParam fields
func buildResponsesReasoning(thinkingMode string) openai.ReasoningParam {
	if thinkingMode == "" {
		return openai.ReasoningParam{}
	}

	switch thinkingMode {
	case "enabled":
		return openai.ReasoningParam{
			Effort:  openai.ReasoningEffortMedium,
			Summary: openai.ReasoningSummaryAuto,
		}
	case "disabled":
		return openai.ReasoningParam{
			Effort: openai.ReasoningEffortNone,
		}
	default:
		// Try parsing as JSON for custom reasoning config
		if len(thinkingMode) > 0 && thinkingMode[0] == '{' {
			var custom map[string]any
			if err := json.Unmarshal([]byte(thinkingMode), &custom); err == nil {
				rp := openai.ReasoningParam{}
				if effort, ok := custom["effort"]; ok {
					if effortStr, ok := effort.(string); ok {
						rp.Effort = openai.ReasoningEffort(effortStr)
					}
				}
				if summary, ok := custom["summary"]; ok {
					if summaryStr, ok := summary.(string); ok {
						rp.Summary = openai.ReasoningSummary(summaryStr)
					}
				}
				// Check for nested "reasoning" key (e.g. {"reasoning": {"effort": "high"}})
				if reasoningObj, ok := custom["reasoning"]; ok {
					if reasoningMap, ok := reasoningObj.(map[string]any); ok {
						if effort, ok := reasoningMap["effort"]; ok {
							if effortStr, ok := effort.(string); ok {
								rp.Effort = openai.ReasoningEffort(effortStr)
							}
						}
						if summary, ok := reasoningMap["summary"]; ok {
							if summaryStr, ok := summary.(string); ok {
								rp.Summary = openai.ReasoningSummary(summaryStr)
							}
						}
					}
				}
				return rp
			}
			log.WithField("thinking_mode", thinkingMode).Warn("[LLM] Failed to parse thinking mode as JSON for Responses API, ignoring")
		}
		return openai.ReasoningParam{}
	}
}

// ---------------------------------------------------------------------------
// Non-streaming: Generate via Responses API
// ---------------------------------------------------------------------------

func (o *OpenAILLM) generateResponses(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	if model == "" {
		model = o.GetDefaultModel()
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"provider":      "openai-responses",
		"model":         model,
		"stream":        false,
		"msg_count":     len(messages),
		"tools_count":   len(tools),
		"thinking_mode": thinkingMode,
		"max_tokens":    o.maxTokens,
	}).Info("[LLM] Starting non-stream request (Responses API)")

	startTime := time.Now()

	// Clamp max tokens to model's limit
	effectiveMaxTokens := o.maxTokens
	if maxOut := modelMaxOutputTokens(model); maxOut > 0 && effectiveMaxTokens > maxOut {
		effectiveMaxTokens = maxOut
	}

	params := toResponsesParams(model, messages, effectiveMaxTokens)

	// Build reasoning config
	reasoning := buildResponsesReasoning(thinkingMode)
	if reasoning.Effort != "" || reasoning.Summary != "" {
		params.Reasoning = reasoning
		log.Ctx(ctx).Debugf("[LLM] Responses API reasoning config: effort=%s, summary=%s", reasoning.Effort, reasoning.Summary)
	}

	// Build tools
	if len(tools) > 0 {
		params.Tools = toResponsesTools(tools)
	}

	// Note: buildThinkingOptions is NOT called here — it injects Chat
	// Completions-specific "thinking" params that are invalid for the
	// Responses API. Reasoning is handled above via params.Reasoning.

	resp, err := o.client.Responses.New(ctx, params)
	if err != nil {
		log.Ctx(ctx).WithFields(log.Fields{
			"provider": "openai-responses",
			"duration": time.Since(startTime).String(),
			"error":    err.Error(),
		}).Error("[LLM] Responses API request failed")
		return nil, fmt.Errorf("openai responses: %w", err)
	}

	// Parse response
	result := &LLMResponse{}

	// Parse token usage
	result.Usage = TokenUsage{
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.TotalTokens,
	}
	if resp.Usage.InputTokensDetails.CachedTokens > 0 {
		result.Usage.CacheHitTokens = resp.Usage.InputTokensDetails.CachedTokens
	}

	// Iterate over output items
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			// Extract text content
			for _, content := range item.Content {
				if content.Type == "output_text" {
					result.Content += content.Text
				}
			}

		case "function_call":
			// Extract tool call
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})

		case "reasoning":
			// Extract reasoning summary content.
			// Note: item.Content (full reasoning text) requires the "reasoning.encrypted_content"
			// include parameter and is not available by default. We only use Summary,
			// which is always returned when reasoning is enabled.
			for _, summary := range item.Summary {
				result.ReasoningContent += summary.Text
			}
		}
	}

	// Map response status to finish reason
	result.FinishReason = responsesStatusToFinishReason(resp.Status, len(result.ToolCalls) > 0)

	fields := log.Fields{
		"provider":          "openai-responses",
		"duration":          time.Since(startTime).String(),
		"output_count":      len(resp.Output),
		"content_len":       len(result.Content),
		"reasoning_len":     len(result.ReasoningContent),
		"tool_calls":        len(result.ToolCalls),
		"finish_reason":     result.FinishReason,
		"prompt_tokens":     result.Usage.PromptTokens,
		"completion_tokens": result.Usage.CompletionTokens,
		"total_tokens":      result.Usage.TotalTokens,
	}
	if isNearEmptyResponse(result) {
		addNearEmptyResponseDebugFields(fields, messages, model, tools, thinkingMode)
		log.Ctx(ctx).WithFields(fields).Warn("[LLM] Responses API request completed with near-empty response")
	} else {
		log.Ctx(ctx).WithFields(fields).Debug("[LLM] Responses API request completed")
	}

	return result, nil
}

// responsesStatusToFinishReason maps Responses API status to xbot FinishReason.
func responsesStatusToFinishReason(status responses.ResponseStatus, hasToolCalls bool) FinishReason {
	switch status {
	case "completed":
		if hasToolCalls {
			return FinishReasonToolCalls
		}
		return FinishReasonStop
	case "incomplete":
		return FinishReasonLength
	case "failed", "cancelled":
		return FinishReasonStop
	case "in_progress", "queued":
		return FinishReasonStop
	default:
		if hasToolCalls {
			return FinishReasonToolCalls
		}
		return FinishReasonStop
	}
}

// ---------------------------------------------------------------------------
// Streaming: GenerateStream via Responses API
// ---------------------------------------------------------------------------

func (o *OpenAILLM) generateStreamResponses(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	if model == "" {
		model = o.GetDefaultModel()
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"provider":      "openai-responses",
		"model":         model,
		"stream":        true,
		"msg_count":     len(messages),
		"tools_count":   len(tools),
		"thinking_mode": thinkingMode,
		"max_tokens":    o.maxTokens,
	}).Info("[LLM] Starting stream request (Responses API)")

	startTime := time.Now()

	// Clamp max tokens
	effectiveMaxTokens := o.maxTokens
	if maxOut := modelMaxOutputTokens(model); maxOut > 0 && effectiveMaxTokens > maxOut {
		effectiveMaxTokens = maxOut
	}

	params := toResponsesParams(model, messages, effectiveMaxTokens)

	// Build reasoning config
	reasoning := buildResponsesReasoning(thinkingMode)
	if reasoning.Effort != "" || reasoning.Summary != "" {
		params.Reasoning = reasoning
	}

	// Build tools
	if len(tools) > 0 {
		params.Tools = toResponsesTools(tools)
	}

	// Note: buildThinkingOptions is NOT called here — it injects Chat
	// Completions-specific "thinking" params that are invalid for the
	// Responses API. Reasoning is handled above via params.Reasoning.

	stream := o.client.Responses.NewStreaming(ctx, params)
	if err := stream.Err(); err != nil {
		log.Ctx(ctx).WithFields(log.Fields{
			"provider": "openai-responses",
			"model":    model,
			"base_url": o.baseURL,
			"error":    err.Error(),
		}).Error("[LLM] Responses stream init error")
		return nil, fmt.Errorf("openai responses stream: %w", err)
	}

	// Create event channel
	eventChan := make(chan StreamEvent, 100)

	// Start goroutine to process streaming response
	go o.processResponsesStream(ctx, stream, eventChan, startTime, messages, model, tools, thinkingMode)

	return eventChan, nil
}

// processResponsesStream processes the Responses API streaming events and
// converts them to xbot StreamEvents.
func (o *OpenAILLM) processResponsesStream(ctx context.Context, stream *ssestream.Stream[responses.ResponseStreamEventUnion], eventChan chan<- StreamEvent, startTime time.Time, messages []ChatMessage, model string, tools []ToolDefinition, thinkingMode string) {
	defer close(eventChan)
	defer stream.Close()

	// Connect context cancellation to stream.Close().
	// Use a done channel to allow the goroutine to exit when the stream
	// processing completes normally (prevents goroutine leak).
	streamDone := make(chan struct{})
	ctxDone := ctx.Done()
	if ctxDone != nil {
		go func() {
			select {
			case <-ctxDone:
				stream.Close()
			case <-streamDone:
			}
		}()
	}
	defer close(streamDone)

	l := log.Ctx(ctx)
	eventCount := 0
	var firstEventTime time.Time
	var lastUsage *TokenUsage
	var lastFinishReason FinishReason
	hasToolCalls := false

	// Track tool calls by item_id for assembling delta arguments
	type toolCallState struct {
		ID        string
		Name      string
		Arguments string
		index     int
	}
	toolCallsByID := make(map[string]*toolCallState)
	toolCallList := make([]*toolCallState, 0)

	for stream.Next() {
		select {
		case <-ctx.Done():
			l.WithFields(log.Fields{
				"provider": "openai-responses",
				"reason":   ctx.Err().Error(),
			}).Warn("[LLM] Stream cancelled")
			eventChan <- StreamEvent{
				Type:  EventError,
				Error: ctx.Err().Error(),
			}
			return
		default:
		}

		event := stream.Current()
		eventCount++

		if eventCount == 1 {
			firstEventTime = time.Now()
			l.WithFields(log.Fields{
				"provider": "openai-responses",
				"ttft":     firstEventTime.Sub(startTime).String(),
			}).Debug("[LLM] First event received")
		}

		switch event.Type {
		case "response.output_text.delta":
			// Text content delta
			if event.Delta != "" {
				eventChan <- StreamEvent{
					Type:    EventContent,
					Content: event.Delta,
				}
			}

		case "response.reasoning_text.delta":
			// Full reasoning text delta
			if event.Delta != "" {
				eventChan <- StreamEvent{
					Type:             EventReasoningContent,
					ReasoningContent: event.Delta,
				}
			}

		case "response.reasoning_summary_text.delta":
			// Reasoning summary delta
			if event.Delta != "" {
				eventChan <- StreamEvent{
					Type:             EventReasoningContent,
					ReasoningContent: event.Delta,
				}
			}

		case "response.output_item.added":
			// New output item — track function calls
			if event.Item.Type == "function_call" {
				callID := event.Item.CallID
				if callID == "" {
					callID = event.Item.ID
				}
				tc := &toolCallState{
					ID:    callID,
					Name:  event.Item.Name,
					index: len(toolCallList),
				}
				// Use event.Item.ID as the map key — this is the "item_id"
				// that subsequent function_call_arguments.delta/done events carry.
				// event.ItemID is empty for output_item.added events (no top-level
				// "item_id" in the JSON; the ID lives inside event.Item.ID).
				toolCallsByID[event.Item.ID] = tc
				toolCallList = append(toolCallList, tc)
				hasToolCalls = true

				// Send initial tool call event (ID + Name)
				eventChan <- StreamEvent{
					Type: EventToolCall,
					ToolCall: &ToolCallDelta{
						Index: tc.index,
						ID:    tc.ID,
						Name:  tc.Name,
					},
				}
			}

		case "response.function_call_arguments.delta":
			// Tool call arguments delta
			tc, ok := toolCallsByID[event.ItemID]
			if ok {
				tc.Arguments += event.Delta
				eventChan <- StreamEvent{
					Type: EventToolCall,
					ToolCall: &ToolCallDelta{
						Index:     tc.index,
						Arguments: event.Delta,
					},
				}
			}

		case "response.function_call_arguments.done":
			// Tool call arguments complete — update with final values
			tc, ok := toolCallsByID[event.ItemID]
			if ok {
				if event.Arguments != "" {
					tc.Arguments = event.Arguments
				}
				if event.Name != "" {
					tc.Name = event.Name
				}
			}

		case "response.output_item.done":
			// Output item complete — use as fallback for tool calls that
			// may have missed delta events (defensive).
			if event.Item.Type == "function_call" {
				tc, ok := toolCallsByID[event.Item.ID]
				if ok {
					// Only update if arguments were missing (delta events failed)
					if tc.Arguments == "" && event.Item.Arguments != "" {
						tc.Arguments = event.Item.Arguments
						// Send the complete arguments as a final delta
						eventChan <- StreamEvent{
							Type: EventToolCall,
							ToolCall: &ToolCallDelta{
								Index:     tc.index,
								Arguments: tc.Arguments,
							},
						}
					}
				}
			}

		case "response.completed":
			// Response completed — extract usage and finish reason
			completed := event.Response
			lastUsage = &TokenUsage{
				PromptTokens:     completed.Usage.InputTokens,
				CompletionTokens: completed.Usage.OutputTokens,
				TotalTokens:      completed.Usage.TotalTokens,
			}
			if completed.Usage.InputTokensDetails.CachedTokens > 0 {
				lastUsage.CacheHitTokens = completed.Usage.InputTokensDetails.CachedTokens
			}
			lastFinishReason = responsesStatusToFinishReason(completed.Status, hasToolCalls)

		case "response.failed":
			// Response failed
			errMsg := "response failed"
			if completed := event.Response; completed.Error.Message != "" {
				errMsg = completed.Error.Message
			}
			l.WithFields(log.Fields{
				"provider": "openai-responses",
				"error":    errMsg,
			}).Error("[LLM] Response failed")
			eventChan <- StreamEvent{
				Type:  EventError,
				Error: errMsg,
			}
			return

		case "response.incomplete":
			lastFinishReason = FinishReasonLength

		case "error":
			errMsg := event.Message
			if errMsg == "" {
				errMsg = "unknown error"
			}
			l.WithFields(log.Fields{
				"provider": "openai-responses",
				"error":    errMsg,
				"code":     event.Code,
				"param":    event.Param,
			}).Error("[LLM] Stream error event")
			eventChan <- StreamEvent{
				Type:  EventError,
				Error: errMsg,
			}
			return
		}
	}

	// Check for stream errors
	if err := stream.Err(); err != nil {
		l.WithFields(log.Fields{
			"provider":    "openai-responses",
			"model":       model,
			"base_url":    o.baseURL,
			"event_count": eventCount,
			"duration":    time.Since(startTime).String(),
			"error":       err.Error(),
		}).Error("[LLM] Stream error")
		eventChan <- StreamEvent{
			Type:  EventError,
			Error: err.Error(),
		}
		return
	}

	// Send usage event before done
	if lastUsage != nil {
		eventChan <- StreamEvent{
			Type:  EventUsage,
			Usage: lastUsage,
		}
	}

	// Infer finish_reason if not set
	if lastFinishReason == "" && hasToolCalls {
		lastFinishReason = FinishReasonToolCalls
	}

	// Send done event
	eventChan <- StreamEvent{
		Type:         EventDone,
		FinishReason: lastFinishReason,
	}

	fields := log.Fields{
		"provider":       "openai-responses",
		"event_count":    eventCount,
		"total_duration": time.Since(startTime).String(),
		"ttft":           firstEventTime.Sub(startTime).String(),
		"finish_reason":  lastFinishReason,
	}
	if lastUsage != nil {
		fields["prompt_tokens"] = lastUsage.PromptTokens
		fields["completion_tokens"] = lastUsage.CompletionTokens
		fields["total_tokens"] = lastUsage.TotalTokens
	}
	if eventCount <= 1 {
		addNearEmptyResponseDebugFields(fields, messages, model, tools, thinkingMode)
		l.WithFields(fields).Warn("[LLM] Stream completed with near-empty response")
	} else {
		l.WithFields(fields).Debug("[LLM] Stream completed")
	}
}
