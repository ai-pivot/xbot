package main

import "testing"

// TestGlobMatch 覆盖通配匹配的基本语义：'*' 任意长度（含空）、'?' 恰好一个字符，
// 其余字符按字面量（包括正则里特殊但这里必须原样匹配的 . [ ] +）。
func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		subject string
		want    bool
	}{
		// 无通配符 = 精确相等
		{"10001", "10001", true},
		{"10001", "10002", false},
		{"group:123", "group:123", true},

		// '*'：任意长度
		{"*", "", true},
		{"*", "10001", true},
		{"*", "group:123456", true},
		{"group:*", "group:123456", true},
		{"group:*", "10001", false},
		{"*:123", "group:123", true},
		{"group*", "group", true},
		{"group*", "gro", false},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},

		// '?'：恰好一个字符
		{"1000?", "10001", true},
		{"1000?", "1000", false},
		{"1000?", "100011", false},

		// 正则元字符按字面量处理
		{"a.b", "axb", false},
		{"a.b", "a.b", true},
		{"a+b", "aab", false},
		{"[x]", "[x]", true},
	}
	for _, tc := range tests {
		if got := globMatch(tc.pattern, tc.subject); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}

// TestParsePatterns 覆盖配置文本的分隔符宽容度与空白清理。
func TestParsePatterns(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"10001", []string{"10001"}},
		{"10001,10002", []string{"10001", "10002"}},
		{"10001，10002", []string{"10001", "10002"}},                     // 中文逗号
		{"10001; 10002；group:3", []string{"10001", "10002", "group:3"}}, // 分号 / 中文分号 / 空格
		{"*", []string{"*"}},
		{"\n10001\n  group:*\n", []string{"10001", "group:*"}},
		{",,10001,,", []string{"10001"}}, // 空项丢弃
	}
	for _, tc := range tests {
		got := ParsePatterns(tc.in).List()
		if len(got) != len(tc.want) {
			t.Fatalf("ParsePatterns(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("ParsePatterns(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

// TestPatternsAllowsAll 确认「可配置为所有消息都处理」靠的是显式的 "*"。
func TestPatternsAllowsAll(t *testing.T) {
	if ParsePatterns("").AllowsAll() {
		t.Fatal("empty allow list must NOT be treated as allow-all")
	}
	if ParsePatterns("10001,group:*").AllowsAll() {
		t.Fatal("prefix wildcard must NOT be treated as allow-all")
	}
	if !ParsePatterns("10001, *").AllowsAll() {
		t.Fatal(`explicit "*" must be treated as allow-all`)
	}
}

// TestPatternsMatchEmpty 空输入一律不匹配（无身份的消息不做准入）。
func TestPatternsMatchEmpty(t *testing.T) {
	if ParsePatterns("*").Match("") {
		t.Fatal(`"*" must not match an empty subject (no identity, nothing to audit)`)
	}
	if ParsePatterns("10001").Match("") {
		t.Fatal("exact pattern must not match an empty subject")
	}
}

// TestPatternsListIsCopy 确认 List 返回副本，调用方改不动内部状态。
func TestPatternsListIsCopy(t *testing.T) {
	p := ParsePatterns("10001")
	l := p.List()
	l[0] = "tampered"
	if !p.Match("10001") {
		t.Fatal("mutating List() result leaked into Patterns")
	}
}
