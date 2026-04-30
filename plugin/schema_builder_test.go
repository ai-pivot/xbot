package plugin

import (
	"testing"

	"xbot/llm"
)

func TestSchemaBuilder_AllTypes(t *testing.T) {
	params := NewSchemaBuilder().
		AddStringParam("name", "The name", true).
		AddNumberParam("count", "The count", false).
		AddBoolParam("active", "Is active", true).
		AddArrayParam("tags", "The tags", false).
		Build()

	if len(params) != 4 {
		t.Fatalf("expected 4 params, got %d", len(params))
	}

	assertSchemaParam(t, params[0], "name", "string", "The name", true)
	assertSchemaParam(t, params[1], "count", "number", "The count", false)
	assertSchemaParam(t, params[2], "active", "boolean", "Is active", true)
	assertSchemaParam(t, params[3], "tags", "array", "The tags", false)

	// Array param should not have Items set in simplified mode.
	if params[3].Items != nil {
		t.Error("array param should not have Items set")
	}
}

func TestSchemaBuilder_Empty(t *testing.T) {
	params := NewSchemaBuilder().Build()

	if params == nil {
		t.Fatal("Build() should return non-nil slice")
	}
	if len(params) != 0 {
		t.Fatalf("expected 0 params, got %d", len(params))
	}
}

func TestSchemaBuilder_MixedRequired(t *testing.T) {
	params := NewSchemaBuilder().
		AddStringParam("a", "required param", true).
		AddStringParam("b", "optional param", false).
		AddNumberParam("c", "also required", true).
		AddBoolParam("d", "optional flag", false).
		Build()

	if len(params) != 4 {
		t.Fatalf("expected 4 params, got %d", len(params))
	}

	var requiredNames []string
	for _, p := range params {
		if p.Required {
			requiredNames = append(requiredNames, p.Name)
		}
	}
	if len(requiredNames) != 2 {
		t.Fatalf("expected 2 required params, got %d: %v", len(requiredNames), requiredNames)
	}
	if requiredNames[0] != "a" || requiredNames[1] != "c" {
		t.Errorf("required names: got %v, want [a c]", requiredNames)
	}
}

func assertSchemaParam(t *testing.T, p llm.ToolParam, name, typ, desc string, required bool) {
	t.Helper()
	if p.Name != name {
		t.Errorf("param Name: got %q, want %q", p.Name, name)
	}
	if p.Type != typ {
		t.Errorf("param Type: got %q, want %q", p.Type, typ)
	}
	if p.Description != desc {
		t.Errorf("param Description: got %q, want %q", p.Description, desc)
	}
	if p.Required != required {
		t.Errorf("param Required: got %v, want %v", p.Required, required)
	}
}
