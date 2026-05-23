package tools

import (
	"strings"
	"testing"
)

func TestSanitizeOutputLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "carriage return keeps last frame",
			input: "Loading 10%\rLoading 50%\rLoading 100%",
			want:  "Loading 100%",
		},
		{
			name:  "ANSI color codes stripped",
			input: "\x1b[32mSuccess\x1b[0m",
			want:  "Success",
		},
		{
			name:  "tqdm-style progress bar with unicode blocks",
			input: "Map:  71%|\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u258d  | 2118/2967 [00:00<00:00, 81922.37 examples/s]",
			want:  "Map:  71%|\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u258d  | 2118/2967 [00:00<00:00, 81922.37 examples/s]",
		},
		{
			name:  "ANSI + carriage return combined",
			input: "\x1b[32m100%\r\x1b[0mDone",
			want:  "Done",
		},
		{
			name:  "empty after carriage return",
			input: "something\r   ",
			want:  "   ",
		},
		{
			name:  "multiple carriage returns",
			input: "a\rb\rc\rdone",
			want:  "done",
		},
		{
			name:  "curl progress output (single line with \\r)",
			input: "  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current\r                                 Dload  Upload   Total   Spent    Left  Speed",
			want:  "                                 Dload  Upload   Total   Spent    Left  Speed",
		},
		{
			name:  "ANSI 256-color",
			input: "\x1b[38;5;196mRed\x1b[0m text",
			want:  "Red text",
		},
		{
			name:  "ANSI bold",
			input: "\x1b[1mBold\x1b[0m",
			want:  "Bold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeOutputLine(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeOutputLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeOutputLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "simple lines",
			input: "line1\nline2\nline3",
			want:  []string{"line1", "line2", "line3"},
		},
		{
			name:  "tqdm progress with \\r\\n (each \\r stripped)",
			input: "Map: 100%|\u2588\u2588\u2588| 2967/2967\r\nMap:  71%|\u2588\u2588| 2118/2967\r\nDone loading",
			want:  []string{"Done loading"},
		},
		{
			name:  "tqdm carriage return within line keeps last frame",
			input: "Map: 100%|\u2588\u2588\u2588| 2967/2967\rMap:  71%|\u2588\u2588| 2118/2967",
			want:  []string{"Map:  71%|\u2588\u2588| 2118/2967"},
		},
		{
			name:  "lines that become empty after sanitization are filtered",
			input: "visible\r   \nalso visible",
			want:  []string{"also visible"},
		},
		{
			name:  "ANSI colored lines",
			input: "\x1b[32mGreen\x1b[0m\n\x1b[31mRed\x1b[0m",
			want:  []string{"Green", "Red"},
		},
		{
			name:  "truly empty lines filtered",
			input: "a\n\n\nb",
			want:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeOutputLines(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("SanitizeOutputLines(%q) = %q, want %q", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SanitizeOutputLines(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSanitizeOutputLineNoControlChars(t *testing.T) {
	// Verify that sanitized output never contains \r or \x1b
	inputs := []string{
		"progress\rmore\r\nfinal",
		"\x1b[1;32mcolor\x1b[0m normal\r\x1b[31mred\x1b[0m",
		strings.Repeat("frame\r", 100) + "done",
	}
	for _, input := range inputs {
		result := SanitizeOutputLine(input)
		if strings.Contains(result, "\r") {
			t.Errorf("SanitizeOutputLine(%q) still contains \\r: %q", input, result)
		}
		if strings.Contains(result, "\x1b") {
			t.Errorf("SanitizeOutputLine(%q) still contains \\x1b: %q", input, result)
		}
	}
}
