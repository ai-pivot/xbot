package runner

import "xbot/tools"

// ToolProvider wraps a Manager to implement tools.ToolProvider.
//
// It resolves the session's bound runner via Manager.ResolveSession, then returns
// that runner's tools. When no session binding exists, falls back to local runner.
type ToolProvider struct {
	mgr *Manager
}

// NewToolProvider creates a ToolProvider backed by the given Manager.
func NewToolProvider(mgr *Manager) *ToolProvider {
	return &ToolProvider{mgr: mgr}
}

func (p *ToolProvider) Name() string { return "runner" }

func (p *ToolProvider) Priority() int { return 2 }

// ListTools returns all tools from the session's bound runner.
func (p *ToolProvider) ListTools(sessionKey string, tenantID int64) []tools.Tool {
	r := p.mgr.ResolveSession(sessionKey)
	if r == nil || len(r.Tools) == 0 {
		return nil
	}
	result := make([]tools.Tool, 0, len(r.Tools))
	for _, t := range r.Tools {
		result = append(result, t)
	}
	return result
}

// GetTool looks up a tool by name from the session's bound runner.
func (p *ToolProvider) GetTool(sessionKey string, tenantID int64, name string) (tools.Tool, bool) {
	r := p.mgr.ResolveSession(sessionKey)
	if r == nil || r.Tools == nil {
		return nil, false
	}
	t, ok := r.Tools[name]
	return t, ok
}
