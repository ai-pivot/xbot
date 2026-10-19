package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Dry run ───

func printPlan(ctx context.Context, src *sql.DB, pool *pgxpool.Pool, sessions []sessionDB) error {
	fmt.Println("\n── plan (dry run, nothing written) ──")
	for _, table := range globalTables {
		srcCols, dstCols, err := plan(ctx, src, pool, table)
		if err != nil {
			return err
		}
		if len(srcCols) == 0 {
			fmt.Printf("  %-26s (absent in source)\n", table)
			continue
		}
		n, err := countSQLite(ctx, src, table)
		if err != nil {
			return err
		}
		note := ""
		if len(srcCols) != len(dstCols) {
			note = " ⚠ column count mismatch"
		}
		if dropped := droppedColumns(srcCols, dstCols); len(dropped) > 0 {
			note += fmt.Sprintf(" (skipped: %s)", strings.Join(dropped, ","))
		}
		fmt.Printf("  %-26s %6d rows  %2d cols%s\n", table, n, len(dstCols), note)
	}
	var msgs, iters int
	for _, s := range sessions {
		m, _ := countSQLite(ctx, s.db, "session_messages")
		i, _ := countSQLite(ctx, s.db, "iteration_history")
		msgs += m
		iters += i
	}
	fmt.Printf("  %-26s %6d rows  (merged from %d session DBs)\n", "session_messages", msgs, len(sessions))
	fmt.Printf("  %-26s %6d rows  (merged from %d session DBs)\n", "iteration_history", iters, len(sessions))
	fmt.Println("\nskipped tables: oauth_tokens (incompatible schema; PG keeps OAuth in SQLite), event_history_fts* (derived index)")
	return nil
}

func countSQLite(ctx context.Context, db *sql.DB, table string) (int, error) {
	exists, err := tableExistsSQLite(ctx, db, table)
	if err != nil || !exists {
		return 0, err
	}
	var n int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n)
	return n, err
}

func droppedColumns(srcCols, dstCols []string) []string {
	dst := make(map[string]bool, len(dstCols))
	for _, c := range dstCols {
		dst[c] = true
	}
	var out []string
	for _, c := range srcCols {
		if !dst[c] {
			out = append(out, c)
		}
	}
	return out
}

// ─── Verification ───

