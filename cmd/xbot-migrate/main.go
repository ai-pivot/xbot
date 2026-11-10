// Command xbot-migrate copies an existing SQLite xbot installation into
// PostgreSQL.
//
// Why a separate command and not a storage-level copy: the SQLite layout is
// main-DB + one DB file per session (v71), while PG keeps everything in one
// schema. Copying therefore means (a) merging N session databases into
// session_messages/iteration_history, and (b) PRESERVING primary keys, because
// session_messages.id is referenced by iteration_history.message_id, by control
// records (target_history_id), by compression snapshots (HistoryIDs) and by mask
// targets — a renumbered id silently corrupts replay. storage.* interfaces do not
// expose PK-preserving inserts, so this command writes explicit per-column
// INSERTs with OVERRIDING SYSTEM VALUE.
//
// Usage:
//
//	xbot-migrate -sqlite ~/.xbot/xbot.db -pg "postgres://user:pass@host:5432/db?sslmode=disable"
//	xbot-migrate ... -dry-run          # plan only, no writes
//	xbot-migrate ... -verify-only      # compare an existing target, no writes
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"

	"xbot/storage"
)

func main() {
	var (
		sqlitePath = flag.String("sqlite", "", "path to the SQLite main database (e.g. ~/.xbot/xbot.db)")
		pgDSN      = flag.String("pg", "", "PostgreSQL DSN of the TARGET database")
		dryRun     = flag.Bool("dry-run", false, "print the plan without writing")
		verifyOnly = flag.Bool("verify-only", false, "only compare source and target")
		wipe       = flag.Bool("wipe", false, "TRUNCATE the target tables before copying (full re-run)")
	)
	flag.Parse()

	if *sqlitePath == "" || *pgDSN == "" {
		fmt.Fprintln(os.Stderr, "usage: xbot-migrate -sqlite <path> -pg <dsn> [-dry-run|-verify-only] [-wipe]")
		os.Exit(2)
	}
	if err := run(*sqlitePath, *pgDSN, *dryRun, *verifyOnly, *wipe); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
}

func run(sqlitePath, pgDSN string, dryRun, verifyOnly, wipe bool) error {
	ctx := context.Background()

	abs, err := filepath.Abs(sqlitePath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("sqlite database not found: %w", err)
	}

	// Open SQLite read-only-ish: we never write to it. A WAL snapshot is taken by
	// the operator (VACUUM INTO) before running this tool; opening the live file is
	// safe for reading but the numbers can drift under an active server, which is
	// why the operator should stop the server first.
	src, err := sql.Open("sqlite", abs+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer src.Close()
	if err := src.Ping(); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}

	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		return fmt.Errorf("open pg pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping pg: %w", err)
	}

	if !verifyOnly {
		if err := storage.InitPGSchema(ctx, pool); err != nil {
			return fmt.Errorf("init pg schema: %w", err)
		}
	}
	if err := discoverIdentityTables(ctx, pool); err != nil {
		return err
	}

	sessions, err := loadSessionDBs(ctx, src, abs)
	if err != nil {
		return err
	}
	fmt.Printf("source : %s\n", abs)
	fmt.Printf("sessions: %d database file(s)\n", len(sessions))

	if verifyOnly {
		return verify(ctx, src, pool, sessions)
	}

	if dryRun {
		return printPlan(ctx, src, pool, sessions)
	}

	if wipe {
		if err := truncateTarget(ctx, pool); err != nil {
			return err
		}
	}

	if err := copyGlobalTables(ctx, src, pool); err != nil {
		return err
	}
	if err := copySessionDataWithOffsets(ctx, pool, sessions); err != nil {
		return err
	}
	if err := resetSequences(ctx, pool); err != nil {
		return err
	}
	fmt.Println("\ncopy complete; verifying…")
	return verify(ctx, src, pool, sessions)
}

// ─── Session database discovery ───

type sessionDB struct {
	tenantID int64
	path     string
	db       *sql.DB
}

// loadSessionDBs resolves every per-session database through the tenants registry
// (db_path is the single authority — paths are never inferred from the naming
// scheme), opens it, and returns the list.
func loadSessionDBs(ctx context.Context, src *sql.DB, mainPath string) ([]sessionDB, error) {
	rows, err := src.QueryContext(ctx,
		`SELECT id, db_path FROM tenants WHERE COALESCE(db_path, '') != '' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read tenants registry: %w", err)
	}
	defer rows.Close()

	baseDir := filepath.Dir(mainPath)
	var out []sessionDB
	for rows.Next() {
		var id int64
		var rel string
		if err := rows.Scan(&id, &rel); err != nil {
			return nil, err
		}
		path := rel
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, rel)
		}
		if _, err := os.Stat(path); err != nil {
			fmt.Printf("  ⚠ tenant %d: session db missing (%s) — its rows will be absent\n", id, path)
			continue
		}
		db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
		if err != nil {
			return nil, fmt.Errorf("open session db %s: %w", path, err)
		}
		out = append(out, sessionDB{tenantID: id, path: path, db: db})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ─── Table definitions ───

// globalTables lists the main-DB tables copied verbatim, in FK-safe order
// (tenants first: six tables reference it). FTS shadow tables are derived indexes
// and are rebuilt, not copied.
var globalTables = []string{
	"tenants",
	"tenant_state",
	"long_term_memory",
	"core_memory_blocks",
	"core_memory_migrations",
	"event_history",
	"user_profiles",
	"archival_memory",
	"user_llm_subscriptions",
	"subscription_models",
	"user_default_model",
	"runners",
	"web_users",
	"user_settings",
	"cron_jobs",
	"event_triggers",
	"user_chats",
	"pending_resumes",
	"user_token_usage",
	"daily_token_usage",
	"shared_artifacts",
	"oauth_tokens",
}

// identityTables lists the tables whose PG primary key is GENERATED ALWAYS AS
// IDENTITY: inserts must say OVERRIDING SYSTEM VALUE, and the sequence must be
// re-aligned afterwards or the next runtime insert collides.
//
// Discovered from PG metadata at run time (see discoverIdentityTables) rather than
// hard-coded: a static list silently rots — an earlier version named
// daily_token_usage, which has no id column at all.
var identityTables = map[string]string{}

// discoverIdentityTables fills identityTables from the target schema.
func discoverIdentityTables(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		SELECT c.relname, a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		WHERE a.attidentity <> '' AND c.relnamespace = current_schema()::regnamespace`)
	if err != nil {
		return fmt.Errorf("discover identity tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			return err
		}
		identityTables[table] = col
	}
	return rows.Err()
}

// columnRename maps SQLite column names onto their PG counterparts where the two
// schemas use different names for the same fact.
var columnRename = map[string]map[string]string{
	"core_memory_migrations": {"applied_at": "created_at"},
}

// skippedColumns are source columns with no PG counterpart.
var skippedColumns = map[string]bool{
	"applied_at": true, // renamed to created_at
}
