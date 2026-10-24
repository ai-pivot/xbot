package agent

import (
	"fmt"
	"strings"
	"testing"
)

// ==================== flattenLines ====================

func TestFlattenLines(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int // expected number of result lines
	}{
		{"nil", nil, 0},
		{"empty", []string{}, 0},
		{"single", []string{"hello"}, 1},
		{"multi elements", []string{"a", "b", "c"}, 3},
		{"newline in element", []string{"a\nb\nc"}, 3},
		{"mixed", []string{"a", "b\nc", "d"}, 4},
		{"empty elements filtered", []string{"", "a", "", "b"}, 2},
		{"newline empty elements", []string{"a\n\nb"}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenLines(tt.lines)
			if len(got) != tt.want {
				t.Errorf("flattenLines() len = %d, want %d (got: %v)", len(got), tt.want, got)
			}
		})
	}
}

// ==================== progressTruncate ====================

func TestProgressTruncate(t *testing.T) {
	tests := []struct {
		s        string
		maxRunes int
		want     string
	}{
		// 基本截断
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 3, "he…"},
		{"hello", 1, "…"},
		{"hello", 0, "…"},
		{"你好世界", 4, "你好世界"},
		{"你好世界", 3, "你好…"},

		// 行内代码闭合
		{"`hello world` end", 12, "`hello worl…`"}, // 截在代码内，闭合 backtick
		{"`hi` done", 10, "`hi` done"},             // 不截断
		{"`a` `b` `c`", 8, "`a` `b`…"},             // 截在第3个代码开头，闭合

		// 粗体闭合
		{"**hello** world", 10, "**hello**…"},   // 已闭合的粗体+空格被截，无未闭合
		{"**hello** world", 8, "**hello…**"},    // 截在粗体内，闭合
		{"**bold text here**", 8, "**bold …**"}, // 截在粗体内

		// 斜体闭合
		{"*hello* world", 10, "*hello* w…"}, // 已闭合
		{"*hello* world", 7, "*hello…*"},    // 截在斜体内，闭合
		{"*italic text*", 8, "*italic…*"},   // 截在斜体内，闭合

		// 链接闭合（链接 []() 内部不做特殊处理，仅 [ 需要 ](…) 闭合）
		{"[link](https://example.com) end", 15, "[link](https:/…"}, // 截在URL中间

		// 删除线闭合
		{"~~hello~~ world", 10, "~~hello~~…"}, // 已闭合
		{"~~hello~~ world", 8, "~~hello…~~"},  // 截在删除线内

		// 混合场景
		{"`code` and **bold**", 12, "`code` and …"},   // 截在 ** 之前
		{"**bold** and *italic*", 12, "**bold** an…"}, // 截在 *italic* 之前
		{"normal `code end", 12, "normal `cod…`"},     // 截在代码内

		// 无需闭合
		{"plain text here", 8, "plain t…"},
		{"already **closed** ok", 21, "already **closed** ok"},

		// 边界：截断在标记中间
		{"hello**world", 10, "hello**wo…**"}, // 截在粗体内
		{"hello*world", 9, "hello*wo…*"},     // 截在斜体内
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%d", tt.s, tt.maxRunes), func(t *testing.T) {
			got := progressTruncate(tt.s, tt.maxRunes)
			if got != tt.want {
				t.Errorf("progressTruncate(%q, %d) = %q, want %q", tt.s, tt.maxRunes, got, tt.want)
			}
		})
	}
}

// ==================== extractRoleName ====================

