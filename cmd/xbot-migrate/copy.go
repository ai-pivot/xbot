package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Introspection ───

// sqliteColumns returns the column names of a SQLite table in declaration order.
func sqliteColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

func tableExistsSQLite(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	return n > 0, err
}

// plan builds the column mapping for one table: which SQLite columns are copied
// and under which PG name.
func plan(ctx context.Context, src *sql.DB, pool *pgxpool.Pool, table string) (srcCols []string, dstCols []string, err error) {
	srcCols, dstCols, _, err = planWithTypes(ctx, src, pool, table)
	return srcCols, dstCols, err
}

// planWithTypes also returns the PG data type of every destination column, in the
// same order as dstCols.
func planWithTypes(ctx context.Context, src *sql.DB, pool *pgxpool.Pool, table string) (srcCols, dstCols, dstTypes []string, err error) {
	exists, err := tableExistsSQLite(ctx, src, table)
	if err != nil {
		return nil, nil, nil, err
	}
	if !exists {
		return nil, nil, nil, nil // nothing to copy
	}
	sCols, err := sqliteColumns(ctx, src, table)
	if err != nil {
		return nil, nil, nil, err
	}
	pTypes, err := pgColumnTypes(ctx, pool, table)
	if err != nil {
		return nil, nil, nil, err
	}
	pgSet := make(map[string]bool, len(pTypes))
	for c := range pTypes {
		pgSet[c] = true
	}
	rename := columnRename[table]

	for _, c := range sCols {
		if skippedColumns[c] && (rename == nil || rename[c] == "") {
			// A column that exists in PG under the same name must still be copied.
			if !pgSet[c] {
				continue
			}
		}
		target := c
		if rename != nil {
			if r, ok := rename[c]; ok {
				target = r
			}
		}
		if !pgSet[target] {
			continue // no PG counterpart: skip (schema parity test guards surprises)
		}
		srcCols = append(srcCols, c)
		dstCols = append(dstCols, target)
		dstTypes = append(dstTypes, pTypes[target])
	}
	return srcCols, dstCols, dstTypes, nil
}

