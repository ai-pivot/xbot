package agent

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsExternalDependencyFailure pins the split between "an upstream endpoint
// is unreachable" and "this process has a bug".
//
// 背景：皇上的基线是「日志零 error」。生产上有一个故意配成不可达的订阅
// （http://3.37.20.114:31001），每分钟一次的 cron 打它必然 i/o timeout，于是
// 每条都记一条 level=error —— 基线永远红，真正的程序缺陷被淹在噪声里。
//
// 判据必须窄：只有传输层特征串才算外部依赖失败。认证错误、参数错误、SQL 错误
// 之类一律仍按 error 处理，不能被这类分类顺手放走。
func TestIsExternalDependencyFailure(t *testing.T) {
	external := []string{
		`LLM generate failed: All attempts fail:
#1: openai stream completion: Post "http://3.37.20.114:31001/v1/chat/completions": dial tcp 3.37.20.114:31001: i/o timeout`,
		"dial tcp 127.0.0.1:9999: connect: connection refused",
		"read tcp 10.0.0.5:443: connection reset by peer",
		"dial tcp: lookup api.example.invalid: no such host",
		"dial tcp 1.2.3.4:80: connect: network is unreachable",
		"TLS handshake timeout",
		"context deadline exceeded",
	}
	for _, m := range external {
		if !isExternalDependencyFailure(errors.New(m)) {
			t.Errorf("should be classified as external dependency failure: %s", m)
		}
	}

	internal := []string{
		"401 Unauthorized: invalid api key",
		"400 Bad Request: model not found",
		"sql: database is closed",
		"open session db (tenant 12): session was destroyed",
		"json: cannot unmarshal array into Go struct field HookConfig.hooks",
		"",
	}
	for _, m := range internal {
		if isExternalDependencyFailure(fmt.Errorf("%s", m)) {
			t.Errorf("must NOT be classified as external: %s", m)
		}
	}
	if isExternalDependencyFailure(nil) {
		t.Error("nil must not be external")
	}
}
