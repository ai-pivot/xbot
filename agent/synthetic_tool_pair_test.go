package agent

import (
	"strings"
	"testing"
)

// 用户 2026-09-17：「我们自己注入的不要告诉他别调用，否则他会以为这个是他自己调用的」。
//
// 注入型（fake）工具的结果必须是【原样的 tool-result 文本】——不加任何
// "不是你调用的工具 / 你从未调用过它 / 不要试图再次调用它" 前缀。
//
// 实测证据：加上该前缀后，模型会在思考里写出「我注意到自己在反复误触一个不存在的工具」
// ——反向暗示凭空造出一个"不存在的工具"的焦虑，比不加提示更糟。
//
// 真正由模型【主动调用】的工具（task_status / task_read / SubAgent inspect）仍带
// tools.PollingHint —— 见 tools/tool_guidance_test.go:TestTaskFormats_CarryPollingHint，
// 那才是需要劝导的场景。
func TestNewSyntheticToolPair_NoInjectionNotice(t *testing.T) {
	const content = "bg_subagent_completed\nrole: explore\nresult: done"
	_, toolMsg := newSyntheticToolPair("bg_subagent_completed", "bgsub_1", content)

	if toolMsg.Content != content {
		t.Errorf("injected tool result must be verbatim (no notice prefix), got:\n%q", toolMsg.Content)
	}
	for _, bad := range []string{
		"NOT A TOOL YOU CALLED",
		"AUTO-INJECTED",
		"不是你调用",
		"从未调用过它",
		"不要试图再次调用",
		"不要回谢",
	} {
		if strings.Contains(toolMsg.Content, bad) {
			t.Errorf("injected tool result must not carry the fake-tool notice (found %q)", bad)
		}
	}
}