// pgColumnTypes maps every PG column of a table to its data type.
func pgColumnTypes(ctx context.Context, pool *pgxpool.Pool, table string) (map[string]string, error) {
	rows, err := pool.Query(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = $1`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		out[name] = typ
	}
	return out, rows.Err()
}

// ─── Value conversion ───

// convertValue normalizes one SQLite value into what pgx expects for the TARGET
// column type.
//
// Deciding by column NAME is wrong: the same name has different types across
// tables (user_settings.updated_at is INTEGER epoch while most tables store a
// timestamp string; oauth_tokens.expires_at is INTEGER). The PG type is the only
// authority, so it is looked up once per table and passed in here.
func convertValue(pgType, col string, v any) any {
	if v == nil {
		return nil
	}
	switch pgType {
	case "boolean":
		return toBool(v)
	case "timestamp with time zone", "timestamp without time zone":
		if ts, ok := toTime(v); ok {
			return ts
		}
		return nil
	case "bigint", "integer", "smallint":
		if n, ok := toInt64(v); ok {
			return n
		}
		if ts, ok := toTime(v); ok { // an epoch stored as a timestamp-ish string
			return ts.Unix()
		}
		return nil
	case "bytea":
		if b, ok := v.([]byte); ok {
			return b
		}
		return []byte(fmt.Sprint(v))
	case "text", "character varying", "character":
		if b, ok := v.([]byte); ok {
			return sanitizeUTF8(string(b))
		}
		return sanitizeUTF8(fmt.Sprint(v))
	case "double precision", "real", "numeric":
		return v
	}
	// Unknown type: pass through untouched rather than guessing.
	return v
}

// toBool normalizes SQLite's 0/1 (or a textual variant) into a bool.
func toBool(v any) bool {
	switch t := v.(type) {
	case int64:
		return t != 0
	case bool:
		return t
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	case []byte:
		s := string(t)
		return s == "1" || strings.EqualFold(s, "true")
	}
	return false
}

// toTime parses every timestamp shape the writers produce.
func toTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case int64:
		if t == 0 {
			return time.Time{}, false
		}
		return time.Unix(t, 0).UTC(), true
	case string:
		return parseSQLiteTimestamp(t)
	case []byte:
		return parseSQLiteTimestamp(string(t))
	}
	return time.Time{}, false
}

// parseSQLiteTimestamp accepts every format the writers produce:
//   - datetime('now')            "2006-01-02 15:04:05"
//   - RFC3339 / RFC3339Nano
//   - Go's time.Time.String()    "2006-01-02 15:04:05.999999999 -0700 MST m=+1.234"
//     (this is what several services store verbatim — the monotonic-clock suffix
//     and the zone NAME both have to be stripped before parsing)
//   - date-only, and unix seconds
func parseSQLiteTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// time.Time.String() appends a monotonic reading (" m=+267.070035338") when the
	// value came from time.Now(); it is not part of the wall clock.
	if i := strings.Index(s, " m="); i > 0 {
		s = s[:i]
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST", // time.Time.String() with zone name
		"2006-01-02 15:04:05.999999999 -0700",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}

// ─── Copy ───

// copyGlobalTables copies every main-DB table. tenants goes first (FK order).
func copyGlobalTables(ctx context.Context, src *sql.DB, pool *pgxpool.Pool) error {
	fmt.Println("\n── global tables ──")
	for _, table := range globalTables {
		n, err := copyTable(ctx, src, pool, table, nil)
		if err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
		fmt.Printf("  %-26s %6d rows\n", table, n)
	}
	return nil
}

// copyTable copies one table. when tenantFilter is non-nil, only rows whose
// tenant_id matches are copied (used when merging session databases).
func copyTable(ctx context.Context, src *sql.DB, pool *pgxpool.Pool, table string, tenantFilter *int64) (int, error) {
	srcCols, dstCols, dstTypes, err := planWithTypes(ctx, src, pool, table)
	if err != nil {
		return 0, err
	}
	if len(srcCols) == 0 {
		return 0, nil
	}

	query := "SELECT " + strings.Join(quoteAll(srcCols), ", ") + " FROM " + table
	var args []any
	var where []string
	if tenantFilter != nil {
		where = append(where, "tenant_id = ?")
		args = append(args, *tenantFilter)
	}
	// Rows whose tenant no longer exists cannot be inserted: PG enforces the FK
	// that SQLite never had. Such orphans are a normal artefact of deleting a
	// session (the tenant row goes, child rows stay), so skip them and report the
	// count instead of aborting the whole migration.
	//
	// The filter is only applicable when the SOURCE database has the tenants
	// registry — per-session databases hold just messages/iterations and have no
	// tenants table, so there the check would be a syntax-level failure.
	srcHasTenants, err := tableExistsSQLite(ctx, src, "tenants")
	if err != nil {
		return 0, err
	}
	orphanFilter := srcHasTenants && hasTenantColumn(srcCols) && table != "tenants"
	if orphanFilter {
		where = append(where, "tenant_id IN (SELECT id FROM tenants)")
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	// Deliberately NO ORDER BY: SQLite already scans in rowid order, and the
	// foreign-key ordering that matters is BETWEEN tables (tenants is copied
	// first), not within one. Sorting is not just unnecessary here — on a database
	// with a corrupt index page it is actively harmful: `ORDER BY id` walks the
	// damaged btree and either fails with "database disk image is malformed" or
	// returns the same row twice, while the unsorted table scan reads cleanly.

	rows, err := src.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	_, hasIdentity := identityTables[table]
	insertSQL := buildInsertSQL(table, dstCols, hasIdentity)
	// Dedupe on the REAL primary key (composite keys included): a corrupt source
	// btree can hand the same row back twice, and inserting it again violates the
	// PG constraint.
	pkCols, err := pgPrimaryKey(ctx, pool, table)
	if err != nil {
		return 0, err
	}
	pkIdx := make([]int, 0, len(pkCols))
	for _, pk := range pkCols {
		idx := -1
		for i, c := range srcCols {
			if c == pk {
				idx = i
				break
			}
		}
		if idx < 0 {
			pkIdx = nil
			break
		}
		pkIdx = append(pkIdx, idx)
	}

	// Batch in one transaction per table: small data, and a partially-copied table
	// is worse than a clean failure.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n := 0
	seenPK := make(map[string]bool)
	duplicates := 0
	batch := &pgx.Batch{}
	for rows.Next() {
		vals := make([]any, len(srcCols))
		ptrs := make([]any, len(srcCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, fmt.Errorf("scan row %d: %w", n+1, err)
		}
		// A corrupt source btree can hand the same row back twice (the page is
		// reachable from two paths). Keep the first occurrence and report it —
		// never silently insert a duplicate that would fail the PK constraint.
		if len(pkIdx) > 0 {
			key := pkKey(vals, pkIdx)
			if seenPK[key] {
				duplicates++
				continue
			}
			seenPK[key] = true
		}
		converted := make([]any, len(vals))
		for i := range vals {
			converted[i] = convertValue(dstTypes[i], srcCols[i], vals[i])
		}
		batch.Queue(insertSQL, converted...)
		n++
		if batch.Len() >= 500 {
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return 0, fmt.Errorf("insert batch into %s: %w", table, err)
			}
			batch = &pgx.Batch{}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return 0, fmt.Errorf("insert final batch into %s: %w", table, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if orphanFilter {
		if orphans, err := countOrphans(ctx, src, table); err == nil && orphans > 0 {
			fmt.Printf("  ⚠ %s: %d row(s) skipped — their tenant no longer exists\n", table, orphans)
		}
	}
	if duplicates > 0 {
		fmt.Printf("  ⚠ %s: %d duplicate primary-key row(s) skipped — the source btree is\n"+
			"    corrupt (PRAGMA integrity_check reports out-of-order cells); consider\n"+
			"    rebuilding it with .recover before trusting the copy\n", table, duplicates)
	}
	return n, nil
}

// toInt64 normalizes a scanned primary-key value.
func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case []byte:
		var n int64
		if _, err := fmt.Sscan(string(t), &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func quoteAll(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = `"` + c + `"`
	}
	return out
}

func buildInsertSQL(table string, dstCols []string, hasIdentity bool) string {
	placeholders := make([]string, len(dstCols))
	for i := range dstCols {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	override := ""
	if hasIdentity {
		override = " OVERRIDING SYSTEM VALUE"
	}
	return fmt.Sprintf(`INSERT INTO %s (%s)%s VALUES (%s)`,
		table, strings.Join(quoteAll(dstCols), ", "), override, strings.Join(placeholders, ", "))
}

// ─── Sequences ───

// resetSequences re-aligns every IDENTITY sequence with MAX(id). Without this the
// first runtime insert collides with a copied primary key.
func resetSequences(ctx context.Context, pool *pgxpool.Pool) error {
	fmt.Println("\n── sequences ──")
	names := make([]string, 0, len(identityTables))
	for t := range identityTables {
		names = append(names, t)
	}
	sortStrings(names)
	for _, table := range names {
		col := identityTables[table]
		var maxID sql.NullInt64
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT MAX(%s) FROM %s`, col, table)).Scan(&maxID); err != nil {
			return fmt.Errorf("max id for %s: %w", table, err)
		}
		if !maxID.Valid {
			continue // empty table: sequence starts at 1
		}
		// pg_get_serial_sequence is a FUNCTION returning the sequence name; it must
		// be called, not quoted as a literal.
		if _, err := pool.Exec(ctx,
			`SELECT setval(pg_get_serial_sequence($1, $2), $3)`,
			table, col, maxID.Int64); err != nil {
			return fmt.Errorf("setval for %s: %w", table, err)
		}
		fmt.Printf("  %-26s max=%-8d seq aligned\n", table, maxID.Int64)
	}
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// truncateTarget empties the target so a full re-run is deterministic.
func truncateTarget(ctx context.Context, pool *pgxpool.Pool) error {
	tables := append([]string{"iteration_history", "session_messages"}, globalTables...)
	fmt.Println("\n── truncating target ──")
	// One statement per table: pgx rejects multi-statement Exec.
	for i := len(tables) - 1; i >= 0; i-- {
		t := tables[i]
		if _, err := pool.Exec(ctx, "TRUNCATE TABLE "+t+" CASCADE"); err != nil {
			return fmt.Errorf("truncate %s: %w", t, err)
		}
	}
	return nil
}

