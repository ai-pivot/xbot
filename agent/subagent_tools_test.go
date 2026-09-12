package agent

import (
	"testing"

	"xbot/tools"
)

// TestFilterSubAgentTools_TodoWriteAlwaysAvailable — todo 列表是所有 agent 的通用工作记忆
// （用户要求："给所有 agent 都默认加上 todo 相关工具"）。任何 role 都应能记录/更新待办，
// 不该要求每个 agent 定义都在 tools 里声明 TodoWrite。
func TestFilterSubAgentTools_TodoWriteAlwaysAvailable(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.TodoWriteTool{})
	reg.Register(&tools.GrepTool{})
	reg.Register(&tools.GlobTool{})

	// 白名单里没有 TodoWrite（也没 Glob）→ 只有 TodoWrite 应该存活
	filterSubAgentTools(reg, []string{"Grep"}, tools.SubAgentCapabilities{}, false)

	names := map[string]bool{}
	for _, tool := range reg.List() {
		names[tool.Name()] = true
	}
	if !names["TodoWrite"] {
		t.Errorf("TodoWrite must stay available for every agent (got %v)", names)
	}
	if !names["Grep"] {
		t.Errorf("allow-listed tool must survive (got %v)", names)
	}
	if names["Glob"] {
		t.Errorf("tool outside the allow-list must be filtered out (got %v)", names)
	}
}
