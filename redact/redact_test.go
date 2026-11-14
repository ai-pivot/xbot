package redact

// redact_test.go — 写入侧脱敏的判据（工部施工单 §二.3 / 兵部 ORDER §3）。
//
// 全部使用**假 key**（sk-BINGBUFAKE…），不含任何真实凭据。

import (
	"encoding/json"
	"strings"
	"testing"
)

// fakeKey 是兵部探针同款假 key（非真实凭据）。
const fakeKey = "sk-BINGBUFAKE0123456789abcdefghijklmnopqrstuvwxyz0123"

const probeMarker = "BINGBUPROBEMARKER7788"

// TestSecretsRemovesHighConfidenceForms 1) 假 key 三种形态经 Secrets 后不含原值。
func TestSecretsRemovesHighConfidenceForms(t *testing.T) {
	cases := map[string]string{
		"bare sk-":      "key 是 " + fakeKey + " 结束",
		"bearer":        "Authorization: Bearer " + fakeKey,
		"kv api_key":    "api_key=" + fakeKey,
		"kv 引号 json":    `{"api_key": "` + fakeKey + `"}`,
		"cli flag":      "--token " + fakeKey,
		"gh token":      "GITHUB_TOKEN=ghp_" + strings.Repeat("a1B2c3D4e5", 2),
		"github_pat":    "github_pat_" + strings.Repeat("A1b2C3d4_", 3),
		"slack":         "xoxb-" + strings.Repeat("1234567890", 2),
		"aws akia":      "AKIAIOSFODNN7EXAMPLE",
		"jwt":           "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"password kv":   "password=hunter2hunter2",
		"client secret": "client_secret: s3cr3t-value-0001",
	}
	for name, in := range cases {
		out := Secrets(in)
		if out == in {
			t.Errorf("%s: 未被脱敏，原样返回 %q", name, out)
		}
		if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
			t.Errorf("%s: AKIA 仍在输出里", name)
		}
		if strings.Contains(out, fakeKey) {
			t.Errorf("%s: 假 key 仍在输出里: %q", name, out)
		}
		if !strings.Contains(out, mask) {
			t.Errorf("%s: 输出里没有占位符 %q: %q", name, mask, out)
		}
	}
}

// TestSecretsKeepsProbeMarker 2) 标记词必须保留（脱敏不得把整条内容吞掉）。
func TestSecretsKeepsProbeMarker(t *testing.T) {
	in := "探针：" + probeMarker + " 假凭据，形如 Bearer " + fakeKey + " 和 api_key=" + fakeKey
	out := Secrets(in)
	if strings.Contains(out, fakeKey) {
		t.Fatalf("假 key 仍在输出里: %q", out)
	}
	if !strings.Contains(out, probeMarker) {
		t.Fatalf("标记词被吞掉: %q", out)
	}
}

// TestSecretsIdempotent 3) 幂等：每种形态与混合文本各测一次。
func TestSecretsIdempotent(t *testing.T) {
	inputs := []string{
		"key 是 " + fakeKey,
		"Authorization: Bearer " + fakeKey,
		"api_key=" + fakeKey,
		`{"api_key": "` + fakeKey + `"}`,
		"--token " + fakeKey,
		"AKIAIOSFODNN7EXAMPLE",
		"ghp_" + strings.Repeat("a1B2c3D4e5", 2),
		"混合：中文 " + probeMarker + " Bearer " + fakeKey + " , api_key=" + fakeKey + " , sk-learn , total_tokens=5",
		"已经是 " + mask + " 的文本",
	}
	for _, in := range inputs {
		once := Secrets(in)
		twice := Secrets(once)
		if once != twice {
			t.Errorf("不幂等:\n in=%q\n 1st=%q\n 2nd=%q", in, once, twice)
		}
	}
}