func TestExtractRoleName(t *testing.T) {
	tests := []struct {
		path []string
		want string
	}{
		{[]string{"main/lead"}, "lead"},
		{[]string{"a/b", "a/b/c"}, "c"},
		{[]string{"simple"}, "simple"},
		{[]string{"deep/nested/path"}, "path"},
		{nil, ""},
		{[]string{}, ""},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path, ","), func(t *testing.T) {
			got := extractRoleName(tt.path)
			if got != tt.want {
				t.Errorf("extractRoleName(%v) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ==================== isSubAgentLine ====================

func TestIsSubAgentLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		// 树状格式
		{"├─ 🔄 backend: ⏳ Shell(ls)", true},
		{"└─ ✅ qa:", true},
		{"│ 🔄 backend: running", true},
		// 引用格式（实际运行时子 Agent 穿透上来的格式化行）
		{"> 🔄 lead: 💭 思考中...", true},
		{"> ✅ backend:", true},
		{"> ❌ qa: Error: test failed", true},
		{"> 　🔄 coordinator: 分派三个子代理", true},
		// 带全角缩进（子 Agent 格式化输出）
		{"　🔄 backend: ⏳ Shell(ls)", true},
		{"　✅ qa:", true},
		// SubAgent 占位行格式
		{"> ⏳ SubAgent [backend]: ...", true},
		{"> ⏳ SubAgent [coordinator]: ...", true},
		{"⏳ SubAgent [test-role]: ...", true},

		// 不是子 Agent 行
		{"> 💭 思考中...", false},           // 引用前缀但无冒号
		{"> ⏳ Shell(ls) ...", false},    // 引用前缀但无冒号
		{"> > ⏳ Shell(go test)", false}, // 嵌套引用
		{"💭 思考中...", false},             // 无冒号
		{"⏳ Shell(ls) ...", false},      // 无冒号
		{"some random text", false},
		{"", false},
		{"  ", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isSubAgentLine(tt.line)
			if got != tt.want {
				t.Errorf("isSubAgentLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// ==================== isStatusEmojiLine ====================

func TestIsStatusEmojiLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"🔄 role: desc", true},
		{"✅ role:", true},
		{"❌ role: error", true},
		{"⏳ role: pending", false}, // ⏳ 不再匹配 — 工具占位符格式由 isSubAgentLine 的专用分支处理
		{"🔄 role", false},          // 无冒号
		{"💡 thinking", false},      // 非 status emoji
		{"", false},
		// Tool names rejected (PascalCase = not agent role)
		{"❌ Shell: kubectl get pods", false},
		{"✅ Read: /home/user/src/xbot-1/tools/shell.go", false},
		{"🔄 FileCreate: test.go", false},
		{"❌ SubAgent: task", false},
		{"✅ Grep: pattern", false},
		// Chinese role names accepted
		{"🔄 qa: 审查中", true},
		{"✅ backend:", true},
		{"❌ planner: 方案有问题", true},
		// Real agent roles accepted
		{"🔄 lead: 调度中", true},
		{"✅ backend:", true},
		{"❌ explore: 分析失败", true},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isStatusEmojiLine(tt.line)
			if got != tt.want {
				t.Errorf("isStatusEmojiLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestIsPlausibleAgentRole(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// Agent roles → true
		{"lead", true},
		{"backend", true},
		{"explore", true},
		{"coordinator", true},
		{"qa", true},
		{"backend", true},
		{"planner", true},
		{"role", true},
		{"test-agent_v2", true},
		// Tool names → false (PascalCase)
		{"Shell", false},
		{"Read", false},
		{"FileCreate", false},
		{"SubAgent", false},
		{"Grep", false},
		{"Glob", false},
		{"Fetch", false},
		{"AskUser", false},
		// Paths → false
		{"/home/user/src", false},
		{"src/main.go", false},
		// Sentences → false
		{"some random text", false},
		{"The environment changed", false},
		// Empty → false
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPlausibleAgentRole(tt.name)
			if got != tt.want {
				t.Errorf("isPlausibleAgentRole(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// ==================== parseSubAgentLine ====================

func TestParseSubAgentLine(t *testing.T) {
	tests := []struct {
		line         string
		wantOK       bool
		wantRole     string
		wantInstance string
		wantStatus   string
		wantDesc     string
	}{
		// 树状格式
		{"├─ 🔄 backend: ⏳ Shell(ls) ...", true, "backend", "", "🔄", "⏳ Shell(ls) ..."},
		{"└─ ✅ qa:", true, "qa", "", "✅", ""},
		{"│ 🔄 backend: running", true, "backend", "", "🔄", "running"},
		// 引用格式
		// 占位行格式
		{"> ⏳ SubAgent [backend]: ...", true, "backend", "", "⏳", "..."},
		{"⏳ SubAgent [coordinator]: some desc", true, "coordinator", "", "⏳", "some desc"},

		{"> 🔄 lead: 💭 思考中...", true, "lead", "", "🔄", "💭 思考中..."},
		{"> ✅ backend:", true, "backend", "", "✅", ""},
		{"> 　🔄 coordinator: 分派三个子代理", true, "coordinator", "", "🔄", "分派三个子代理"},
		{"　🔄 backend: ⏳ Shell(ls)", true, "backend", "", "🔄", "⏳ Shell(ls)"},
		// 实例格式：同 role 不同 instance 的 SubAgent
		{"> 🔄 explore [mem-1]: reading files", true, "explore", "mem-1", "🔄", "reading files"},
		{"> 🔄 explore [hooks-1]: reading hooks", true, "explore", "hooks-1", "🔄", "reading hooks"},
		{"> ✅ explore [mem-1]:", true, "explore", "mem-1", "✅", ""},
		{"> 🔄 lead [audit]: thinking", true, "lead", "audit", "🔄", "thinking"},
		// 实例格式：no-colon completion
		{"✅ explore [mem-1]", true, "explore", "mem-1", "✅", ""},
		// 失败场景
		{"> 💭 思考中...", false, "", "", "", ""},       // 不是子 Agent 格式
		{"some random text", false, "", "", "", ""}, // 空白
		{"", false, "", "", "", ""},                 // 空
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got, ok := parseSubAgentLine(tt.line)
			if ok != tt.wantOK {
				t.Errorf("parseSubAgentLine(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
				return
			}
			if !tt.wantOK {
				return
			}
			if got.Role != tt.wantRole || got.Instance != tt.wantInstance || got.Status != tt.wantStatus || got.Desc != tt.wantDesc {
				t.Errorf("parseSubAgentLine(%q) = %+v, want {Role:%q Instance:%q Status:%q Desc:%q}",
					tt.line, got, tt.wantRole, tt.wantInstance, tt.wantStatus, tt.wantDesc)
			}
		})
	}
}

// ==================== formatChildAgentsSummary ====================

func TestFormatChildAgentsSummary(t *testing.T) {
	tests := []struct {
		name string
		c    []childAgentStatus
		max  int
		want string
	}{
		{
			"empty",
			nil, 100, "",
		},
		{
			"single running",
			[]childAgentStatus{{Role: "backend", Status: "🔄", Desc: "⏳ Shell(ls)"}},
			100, "🔄 backend(⏳ Shell(ls))",
		},
		{
			"single completed no desc",
			[]childAgentStatus{{Role: "qa", Status: "✅"}},
			100, "✅ qa",
		},
		{
			"3 mixed",
			[]childAgentStatus{
				{Role: "backend", Status: "🔄", Desc: "⏳ Shell(go version)"},
				{Role: "qa", Status: "✅"},
				{Role: "docs", Status: "🔄", Desc: "💭 思考中"},
			},
			100, "🔄 backend(⏳ Shell(go version)) · ✅ qa · 🔄 docs(💭 思考中)",
		},
		{
			"all completed",
			[]childAgentStatus{
				{Role: "backend", Status: "✅"},
				{Role: "qa", Status: "✅"},
				{Role: "docs", Status: "✅"},
			},
			100, "✅ backend · ✅ qa · ✅ docs",
		},
		{
			"with failure",
			[]childAgentStatus{
				{Role: "backend", Status: "✅"},
				{Role: "qa", Status: "❌", Desc: "Error: test failed"},
				{Role: "docs", Status: "🔄", Desc: "⏳ running"},
			},
			100, "✅ backend · ❌ qa(Error: test failed) · 🔄 docs(⏳ running)",
		},
		{
			"desc truncated",
			[]childAgentStatus{{Role: "backend", Status: "🔄", Desc: "this is a very long description that should be truncated"}},
			100, "🔄 backend(this is a very long…)",
		},
		{
			"total truncated",
			[]childAgentStatus{
				{Role: "a", Status: "🔄", Desc: "very long desc"},
				{Role: "b", Status: "✅", Desc: "another long desc"},
				{Role: "c", Status: "🔄", Desc: "yet another long desc"},
			},
			30, "🔄 a(very long desc) · ✅ b(ano…",
		},
		{
			"many agents - stats only",
			[]childAgentStatus{
				{Role: "a", Status: "🔄"}, {Role: "b", Status: "🔄"}, {Role: "c", Status: "🔄"},
				{Role: "d", Status: "✅"}, {Role: "e", Status: "✅"}, {Role: "f", Status: "✅"}, {Role: "g", Status: "❌"},
			},
			100, "🔄×3 · ✅×3 · ❌×1",
		},
		{
			"many agents with pending",
			[]childAgentStatus{
				{Role: "a", Status: "🔄"}, {Role: "b", Status: "⏳"}, {Role: "c", Status: "✅"},
				{Role: "d", Status: "✅"}, {Role: "e", Status: "✅"}, {Role: "f", Status: "✅"}, {Role: "g", Status: "❌"},
			},
			100, "🔄×1 · ⏳×1 · ✅×4 · ❌×1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatChildAgentsSummary(tt.c, tt.max)
			if got != tt.want {
				t.Errorf("formatChildAgentsSummary() =\n  got: %q\n  want: %q", got, tt.want)
			}
		})
	}
}

// ==================== extractOwnAndChildProgress ====================

func TestExtractOwnAndChildProgress(t *testing.T) {
	tests := []struct {
		name          string
		flat          []string
		wantOwn       string
		wantChildLen  int
		wantChildRole string // first child role (if any)
	}{
		{
			"own lines only",
			[]string{"💭 思考中...", "⏳ Shell(ls)"},
			"⏳ Shell(ls)", 0, "",
		},
		{
			"tree lines only",
			[]string{"├─ 🔄 backend: ⏳ ls", "├─ ✅ qa:"},
			"", 2, "backend",
		},
		{
			"quoted child lines (actual runtime format, with indent)",
			[]string{"> 🔄 backend: ⏳ Shell(go version)", "> ✅ qa:"},
			"", 2, "backend",
		},
		{
			"own + tree children",
			[]string{"分派三个子代理", "├─ 🔄 backend: ⏳ ls", "├─ ✅ qa:"},
			"分派三个子代理", 2, "backend",
		},
		{
			"own + quoted children (actual runtime format, with indent)",
			[]string{"分派三个子代理并行执行", "> 🔄 backend: ⏳ Shell(go version)", "> ✅ qa:"},
			"分派三个子代理并行执行", 2, "backend",
		},
		{
			"mixed: own + quoted children + deep quoted lines",
			[]string{
				"三部执行中",
				"> 🔄 backend: ⏳ Shell(go version)",
				"> ✅ qa:",
				"> 💭 思考中...", // emoji status line without colon → filtered out
			},
			"三部执行中", 2, "backend",
		},
		{
			"quoted progress status filtered out (no role:colon format)",
			[]string{"> 💭 思考中...", "> ⏳ Shell(ls)"},
			"", 0, "",
		},
		{
			"multiline own content",
			[]string{"【状态回报】判定：🟢 直接执行\n理由：任务清晰\n→ coordinator"},
			"→ coordinator", 0, "",
		},
		{
			"multiline with children mixed in",
			[]string{
				"【状态回报】判定：🟢 直接执行\n理由：任务清晰\n→ coordinator",
				"> 🔄 coordinator: 分派三个子代理",
			},
			"→ coordinator", 1, "coordinator",
		},
		{
			"tool completion lines ignored (no indent, looks like sub-agent but is tool)",
			[]string{
				"调度中",
				"> ✅ Shell: go version (508ms)",
				"> ✅ Read: /workspace/xbot/agent/progress.go (120ms)",
			},
			"调度中", 0, "",
		},
		{
			"tool lines ignored, real child agents with indent preserved",
			[]string{
				"分派中",
				"> ✅ Shell: go version (508ms)",
				"> 🔄 coordinator: 分派三个子代理",
				"> 　　🔄 backend: ⏳ Shell(ls)",
			},
			"分派中", 1, "coordinator",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flat := flattenLines(tt.flat)
			own, children := extractOwnAndChildProgress(flat)
			if own != tt.wantOwn {
				t.Errorf("own = %q, want %q", own, tt.wantOwn)
			}
			if len(children) != tt.wantChildLen {
				t.Errorf("children len = %d, want %d", len(children), tt.wantChildLen)
			}
			if tt.wantChildLen > 0 && tt.wantChildRole != "" && children[0].Role != tt.wantChildRole {
				t.Errorf("first child role = %q, want %q", children[0].Role, tt.wantChildRole)
			}
		})
	}
}

// ==================== formatSubAgentProgress 主测试 ====================

func TestFormatSubAgentProgress(t *testing.T) {
	tests := []struct {
		name   string
		detail SubAgentProgressDetail
		want   string
	}{
		// === 基础场景 ===
		{
			name: "single line thinking",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead"},
				Lines: []string{"💭 思考中..."},
				Depth: 2,
			},
			want: "> 🔄 lead: 💭 思考中...",
		},
		{
			name: "single line tool progress",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead"},
				Lines: []string{"⏳ Shell(ls) ..."},
				Depth: 2,
			},
			want: "> 🔄 lead: ⏳ Shell(ls) ...",
		},
		{
			name: "no content (empty lines) — should show running, not done",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead"},
				Lines: []string{""},
				Depth: 2,
			},
			want: "> 🔄 lead:",
		},
		{
			name: "no content (nil lines) — should show running, not done",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead"},
				Lines: nil,
				Depth: 2,
			},
			want: "> 🔄 lead:",
		},
		{
			name: "empty path with content",
			detail: SubAgentProgressDetail{
				Path:  nil,
				Lines: []string{"some progress"},
				Depth: 2,
			},
			want: "> 🔄 : some progress",
		},
		{
			name: "empty path no content — should show running, not done",
			detail: SubAgentProgressDetail{
				Path:  nil,
				Lines: nil,
				Depth: 2,
			},
			want: "> 🔄 :",
		},
		{
			name: "path without slash",
			detail: SubAgentProgressDetail{
				Path:  []string{"simple-role"},
				Lines: []string{"working"},
				Depth: 2,
			},
			want: "> 🔄 simple-role: working",
		},
		// === 多行内容 ===
		{
			name: "multi line content - takes last non-empty",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead"},
				Lines: []string{"💭 思考中...", "⏳ Shell(ls) ...", "⏳ Shell(go test) ..."},
				Depth: 2,
			},
			want: "> 🔄 lead: ⏳ Shell(go test) ...",
		},
		{
			name: "multiline content with newlines in single element",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead"},
				Lines: []string{"【状态回报】指令：启动三层测试\n判定：🟢 直接执行\n→ coordinator"},
				Depth: 2,
			},
			want: "> 🔄 lead: → coordinator",
		},
		{
			name: "double quote prefix cleanup — no own content after cleanup",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead"},
				Lines: []string{"> > ⏳ Shell(go test) ..."},
				Depth: 2,
			},
			want: "> 🔄 lead:",
		},
		// === 深度缩进 ===
		{
			name: "depth 3 multi line with indent",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead", "main/lead/backend"},
				Lines: []string{"💭 审计中...", "⏳ Shell(go test) ..."},
				Depth: 3,
			},
			want: "> 　🔄 backend: ⏳ Shell(go test) ...",
		},
		{
			name: "depth 3 no content — should show running, not done",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead", "main/lead/backend"},
				Lines: []string{""},
				Depth: 3,
			},
			want: "> 　🔄 backend:",
		},
		{
			name: "depth 4 multi line",
			detail: SubAgentProgressDetail{
				Path:  []string{"a/b", "a/b/c", "a/b/c/d"},
				Lines: []string{"💭 运行测试...", "✅ Shell(go test) (1.2s)"},
				Depth: 4,
			},
			want: "> 　　🔄 d: ✅ Shell(go test) (1.2s)",
		},
		// === 子 Agent 并发摘要 → 多行缩进树 ===
		{
			name: "own + 3 child agents tree format",
			detail: SubAgentProgressDetail{
				Path: []string{"main/lead"},
				Lines: []string{
					"→ coordinator并发派发三部",
					"├─ 🔄 backend: ⏳ Shell(go version)",
					"├─ ✅ qa:",
					"├─ 🔄 docs: 💭 思考中",
				},
				Depth: 2,
			},
			want: "> 🔄 lead: → coordinator并发派发三部\n" +
				"> 　🔄 backend: ⏳ Shell(go version)\n" +
				"> 　✅ qa:\n" +
				"> 　🔄 docs: 💭 思考中",
		},
		{
			name: "own + all children completed tree format",
			detail: SubAgentProgressDetail{
				Path: []string{"main/coordinator"},
				Lines: []string{
					"三部任务已分派完毕",
					"├─ ✅ backend:",
					"├─ ✅ qa:",
					"├─ ✅ docs:",
				},
				Depth: 2,
			},
			want: "> 🔄 coordinator: 三部任务已分派完毕\n" +
				"> 　✅ backend:\n" +
				"> 　✅ qa:\n" +
				"> 　✅ docs:",
		},
		{
			name: "only child progress no own tree format",
			detail: SubAgentProgressDetail{
				Path: []string{"main/coordinator"},
				Lines: []string{
					"├─ 🔄 backend: ⏳ Shell(ls)",
					"├─ ✅ qa:",
				},
				Depth: 2,
			},
			want: "> 🔄 coordinator:\n" +
				"> 　🔄 backend: ⏳ Shell(ls)\n" +
				"> 　✅ qa:",
		},
		{
			name: "child with failure tree format",
			detail: SubAgentProgressDetail{
				Path: []string{"main/coordinator"},
				Lines: []string{
					"三部执行中",
					"├─ ✅ backend:",
					"├─ ❌ qa: Error: test failed",
					"├─ 🔄 docs: ⏳ running",
				},
				Depth: 2,
			},
			want: "> 🔄 coordinator: 三部执行中\n" +
				"> 　✅ backend:\n" +
				"> 　❌ qa: Error: test failed\n" +
				"> 　🔄 docs: ⏳ running",
		},
		// === 子 Agent（引用格式 - 实际运行时穿透）===
		{
			name: "own + quoted child agents (actual runtime format)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/lead"},
				Lines: []string{
					"→ coordinator并发派发三部",
					"> 🔄 coordinator: ⏳ SubAgent [backend]...",
					"> 🔄 coordinator: ⏳ SubAgent [qa]...",
				},
				Depth: 2,
			},
			want: "> 🔄 lead: → coordinator并发派发三部\n" +
				"> 　🔄 coordinator: ⏳ SubAgent [backend]...\n" +
				"> 　🔄 coordinator: ⏳ SubAgent [qa]...",
		},
		{
			name: "quoted children completed (actual runtime format)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/coordinator"},
				Lines: []string{
					"三部全部完成",
					"> ✅ backend:",
					"> ✅ qa:",
					"> ✅ docs:",
				},
				Depth: 2,
			},
			want: "> 🔄 coordinator: 三部全部完成\n" +
				"> 　✅ backend:\n" +
				"> 　✅ qa:\n" +
				"> 　✅ docs:",
		},
		{
			name: "quoted children mixed (actual runtime format)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/coordinator"},
				Lines: []string{
					"分派三个子代理并行执行",
					"> 　🔄 backend: ⏳ Shell(go version) ...",
					"> 　✅ qa: ✅ Shell(go version) (4.66s)",
					"> 　🔄 docs: 💭 思考中...",
				},
				Depth: 3,
			},
			want: "> 　🔄 coordinator: 分派三个子代理并行执行\n" +
				"> 　　🔄 backend: ⏳ Shell(go version) ...\n" +
				"> 　　✅ qa: ✅ Shell(go version) (4.66s)\n" +
				"> 　　🔄 docs: 💭 思考中...",
		},
		// === 实际运行时场景：SubAgent 占位行 + emoji 状态行 ===
		{
			name: "SubAgent placeholder lines recognized as children",
			detail: SubAgentProgressDetail{
				Path: []string{"main/lead"},
				Lines: []string{
					"臣即刻将任务派发给coordinator",
					"> ⏳ SubAgent [coordinator]: 【coordinator·接旨】三层并发测试",
				},
				Depth: 2,
			},
			want: "> 🔄 lead: 臣即刻将任务派发给coordinator\n" +
				"> 　⏳ coordinator: 【coordinator·接旨】三层并发测试",
		},
		{
			name: "SubAgent placeholder with progress status lines (filtered)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/lead"},
				Lines: []string{
					"分析任务中",
					"> 💭 思考中...",
					"> ⏳ Shell(ls -la)",
					"> 📦 压缩中...",
				},
				Depth: 2,
			},
			want: "> 🔄 lead: 分析任务中",
		},
		{
			name: "mixed: own text + SubAgent placeholders + emoji status",
			detail: SubAgentProgressDetail{
				Path: []string{"main/coordinator"},
				Lines: []string{
					"分派三个子代理",
					"> ⏳ SubAgent [backend]: backend",
					"> ⏳ SubAgent [qa]: qa",
					"> ⏳ SubAgent [docs]: docs",
					"> 💭 思考中...",
				},
				Depth: 3,
			},
			want: "> 　🔄 coordinator: 分派三个子代理\n" +
				"> 　　⏳ backend: backend\n" +
				"> 　　⏳ qa: qa\n" +
				"> 　　⏳ docs: docs",
		},
		// === lead多层穿透场景 ===
		{
			name: "lead多层穿透 - 有子Agent (quoted format)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/lead"},
				Lines: []string{
					"【状态回报】判定：🟢 直接执行 → coordinator\n理由：明确的调度测试任务\n臣这就调度coordinator",
					"> 🔄 coordinator: → 🔄 backend(⏳ls) · ✅ qa",
				},
				Depth: 2,
			},
			want: "> 🔄 lead: 臣这就调度coordinator\n" +
				"> 　🔄 coordinator: → 🔄 backend(⏳ls) · ✅ qa",
		},
		// === 混合引用前缀 + 树状行 ===
		{
			name: "quoted lines kept as children, own + tree kept",
			detail: SubAgentProgressDetail{
				Path: []string{"main/lead"},
				Lines: []string{
					"> 💭 思考中...",         // emoji status line → kept as child
					"> ⏳ Shell(ls)",      // emoji status line → kept as child
					"├─ 🔄 backend: ⏳ ls", // 树状行 → 子Agent
					"├─ ✅ qa:",           // 树状行 → 子Agent
				},
				Depth: 2,
			},
			want: "> 🔄 lead:\n" +
				"> 　🔄 backend: ⏳ ls\n" +
				"> 　✅ qa:",
		},
		// === 长文本截断 ===
		{
			name: "long content truncated",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/lead"},
				Lines: []string{"这是一段非常非常非常非常非常非常非常非常非常非常长的进度文本用来测试截断功能是否正常工作"},
				Depth: 2,
			},
			want: "> 🔄 lead: 这是一段非常非常非常非常非常非常非常非常非常非常长的进度文本用来测试截断功能是否正常工作",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSubAgentProgress(tt.detail)
			if got != tt.want {
				t.Errorf("formatSubAgentProgress() =\n  got: %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestSubAgentSessionKeyConversion(t *testing.T) {
	const parentKey = "cli:chat-1/reviewer:review-1"
	const childKey = "cli:chat-1/fixer:fix-1"
	detail := SubAgentProgressDetail{
		Path:       []string{"main/reviewer"},
		Instance:   "review-1",
		SessionKey: parentKey,
	}
	nodes := extractSubAgentNodesFromDetail(detail)
	if len(nodes) != 1 || nodes[0].SessionKey != parentKey {
		t.Fatalf("extracted nodes = %#v, want session key %q", nodes, parentKey)
	}
	nodes[0].Children = []SubAgentNode{{
		Role:       "fixer",
		Instance:   "fix-1",
		SessionKey: childKey,
		Status:     "running",
	}}

	converted := convertCLISubAgentTree(nodes)
	if len(converted) != 1 || converted[0].SessionKey != parentKey {
		t.Fatalf("conversion = %#v, want parent key %q", converted, parentKey)
	}
	if len(converted[0].Children) != 1 || converted[0].Children[0].SessionKey != childKey {
		t.Fatalf("nested conversion = %#v, want child key %q", converted, childKey)
	}
}

// ==================== 输出格式验证测试 ====================

func TestFormatSubAgentProgress_LeafIsSingleLine(t *testing.T) {
	leafDetails := []SubAgentProgressDetail{
		{
			Path:  []string{"main/lead"},
			Lines: []string{"💭 thinking", "⏳ Shell(ls)", "done"},
			Depth: 2,
		},
		{
			Path:  []string{"main/lead"},
			Lines: nil,
			Depth: 2,
		},
		{
			Path:  []string{"a", "a/b"},
			Lines: []string{"working"},
			Depth: 3,
		},
	}
	for i, detail := range leafDetails {
		t.Run(fmt.Sprintf("leaf_%d", i), func(t *testing.T) {
			got := formatSubAgentProgress(detail)
			if strings.Contains(got, "\n") {
				t.Errorf("leaf node output should be single line: %q", got)
			}
		})
	}
}

func TestFormatSubAgentProgress_TreeAllLinesQuoted(t *testing.T) {
	treeDetails := []SubAgentProgressDetail{
		{
			Path: []string{"main/coordinator"},
			Lines: []string{
				"分派三个子代理",
				"├─ 🔄 backend: ⏳ Shell(go version)",
				"├─ ✅ qa:",
			},
			Depth: 2,
		},
		{
			Path: []string{"main/coordinator"},
			Lines: []string{
				"> 🔄 backend: ⏳ Shell(go version)",
				"> ✅ qa:",
			},
			Depth: 3,
		},
	}
	for i, detail := range treeDetails {
		t.Run(fmt.Sprintf("tree_%d", i), func(t *testing.T) {
			got := formatSubAgentProgress(detail)
			for j, line := range strings.Split(got, "\n") {
				if !strings.HasPrefix(line, "> ") {
					t.Errorf("line %d does not start with '> ': %q", j, line)
				}
			}
		})
	}
}

func TestFormatSubAgentProgress_StartsWithQuote(t *testing.T) {
	testDetails := []SubAgentProgressDetail{
		{Path: []string{"a"}, Lines: []string{"x"}, Depth: 2},
		{Path: []string{"a"}, Lines: nil, Depth: 2},
		{Path: []string{"a", "a/b"}, Lines: []string{"y"}, Depth: 3},
		{
			Path: []string{"a"}, Lines: []string{
				"own", "> 🔄 child: desc",
			}, Depth: 2,
		},
	}
	for i, detail := range testDetails {
		t.Run(fmt.Sprintf("quote_prefix_%d", i), func(t *testing.T) {
			got := formatSubAgentProgress(detail)
			if !strings.HasPrefix(got, "> ") {
				t.Errorf("output does not start with '> ': %q", got)
			}
		})
	}
}

// ==================== 真实三层嵌套场景模拟 ====================

func TestFormatSubAgentProgress_ThreeLayerScenario(t *testing.T) {
	// 模拟三层并发场景的实际数据流：
	// L1: 主Agent (owner)
	// L2: lead (lead) → 调度coordinator
	// L3: coordinator (coordinator) → 并发派发三部

	// 场景1: coordinator正在并发执行三部（实际运行时引用格式穿透）→ 多行树
	t.Run("coordinator concurrent execution", func(t *testing.T) {
		detail := SubAgentProgressDetail{
			Path: []string{"main/lead", "main/lead/coordinator"},
			Lines: []string{
				"分派三个子代理并行执行",
				"> 　🔄 backend: ⏳ Shell(go version) ...",
				"> 　✅ qa:",
				"> 　🔄 docs: 💭 思考中...",
			},
			Depth: 3,
		}
		got := formatSubAgentProgress(detail)
		// 验证: 多行树、带缩进、包含三个子Agent状态
		if !strings.Contains(got, "　") {
			t.Errorf("should have fullwidth indent for depth=3: %q", got)
		}
		for _, agent := range []string{"backend", "qa", "docs"} {
			if !strings.Contains(got, agent) {
				t.Errorf("should contain child agent %q: %q", agent, got)
			}
		}
		// 验证: 所有行以 "> " 开头
		for i, line := range strings.Split(got, "\n") {
			if !strings.HasPrefix(line, "> ") {
				t.Errorf("line %d should start with '> ': %q", i, line)
			}
		}
	})

	// 场景2: lead收到coordinator的穿透进度（含内联子Agent描述）→ 多行树
	t.Run("lead receives coordinator penetration", func(t *testing.T) {
		detail := SubAgentProgressDetail{
			Path: []string{"main/lead"},
			Lines: []string{
				"【状态回报】判定：🟢 直接执行 → coordinator",
				"> 🔄 coordinator: 分派三个子代理并行执行 → 🔄 backend(⏳ Shell(go…) · ✅ qa · 🔄 docs(💭)",
			},
			Depth: 2,
		}
		got := formatSubAgentProgress(detail)
		if !strings.Contains(got, "coordinator") {
			t.Errorf("should identify coordinator as child agent: %q", got)
		}
		if !strings.HasPrefix(got, "> ") {
			t.Errorf("should start with '> ': %q", got)
		}
	})

	// 场景3: coordinator所有子Agent完成 → 多行树展示完成状态
	t.Run("coordinator all children done", func(t *testing.T) {
		detail := SubAgentProgressDetail{
			Path: []string{"main/lead", "main/lead/coordinator"},
			Lines: []string{
				"三部全部完成，汇总结果",
				"> 　✅ backend:",
				"> 　✅ qa:",
				"> 　✅ docs:",
			},
			Depth: 3,
		}
		got := formatSubAgentProgress(detail)
		for _, agent := range []string{"backend", "qa", "docs"} {
			if !strings.Contains(got, "✅ "+agent) {
				t.Errorf("should show completed %q: %q", agent, got)
			}
		}
	})
}

// ==================== 新增：树状渲染与嵌套解析测试 ====================

func TestRenderChildrenTree(t *testing.T) {
	t.Run("single leaf child", func(t *testing.T) {
		children := []childAgentStatus{
			{Role: "backend", Status: "🔄", Desc: "⚡ Shell(ls)"},
		}
		lines := renderChildrenTree(children, "", 0)
		if len(lines) != 1 {
			t.Fatalf("expected 1 line, got %d: %v", len(lines), lines)
		}
		if lines[0] != "> 　🔄 backend: ⚡ Shell(ls)" {
			t.Errorf("got %q", lines[0])
		}
	})

	t.Run("multiple leaf children", func(t *testing.T) {
		children := []childAgentStatus{
			{Role: "backend", Status: "🔄", Desc: "⚡ Shell(ls)"},
			{Role: "qa", Status: "✅"},
			{Role: "docs", Status: "🔄", Desc: "💭 思考中"},
		}
		lines := renderChildrenTree(children, "", 0)
		if len(lines) != 3 {
			t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
		}
		if lines[1] != "> 　✅ qa:" {
			t.Errorf("completed child got %q", lines[1])
		}
	})

	t.Run("child with nested children", func(t *testing.T) {
		children := []childAgentStatus{
			{
				Role: "coordinator", Status: "🔄", Desc: "分派两个子代理",
				Children: []childAgentStatus{
					{Role: "backend", Status: "🔄", Desc: "⚡ Shell(ls)"},
					{Role: "qa", Status: "✅"},
				},
			},
		}
		lines := renderChildrenTree(children, "", 0)
		if len(lines) != 3 {
			t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
		}
		// coordinator indent:
		if !strings.HasPrefix(lines[0], "> 　🔄 coordinator:") {
			t.Errorf("parent line: %q", lines[0])
		}
		// backend indent:
		if !strings.HasPrefix(lines[1], "> 　　🔄 backend:") {
			t.Errorf("child line: %q", lines[1])
		}
	})

	t.Run("stats fallback for many children", func(t *testing.T) {
		var many []childAgentStatus
		for i := 0; i < 8; i++ {
			many = append(many, childAgentStatus{Role: fmt.Sprintf("agent-%d", i), Status: "🔄"})
		}
		lines := renderChildrenTree(many, "", 0)
		if len(lines) != 1 {
			t.Fatalf("expected 1 summary line, got %d: %v", len(lines), lines)
		}
		if !strings.Contains(lines[0], "🔄×8") {
			t.Errorf("should show stats: %q", lines[0])
		}
	})
}

func TestBuildChildTree(t *testing.T) {
	t.Run("flat same depth", func(t *testing.T) {
		items := []indexedChild{
			{depth: 0, child: childAgentStatus{Role: "A", Status: "🔄"}},
			{depth: 0, child: childAgentStatus{Role: "B", Status: "✅"}},
		}
		tree := buildChildTree(items)
		if len(tree) != 2 {
			t.Fatalf("expected 2 children, got %d", len(tree))
		}
		if len(tree[0].Children) != 0 || len(tree[1].Children) != 0 {
			t.Error("flat items should have no Children")
		}
	})

	t.Run("nested two levels", func(t *testing.T) {
		items := []indexedChild{
			{depth: 0, child: childAgentStatus{Role: "A", Status: "🔄"}},
			{depth: 1, child: childAgentStatus{Role: "A1", Status: "🔄"}},
			{depth: 1, child: childAgentStatus{Role: "A2", Status: "✅"}},
			{depth: 0, child: childAgentStatus{Role: "B", Status: "✅"}},
		}
		tree := buildChildTree(items)
		if len(tree) != 2 {
			t.Fatalf("expected 2 top-level, got %d", len(tree))
		}
		if tree[0].Role != "A" || len(tree[0].Children) != 2 {
			t.Errorf("A should have 2 children, got %d", len(tree[0].Children))
		}
		if tree[1].Role != "B" || len(tree[1].Children) != 0 {
			t.Errorf("B should be leaf, got %d children", len(tree[1].Children))
		}
	})

	t.Run("three levels", func(t *testing.T) {
		items := []indexedChild{
			{depth: 0, child: childAgentStatus{Role: "lead", Status: "🔄"}},
			{depth: 1, child: childAgentStatus{Role: "coordinator", Status: "🔄"}},
			{depth: 2, child: childAgentStatus{Role: "backend", Status: "🔄"}},
			{depth: 2, child: childAgentStatus{Role: "qa", Status: "✅"}},
		}
		tree := buildChildTree(items)
		if len(tree) != 1 {
			t.Fatalf("expected 1 top-level, got %d", len(tree))
		}
		if len(tree[0].Children) != 1 {
			t.Fatalf("lead should have 1 child, got %d", len(tree[0].Children))
		}
		if len(tree[0].Children[0].Children) != 2 {
			t.Fatalf("尚書 should have 2 children, got %d", len(tree[0].Children[0].Children))
		}
	})
}

func TestCountFullWidthIndent(t *testing.T) {
	tests := []struct {
		line string
		want int
	}{
		{"> 🔄 role: desc", 0},
		{"> 　🔄 role: desc", 1},
		{"> 　　🔄 role: desc", 2},
		{"　🔄 role: desc", 1},
		{"├─ 🔄 role: desc", 0},
		{"> 　　　✅ role:", 3},
		{"no prefix", 0},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := countFullWidthIndent(tt.line)
			if got != tt.want {
				t.Errorf("countFullWidthIndent(%q) = %d, want %d", tt.line, got, tt.want)
			}
		})
	}
}

