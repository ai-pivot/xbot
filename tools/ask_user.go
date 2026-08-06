package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"

	"github.com/google/uuid"
)

// AskUserTool allows the agent to ask the user a question and wait for their response.
// Supported channels: CLI, Feishu, Web.
// In CLI, opens an interactive TUI panel. In Feishu, sends an interactive card with buttons/options.
// In Web, sends a WebSocket message that renders a form.
// Only available in channels that support interactive responses (implements ChannelProvider).
type AskUserTool struct{}

func (t *AskUserTool) Name() string { return "AskUser" }

func (t *AskUserTool) Description() string {
	return `Ask the user a question and wait for their response. Use this when you need confirmation, clarification, or additional information from the user. Supports optional choices for multiple-choice questions.

CRITICAL: Each question MUST be a separate item in the array. Do NOT combine multiple questions into a single question string.
BAD:  [{"question": "1. Which theme? 2. What layout?", "options": ["dark","light"]}]
GOOD: [{"question": "Which theme?", "options": ["dark","light"]}, {"question": "What layout?"}]
Each item gets its own answer. Options only apply to the question they belong to.`
}

func (t *AskUserTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "questions",
			Type:        "array",
			Description: `Array of questions to ask the user. Each item is an object with "question" (string, required, supports multi-line) and "options" (array of strings, optional) fields. Each question MUST be a separate array item — never merge multiple questions into one string. Example: [{"question":"Choose a theme","options":["dark","light"]},{"question":"Any other preferences?"}]`,
			Required:    true,
			Items: &llm.ToolParamItems{
				Type: "object",
				Properties: map[string]any{
					"question": map[string]any{"type": "string", "description": "The question to ask the user (supports multi-line)"},
					"options":  map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Optional choices for multiple-choice questions"},
				},
				Required: []string{"question"},
			},
		},
	}
}

type askUserArgs struct {
	Questions []askQItem `json:"questions"`
}

type askQItem struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

func (t *AskUserTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	args, err := parseToolArgs[askUserArgs](input)
	if err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	if len(args.Questions) == 0 {
		return nil, fmt.Errorf("questions parameter is required")
	}

	qJSON, _ := json.Marshal(args.Questions)
	metadata := map[string]string{
		"ask_questions": string(qJSON),
		"request_id":    uuid.NewString(),
	}

	// For CLI, the engine sends OutboundMessage{WaitingUser:true} to the channel
	// adapter which opens the TUI panel. For Feishu, the channel adapter builds
	// and sends an interactive card. No SendFunc needed here.
	_ = ctx // ctx is available for future use but not needed currently

	// The Summary is what the model sees as the tool result. It MUST state the
	// async semantics explicitly: the questions were SENT to the user, this
	// turn ends now (WaitingUser), and the answer(s) arrive as a user message
	// in the next turn. Without this, models get confused ("is AskUser async?
	// did it already return the answer?") and keep generating instead of
	// stopping to wait.
	qs := make([]string, 0, len(args.Questions))
	for _, q := range args.Questions {
		qs = append(qs, q.Question)
	}
	detail := ""
	if len(qs) > 0 {
		detail = " Questions: " + strings.Join(qs, " | ")
	}
	return &ToolResult{
		Summary: fmt.Sprintf(
			"Asked %d question(s) to the user; awaiting their answer(s). "+
				"This is ASYNC: end this turn now (the engine pauses until the user replies). "+
				"The user's answer(s) will arrive as a user message in the next turn.%s",
			len(args.Questions), detail),
		WaitingUser: true,
		Metadata:    metadata,
	}, nil
}

// SupportedChannels implements ChannelProvider interface.
// web must be included: the web AskUserPanel + ask_user SSE pipeline is
// complete; without the tool, web agents can never initiate a question.
func (t *AskUserTool) SupportedChannels() []string {
	return []string{"cli", "feishu", "web"}
}
