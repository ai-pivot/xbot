package agent

import "xbot/tools"

// agentToolProvider implements tools.ToolProvider for agent-core orchestration tools.
//
// Agent core tools are always available regardless of runner. They handle
// agent orchestration: spawning sub-agents, sending messages, managing todos, etc.
//
// Priority=1 means agent core tools take precedence over runner tools (priority=2)
// in case of name collisions.
type agentToolProvider struct {
	toolMap map[string]tools.Tool
}

func newAgentToolProvider() *agentToolProvider {
	return &agentToolProvider{
		toolMap: make(map[string]tools.Tool),
	}
}

func (p *agentToolProvider) Name() string { return "agent-core" }

func (p *agentToolProvider) Priority() int { return 1 }

func (p *agentToolProvider) ListTools(sessionKey string, tenantID int64) []tools.Tool {
	result := make([]tools.Tool, 0, len(p.toolMap))
	for _, t := range p.toolMap {
		result = append(result, t)
	}
	return result
}

func (p *agentToolProvider) GetTool(sessionKey string, tenantID int64, name string) (tools.Tool, bool) {
	t, ok := p.toolMap[name]
	return t, ok
}
