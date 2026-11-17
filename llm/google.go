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

	log "github.com/sirupsen/logrus"
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
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
}

type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFuncCall struct {
	ID   string          `json:"id,omitempty"`
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
	MaxOutputTokens int                `json:"maxOutputTokens,omitempty"`
	Temperature     *float64           `json:"temperature,omitempty"`
	ThinkingConfig  *geminiThinkConfig `json:"thinkingConfig,omitempty"`
}

// geminiThinkConfig 覆盖两代模型的思考参数：
//   - gemini-2.x：thinkingBudget（0 表示关闭）
//   - gemini-3/4：thinkingLevel（LOW/MEDIUM/HIGH，MINIMAL 不支持；该模型不接受
//     thinkingBudget，传了会 400 "Thinking budget is not supported for this model"）
type geminiThinkConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
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
	// 隐式/显式缓存命中的输入 tokens（实测 gemini-4-argon 第 3 次相同前缀返回
	// cachedContentTokenCount=1346 / promptTokenCount=3400）。不解析这一项，
	// 上层 CacheHitTokens 恒为 0，缓存命中率就永远是 0%。
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	// 思考 tokens。注意 candidatesTokenCount **不含** thinking，上游
	// totalTokenCount = prompt + candidates + thoughts。
	ThoughtsTokenCount int64 `json:"thoughtsTokenCount"`
}

// toTokenUsage 把 Gemini usageMetadata 映射到业务 TokenUsage。
// 口径与 OpenAI 对齐：completion 含 reasoning tokens（OpenAI 的
// completion_tokens 同样含 reasoning_tokens），prompt 含 cached（OpenAI 的
// prompt_tokens 同样含 cached_tokens），因此 cacheHit/prompt 就是命中率。
func (u *geminiUsage) toTokenUsage() TokenUsage {
	completion := u.CandidatesTokenCount + u.ThoughtsTokenCount
	total := u.TotalTokenCount
	if total == 0 {
		total = u.PromptTokenCount + completion
	}
	return TokenUsage{
		PromptTokens:     u.PromptTokenCount,
		CompletionTokens: completion,
		TotalTokens:      total,
		CacheHitTokens:   u.CachedContentTokenCount,
	}
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
	// Gemini 在流式中途出错时会在 SSE 里推 {"error":{...}}（而不是 HTTP 错误码）。
	// 不解析它的话，这种帧会被当成"没有 candidates 的普通帧"忽略掉，
	// 最终表现为"会话自己终止、前端没有任何报错"。
	Error *geminiAPIError `json:"error,omitempty"`
}

type geminiAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func (e *geminiAPIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("gemini API error %d %s: %s", e.Code, e.Status, e.Message)
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
func (g *GoogleLLM) toGeminiRequest(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) *geminiRequest {
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
			fr := geminiPart{FunctionResponse: &geminiFuncResp{
				Name:     name,
				Response: json.RawMessage(fmt.Sprintf(`{"result":%s}`, jsonString(m.Content))),
			}}
			// Gemini 硬性要求：一个 model turn 里的 N 个 functionCall，必须由
			// **紧接着的同一个 user turn** 里的 N 个 functionResponse 一一对应。
			// 并行工具调用（xbot 很常见）会产生 N 条连续的 tool 消息，如果每条
			// 都单独开一个 user turn，就会 400：
			//   "Please ensure that the number of function response parts is equal
			//    to the number of function call parts of the function call turn."
			// 所以这里把连续的 tool 响应合并进同一个 user turn。
			if n := len(req.Contents); n > 0 && isToolResponseTurn(req.Contents[n-1]) {
				req.Contents[n-1].Parts = append(req.Contents[n-1].Parts, fr)
			} else {
				req.Contents = append(req.Contents, geminiContent{
					Role:  "user",
					Parts: []geminiPart{fr},
				})
			}
		default: // user
			// 复用与 OpenAI/Anthropic 同一套多模态拆解：parseMultimodalContent 负责
			// 识别 markdown/legacy 图片引用、按预算裁剪、并在「视觉关闭 / 解析失败 /
			// 超预算」时降级成文本占位（永不因为图片让请求 400）。
			// Gemini 原生要求 inline_data{mimeType,data(base64)}，所以这里用
			// splitDataURL 把 data: URL 精确切成 mime + payload。
			parts := make([]geminiPart, 0, 2)
			for _, item := range parseMultimodalContent(ctx, m.Content, g.cfg.Multimodal) {
				switch item.Type {
				case "image":
					mimeType, b64, ok := splitDataURL(item.URL)
					if !ok {
						// parseMultimodalContent 已保证非 data: 的引用被降级；
						// 走到这里说明 data: URL 本身畸形 —— 明确降级而不是塞坏数据。
						parts = append(parts, geminiPart{Text: "[图片: 无法解析的 data URL]"})
						continue
					}
					parts = append(parts, geminiPart{InlineData: &geminiInlineData{MIMEType: mimeType, Data: b64}})
				default:
					if t := item.Text; t != "" {
						parts = append(parts, geminiPart{Text: t})
					}
				}
			}
			if len(parts) == 0 {
				continue
			}
			req.Contents = append(req.Contents, geminiContent{Role: "user", Parts: parts})
		}
	}

	if len(systems) > 0 {
		req.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: strings.Join(systems, "\n\n")}}}
	}
	// 真实会话里 tool 结果可能缺失（取消、工具跳过）或多出（跨 provider 切换
	// 带进来的历史）。Gemini 对此零容忍并直接 400，所以在发请求前把结构修齐。
	req.Contents = normalizeFunctionCallPairs(req.Contents)

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
	if tc := geminiThinkingFor(model, thinkingMode); tc != nil {
		gc.ThinkingConfig = tc
	}
	req.GenerationConfig = gc
	return req
}

// geminiThinkingFor 把 xbot 的 thinkingMode 映射成 Gemini 原生思考参数。
//
// 关键实测事实（mint 网关 / gemini-4-argon，2026-11-17）：
//
//   - 不传 thinkingConfig → 上游默认**不思考**（usageMetadata 无 thoughtsTokenCount）
//
//   - thinkingBudget 任何值（含 0）→ HTTP 400 "Thinking budget is not supported
//     for this model. Use thinking level instead."
//
//   - thinkingLevel=HIGH → thoughtsTokenCount≈784~886；LOW/MEDIUM 合法，
//     MINIMAL 不支持（400 "Thinking level is unsupported: THINKING_LEVEL_MINIMAL"）
//
//   - includeThoughts 必须显式置 true 才会返回思考正文（thought=true 的 part）；
//     缺了它上游只回 thoughtSignature，UI 里就"看不到在想什么"。
//     实测 HIGH+includeThoughts：非流式 1 个 thought part（thoughtsTokenCount=1145），
//     流式逐段推 2 个 thought part。
//
// 因此："默认/auto/enabled" 必须显式发 thinkingLevel 才会思考；"disabled"
// 只能靠"不发"来等价关闭（该模型没有真正的 0 档）。
func geminiThinkingFor(model, thinkingMode string) *geminiThinkConfig {
	mode := strings.ToLower(strings.TrimSpace(thinkingMode))
	legacy := strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini-2")
	switch mode {
	case "disabled", "none", "off":
		if legacy {
			zero := 0
			return &geminiThinkConfig{ThinkingBudget: &zero}
		}
		return nil // 3/4 代无 0 档：不发即为服务端默认（不思考）
	case "low", "minimal":
		if legacy {
			b := 1024
			return &geminiThinkConfig{ThinkingBudget: &b, IncludeThoughts: true}
		}
		return &geminiThinkConfig{ThinkingLevel: "LOW", IncludeThoughts: true}
	case "medium", "mid":
		if legacy {
			b := 4096
			return &geminiThinkConfig{ThinkingBudget: &b, IncludeThoughts: true}
		}
		return &geminiThinkConfig{ThinkingLevel: "MEDIUM", IncludeThoughts: true}
	default: // ""、"auto"、"enabled"、"think"、"high"、未识别的 JSON → 最高档
		if legacy {
			b := 8192
			return &geminiThinkConfig{ThinkingBudget: &b, IncludeThoughts: true}
		}
		return &geminiThinkConfig{ThinkingLevel: "HIGH", IncludeThoughts: true}
	}
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
	log.WithFields(log.Fields{
		"provider": "google", "model": model, "stream": false,
		"msg_count": len(messages), "tools_count": len(tools),
		"thinking_mode": thinkingMode,
	}).Info("[LLM] Starting gemini request")
	reqBody := g.toGeminiRequest(ctx, model, messages, tools, thinkingMode)
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
	out, convErr := convertGeminiResponse(&gr)
	if convErr != nil {
		return nil, convErr
	}
	return out, nil
}

