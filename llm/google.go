package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Gemini / Google Generative Language 原生协议客户端。
//
// 为什么需要它（而不是复用 OpenAI 兼容层）：
//   - Google 原生支持 OpenAI 兼容层覆盖不到的能力：cachedContents（显式上下文
//     缓存）、thinkingConfig.thinkingBudget、原生 functionDeclarations、safetySettings、
//     inline_data 多模态 part。
//   - 部分网关（例如我们自己的 mint）只暴露 /v1beta/models/{model}:generateContent，
//     OpenAI 兼容路径不可用或语义不同。
//
// 端点（base 不带 /v1，由调用方保证）：
//   POST {base}/v1beta/models/{model}:generateContent
//   POST {base}/v1beta/models/{model}:streamGenerateContent?alt=sse
// 鉴权：x-goog-api-key（也兼容 Bearer，实测 mint 网关两者都收）。

type GoogleConfig struct {
	BaseURL      string
	APIKey       string
	DefaultModel string
	MaxTokens    int
	Multimodal   *MultimodalConfig
}

type GoogleLLM struct {
	cfg    GoogleConfig
	client *http.Client
}

func NewGoogleLLM(cfg GoogleConfig) *GoogleLLM {
	return &GoogleLLM{
		cfg: cfg,
		// 与其它客户端一致：不设全局超时，靠 ctx 控制（长上下文首 token 可能很慢）
		client: &http.Client{Transport: http.DefaultTransport},
	}
}

func (g *GoogleLLM) ListModels() []string {
	if g.cfg.DefaultModel != "" {
		return []string{g.cfg.DefaultModel}
	}
	return nil
}

func (g *GoogleLLM) GetDefaultModel() string { return g.cfg.DefaultModel }

// ─── 请求/响应结构（只保留用到的字段）────────────────────────────

type geminiPart struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *geminiInlineData `json:"inline_data,omitempty"`
	FunctionCall     *geminiFuncCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFuncResp   `json:"functionResponse,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
}

type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFuncCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFuncResp struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiFuncDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations"`
}

type geminiGenConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	ThinkingConfig  *struct {
		ThinkingBudget int `json:"thinkingBudget"`
	} `json:"thinkingConfig,omitempty"`
}

