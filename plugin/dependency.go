package plugin

import (
	"sort"
)

// DependencyResolver resolves plugin dependency graphs using topological sort.
//
// Usage:
//
//	dr := NewDependencyResolver()
//	dr.AddManifest(manifest1)
//	dr.AddManifest(manifest2)
//	order, err := dr.Resolve() // returns activation order
//
// The resolver uses Kahn's algorithm (BFS-based topological sort) with O(V+E) time complexity.
type DependencyResolver struct {
	manifests []*PluginManifest
	byID      map[string]*PluginManifest // pluginID → manifest lookup
}

// NewDependencyResolver creates a new DependencyResolver.
func NewDependencyResolver() *DependencyResolver {
	return &DependencyResolver{
		byID: make(map[string]*PluginManifest),
	}
}

// AddManifest adds a plugin manifest to the dependency graph.
// If a manifest with the same ID was already added, it is replaced.
func (dr *DependencyResolver) AddManifest(m *PluginManifest) {
	// Replace if already exists
	for i, existing := range dr.manifests {
		if existing.ID == m.ID {
			dr.manifests[i] = m
			dr.byID[m.ID] = m
			return
		}
	}
	dr.manifests = append(dr.manifests, m)
	dr.byID[m.ID] = m
}

// Resolve returns the activation order of all added plugins using Kahn's algorithm.
// Plugins with no dependencies come first, followed by plugins that depend on them.
// Returns ErrCircularDependency if a cycle is detected.
// Returns ErrMissingDependency if a dependency references a plugin that was not added.
func (dr *DependencyResolver) Resolve() ([]string, error) {
	if len(dr.manifests) == 0 {
		return nil, nil
	}

	// Step 1: Build in-degree map and adjacency list
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // depID → list of plugins that depend on it

	for _, m := range dr.manifests {
		inDegree[m.ID] = 0
	}

	for _, m := range dr.manifests {
		for _, dep := range m.Dependencies {
			if _, ok := dr.byID[dep.ID]; !ok {
				return nil, &ErrMissingDependency{
					PluginID: m.ID,
					Missing:  dep.ID,
				}
			}
			inDegree[m.ID]++
			dependents[dep.ID] = append(dependents[dep.ID], m.ID)
		}
	}

	// Step 2: Initialize queue with all nodes that have in-degree 0
	var queue []string
	for _, m := range dr.manifests {
		if inDegree[m.ID] == 0 {
			queue = append(queue, m.ID)
		}
	}
	// Sort for deterministic order among equal-priority nodes
	sort.Strings(queue)

	// Step 3: BFS
	var order []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)

		deps := dependents[id]
		sort.Strings(deps) // deterministic
		for _, depID := range deps {
			inDegree[depID]--
			if inDegree[depID] == 0 {
				queue = append(queue, depID)
				sort.Strings(queue) // keep queue sorted for determinism
			}
		}
	}

	// Step 4: Check for cycles
	if len(order) < len(dr.manifests) {
		var cycle []string
		for _, m := range dr.manifests {
			if inDegree[m.ID] > 0 {
				cycle = append(cycle, m.ID)
			}
		}
		sort.Strings(cycle)
		return nil, &ErrCircularDependency{Cycle: cycle}
	}

	return order, nil
}

// Validate checks that all declared dependencies exist among the added manifests.
// Returns ErrMissingDependency for the first missing dependency found.
// Returns nil if all dependencies are satisfied.
func (dr *DependencyResolver) Validate() error {
	var missing []struct{ plugin, dep string }
	for _, m := range dr.manifests {
		for _, dep := range m.Dependencies {
			if _, ok := dr.byID[dep.ID]; !ok {
				missing = append(missing, struct{ plugin, dep string }{m.ID, dep.ID})
			}
		}
	}
	if len(missing) > 0 {
		return &ErrMissingDependency{
			PluginID: missing[0].plugin,
			Missing:  missing[0].dep,
		}
	}
	return nil
}
