package llm

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

// ---------------------------------------------------------------------------
// toResponsesParams
// ---------------------------------------------------------------------------

func TestToResponsesParams_SystemMessageBecomesInstructions(t *testing.T) {
	msgs := []ChatMessage{NewSystemMessage("You are a helpful assistant.")}

	p := toResponsesParams("test-model", msgs, 1000)

	// Instructions should hold the system content
	if !p.Instructions.Valid() {
		t.Fatal("expected Instructions to be set for system message")
	}
	if got := p.Instructions.Value; got != "You are a helpful assistant." {
		t.Errorf("Instructions = %q, want %q", got, "You are a helpful assistant.")
	}

	// No input items should be produced from a system message
	if got := len(p.Input.OfInputItemList); got != 0 {
		t.Errorf("expected 0 input items, got %d", got)
	}

	// Model should be carried through
	if p.Model != "test-model" {
		t.Errorf("Model = %q, want %q", p.Model, "test-model")
	}
}

func TestToResponsesParams_MultipleSystemMessagesConcatenated(t *testing.T) {
	msgs := []ChatMessage{
		NewSystemMessage("Rule one."),
		NewSystemMessage("Rule two."),
	}

	p := toResponsesParams("m", msgs, 0)

	if !p.Instructions.Valid() {
		t.Fatal("expected Instructions to be set")
	}
	want := "Rule one.\n\nRule two."
	if got := p.Instructions.Value; got != want {
		t.Errorf("Instructions = %q, want %q", got, want)
	}
}

func TestToResponsesParams_UserPlainText(t *testing.T) {
	msgs := []ChatMessage{NewUserMessage("Hello world")}

	p := toResponsesParams("m", msgs, 0)

	items := p.Input.OfInputItemList
	if len(items) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(items))
	}
	msg := items[0].OfMessage
	if msg == nil {
		t.Fatal("expected OfMessage to be set for user text")
	}
	if msg.Role != responses.EasyInputMessageRoleUser {
		t.Errorf("Role = %q, want %q", msg.Role, responses.EasyInputMessageRoleUser)
	}
	if !msg.Content.OfString.Valid() {
		t.Fatal("expected OfString content for plain user text")
	}
	if got := msg.Content.OfString.Value; got != "Hello world" {
		t.Errorf("content = %q, want %q", got, "Hello world")
	}
}

func TestToResponsesParams_UserMultimodalImage(t *testing.T) {
	// Markdown image with data: URL triggers the multi-part path.
	// Surrounding text is needed so parseEmbeddedImages produces >1 part
	// (the multi-part branch in toResponsesParams requires len(parts) > 1).
	imgURL := "data:image/png;base64,iVBORw0KGgo="
	msgs := []ChatMessage{NewUserMessage("What is this? ![pic](" + imgURL + ")")}

	p := toResponsesParams("m", msgs, 0)

	items := p.Input.OfInputItemList
	if len(items) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(items))
	}
	msg := items[0].OfMessage
	if msg == nil {
		t.Fatal("expected OfMessage for multimodal user message")
	}
	if msg.Role != responses.EasyInputMessageRoleUser {
		t.Errorf("Role = %q, want user", msg.Role)
	}
	parts := msg.Content.OfInputItemContentList
	if len(parts) < 1 {
		t.Fatalf("expected at least 1 content part, got %d", len(parts))
	}

	// Find the image part among the content parts.
	found := false
	for _, part := range parts {
		if part.OfInputImage != nil {
			found = true
			if !part.OfInputImage.ImageURL.Valid() {
				t.Fatal("expected ImageURL to be set")
			}
			if got := part.OfInputImage.ImageURL.Value; got != imgURL {
				t.Errorf("ImageURL = %q, want %q", got, imgURL)
			}
		}
	}
	if !found {
		t.Error("expected at least one image content part, none found")
	}
}

