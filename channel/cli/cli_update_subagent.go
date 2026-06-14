package cli

import (
	"xbot/protocol"
)

// maxTreeDepth returns the maximum depth of the SubAgent tree (1 for top-level nodes).
// mergeSubAgentTrees merges new SubAgent data into the previous tree.
// Agents present in both trees are updated with new data (status, tools, description).
// Agents only in prev are kept as-is (they may have completed between server updates).
// Agents only in new are added.
//
// Uniqueness key: Role + ":" + Instance. When Instance is empty, Role alone is used.
// This prevents same-role different-instance agents from being merged into one.
//
// Key rule: if an agent in prev is NOT in new, it means the server stopped reporting
// it. This is normal — the server only reports actively-running agents. We mark
// stale running/pending agents as "done" so they don't linger in the progress panel
// (Issue #29: zombie agents that completed but were never marked done by the server).
func mergeSubAgentTrees(prev, new []protocol.SubAgentInfo) []protocol.SubAgentInfo {
	if len(prev) == 0 {
		return new
	}
	if len(new) == 0 {
		// Server stopped reporting all agents — they completed.
		// Return empty slice (no zombies). Previous carry-forward
		// in carryForwardProgressState will handle pruning.
		return nil
	}

	// Build lookup from new by unique key (Role + Instance)
	newByKey := make(map[string]int, len(new))
	for i, a := range new {
		key := subAgentKey(a.Role, a.Instance)
		newByKey[key] = i
	}

	result := make([]protocol.SubAgentInfo, 0, len(prev)+len(new))

	// Start with all prev entries, updating those that have new data
	for _, p := range prev {
		key := subAgentKey(p.Role, p.Instance)
		if idx, ok := newByKey[key]; ok {
			// Agent exists in both — merge: use new data but preserve
			// previous Desc when new is empty (SubAgent progress may
			// report an empty Desc between activity bursts).
			n := new[idx]
			merged := n
			if merged.Desc == "" && p.Desc != "" {
				merged.Desc = p.Desc
			}
			merged.Children = mergeSubAgentTrees(p.Children, n.Children)
			// When parent agent is still active but its children list is empty in
			// the new update, preserve previous children (marked as done if still
			// running) instead of dropping them entirely. This prevents child
			// progress trees from flickering when the server doesn't include
			// children in every update frame.
			if len(n.Children) == 0 && len(merged.Children) == 0 && len(p.Children) > 0 {
				merged.Children = markAllDone(p.Children)
			}
			result = append(result, merged)
			delete(newByKey, key)
		} else {
			// Agent only in prev — server stopped reporting it.
			// If already done/error, skip it (zombie cleanup — prevents
			// completed agents from accumulating in the tree forever).
			// If still running/pending, mark as done (it completed between
			// updates) but also skip — the user already saw it finish.
			_ = markDoneIfRunning(p) // mark children recursively
		}
	}

	// Add agents only in new
	for key := range newByKey {
		result = append(result, new[newByKey[key]])
	}

	return result
}

// subAgentKey builds a unique key for a SubAgent from Role and Instance.
func subAgentKey(role, instance string) string {
	if instance == "" {
		return role
	}
	return role + ":" + instance
}

// markDoneIfRunning marks a SubAgent and its children as done if they are
// still in running/pending state. This handles the case where the server
// stops reporting a completed SubAgent — without this, the agent would
// linger as "running" forever (Issue #29).
func markDoneIfRunning(sa protocol.SubAgentInfo) protocol.SubAgentInfo {
	if sa.Status == "running" || sa.Status == "pending" {
		sa.Status = "done"
	}
	for i := range sa.Children {
		sa.Children[i] = markDoneIfRunning(sa.Children[i])
	}
	return sa
}

// markAllDone applies markDoneIfRunning to every agent in the slice.
// Used by mergeSubAgentTrees when a parent's children list is empty in the
// new update but existed in prev — preserves the subtree as completed.
func markAllDone(agents []protocol.SubAgentInfo) []protocol.SubAgentInfo {
	result := make([]protocol.SubAgentInfo, len(agents))
	for i := range agents {
		result[i] = markDoneIfRunning(agents[i])
	}
	return result
}

// pruneDoneSubAgents removes agents (and their children) that are already
// marked "done". This prevents zombie entries from accumulating across
// iteration boundaries when no new SubAgent data arrives.
// Agents still "running" or "pending" are kept (they may complete soon).
func pruneDoneSubAgents(agents []protocol.SubAgentInfo) []protocol.SubAgentInfo {
	var kept []protocol.SubAgentInfo
	for _, a := range agents {
		a.Children = pruneDoneSubAgents(a.Children)
		if a.Status != "done" && a.Status != "error" {
			kept = append(kept, a)
		}
	}
	return kept
}
