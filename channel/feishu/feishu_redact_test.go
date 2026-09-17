package feishu

// feishu_redact_test.go — 飞书工具脱敏契约（用户 2026-09-17：「飞书加一个工具脱敏
// 功能，自动去掉所有敏感内容」）。外发面 = CoT 的 title/args/result + 卡片的
// 工具面板；脱敏必须在这些出口统一生效，普通内容（路径/代码/中文）原样保留。

import "testing"

func TestRedactSensitive(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"shell 风格 kv", "cd /tmp && GH_TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefgh12 make", "cd /tmp && GH_TOKEN=*** make"},
		{"JSON 风格 kv", `{"api_key": "sk-abc123XYZhere", "path": "/tmp/a.py"}`, `{"api_key": "***", "path": "/tmp/a.py"}`},
		{"JSON 紧凑", `{"apikey":"supersecret9"}`, `{"apikey":"***"}`},
		{"password 等号", "connect password=hunter2 db", "connect password=*** db"},
		{"URL query", "curl https://api.x.com/v1?token=abc123&page=2", "curl https://api.x.com/v1?token=***&page=2"},
		{"Bearer JWT", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig123456", "Authorization: Bearer ***"},
		{"ghp_ 格式化 token", "git push with ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefgh12 ok", "git push with *** ok"},
		{"sk- 格式化 token", "openai sk-proj-abcdefgh123456789 done", "openai *** done"},
		{"AWS AKIA", "aws s3 cp --access-key AKIAIOSFODNN7EXAMPLE f", "aws s3 cp --access-key *** f"},
		{"flag 空格分隔", "svc start --secret-key abcdef123456 else", "svc start --secret-key *** else"},
		{"多行输出里的密码", "line1\nDB_PASSWORD=Sup3rS3cret!\nline3", "line1\nDB_PASSWORD=***\nline3"},
		{"数字值不掩码", "total_tokens=5", "total_tokens=5"},
		{"短值不掩码", "token=abc", "token=abc"},
		{"Bearer 词不算值", "Authorization: Bearer abcdef1234567890", "Authorization: Bearer ***"},
		{"普通参数不动", `{"path":"/tmp/sz_weather.py","content":"print('hello 世界')"}`, `{"path":"/tmp/sz_weather.py","content":"print('hello 世界')"}`},
		{"普通命令不动", "sed -n '1,44p' /tmp/ems_weather.py", "sed -n '1,44p' /tmp/ems_weather.py"},
		{"空串", "", ""},
	}
	for _, c := range cases {
		if got := redactSensitive(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
