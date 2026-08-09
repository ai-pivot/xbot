package xbot

import (
	"strings"
	"testing"
)

func TestCJKSpacing(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"pure chinese", "记忆系统", "记 忆 系 统"},
		{"mixed cn+en", "GLM模型部署", "GLM 模 型 部 署"},
		{"english only", "GLM DCP HiCache", "GLM DCP HiCache"},
		{"cn phrase with punct", "跨会话记忆。很强大", "跨 会 话 记 忆。很 强 大"},
		{"empty", "", ""},
		{"single cjk", "记", "记"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cjkSpaceRuns(tt.in)
			if got != tt.want {
				t.Errorf("cjkSpaceRuns(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFts5SafeQueryChinese(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"chinese substring", "记忆", `"记" AND "忆"`},
		{"chinese 3 char", "跨会话", `"跨" AND "会" AND "话"`},
		{"mixed cn+en", "GLM模型", `"GLM" AND "模" AND "型"`},
		{"english", "GLM DCP", `"GLM" AND "DCP"`},
		{"empty", "  ", `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fts5SafeQuery(tt.in)
			if got != tt.want {
				t.Errorf("fts5SafeQuery(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildSearchText(t *testing.T) {
	got := buildSearchText("记忆系统很强大", "GLM, DCP", "部署")
	want := "记 忆 系 统 很 强 大 GLM, DCP 部 署"
	if got != want {
		t.Errorf("buildSearchText() = %q, want %q", got, want)
	}
}

func TestFts5SafeQueryLongMessage(t *testing.T) {
	// A pasted document / log dump must not blow up into an unbounded MATCH
	// expression or truncate a UTF-8 char mid-sequence.
	long := "这是一个非常长的中文文档，包含大量无意义内容。" + string(make([]rune, 5000))
	q := fts5SafeQuery(long)
	if q == "" {
		t.Fatal("long query produced empty MATCH expression")
	}
	// Token cap: no more than maxQueryTokens AND terms.
	tokens := strings.Count(q, " AND ") + 1
	if tokens > 25 {
		t.Errorf("long query produced %d tokens, want <= 25 (capped)", tokens)
	}
	// Must be valid FTS5: every token is a quoted literal, no raw operators.
	if !strings.HasPrefix(q, `"`) || !strings.HasSuffix(q, `"`) {
		t.Errorf("query not fully quoted: %q", q)
	}
}

func TestTruncateRunes(t *testing.T) {
	// Chinese 3-byte chars must not be split mid-sequence.
	s := "记忆系统很强大"
	got := truncateRunes(s, 3)
	if got != "记忆系..." {
		t.Errorf("truncateRunes(%q, 3) = %q, want %q", s, got, "记忆系...")
	}
	// Short string unchanged.
	if truncateRunes("ab", 5) != "ab" {
		t.Error("short string should be unchanged")
	}
	// Max <= 0 → empty.
	if truncateRunes("abc", 0) != "" {
		t.Error("max 0 should return empty")
	}
}