func TestToResponsesParams_AssistantPlainText(t *testing.T) {
	msgs := []ChatMessage{NewAssistantMessage("Sure, here is the answer.")}

	p := toResponsesParams("m", msgs, 0)

	items := p.Input.OfInputItemList
	if len(items) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(items))
	}
	msg := items[0].OfMessage
	if msg == nil {
		t.Fatal("expected OfMessage for assistant text")
	}
	if msg.Role != responses.EasyInputMessageRoleAssistant {
		t.Errorf("Role = %q, want %q", msg.Role, responses.EasyInputMessageRoleAssistant)
	}
	if !msg.Content.OfString.Valid() {
		t.Fatal("expected OfString content for assistant text")
	}
	if got := msg.Content.OfString.Value; got != "Sure, here is the answer." {
		t.Errorf("content = %q, want %q", got, "Sure, here is the answer.")
	}
}

func TestToResponsesParams_AssistantWithToolCalls(t *testing.T) {
	msgs := []ChatMessage{
		NewUserMessage("read file"),
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: `{"path":"/tmp/a"}`},
				{ID: "call_2", Name: "list_dir", Arguments: `{"dir":"/tmp"}`},
			},
		},
	}

	p := toResponsesParams("m", msgs, 0)

	items := p.Input.OfInputItemList
	// user message + 2 function_call items, no assistant message (content empty)
	if len(items) != 3 {
		t.Fatalf("expected 3 input items (user + 2 tool calls), got %d", len(items))
	}

	// items[1] and items[2] should be function calls.
	for i, idx := range []int{1, 2} {
		fc := items[idx].OfFunctionCall
		if fc == nil {
			t.Fatalf("item %d: expected OfFunctionCall, got nil", idx)
		}
		tc := msgs[1].ToolCalls[i]
		if fc.CallID != tc.ID {
			t.Errorf("item %d: CallID = %q, want %q", idx, fc.CallID, tc.ID)
		}
		if fc.Name != tc.Name {
			t.Errorf("item %d: Name = %q, want %q", idx, fc.Name, tc.Name)
		}
		if fc.Arguments != tc.Arguments {
			t.Errorf("item %d: Arguments = %q, want %q", idx, fc.Arguments, tc.Arguments)
		}
	}
}

func TestToResponsesParams_AssistantToolCallEmptyArgumentsDefaultsToBraces(t *testing.T) {
	msgs := []ChatMessage{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_x", Name: "no_args", Arguments: ""},
			},
		},
	}

	p := toResponsesParams("m", msgs, 0)

	items := p.Input.OfInputItemList
	if len(items) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(items))
	}
	fc := items[0].OfFunctionCall
	if fc == nil {
		t.Fatal("expected OfFunctionCall")
	}
	if fc.Arguments != "{}" {
		t.Errorf("empty arguments should default to {}, got %q", fc.Arguments)
	}
}

func TestToResponsesParams_AssistantWithReasoningContent(t *testing.T) {
	msgs := []ChatMessage{
		{
			Role:             "assistant",
			ReasoningContent: "thinking about the question",
		},
	}

	p := toResponsesParams("m", msgs, 0)

	items := p.Input.OfInputItemList
	if len(items) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(items))
	}
	r := items[0].OfReasoning
	if r == nil {
		t.Fatal("expected OfReasoning to be set for reasoning_content")
	}
	if r.ID != "rs_x_0" {
		t.Errorf("reasoning ID = %q, want %q", r.ID, "rs_x_0")
	}
	if len(r.Summary) != 1 {
		t.Fatalf("expected 1 summary entry, got %d", len(r.Summary))
	}
	if r.Summary[0].Text != "thinking about the question" {
		t.Errorf("summary text = %q, want %q", r.Summary[0].Text, "thinking about the question")
	}
}

func TestToResponsesParams_ToolMessageBecomesFunctionCallOutput(t *testing.T) {
	msgs := []ChatMessage{
		{
			Role:          "tool",
			Content:       `{"ok":true}`,
			ToolCallID:    "call_42",
			ToolName:      "get_weather",
			ToolArguments: `{"city":"SF"}`,
		},
	}

	p := toResponsesParams("m", msgs, 0)

	items := p.Input.OfInputItemList
	if len(items) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(items))
	}
	out := items[0].OfFunctionCallOutput
	if out == nil {
		t.Fatal("expected OfFunctionCallOutput for tool message")
	}
	if out.CallID != "call_42" {
		t.Errorf("CallID = %q, want %q", out.CallID, "call_42")
	}
	if !out.Output.OfString.Valid() {
		t.Fatal("expected OfString output")
	}
	if got := out.Output.OfString.Value; got != `{"ok":true}` {
		t.Errorf("Output = %q, want %q", got, `{"ok":true}`)
	}
}