type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	Tools             []geminiTool     `json:"tools,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiUsage struct {
	PromptTokenCount     int64 `json:"promptTokenCount"`
	CandidatesTokenCount int64 `json:"candidatesTokenCount"`
	TotalTokenCount      int64 `json:"totalTokenCount"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type geminiResponse struct {
	Candidates     []geminiCandidate `json:"candidates"`
	UsageMetadata  *geminiUsage      `json:"usageMetadata"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

// ─── 请求构造 ─────────────────────────────────────────────

func (g *GoogleLLM) endpoint(model, method string) string {
	base := strings.TrimSpace(g.cfg.BaseURL)
	// 调用方可能给出 .../v1beta 或 .../v1，统一剥掉版本段再拼。
	for _, suffix := range []string{"/v1beta", "/v1"} {
		base = strings.TrimSuffix(base, suffix)
	}
	base = strings.TrimSuffix(base, "/")
	return fmt.Sprintf("%s/v1beta/models/%s:%s", base, model, method)
}

// toGeminiRequest 把业务消息转换成 Gemini 原生结构。
//
// 关键语义（与 OpenAI 不同，错了会静默丢内容）：
//   - system 消息 → systemInstruction（不是 contents）
//   - assistant → role="model"
//   - tool 结果 → role="user" + functionResponse（Gemini 要求由 user 回合携带）
func (g *GoogleLLM) toGeminiRequest(model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) *geminiRequest {
	req := &geminiRequest{}
	var systems []string
	toolNames := map[string]string{} // tool_call_id → function name

	for _, m := range messages {
		switch m.Role {
		case "system":
			if c := strings.TrimSpace(m.Content); c != "" {
				systems = append(systems, c)
			}
		case "assistant":
			parts := make([]geminiPart, 0, 1+len(m.ToolCalls))
			if c := m.Content; c != "" {
				parts = append(parts, geminiPart{Text: c})
			}
			for _, tc := range m.ToolCalls {
				args := json.RawMessage(tc.Arguments)
				if len(args) == 0 || !json.Valid(args) {
					args = json.RawMessage(`{}`)
				}
				toolNames[tc.ID] = tc.Name
				parts = append(parts, geminiPart{FunctionCall: &geminiFuncCall{Name: tc.Name, Args: args}})
			}
			if len(parts) > 0 {
				req.Contents = append(req.Contents, geminiContent{Role: "model", Parts: parts})
			}
		case "tool":
			name := m.ToolName
			if name == "" {
				name = toolNames[m.ToolCallID]
			}
			if name == "" {
				name = "tool"
			}
			req.Contents = append(req.Contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{FunctionResponse: &geminiFuncResp{
					Name:     name,
					Response: json.RawMessage(fmt.Sprintf(`{"result":%s}`, jsonString(m.Content))),
				}}},
			})
		default: // user
			parts := make([]geminiPart, 0, 2)
			if c := m.Content; c != "" {
				parts = append(parts, geminiPart{Text: c})
			}
			// 图片暂不注入：Gemini 原生需要 inline_data(base64)，而 MultimodalConfig
			// 只提供 OpenAI 风格 image_url 解析。视觉开关打开时这里会退化为纯文本
			// （不会 400），后续单独补 inline_data 支持。
			if len(parts) == 0 {
				continue
			}
			req.Contents = append(req.Contents, geminiContent{Role: "user", Parts: parts})
		}
	}

	if len(systems) > 0 {
		req.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: strings.Join(systems, "\n\n")}}}
	}
	if len(tools) > 0 {
		decls := make([]geminiFuncDecl, 0, len(tools))
		for _, t := range tools {
			decls = append(decls, geminiFuncDecl{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  toolParamsToSchema(t.Parameters()),
			})
		}
		req.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}

	gc := &geminiGenConfig{}
	if g.cfg.MaxTokens > 0 {
		gc.MaxOutputTokens = g.cfg.MaxTokens
	}
	if thinkingMode == "disabled" {
		gc.ThinkingConfig = &struct {
			ThinkingBudget int `json:"thinkingBudget"`
		}{ThinkingBudget: 0}
	}
	req.GenerationConfig = gc
	return req
}

// toolParamsToSchema 把业务 ToolParam 列表合成 Gemini 要的 JSON Schema object。
// 注意业务 ToolParam 的语义：Type 可能是 string 或 []string（union），
// Required 是**单个参数**是否必填（bool），不是整个 object 的 required 列表。
func toolParamsToSchema(params []ToolParam) map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range params {
		if p.Name == "" {
			continue
		}
		prop := map[string]any{}
		if p.Type != nil {
			prop["type"] = p.Type
		}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if p.Items != nil {
			prop["items"] = p.Items
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ─── 非流式 ─────────────────────────────────────────────

func (g *GoogleLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	if model == "" {
		model = g.cfg.DefaultModel
	}
	reqBody := g.toGeminiRequest(model, messages, tools, thinkingMode)
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint(model, "generateContent"), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	g.setHeaders(req)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini %s: HTTP %d: %s", model, resp.StatusCode, truncateForError(body))
	}
	var gr geminiResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("gemini decode: %w", err)
	}
	return convertGeminiResponse(&gr), nil
}

func (g *GoogleLLM) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if g.cfg.APIKey != "" {
		req.Header.Set("x-goog-api-key", g.cfg.APIKey)
	}
}

// convertGeminiResponse 把 Gemini candidates 转成业务响应（文本 / 工具调用 / 用量）。
func convertGeminiResponse(gr *geminiResponse) *LLMResponse {
	out := &LLMResponse{FinishReason: FinishReasonStop}
	var texts, thoughts []string
	idx := 0
	for _, cand := range gr.Candidates {
		for _, part := range cand.Content.Parts {
			switch {
			case part.FunctionCall != nil:
				args := "{}"
				if len(part.FunctionCall.Args) > 0 {
					args = string(part.FunctionCall.Args)
				}
				out.ToolCalls = append(out.ToolCalls, ToolCall{
					ID:        fmt.Sprintf("call_%d", idx),
					Name:      part.FunctionCall.Name,
					Arguments: args,
				})
				idx++
			case part.Thought:
				if part.Text != "" {
					thoughts = append(thoughts, part.Text)
				}
			case part.Text != "":
				texts = append(texts, part.Text)
			}
		}
		out.FinishReason = mapGeminiFinish(cand.FinishReason)
	}
	out.Content = strings.Join(texts, "")
	out.ReasoningContent = strings.Join(thoughts, "")
	if len(out.ToolCalls) > 0 {
		out.FinishReason = FinishReasonToolCalls
	}
	if gr.UsageMetadata != nil {
		out.Usage = TokenUsage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		}
	}
	if gr.PromptFeedback != nil && gr.PromptFeedback.BlockReason != "" && out.Content == "" && len(out.ToolCalls) == 0 {
		out.FinishReason = FinishReasonContentFilter
	}
	return out
}

func mapGeminiFinish(reason string) FinishReason {
	switch strings.ToUpper(reason) {
	case "", "STOP":
		return FinishReasonStop
	case "MAX_TOKENS":
		return FinishReasonLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return FinishReasonContentFilter
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		return FinishReasonToolCalls
	default:
		return FinishReasonStop
	}
}

func truncateForError(b []byte) string {
	const max = 400
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

// ─── 流式 ─────────────────────────────────────────────

func (g *GoogleLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	if model == "" {
		model = g.cfg.DefaultModel
	}
	reqBody := g.toGeminiRequest(model, messages, tools, thinkingMode)
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	url := g.endpoint(model, "streamGenerateContent") + "?alt=sse"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	g.setHeaders(req)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("gemini stream %s: HTTP %d: %s", model, resp.StatusCode, truncateForError(body))
	}
	ch := make(chan StreamEvent, 64)
	go g.pumpStream(ctx, resp.Body, ch)
	return ch, nil
}

func (g *GoogleLLM) pumpStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	toolIdx := 0
	sentDone := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var gr geminiResponse
		if err := json.Unmarshal([]byte(payload), &gr); err != nil {
			ch <- StreamEvent{Type: EventError, Error: "gemini stream decode: " + err.Error()}
			return
		}
		for _, cand := range gr.Candidates {
			for _, part := range cand.Content.Parts {
				switch {
				case part.FunctionCall != nil:
					args := "{}"
					if len(part.FunctionCall.Args) > 0 {
						args = string(part.FunctionCall.Args)
					}
					ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{
						Index: toolIdx, ID: fmt.Sprintf("call_%d", toolIdx),
						Name: part.FunctionCall.Name, Arguments: args, ArgumentsComplete: true,
					}}
					toolIdx++
				case part.Thought:
					if part.Text != "" {
						ch <- StreamEvent{Type: EventReasoningContent, ReasoningContent: part.Text}
					}
				case part.Text != "":
					ch <- StreamEvent{Type: EventContent, Content: part.Text}
				}
			}
			if cand.FinishReason != "" {
				fin := mapGeminiFinish(cand.FinishReason)
				if toolIdx > 0 {
					fin = FinishReasonToolCalls
				}
				if gr.UsageMetadata != nil {
					ch <- StreamEvent{Type: EventUsage, Usage: &TokenUsage{
						PromptTokens:     gr.UsageMetadata.PromptTokenCount,
						CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
						TotalTokens:      gr.UsageMetadata.TotalTokenCount,
					}}
				}
				ch <- StreamEvent{Type: EventDone, FinishReason: fin}
				sentDone = true
			}
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		ch <- StreamEvent{Type: EventError, Error: "gemini stream read: " + err.Error()}
		return
	}
	if !sentDone {
		ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
	}
}