func TestExtractOwnAndChildProgress_Nested(t *testing.T) {
	t.Run("two-level nesting from runtime format", func(t *testing.T) {
		flat := flattenLines([]string{
			"调度中",
			"> 　🔄 中书: 💭 思考中",
			"> 　🔄 coordinator: 分派两个子代理",
			"> 　　🔄 backend: ⚡ Shell(ls)",
			"> 　　✅ qa:",
		})
		own, children := extractOwnAndChildProgress(flat)
		if own != "调度中" {
			t.Errorf("own = %q, want %q", own, "调度中")
		}
		if len(children) != 2 {
			t.Fatalf("expected 2 direct children, got %d", len(children))
		}
		if children[0].Role != "中书" || len(children[0].Children) != 0 {
			t.Errorf("中书 should be leaf: %+v", children[0])
		}
		if children[1].Role != "coordinator" || len(children[1].Children) != 2 {
			t.Errorf("尚書 should have 2 children: %+v", children[1])
		}
		if children[1].Children[0].Role != "backend" {
			t.Errorf("first grandchild should be backend: %+v", children[1].Children[0])
		}
	})
}

func TestFormatSubAgentProgress_FullTreeScenario(t *testing.T) {
	// 子 Agent 穿透行带有全角空格缩进（renderChildrenTree 的 childIndent）
	// 模拟完整三层场景: 主Agent → lead → 中书(leaf) + coordinator → backend + qa
	// lead的进度文本（从lead的 notifyProgress 输出）
	detail := SubAgentProgressDetail{
		Path: []string{"main/lead"},
		Lines: []string{
			"臣调度coordinator",
			"> 　🔄 中书: 💭 思考中",
			"> 　🔄 coordinator: 分派两个子代理",
			"> 　　🔄 backend: ⚡ Shell(ls)",
			"> 　　✅ qa:",
		},
		Depth: 2,
	}
	got := formatSubAgentProgress(detail)

	// 根行
	if !strings.HasPrefix(got, "> 🔄 lead: 臣调度coordinator\n") {
		t.Errorf("root line wrong: %q", got)
	}

	lines := strings.Split(got, "\n")
	// 应有 5 行: root + 中书 + coordinator + backend + qa
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d:\n%s", len(lines), got)
	}

	// 中书 (depth 1: 　)
	if !strings.Contains(lines[1], "　🔄 中书:") {
		t.Errorf("line 1 (中书): %q", lines[1])
	}
	// coordinator (depth 1: 　)
	if !strings.Contains(lines[2], "　🔄 coordinator:") {
		t.Errorf("line 2 (尚書): %q", lines[2])
	}
	// backend (depth 2: 　　)
	if !strings.Contains(lines[3], "　　🔄 backend:") {
		t.Errorf("line 3 (backend): %q", lines[3])
	}
	// qa (depth 2: 　　)
	if !strings.Contains(lines[4], "　　✅ qa:") {
		t.Errorf("line 4 (qa): %q", lines[4])
	}
}

