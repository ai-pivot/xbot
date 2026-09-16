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

// sanitizeID ensures a string is safe to embed in an item ID.
// Returns a truncated, alphanumeric-only version; empty input yields "x".
func sanitizeID(s string) string {
	if s == "" {
		return "x"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Reasoning items (Responses API) — 加密思维链的捕获与回传
// ---------------------------------------------------------------------------

// responsesInclude 返回 Responses API 请求的 `include` 列表。
//
// `reasoning.encrypted_content` 是 xbot（**无状态**：每轮重放完整历史、
// store=false、不用 previous_response_id）的**必需**参数：
//   - 不请求它 → 服务端不返回加密思维链 → 后续轮次无法把 reasoning 原样回传；
//   - OpenAI 对带 function_call 历史的重放做校验，缺 reasoning item 会 400：
//     "Item 'fc_…' of type 'function_call' was provided without its required
//     'reasoning' item: 'rs_…'"
//
// 触发条件：本次请求带了 reasoning 配置，或历史里已经有加密 reasoning 需要回传。
// 非 reasoning 模型上该参数被服务端忽略（openai/codex 也是无条件发送，
// 见 codex-rs/core/src/client.rs: `let include = vec!["reasoning.encrypted_content"]`）。
func responsesInclude(reasoning openai.ReasoningParam, messages []ChatMessage) []responses.ResponseIncludable {
	if reasoning.Effort == "" && reasoning.Summary == "" && !hasEncryptedReasoning(messages) {
		return nil
	}
	return []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
}

// hasEncryptedReasoning 判断历史里是否已有加密 reasoning（有则需要继续请求/回传）。
func hasEncryptedReasoning(messages []ChatMessage) bool {
	for _, m := range messages {
		for _, ri := range m.ReasoningItems {
			if ri.EncryptedContent != "" || ri.ID != "" {
				return true
			}
		}
	}
	return false
}

// reasoningItemFromOutputItem 把服务端返回的 reasoning item 转成要原样回传的
// ReasoningItem（id / encrypted_content / summary_text / reasoning_text）。
func reasoningItemFromOutputItem(item responses.ResponseOutputItemUnion) *ReasoningItem {
	ri := &ReasoningItem{ID: item.ID, EncryptedContent: item.EncryptedContent}
	for _, part := range item.Content {
		if part.Type == "reasoning_text" {
			ri.Content += part.Text
		}
	}
	for _, s := range item.Summary {
		ri.Summary += s.Text
	}
	return ri
}

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
//
// toResponsesParams converts xbot messages into Responses API params.
// mc carries the per-model multimodal (vision) config — see llm/multimodal.go.
// nil mc = zero value (vision off → image references degrade to placeholders).
func toResponsesParams(model string, messages []ChatMessage, maxTokens int, mc *MultimodalConfig) responses.ResponseNewParams {
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
			// Multimodal user messages: image references (markdown / legacy
			// <image> tags) become input_image content parts when vision is
			// enabled; otherwise they degrade to text placeholders.
			var parts []imageContentPart
			if hasMultimodalImages(msg.Content) {
				parts = parseMultimodalContent(context.Background(), msg.Content, mc)
			}
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
				// Single text part (or no images): preserve original content
				// unless an image degraded to a placeholder.
				content := msg.Content
				if len(parts) == 1 && parts[0].Type == "text" && parts[0].Text != msg.Content {
					content = parts[0].Text
				}
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role:    responses.EasyInputMessageRoleUser,
						Content: responses.EasyInputMessageContentUnionParam{OfString: param.Opt[string]{Value: content}},
					},
				})
			}

		case "assistant":
			// Reasoning MUST be passed back to the API for subsequent turns.
			//
			// 首选：把服务端给的 reasoning item **原样**回传（含 id + encrypted_content）。
			// OpenAI 校验 item id 与顺序；store=false 时 encrypted_content 是唯一载体，
			// 缺了它下一轮带着 function_call 历史的请求会被拒：
			//   "Item 'fc_…' of type 'function_call' was provided without its required
			//    'reasoning' item: 'rs_…'"
			// （openai/codex 的做法：始终 include ["reasoning.encrypted_content"]，
			//  并把收到的 reasoning item 原样放回 input。）
			//
			// 兜底：历史里没有原始 item（老数据 / 网关只给文本）时，用明文重建
			// reasoning item。此时必须**同时**填 `summary`(summary_text) 与
			// `content`(reasoning_text)：部分兼容网关（tokendance 等）校验
			// "The reasoning_text in the thinking mode must be passed back to the API."
			if len(msg.ReasoningItems) > 0 {
				for i, ri := range msg.ReasoningItems {
					id := ri.ID
					if id == "" {
						id = fmt.Sprintf("rs_%s_%d", sanitizeID(msg.ToolCallID), i)
					}
					item := &responses.ResponseReasoningItemParam{ID: id}
					if len(ri.Summary) > 0 {
						item.Summary = []responses.ResponseReasoningItemSummaryParam{{Text: ri.Summary}}
					}
					if len(ri.Content) > 0 {
						item.Content = []responses.ResponseReasoningItemContentParam{{Text: ri.Content}}
					}
					if ri.EncryptedContent != "" {
						item.EncryptedContent = param.NewOpt(ri.EncryptedContent)
					}
					inputItems = append(inputItems, responses.ResponseInputItemUnionParam{OfReasoning: item})
				}
			} else if msg.ReasoningContent != "" {
				inputItems = append(inputItems, responses.ResponseInputItemUnionParam{
					OfReasoning: &responses.ResponseReasoningItemParam{
						ID: fmt.Sprintf("rs_%s_%d", sanitizeID(msg.ToolCallID), len(inputItems)),
						Summary: []responses.ResponseReasoningItemSummaryParam{
							{Text: msg.ReasoningContent},
						},
						Content: []responses.ResponseReasoningItemContentParam{
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

			// If there's text content, add an assistant message. An assistant item
			// with EMPTY text is never emitted: OpenAI/Anthropic reject
			// "content or tool_calls must be set" (SanitizeMessages strips such
			// messages upstream — this is the secondary guard for the Responses path).
			if msg.Content != "" {
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
		// xbot is stateless — it sends full message history each turn and
		// never uses previous_response_id. Setting store=false avoids
		// unnecessary server-side storage of conversation state.
		Store: param.NewOpt(false),
	}
	// max_output_tokens must be > 0 when present (0 is rejected by the API);
	// omit it entirely and let the server use the model default.
	if maxTokens > 0 {
		p.MaxOutputTokens = param.Opt[int64]{Value: int64(maxTokens)}
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
			return openai.ReasoningParam{}
		}
		// Non-JSON unknown value
		log.WithField("thinking_mode", thinkingMode).Warn("[LLM] Unknown thinking mode is not valid JSON for Responses API, ignoring")
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

	params := toResponsesParams(model, messages, effectiveMaxTokens, o.mm)

	// Build reasoning config
	reasoning := buildResponsesReasoning(thinkingMode)
	if reasoning.Effort != "" || reasoning.Summary != "" {
		params.Reasoning = reasoning
		log.Ctx(ctx).Debugf("[LLM] Responses API reasoning config: effort=%s, summary=%s", reasoning.Effort, reasoning.Summary)
	}
	// 加密思维链：无状态重放（store=false）必需（见 responsesInclude 注释）。
	params.Include = responsesInclude(reasoning, messages)

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
			// Reasoning text (type `reasoning_text`) is the authoritative full
			// reasoning; `summary` (type `summary_text`) is a condensed form.
			// Prefer content, but only when it actually yields text — a content
			// array with empty/placeholder parts must NOT shadow a usable summary
			// (that would silently drop the reasoning).
			text := ""
			for _, part := range item.Content {
				if part.Type == "reasoning_text" {
					text += part.Text
				}
			}
			if text == "" {
				for _, summary := range item.Summary {
					text += summary.Text
				}
			}
			// 累积（多个 reasoning item 时不能互相覆盖）。
			result.ReasoningContent += text
			// 原样保留该 item（id + encrypted_content + summary/content）——
			// 后续轮次必须把它逐字段放回 input（见 toResponsesParams）。
			result.ReasoningItems = append(result.ReasoningItems, *reasoningItemFromOutputItem(item))
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
		return FinishReasonContentFilter
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

	params := toResponsesParams(model, messages, effectiveMaxTokens, o.mm)

	// Build reasoning config
	reasoning := buildResponsesReasoning(thinkingMode)
	if reasoning.Effort != "" || reasoning.Summary != "" {
		params.Reasoning = reasoning
	}
	// 加密思维链：无状态重放（store=false）必需（见 responsesInclude 注释）。
	params.Include = responsesInclude(reasoning, messages)

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
	// Reasoning dedup: `reasoning_text.delta` carries the FULL reasoning text and
	// `reasoning_summary_text.delta` a condensed summary. Both map to
	// EventReasoningContent, so a provider that emits BOTH would double the
	// reasoning text. Prefer the full text per reasoning item: once an item has
	// delivered text deltas, its summary deltas are ignored.
	reasoningItemHasText := make(map[string]bool)
	// Reasoning items (Responses API)：捕获 id + encrypted_content 以便原样回传
	// （见 responsesInclude / toResponsesParams）。同一个 item 可能在
	// `response.output_item.done` 与 `response.completed.response.output` 各出现一次
	// （后者才带 encrypted_content）——两处都发事件，消费端按 id 合并补全。
	emitReasoningItem := func(item *ReasoningItem) {
		if item == nil || item.ID == "" {
			return
		}
		eventChan <- StreamEvent{Type: EventReasoningItem, ReasoningItem: item}
	}

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
			// Full reasoning text delta — authoritative for this item.
			if event.Delta != "" {
				reasoningItemHasText[event.ItemID] = true
				eventChan <- StreamEvent{
					Type:             EventReasoningContent,
					ReasoningContent: event.Delta,
				}
			}

		case "response.reasoning_summary_text.delta":
			// Reasoning summary delta — skipped when this item already delivered
			// its full reasoning text (prevents duplicated reasoning).
			if event.Delta != "" && !reasoningItemHasText[event.ItemID] {
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
				// Reasoning item 完成：捕获 id/encrypted_content/summary 供回传。
				if event.Item.Type == "reasoning" {
					emitReasoningItem(reasoningItemFromOutputItem(event.Item))
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
			// 最终 output 里的 reasoning item（部分网关只在这里带 encrypted_content）。
			for _, item := range completed.Output {
				if item.Type == "reasoning" {
					emitReasoningItem(reasoningItemFromOutputItem(item))
				}
			}

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
		"finish_reason":  lastFinishReason,
	}
	if eventCount > 0 {
		fields["ttft"] = firstEventTime.Sub(startTime).String()
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
