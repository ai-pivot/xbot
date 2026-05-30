package plugin

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	data := map[string]any{"key": "value", "num": 42}
	if err := WriteJSON(&buf, data); err != nil {
		t.Fatalf("WriteJSON error: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, `"key"`) || !strings.Contains(line, `"value"`) {
		t.Errorf("WriteJSON output = %q, want key-value pair", line)
	}
	if !strings.HasSuffix(line, "\n") {
		t.Errorf("WriteJSON output should end with newline, got %q", line)
	}
}

func TestReadJSON(t *testing.T) {
	input := `{"name":"test","count":7}` + "\n"
	reader := strings.NewReader(input)
	var got map[string]any
	if err := ReadJSON(reader, &got); err != nil {
		t.Fatalf("ReadJSON error: %v", err)
	}
	if got["name"] != "test" {
		t.Errorf("got name = %v, want 'test'", got["name"])
	}
}

func TestReadJSON_EOF(t *testing.T) {
	reader := strings.NewReader("")
	var got map[string]any
	err := ReadJSON(reader, &got)
	if err == nil {
		t.Fatal("expected error for EOF")
		return
	}
}

func TestReadJSON_InvalidJSON(t *testing.T) {
	reader := strings.NewReader("not json\n")
	var got map[string]any
	err := ReadJSON(reader, &got)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
		return
	}
}

func TestFormatJSON(t *testing.T) {
	data := map[string]any{"z": 1, "a": 2}
	result := FormatJSON(data)
	if !strings.Contains(result, `"a"`) || !strings.Contains(result, `"z"`) {
		t.Errorf("FormatJSON = %q, want formatted JSON", result)
	}
	// Should be pretty-printed (indented)
	if !strings.Contains(result, "\n") {
		t.Errorf("FormatJSON should be indented, got %q", result)
	}
}

func TestFormatJSON_Nil(t *testing.T) {
	result := FormatJSON(nil)
	if result != "null" {
		t.Errorf("FormatJSON(nil) = %q, want 'null'", result)
	}
}
