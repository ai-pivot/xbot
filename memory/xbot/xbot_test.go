package xbot

import (
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
