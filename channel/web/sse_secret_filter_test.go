package web

// sse_secret_filter_test.go — SSE 出站过滤契约（2026-11-07 用户裁定）。
//
// 三条硬边界，任一破坏即红：
//  1. 固定格式凭据（真实长度/字符集）必须打码 —— 防工具输出意外泄露
//  2. 近似但不合格的串（长度不足、普通词、路径、字段名、含 sk- 子串的普通单词）
//     绝不能打码 —— 误杀一律算 bug（用户：「这种问题一次都不能有」）
//  3. 模型侧字段（Content/Reasoning/Args/Label）与入参对象绝不能被改

import (
	"testing"

	"xbot/protocol"
)

func TestRedactFixedFormats(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// —— 必须打码（真实固定格式）——
		{"GitHub PAT classic", "token ghp_" + "a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8" + " ok",
			"token *** ok"},
		{"GitHub fine-grained", "x github_pat_" + "11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOP" + " y",
			"x *** y"},
		{"GitHub oauth", "gho_" + "a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8" + "",
			"***"},
		{"OpenAI style", "sk-" + "abcdefghijklmnopqrstuvwxyz0123456789ABCD" + "",
			"***"},
		{"Anthropic style", "sk-ant-" + "api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEF" + "",
			"***"},
		{"AWS AKIA", "key AKIA" + "IOSFODNN7EXAMPLE" + " end",
			"key *** end"},
		{"Slack", "xoxb-" + "123456789012-abcdefghij" + "",
			"***"},
		{"Google API key", "AIza" + "SyA1234567890abcdefghijklmnopqrstuv" + "",
			"***"},
		{"GitLab PAT", "glpat-" + "abcdefghij0123456789" + "",
			"***"},
		{"JWT", "Authorization: Bearer eyJ" + "hbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0." + "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U" + "",
			"Authorization: Bearer ***"},

		// —— 绝不能打码（近似但非凭据 / 普通内容）——
		{"短 ghp_ 不是 token", "ghp_short", "ghp_short"},
		{"ghp_ 长度不足 36", "ghp_" + "abc123", "ghp_" + "abc123"},
		{"AKIA 长度不足", "AKIA1234", "AKIA1234"},
		{"普通路径", "/home/smith/src/xbot/channel/web", "/home/smith/src/xbot/channel/web"},
		{"字段名", "num_tokens=16384 max_tokens=8192 total_tokens=1711", "num_tokens=16384 max_tokens=8192 total_tokens=1711"},
		{"命令 pwd", "pwd: ***", "pwd: ***"},
		{"函数名", "verifyAgentHS signature_ok", "verifyAgentHS signature_ok"},
		{"sk- 太短", "sk-abc", "sk-abc"},
		{"中文与代码", "编译通过，rc=0；file:line 指向 serial_runtime.py:1037", "编译通过，rc=0；file:line 指向 serial_runtime.py:1037"},

		// —— sk- 子串误杀回归（2026-11-07 实锤：$(cat /tmp/task-...) 被替换成 ***）——
		{"task- 长 ID 不是 key", "$(cat /tmp/task-20261107-abcdefghijklmnopqrstuvwxyz012345)",
			"$(cat /tmp/task-20261107-abcdefghijklmnopqrstuvwxyz012345)"},
		{"disk- 长 ID", "/mnt/disk-abcdefghijklmnopqrstuvwxyz0123456789/bench",
			"/mnt/disk-abcdefghijklmnopqrstuvwxyz0123456789/bench"},
		{"mask- 长 ID", "mask-abc123def456ghi789jkl012mno345", "mask-abc123def456ghi789jkl012mno345"},
		{"risk_ 长变量", "risk_score_abcdefghijklmnopqrstuvwxyz0123", "risk_score_abcdefghijklmnopqrstuvwxyz0123"},
		{"顶部裸 sk- key 仍打码", "sk-" + "abcdefghijklmnopqrstuvwxyz0123456789" + "", "***"},
		{"空格后的 sk- key 仍打码", "export OPENAI_KEY= sk-" + "abcdefghijklmnopqrstuvwxyz0123456789" + "",
			"export OPENAI_KEY= ***"},
	}
	for _, c := range cases {
		if got := protocol.RedactFixedFormats(c.in); got != c.want {
			t.Errorf("[%s]\n in  = %q\n got = %q\n want= %q", c.name, c.in, got, c.want)
		}
	}
}

func TestFilterProgressSecrets_OnlyToolOutputAndNoMutation(t *testing.T) {
	realKey := "ghp_" + "a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8"
	orig := &protocol.ProgressEvent{
		Content:       "模型自己说：这个 key 是 " + realKey, // 模型文本：不碰
		Reasoning:     "reasoning 里提到 " + realKey,  // 推理：不碰
		StreamContent: "stream " + realKey,         // 流式文本：不碰
		ActiveTools: []protocol.ToolProgress{
			{Name: "shell", Label: "exec", Args: `{"cmd":"echo ` + realKey + `"}`,
				Summary: "输出: " + realKey, Detail: "raw " + realKey},
		},
	}
	got := filterProgressSecrets(orig)

	// 1) 模型侧字段原样
	if got.Content != orig.Content || got.Reasoning != orig.Reasoning || got.StreamContent != orig.StreamContent {
		t.Errorf("模型侧文本被改动: %q / %q / %q", got.Content, got.Reasoning, got.StreamContent)
	}
	// 2) Args / Label 原样（模型写的参数）
	if got.ActiveTools[0].Args != orig.ActiveTools[0].Args || got.ActiveTools[0].Label != orig.ActiveTools[0].Label {
		t.Errorf("Args/Label 被改动")
	}
	// 3) 工具输出物被打码
	if got.ActiveTools[0].Summary == orig.ActiveTools[0].Summary || got.ActiveTools[0].Detail == orig.ActiveTools[0].Detail {
		t.Errorf("工具输出未打码: summary=%q detail=%q", got.ActiveTools[0].Summary, got.ActiveTools[0].Detail)
	}
	// 4) 入参对象未被污染（深拷贝）
	if orig.ActiveTools[0].Summary != "输出: "+realKey || orig.ActiveTools[0].Detail != "raw "+realKey {
		t.Errorf("入参被污染（引擎侧/历史会拿到打码值）: %q / %q", orig.ActiveTools[0].Summary, orig.ActiveTools[0].Detail)
	}
}
