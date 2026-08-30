package tools

import (
	"encoding/json"
	"testing"
)

// TestFlexStrings_ObjectOptions reproduces the user-reported parse failure:
// LLM sends options as semantic objects instead of plain strings.
func TestFlexStrings_ObjectOptions(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "plain strings (original format)",
			input: `["dark","light"]`,
			want:  []string{"dark", "light"},
		},
		{
			name:  "objects with label",
			input: `[{"label":"WS push","description":"real-time"},{"label":"500ms polling"}]`,
			want:  []string{"WS push", "500ms polling"},
		},
		{
			name:  "objects with value",
			input: `[{"value":"option-a"},{"value":"option-b","label":"ignored-no-label-first"}]`,
			want:  []string{"option-a", "ignored-no-label-first"},
		},
		{
			name:  "objects with name",
			input: `[{"name":"theme-dark"},{"name":"theme-light"}]`,
			want:  []string{"theme-dark", "theme-light"},
		},
		{
			name:  "mixed strings and objects",
			input: `["plain",{"label":"object"}]`,
			want:  []string{"plain", "object"},
		},
		{
			name:  "empty array",
			input: `[]`,
			want:  []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f flexStrings
			if err := json.Unmarshal([]byte(tt.input), &f); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(f) != len(tt.want) {
				t.Fatalf("got %v, want %v", f, tt.want)
			}
			for i, v := range f {
				if v != tt.want[i] {
					t.Fatalf("item[%d]: got %q, want %q", i, v, tt.want[i])
				}
			}
		})
	}
}

// TestAskUserParse_ObjectOptions verifies the full Execute path accepts object-format options.
func TestAskUserParse_ObjectOptions(t *testing.T) {
	tool := &AskUserTool{}
	// LLM sends object-format options — the exact input that caused
	// "parse args: json: cannot unmarshal object into Go struct field askQItem.questions.options of type string"
	input := `{"questions":[{"question":"Which approach?","options":[{"label":"SSE push (recommended)","description":"real-time"},{"label":"500ms polling","description":"simple but wasteful"},{"label":"Don't change for now"}]}]}`
	res, err := tool.Execute(&ToolContext{}, input)
	if err != nil {
		t.Fatalf("Execute with object options should succeed, got: %v", err)
	}
	if res == nil || !res.WaitingUser {
		t.Fatalf("expected WaitingUser result, got %+v", res)
	}
	var items []askQItem
	if err := json.Unmarshal([]byte(res.Metadata["ask_questions"]), &items); err != nil {
		t.Fatalf("parse ask_questions: %v", err)
	}
	if len(items) != 1 || len(items[0].Options) != 3 {
		t.Fatalf("expected 1 question with 3 options, got %+v", items)
	}
	if items[0].Options[0] != "SSE push (recommended)" {
		t.Fatalf("option[0] should be 'SSE push (recommended)', got %q", items[0].Options[0])
	}
}
