package tools

import (
	"database/sql"
	"fmt"
	"strings"
)

// RunnerState classifies a registry row against the operator's managed set.
//
// The runner registry (`runners` table) is the single authority for "which
// machines exist", read by every surface (management view + execution-target
// picker). `runner_create` must mint a token *before* the machine can connect,
// so an enrollment row appears before the machine does — and a provisioning
// flow that fails or is abandoned used to leave it behind forever, showing up
// as a permanently "offline" execution target that no UI could remove
// (user-reported ghost entries: `default`/`ubuntu`/`web1`/`remote-arch`/`linked`).
//
// Classification needs the one thing the server cannot derive on its own: the
// operator's machine list (the management view's target registry — machines are
// added and removed there). Callers declare it; see BuildRunnerRegistry.
type RunnerState string

const (
	// RunnerStateManaged: the operator manages this machine (it has an SSH
	// target in the management view). Selectable — may be offline right now,
	// which the picker must state explicitly before binding.
	RunnerStateManaged RunnerState = "managed"
	// RunnerStateLive: not managed, but a runner process is connected with this
	// name right now. A live machine is real by definition — never hidden.
	RunnerStateLive RunnerState = "live"
	// RunnerStateOrphan: neither managed nor connected. A leftover enrollment
	// (aborted/legacy provisioning). Not selectable; deletable from the
	// management view, which is the only place these rows are surfaced.
	RunnerStateOrphan RunnerState = "orphan"
)

// RunnerEntry is a registry row annotated for the UI.
//
// RunnerInfo is embedded so the wire shape stays a superset of `runner_list`
// (existing consumers keep working).
type RunnerEntry struct {
	RunnerInfo
	// Managed reports whether the caller's managed set contains this machine.
	Managed bool `json:"managed"`
	// State is the classification (managed / live / orphan).
	State RunnerState `json:"state"`
	// Selectable tells the execution-target picker whether this row may be
	// chosen at all. Orphans must not be: binding a session to a machine that
	// cannot exist hard-fails every tool call in that session
	// (SandboxRouter.SandboxForSession → OfflineRunnerSandbox).
	Selectable bool `json:"selectable"`
	// BoundCount is how many sessions still run on this machine
	// (tenants.runner_id). Non-zero means deleting the row strands those
	// sessions — the management view warns before removal.
	BoundCount int `json:"bound_count"`
}

// RunnerRegistry is the annotated registry: every row, plus the orphan names
// the management view offers to clean up.
type RunnerRegistry struct {
	Runners []RunnerEntry `json:"runners"`
	// Orphans lists state==orphan names, in registry order (created_at, name).
	// Cleanup is an explicit user action (`runner_delete` per name) — listing
	// never mutates the registry.
	Orphans []string `json:"orphans"`
}

// BuildRunnerRegistry reads the runner registry and annotates every row against
// `managed` (the caller's managed-machine set).
//
// Read-only by design: it never deletes anything. Orphan rows are reported so
// the management view can show them and let the operator remove them — the
// server does not silently drop rows the operator never confirmed.
func BuildRunnerRegistry(db *sql.DB, managed []string) (RunnerRegistry, error) {
	if db == nil {
		return RunnerRegistry{}, fmt.Errorf("runner management not configured")
	}

	// The declared set is a set: duplicates/blank names must not create
	// phantom classifications.
	managedSet := make(map[string]bool, len(managed))
	for _, name := range managed {
		if n := strings.TrimSpace(name); n != "" {
			managedSet[n] = true
		}
	}

	bound, err := boundRunnerCounts(db)
	if err != nil {
		return RunnerRegistry{}, err
	}

	rows, err := NewRunnerStore(db).List()
	if err != nil {
		return RunnerRegistry{}, err
	}

	registry := RunnerRegistry{Runners: make([]RunnerEntry, 0, len(rows))}
	for _, row := range rows {
		entry := RunnerEntry{RunnerInfo: row, Managed: managedSet[row.Name]}
		entry.BoundCount = bound[row.Name]
		switch {
		case entry.Managed:
			entry.State = RunnerStateManaged
			entry.Selectable = true
		case runnerConnected(row.Name):
			entry.State = RunnerStateLive
			entry.Selectable = true
		default:
			entry.State = RunnerStateOrphan
			entry.Selectable = false
			registry.Orphans = append(registry.Orphans, row.Name)
		}
		registry.Runners = append(registry.Runners, entry)
	}

	// Online status for the whole registry (including orphans, so the
	// management view can show what is actually up).
	infos := make([]RunnerInfo, len(registry.Runners))
	for i := range registry.Runners {
		infos[i] = registry.Runners[i].RunnerInfo
	}
	PopulateRunnerOnlineStatus(infos)
	for i := range registry.Runners {
		registry.Runners[i].RunnerInfo = infos[i]
	}
	return registry, nil
}

// runnerConnected reports whether the named runner currently holds a live
// connection. A connected machine is real by definition, so it is never
// classified as an orphan.
func runnerConnected(name string) bool {
	router, ok := GetSandbox().(*SandboxRouter)
	if !ok || router == nil {
		return false
	}
	return router.IsRunnerOnline(name)
}

// boundRunnerCounts returns how many sessions are bound to each runner
// (authority: tenants.runner_id — the column runner_session_set writes).
//
// A missing tenants table means "no bindings exist" (fresh/partial DB); any
// other failure aborts the listing, so a classification is never made on an
// unknown binding state.
func boundRunnerCounts(db *sql.DB) (map[string]int, error) {
	counts := map[string]int{}
	ok, err := runnerTableExists(db, "tenants")
	if err != nil {
		return nil, err
	}
	if !ok {
		return counts, nil
	}
	rows, err := db.Query(`SELECT runner_id, COUNT(*) FROM tenants WHERE COALESCE(runner_id,'') <> '' GROUP BY runner_id`)
	if err != nil {
		return nil, fmt.Errorf("read session→runner bindings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, fmt.Errorf("scan session→runner binding: %w", err)
		}
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			counts[trimmed] = n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read session→runner bindings: %w", err)
	}
	return counts, nil
}

// runnerTableExists reports whether a table exists (non-transactional variant
// of tableExistsTx).
func runnerTableExists(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check table %s: %w", table, err)
	}
	return true, nil
}