// TestSecretsLeavesBenignTextByteIdentical 4) 非凭据文本必须逐字节不变。
func TestSecretsLeavesBenignTextByteIdentical(t *testing.T) {
	benign := []string{
		"中文内容原样保留：今天的会议纪要，没有秘密。",
		"```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```",
		"sk-learn 是课程名，不是 key",
		"total_tokens=5 prompt_tokens=1234",
		"路径 /home/smith/.xbot/memory/122514/notes/fact_181735.md",
		"task-frontend-cancel-reason-consume", // 兵部点名的假阳性源
		"decision_181735.md / task-frontend-cancel-reason",
		"SELECT * FROM session_messages WHERE role = 'tool'",
		"Bearer",                // 字面量 Bearer
		"authorization: Bearer", // 值就是字面量 Bearer
		"signature 校验通过，返回 200",
		`{"secret":true,"token":null,"password":12.5}`,
		"",
		"   ",
	}
	for _, in := range benign {
		out := Secrets(in)
		if out != in {
			t.Errorf("非凭据文本被改写:\n in=%q\nout=%q", in, out)
		}
	}
}

// TestSecretsLeftBoundary 5) 左边界：前一个字符是字母 ⇒ 不认；是 = ⇒ 认。
func TestSecretsLeftBoundary(t *testing.T) {
	// 必须是 false（前一个是字母）
	for _, in := range []string{
		"ask-" + fakeKey[3:],
		"task-frontend-cancel-reason-consume",
		"xsk-" + fakeKey[3:],
		"9sk-" + fakeKey[3:],
		"_sk-" + fakeKey[3:],
	} {
		if out := Secrets(in); out != in {
			t.Errorf("左边界失效（%q 不该被脱敏）: %q", in, out)
		}
	}
	// 必须是 true（前一个是 = / 空格 / 行首 / 冒号 / 中文）
	for _, in := range []string{
		"x=" + fakeKey,
		" " + fakeKey,
		fakeKey,
		":" + fakeKey,
		"是：" + fakeKey,
	} {
		if out := Secrets(in); strings.Contains(out, fakeKey) {
			t.Errorf("左边界误杀（%q 应被脱敏）: %q", in, out)
		}
	}
}

// TestSecretsKeepsJSONValidWithShellQuote tool_arguments/tools 这类 JSON 列里常见
// 「JSON 字符串里嵌 shell 单引号」的形态：裸值分支若把结尾的 ' " 一起吞掉，落库的就是
// 非法 JSON。这里钉死「只吃凭据、不吃引号/反斜杠」。
func TestSecretsKeepsJSONValidWithShellQuote(t *testing.T) {
	in := `{"cmd":"curl -H 'api_key=` + fakeKey + `'"}` + "\n" +
		`{"env":"{\"Authorization\":\"Bearer ` + fakeKey + `\"}"}`
	out := Secrets(in)
	if strings.Contains(out, fakeKey) {
		t.Fatalf("仍含假 key: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("脱敏后不是合法 JSON: %q (整段 %q)", line, out)
		}
	}
}

// TestSecretsKeepsJSONValid iteration_history.tools 是 JSON 列：脱敏后必须仍是合法 JSON。
func TestSecretsKeepsJSONValid(t *testing.T) {
	in := `[{"name":"Shell","args":"{\"cmd\":\"curl -H 'Authorization: Bearer ` + fakeKey + `' \"}","ok":true,"tokens":120,"secret":null,"password":1.5}]`
	out := Secrets(in)
	if strings.Contains(out, fakeKey) {
		t.Fatalf("JSON 里的假 key 未脱敏: %q", out)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("脱敏把 JSON 打坏了: %q", out)
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("脱敏后无法解析: %v (%q)", err, out)
	}
}

// TestSecretsMultipleKeysInOneLine 同一行多个 key 必须各自命中。
func TestSecretsMultipleKeysInOneLine(t *testing.T) {
	in := fakeKey + " 和 " + fakeKey
	out := Secrets(in)
	if strings.Contains(out, fakeKey) {
		t.Fatalf("同行多 key 有残留: %q", out)
	}
}
