package plugin

import (
	"bytes"
	"encoding/json"
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

// TestReadJSON_LineLargerThanOneMB —— 复现 "bufio.Scanner: token too long"。
//
// 事故：git-fancy 插件某次输出单行 JSON > 1MB（大 diff / 长 commit 列表），
// bufio.Scanner 到达上限后直接放弃 → jsonLineReader 报错 → 宿主判定
// "plugin stdout closed" → 前端 unhandledrejection（用户看到应用崩溃）。
//
// 断言：超过 1MB 的单行 JSON 必须能正常读出（读取不得受行长度限制）。
func TestReadJSON_LineLargerThanOneMB(t *testing.T) {
	big := strings.Repeat("x", 2*1024*1024) // 2MB payload — 超过旧的 1MB 上限
	input := `{"data":"` + big + `"}` + "\n"

	reader := newJSONLineReader(strings.NewReader(input))
	line, err := reader.readLine()
	if err != nil {
		t.Fatalf("readLine must not fail on a >1MB line: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s, _ := got["data"].(string); len(s) != len(big) {
		t.Fatalf("payload truncated: got %d want %d", len(s), len(big))
	}

	// ReadJSON 走同一条读取路径，也必须能处理超长行。
	var got2 map[string]any
	if err := ReadJSON(strings.NewReader(input), &got2); err != nil {
		t.Fatalf("ReadJSON must not fail on a >1MB line: %v", err)
	}
	if s, _ := got2["data"].(string); len(s) != len(big) {
		t.Fatalf("ReadJSON payload truncated: got %d want %d", len(s), len(big))
	}
}

// TestReadJSONLineReader_MultipleLines —— 大行之后仍能继续读下一行（边界正确）。
func TestReadJSONLineReader_MultipleLines(t *testing.T) {
	big := strings.Repeat("y", 1500*1024)
	input := `{"n":1,"pad":"` + big + `"}` + "\n" + `{"n":2}` + "\n"
	r := newJSONLineReader(strings.NewReader(input))
	if _, err := r.readLine(); err != nil {
		t.Fatalf("first (large) line: %v", err)
	}
	second, err := r.readLine()
	if err != nil {
		t.Fatalf("second line after a large one: %v", err)
	}
	if !strings.Contains(string(second), `"n":2`) {
		t.Fatalf("second line = %q, want {\"n\":2}", second)
	}
}