func (g *GoogleLLM) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if g.cfg.APIKey != "" {
		req.Header.Set("x-goog-api-key", g.cfg.APIKey)
	}
}

// convertGeminiResponse 把 Gemini candidates 转成业务响应（文本 / 工具调用 / 用量）。
func convertGeminiResponse(gr *geminiResponse) (*LLMResponse, error) {
	if gr.Error != nil {
		return nil, gr.Error
	}
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
				id := part.FunctionCall.ID
				if id == "" {
					id = fmt.Sprintf("call_%d", idx)
				}
				out.ToolCalls = append(out.ToolCalls, ToolCall{
					ID:        id,
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
		out.Usage = gr.UsageMetadata.toTokenUsage()
	}
	if gr.PromptFeedback != nil && gr.PromptFeedback.BlockReason != "" && out.Content == "" && len(out.ToolCalls) == 0 {
		out.FinishReason = FinishReasonContentFilter
	}
	return out, nil
}

// geminiCallID 优先用上游给的工具调用 id（后续轮次回传时保持一致），
// 缺失时才退化成 call_N。
func geminiCallID(fc *geminiFuncCall, idx int) string {
	if fc != nil && fc.ID != "" {
		return fc.ID
	}
	return fmt.Sprintf("call_%d", idx)
}

// isToolResponseTurn 判断一个 content 是否是"纯工具响应" turn。
func isToolResponseTurn(c geminiContent) bool {
	if c.Role != "user" || len(c.Parts) == 0 {
		return false
	}
	for _, p := range c.Parts {
		if p.FunctionResponse == nil {
			return false
		}
	}
	return true
}

// normalizeFunctionCallPairs 修齐 Gemini 的 functionCall/functionResponse 配对：
//
//   - 含 N 个 functionCall 的 model turn，其后必须紧跟一个含 N 个
//     functionResponse 的 user turn（不足补占位、超出截断、缺失补一个占位 turn）
//   - 没有对应 functionCall 的孤儿 tool turn（跨 provider 历史、取消的调用）
//     里的响应会被转成普通文本，避免 Gemini 以结构错误拒绝整个请求
func normalizeFunctionCallPairs(contents []geminiContent) []geminiContent {
	out := make([]geminiContent, 0, len(contents)+1)
	for i := 0; i < len(contents); i++ {
		c := contents[i]
		calls := 0
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				calls++
			}
		}
		// 孤儿 tool turn：前面没有 functionCall，把结果降级成文本
		if isToolResponseTurn(c) && calls == 0 {
			texts := make([]geminiPart, 0, len(c.Parts))
			for _, p := range c.Parts {
				if p.FunctionResponse == nil {
					continue
				}
				texts = append(texts, geminiPart{Text: fmt.Sprintf("[工具结果 %s] %s",
					p.FunctionResponse.Name, string(p.FunctionResponse.Response))})
			}
			if len(texts) == 0 {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Role == "user" && !isToolResponseTurn(out[n-1]) {
				out[n-1].Parts = append(out[n-1].Parts, texts...)
			} else {
				out = append(out, geminiContent{Role: "user", Parts: texts})
			}
			continue
		}
		out = append(out, c)
		if calls == 0 {
			continue
		}
		// 收集紧随的 tool 响应 turn（正常情况下已由 tool 分支合并成一个）
		var resp *geminiContent
		if i+1 < len(contents) && isToolResponseTurn(contents[i+1]) {
			nxt := contents[i+1]
			resp = &nxt
			i++
		}
		if resp == nil {
			resp = &geminiContent{Role: "user"}
		}
		have := 0
		for _, p := range resp.Parts {
			if p.FunctionResponse != nil {
				have++
			}
		}
		switch {
		case have < calls:
			// 缺失的响应用占位补足，宁可让模型看到"没有结果"也不要整个请求 400
			for have < calls {
				resp.Parts = append(resp.Parts, geminiPart{FunctionResponse: &geminiFuncResp{
					Name:     "unknown",
					Response: json.RawMessage(`{"result":"no result (tool call was not answered)"}`),
				}})
				have++
			}
		case have > calls:
			kept := make([]geminiPart, 0, len(resp.Parts))
			excess := have - calls
			for _, p := range resp.Parts {
				if p.FunctionResponse != nil && excess > 0 {
					excess--
					continue
				}
				kept = append(kept, p)
			}
			resp.Parts = kept
		}
		out = append(out, *resp)
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
	reqBody := g.toGeminiRequest(ctx, model, messages, tools, thinkingMode)
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	log.WithFields(log.Fields{
		"provider": "google", "model": model, "stream": true,
		"msg_count": len(messages), "tools_count": len(tools),
		"thinking_mode": thinkingMode,
	}).Info("[LLM] Starting gemini stream request")
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
	go g.pumpStream(ctx, model, resp.Body, ch)
	return ch, nil
}

func (g *GoogleLLM) pumpStream(ctx context.Context, model string, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	toolIdx := 0
	sentDone := false
	produced := false // 是否真的产出过内容/思考/工具调用
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
		if gr.Error != nil {
			ch <- StreamEvent{Type: EventError, Error: gr.Error.Error()}
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
					produced = true
					ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCallDelta{
						Index: toolIdx, ID: geminiCallID(part.FunctionCall, toolIdx),
						Name: part.FunctionCall.Name, Arguments: args, ArgumentsComplete: true,
					}}
					toolIdx++
				case part.Thought:
					if part.Text != "" {
						produced = true
						ch <- StreamEvent{Type: EventReasoningContent, ReasoningContent: part.Text}
					}
				case part.Text != "":
					produced = true
					ch <- StreamEvent{Type: EventContent, Content: part.Text}
				}
			}
			if cand.FinishReason != "" {
				fin := mapGeminiFinish(cand.FinishReason)
				if toolIdx > 0 {
					fin = FinishReasonToolCalls
				}
				// 只发一次、且只在有真实 token 数时发：SSE 首个 chunk 的
				// usageMetadata 只有 trafficType（全零），零值会把上层计数清零。
				if um := gr.UsageMetadata; um != nil && um.PromptTokenCount > 0 {
					u := um.toTokenUsage()
					ch <- StreamEvent{Type: EventUsage, Usage: &u}
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
		// 上游没给 finishReason 就结束了。以前这里直接发 FinishReasonStop，
		// 等于把"截断/空流"伪装成正常完成 —— 上层（CollectStream）看到
		// EventDone 就不再报错，前端表现就是"会话自己终止、看不到任何报错"。
		// 现在：产出了内容才降级为 Stop（并记一条 warn），完全空流必须报错，
		// 让 retry 机制接手、最终把错误显示给用户。
		if produced {
			log.WithFields(log.Fields{"model": model, "tool_calls": toolIdx}).
				Warn("[LLM] gemini stream ended without finishReason; treating partial output as complete")
			ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
		} else {
			ch <- StreamEvent{Type: EventError, Error: "gemini stream returned no content and no finishReason (upstream closed the stream; possible quota/safety/network issue)"}
		}
	}
}