func TestToResponsesParams_ToolMessageEmptyContentDefaultsToBraces(t *testing.T) {
	msgs := []ChatMessage{
		{
			Role:       "tool",
			Content:    "",
			ToolCallID: "call_9",
		},
	}

	p := toResponsesParams("m", msgs, 0)

	items := p.Input.OfInputItemList
	if len(items) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(items))
	}
	out := items[0].OfFunctionCallOutput
	if out == nil {
		t.Fatal("expected OfFunctionCallOutput")
	}
	if !out.Output.OfString.Valid() {
		t.Fatal("expected OfString output")
	}
	if got := out.Output.OfString.Value; got != "{}" {
		t.Errorf("empty tool output should default to {}, got %q", got)
	}
}

func TestToResponsesParams_MaxOutputTokensCarried(t *testing.T) {
	p := toResponsesParams("m", []ChatMessage{NewUserMessage("hi")}, 2048)

	if !p.MaxOutputTokens.Valid() {
		t.Fatal("expected MaxOutputTokens to be set")
	}
	if p.MaxOutputTokens.Value != 2048 {
		t.Errorf("MaxOutputTokens = %d, want 2048", p.MaxOutputTokens.Value)
	}
}

// ---------------------------------------------------------------------------
// toResponsesTools
// ---------------------------------------------------------------------------

func TestToResponsesTools_SingleTool(t *testing.T) {
	tools := []ToolDefinition{
		mockToolDefinition{
			name:        "get_weather",
			description: "Get the weather for a city",
			parameters: []ToolParam{
				{Name: "city", Type: "string", Description: "City name", Required: true},
				{Name: "unit", Type: "string", Description: "Temperature unit"},
			},
		},
	}

	result := toResponsesTools(tools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}

	ft := result[0].OfFunction
	if ft == nil {
		t.Fatal("expected OfFunction to be set")
	}
	if ft.Name != "get_weather" {
		t.Errorf("Name = %q, want %q", ft.Name, "get_weather")
	}
	if !ft.Description.Valid() || ft.Description.Value != "Get the weather for a city" {
		t.Errorf("Description = %q, want %q", ft.Description, "Get the weather for a city")
	}
	// Strict must be false to match Chat Completions behavior.
	// Note: we check Value directly because param.Opt[bool]{Value:false}
	// is indistinguishable from an unset Opt (false is the zero value),
	// so Valid() returns false even though the value is explicitly set.
	if ft.Strict.Value != false {
		t.Errorf("Strict = %v, want false", ft.Strict.Value)
	}

	// Parameters schema
	if ft.Parameters["type"] != "object" {
		t.Errorf("parameters type = %v, want object", ft.Parameters["type"])
	}
	props, ok := ft.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", ft.Parameters["properties"])
	}
	city, ok := props["city"].(map[string]any)
	if !ok {
		t.Fatalf("expected city property, got %T", props["city"])
	}
	if city["type"] != "string" {
		t.Errorf("city type = %v, want string", city["type"])
	}
	if city["description"] != "City name" {
		t.Errorf("city description = %v, want 'City name'", city["description"])
	}

	required, ok := ft.Parameters["required"].([]string)
	if !ok {
		t.Fatalf("expected required []string, got %T", ft.Parameters["required"])
	}
	if len(required) != 1 || required[0] != "city" {
		t.Errorf("required = %v, want [city]", required)
	}
}

func TestToResponsesTools_MultipleTools(t *testing.T) {
	tools := []ToolDefinition{
		mockToolDefinition{name: "tool_a", description: "A", parameters: nil},
		mockToolDefinition{name: "tool_b", description: "B", parameters: []ToolParam{
			{Name: "x", Type: "string", Description: "x param", Required: true},
		}},
	}

	result := toResponsesTools(tools)
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}
	if result[0].OfFunction.Name != "tool_a" {
		t.Errorf("tool[0] Name = %q, want tool_a", result[0].OfFunction.Name)
	}
	if result[1].OfFunction.Name != "tool_b" {
		t.Errorf("tool[1] Name = %q, want tool_b", result[1].OfFunction.Name)
	}
}