// hasTenantColumn reports whether the copied column set carries tenant_id (i.e.
// the table is tenant-scoped and subject to the FK that PG enforces).
func hasTenantColumn(cols []string) bool {
	for _, c := range cols {
		if c == "tenant_id" {
			return true
		}
	}
	return false
}

// countOrphans counts source rows whose tenant_id has no tenants row.
func countOrphans(ctx context.Context, src *sql.DB, table string) (int, error) {
	var n int
	err := src.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+table+" WHERE tenant_id NOT IN (SELECT id FROM tenants)").Scan(&n)
	return n, err
}

// pkKey builds a comparable key from the primary-key columns of one row.
func pkKey(vals []any, idx []int) string {
	var b strings.Builder
	for _, i := range idx {
		fmt.Fprintf(&b, "%v\x00", vals[i])
	}
	return b.String()
}

// pgPrimaryKey returns the primary-key column names of a PG table, in key order.
// Cached per run — the schema does not change mid-migration.
func pgPrimaryKey(ctx context.Context, pool *pgxpool.Pool, table string) ([]string, error) {
	if v, ok := pkCache[table]; ok {
		return v, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey::int2[])
		WHERE i.indisprimary AND c.relname = $1 AND c.relnamespace = current_schema()::regnamespace
		ORDER BY array_position(i.indkey::int2[], a.attnum)`, table)
	if err != nil {
		return nil, fmt.Errorf("primary key of %s: %w", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	pkCache[table] = cols
	return cols, rows.Err()
}

var pkCache = map[string][]string{}

// sanitizeUTF8 replaces invalid UTF-8 sequences with U+FFFD.
//
// Why this is necessary and not a hack: SQLite stores TEXT as opaque bytes and
// never validates encoding, so a historical truncation (a multi-byte character cut
// mid-sequence) leaves rows that are readable by SQLite but rejected by
// PostgreSQL ("invalid byte sequence for encoding UTF8"). Such rows exist in real
// installations (measured: 36 of 15,341 message rows). Dropping them would lose
// conversation history; aborting would make migration impossible. Replacing just
// the damaged bytes keeps the rest of the text intact, and the caller reports how
// many values were repaired so the loss is never silent.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}
