package agent

import (
	"fmt"
	"strings"
	"testing"
)

// TestSubAgentSystemPromptTemplates 锁定两套 SubAgent 系统提示模板：
//
//  1. 不得再出现已删除能力的指令（同伴注入段随 peer 能力一同删除）——
//     教模型使用一个不存在的能力，比不教更糟；这段文字会进**每一个** SubAgent 的
//     system prompt，所以必须零残留。
//  2. 与工具无关、仍然成立的协作规则必须留下（冲突先协商 → 3 轮无果交用户仲裁）。
func TestSubAgentSystemPromptTemplates(t *testing.T) {
	tpls := map[string]string{
		"full":    subagentSystemPromptTemplate,
		"concise": subagentSystemPromptTemplateConcise,
	}
	for name, tpl := range tpls {
		t.Run(name, func(t *testing.T) {
			got := fmt.Sprintf(tpl, "/workspace", "\n- 当前目录：/workspace", "proj.xbot.dev", "agent:proj.xbot.lead/main", "2026-10-27 09:30:00 UTC")

			// "<peer" 覆盖该能力注入段的全部标签形态，同时不把标签字面量留在源码里。
			if strings.Contains(got, "<peer") {
				t.Errorf("deleted capability guidance must be gone, got:\n%s", got)
			}
			if !strings.Contains(got, "3 轮无果或语义冲突提交用户仲裁") {
				t.Errorf("collaboration rule must survive the cleanup, got:\n%s", got)
			}
			if !strings.Contains(got, "## 协作") {
				t.Errorf("collaboration section must not be deleted wholesale, got:\n%s", got)
			}
		})
	}
}