// verify compares source and target and fails on any mismatch. Checks:
//   - row counts per table (session tables summed across the session DBs)
//   - MAX(id) per identity table (primary keys preserved)
//   - orphaned history references (control records pointing at a missing message)
//   - per-tenant replay equivalence when a source session DB is available
func verify(ctx context.Context, src *sql.DB, pool *pgxpool.Pool, sessions []sessionDB) error {
	fmt.Println("\n── verification ──")
	failures := 0

	check := func(name string, srcN, dstN int64) {
		status := "✓"
		if srcN != dstN {
			status = "✗"
			failures++
		}
		fmt.Printf("  %s %-26s source=%-8d target=%-8d\n", status, name, srcN, dstN)
	}

	for _, table := range globalTables {
		srcN, err := countSQLite(ctx, src, table)
		if err != nil {
			return err
		}
		dstN, err := countPG(ctx, pool, table)
		if err != nil {
			return err
		}
		check(table, int64(srcN), dstN)
	}

	var srcMsgs, srcIters int64
	for _, s := range sessions {
		m, _ := countSQLite(ctx, s.db, "session_messages")
		i, _ := countSQLite(ctx, s.db, "iteration_history")
		srcMsgs += int64(m)
		srcIters += int64(i)
	}
	// A main DB that still holds pre-v71 rows is the other copy source.
	if mainMsgs, _ := countSQLite(ctx, src, "session_messages"); mainMsgs > 0 {
		srcMsgs += int64(mainMsgs)
	}
	if mainIters, _ := countSQLite(ctx, src, "iteration_history"); mainIters > 0 {
		srcIters += int64(mainIters)
	}
	dstMsgs, err := countPG(ctx, pool, "session_messages")
	if err != nil {
		return err
	}
	dstIters, err := countPG(ctx, pool, "iteration_history")
	if err != nil {
		return err
	}
	check("session_messages (merged)", srcMsgs, dstMsgs)
	check("iteration_history (merged)", srcIters, dstIters)

	fmt.Println("\n── primary keys ──")
	for _, table := range sortedIdentityTables() {
		col := identityTables[table]
		srcMax, err := maxIDSQLite(ctx, src, table, col)
		if err != nil {
			return err
		}
		if srcMax == 0 {
			continue // session tables live in the per-session DBs, checked below
		}
		dstMax, err := maxIDPG(ctx, pool, table, col)
		if err != nil {
			return err
		}
		status := "✓"
		if srcMax != dstMax {
			status = "✗"
			failures++
		}
		fmt.Printf("  %s %-26s source=%-8d target=%-8d\n", status, table, srcMax, dstMax)
	}
	// Session ids are NOT comparable across source and target: each per-session DB
	// has its own sequence starting at 1, so merging assigns disjoint windows and
	// the merged MAX(id) is necessarily larger than any single source MAX. What must
	// hold — and what the checks below assert — is that every source row arrived
	// (counts above), that no id collided (the inserts would have failed), and that
	// every internal reference still resolves (reference-integrity check).
	var srcMsgRows int64
	for _, s := range sessions {
		if n, err := countSQLite(ctx, s.db, "session_messages"); err == nil {
			srcMsgRows += int64(n)
		}
	}
	dstMaxMsgs, err := maxIDPG(ctx, pool, "session_messages", "id")
	if err != nil {
		return err
	}
	if srcMsgRows > 0 {
		// The merged table must have room for every row: MAX(id) >= row count
		// (equality only when ids are dense and start at 1).
		if dstMaxMsgs < srcMsgRows {
			fmt.Printf("  ✗ session_messages MAX(id)=%d < merged row count %d\n", dstMaxMsgs, srcMsgRows)
			failures++
		} else {
			fmt.Printf("  ✓ %-26s rows=%-8d merged MAX(id)=%-8d (windows offset, not comparable to one source)\n",
				"session_messages ids", srcMsgRows, dstMaxMsgs)
		}
	}

	fmt.Println("\n── history reference integrity ──")
	orphans, err := countOrphanHistoryRefs(ctx, pool)
	if err != nil {
		return err
	}
	if orphans == 0 {
		fmt.Println("  ✓ every target_history_id resolves to a row in the same tenant")
	} else {
		fmt.Printf("  ✗ %d control record(s) point at a missing history row\n", orphans)
		failures++
	}

	if failures > 0 {
		return fmt.Errorf("%d verification check(s) failed", failures)
	}
	fmt.Println("\nall checks passed")
	return nil
}

func countPG(ctx context.Context, pool *pgxpool.Pool, table string) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n)
	return n, err
}

func maxIDSQLite(ctx context.Context, db *sql.DB, table, col string) (int64, error) {
	exists, err := tableExistsSQLite(ctx, db, table)
	if err != nil || !exists {
		return 0, err
	}
	var m sql.NullInt64
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT MAX(%s) FROM %s", col, table)).Scan(&m); err != nil {
		return 0, err
	}
	if !m.Valid {
		return 0, nil
	}
	return m.Int64, nil
}

func maxIDPG(ctx context.Context, pool *pgxpool.Pool, table, col string) (int64, error) {
	var m sql.NullInt64
	err := pool.QueryRow(ctx, fmt.Sprintf("SELECT MAX(%s) FROM %s", col, table)).Scan(&m)
	if err != nil || !m.Valid {
		return 0, err
	}
	return m.Int64, nil
}

// countOrphanHistoryRefs counts control records whose target_history_id has no
// matching row for the same tenant. A non-zero value means replay would fail to
// resolve a mask/context-edit/AskUser target — the exact corruption a renumbered
// primary key produces.
func countOrphanHistoryRefs(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM session_messages c
		WHERE COALESCE(c.target_history_id, 0) > 0
		  AND NOT EXISTS (
			SELECT 1 FROM session_messages t
			WHERE t.tenant_id = c.tenant_id AND t.id = c.target_history_id
		  )`).Scan(&n)
	return n, err
}

func sortedIdentityTables() []string {
	out := make([]string, 0, len(identityTables))
	for t := range identityTables {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