func TestToResponsesTools_EmptyList(t *testing.T) {
	result := toResponsesTools(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 tools for nil input, got %d", len(result))
	}

	result = toResponsesTools([]ToolDefinition{})
	if len(result) != 0 {
		t.Errorf("expected 0 tools for empty slice, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// buildResponsesReasoning
// ---------------------------------------------------------------------------

func TestBuildResponsesReasoning_Empty(t *testing.T) {
	r := buildResponsesReasoning("")
	if r.Effort != "" {
		t.Errorf("Effort = %q, want empty", r.Effort)
	}
	if r.Summary != "" {
		t.Errorf("Summary = %q, want empty", r.Summary)
	}
}

func TestBuildResponsesReasoning_Enabled(t *testing.T) {
	r := buildResponsesReasoning("enabled")
	if r.Effort != "medium" {
		t.Errorf("Effort = %q, want medium", r.Effort)
	}
	if r.Summary != "auto" {
		t.Errorf("Summary = %q, want auto", r.Summary)
	}
}

func TestBuildResponsesReasoning_Disabled(t *testing.T) {
	r := buildResponsesReasoning("disabled")
	if r.Effort != "none" {
		t.Errorf("Effort = %q, want none", r.Effort)
	}
	// disabled only sets Effort, not Summary
	if r.Summary != "" {
		t.Errorf("Summary = %q, want empty", r.Summary)
	}
}

func TestBuildResponsesReasoning_CustomEffortJSON(t *testing.T) {
	r := buildResponsesReasoning(`{"effort":"high"}`)
	if r.Effort != "high" {
		t.Errorf("Effort = %q, want high", r.Effort)
	}
}

func TestBuildResponsesReasoning_NestedReasoningJSON(t *testing.T) {
	r := buildResponsesReasoning(`{"reasoning":{"effort":"low"}}`)
	if r.Effort != "low" {
		t.Errorf("Effort = %q, want low", r.Effort)
	}
}

func TestBuildResponsesReasoning_CustomSummaryJSON(t *testing.T) {
	r := buildResponsesReasoning(`{"effort":"high","summary":"detailed"}`)
	if r.Effort != "high" {
		t.Errorf("Effort = %q, want high", r.Effort)
	}
	if r.Summary != "detailed" {
		t.Errorf("Summary = %q, want detailed", r.Summary)
	}
}

func TestBuildResponsesReasoning_InvalidJSONFallsBackToEmpty(t *testing.T) {
	// Non-JSON, non-keyword value should return empty ReasoningParam.
	r := buildResponsesReasoning("not-a-real-mode")
	if r.Effort != "" {
		t.Errorf("Effort = %q, want empty for invalid mode", r.Effort)
	}
	if r.Summary != "" {
		t.Errorf("Summary = %q, want empty for invalid mode", r.Summary)
	}
}

// ---------------------------------------------------------------------------
// responsesStatusToFinishReason
// ---------------------------------------------------------------------------

func TestResponsesStatusToFinishReason(t *testing.T) {
	cases := []struct {
		name         string
		status       responses.ResponseStatus
		hasToolCalls bool
		want         FinishReason
	}{
		{"completed no tool calls", responses.ResponseStatusCompleted, false, FinishReasonStop},
		{"completed with tool calls", responses.ResponseStatusCompleted, true, FinishReasonToolCalls},
		{"incomplete", responses.ResponseStatusIncomplete, false, FinishReasonLength},
		{"incomplete with tool calls still length", responses.ResponseStatusIncomplete, true, FinishReasonLength},
		{"failed", responses.ResponseStatusFailed, false, FinishReasonContentFilter},
		{"cancelled", responses.ResponseStatusCancelled, false, FinishReasonContentFilter},
		{"in_progress", responses.ResponseStatusInProgress, false, FinishReasonStop},
		{"queued", responses.ResponseStatusQueued, false, FinishReasonStop},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := responsesStatusToFinishReason(c.status, c.hasToolCalls)
			if got != c.want {
				t.Errorf("responsesStatusToFinishReason(%q, %v) = %q, want %q",
					c.status, c.hasToolCalls, got, c.want)
			}
		})
	}
}