// TestFormatToolProgress_SubAgent_InstanceDistinguishesLabel verifies that
// the SubAgent tool progress label includes the instance parameter so that
// parallel SubAgent calls with the same role+task get unique labels and are
// not incorrectly deduplicated by liveIterationBlocks.
func TestFormatToolProgress_SubAgent_InstanceDistinguishesLabel(t *testing.T) {
	// Three parallel SubAgent calls, all with role=explore and same task,
	// but different instances. Without instance in the label, all three
	// would share the same key and get deduplicated to one.
	label1 := formatToolProgress("SubAgent", `{"role":"explore","task":"Investigate crash","instance":"debug-1"}`)
	label2 := formatToolProgress("SubAgent", `{"role":"explore","task":"Investigate crash","instance":"debug-2"}`)
	label3 := formatToolProgress("SubAgent", `{"role":"explore","task":"Investigate crash","instance":"debug-3"}`)

	// All three must have unique labels
	if label1 == label2 {
		t.Errorf("label1 and label2 are identical: %q — instance not included", label1)
	}
	if label1 == label3 {
		t.Errorf("label1 and label3 are identical: %q — instance not included", label1)
	}
	if label2 == label3 {
		t.Errorf("label2 and label3 are identical: %q — instance not included", label2)
	}

	// Verify instances appear in the labels
	if !strings.Contains(label1, "debug-1") {
		t.Errorf("label1 missing instance: %q", label1)
	}
	if !strings.Contains(label2, "debug-2") {
		t.Errorf("label2 missing instance: %q", label2)
	}
	if !strings.Contains(label3, "debug-3") {
		t.Errorf("label3 missing instance: %q", label3)
	}

	// Backward compat: SubAgent without instance should still work
	labelNoInst := formatToolProgress("SubAgent", `{"role":"explore","task":"Investigate crash"}`)
	if !strings.Contains(labelNoInst, "explore") {
		t.Errorf("label without instance should still include role: %q", labelNoInst)
	}
}
