package protocol

import (
	"fmt"
	"strings"

	"xbot/llm"
)

// ---------------------------------------------------------------------------
// JSONL session export/import — benchmark-friendly format.
//
// Mirrors the HLE / mint-bench conversation format (see
// HLEexpert.stem7.jsonl): one JSON object per line, each line is a complete
// question→answer record with an OpenAI-style message flow:
//
//	{
//	  "uuid": "biology:13",
//	  "question": "<first user prompt>",
//	  "answer": "<final assistant text>",
//	  "domain": "<domain or empty>",
//	  "messages": [
//	    {"kind":"request",  "parts":[{"part_kind":"user-prompt","content":"...","tool_name":"None","tool_call_id":"None","args":"None"}]},
//	    {"kind":"response", "parts":[{"part_kind":"thinking","content":"...","tool_name":"None","tool_call_id":"None","args":"None"}]},
//	    {"kind":"response", "parts":[{"part_kind":"tool-call","content":"None","tool_name":"python","tool_call_id":"python_0","args":"{...}"}]},
//	    {"kind":"request",  "parts":[{"part_kind":"tool-return","content":"...","tool_name":"python","tool_call_id":"python_0","args":"None"}]},
//	    {"kind":"response", "parts":[{"part_kind":"text","content":"...","tool_name":"None","tool_call_id":"None","args":"None"}]}
//	  ],
//	  "correct": false,
//	  "judge_applied": false
//	}
//
// One exported record is produced per user turn (a user message plus all
// following assistant/tool messages until the next user message).
// ---------------------------------------------------------------------------

// DemoRecord is one JSONL line (one question→answer turn).
type DemoRecord struct {
	UUID         string        `json:"uuid"`
	Question     string        `json:"question"`
	Answer       string        `json:"answer"`
	Domain       string        `json:"domain"`
	Messages     []DemoMessage `json:"messages"`
	Correct      bool          `json:"correct"`
	JudgeApplied bool          `json:"judge_applied"`
}

// DemoMessage is a request or response message in the conversation flow.
type DemoMessage struct {
	Kind  string     `json:"kind"` // "request" | "response"
	Parts []DemoPart `json:"parts"`
}

// DemoPart is one content part inside a message.
type DemoPart struct {
	PartKind   string `json:"part_kind"` // user-prompt | thinking | text | tool-call | tool-return
	Content    string `json:"content"`
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Args       string `json:"args"`
}

// noneStr mirrors the benchmark convention of "None" for absent values.
func noneStr(s string) string {
	if s == "" {
		return "None"
	}
	return s
}

// ExportSessionJSONL converts xbot messages into benchmark-format records,
// one record per user turn. DisplayOnly messages are skipped. The first
// message of each turn becomes the question; the last assistant text becomes
// the answer.
func ExportSessionJSONL(chatID string, msgs []llm.ChatMessage) []DemoRecord {
	var records []DemoRecord
	var cur *DemoRecord
	turn := 0

	for _, msg := range msgs {
		if msg.DisplayOnly {
			continue
		}
		switch msg.Role {
		case "system":
			// system instructions are not part of the benchmark conversation
			continue
		case "user":
			if cur != nil {
				records = append(records, *cur)
			}
			turn++
			cur = &DemoRecord{
				UUID:         fmt.Sprintf("%s:%d", sanitizeUUIDBase(chatID), turn),
				Question:     msg.Content,
				Domain:       "",
				Correct:      false,
				JudgeApplied: false,
			}
			cur.Messages = append(cur.Messages, DemoMessage{
				Kind: "request",
				Parts: []DemoPart{{
					PartKind:   "user-prompt",
					Content:    msg.Content,
					ToolName:   "None",
					ToolCallID: "None",
					Args:       "None",
				}},
			})
		case "assistant":
			if cur == nil {
				continue
			}
			dm := DemoMessage{Kind: "response"}
			if msg.ReasoningContent != "" {
				dm.Parts = append(dm.Parts, DemoPart{
					PartKind:   "thinking",
					Content:    msg.ReasoningContent,
					ToolName:   "None",
					ToolCallID: "None",
					Args:       "None",
				})
			}
			for _, tc := range msg.ToolCalls {
				dm.Parts = append(dm.Parts, DemoPart{
					PartKind:   "tool-call",
					Content:    "None",
					ToolName:   noneStr(tc.Name),
					ToolCallID: noneStr(tc.ID),
					Args:       noneStr(tc.Arguments),
				})
			}
			if msg.Content != "" {
				dm.Parts = append(dm.Parts, DemoPart{
					PartKind:   "text",
					Content:    msg.Content,
					ToolName:   "None",
					ToolCallID: "None",
					Args:       "None",
				})
				cur.Answer = msg.Content
			}
			if len(dm.Parts) > 0 {
				cur.Messages = append(cur.Messages, dm)
			}
		case "tool":
			if cur == nil {
				continue
			}
			cur.Messages = append(cur.Messages, DemoMessage{
				Kind: "request",
				Parts: []DemoPart{{
					PartKind:   "tool-return",
					Content:    msg.Content,
					ToolName:   noneStr(msg.ToolName),
					ToolCallID: noneStr(msg.ToolCallID),
					Args:       "None",
				}},
			})
		}
	}
	if cur != nil {
		records = append(records, *cur)
	}
	return records
}

// ImportSessionJSONL converts benchmark-format records back into xbot
// ChatMessages (the inverse of ExportSessionJSONL). Each record's messages are
// flattened into a single message stream; a response message with multiple
// parts is merged into ONE assistant message (reasoning + tool_calls +
// content).
func ImportSessionJSONL(records []DemoRecord) []llm.ChatMessage {
	var out []llm.ChatMessage
	for _, rec := range records {
		for _, dm := range rec.Messages {
			switch dm.Kind {
			case "request":
				for _, p := range dm.Parts {
					switch p.PartKind {
					case "user-prompt":
						out = append(out, llm.ChatMessage{Role: "user", Content: p.Content})
					case "tool-return":
						out = append(out, llm.ChatMessage{
							Role:       "tool",
							Content:    p.Content,
							ToolName:   unNone(p.ToolName),
							ToolCallID: unNone(p.ToolCallID),
						})
					}
				}
			case "response":
				am := llm.ChatMessage{Role: "assistant"}
				for _, p := range dm.Parts {
					switch p.PartKind {
					case "thinking":
						am.ReasoningContent = p.Content
					case "text":
						am.Content = p.Content
					case "tool-call":
						am.ToolCalls = append(am.ToolCalls, llm.ToolCall{
							ID:        unNone(p.ToolCallID),
							Name:      unNone(p.ToolName),
							Arguments: unNone(p.Args),
						})
					}
				}
				if am.Content != "" || len(am.ToolCalls) > 0 || am.ReasoningContent != "" {
					out = append(out, am)
				}
			}
		}
	}
	return out
}

// unNone converts the "None" placeholder back to an empty string.
func unNone(s string) string {
	if s == "" || s == "None" {
		return ""
	}
	return s
}

// sanitizeUUIDBase keeps a chatID usable inside a uuid (strip path chars).
func sanitizeUUIDBase(chatID string) string {
	s := strings.ReplaceAll(chatID, "/", "-")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}
