package agent

import "xbot/internal/sessionaddr"

// isSubAgentID centralizes the distinction between the sole main agent and
// every subagent identity. Historically this was inferred independently via
// strings.Contains(id, "/"), which misclassified a top-level child whose path
// was the single segment "taizi" as the main agent. Only the canonical main ID
// denotes the main agent; every other non-empty ID is subagent-scoped.
func isSubAgentID(agentID string) bool { return sessionaddr.IsSubAgent(agentID) }
