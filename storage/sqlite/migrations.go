package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	log "xbot/logger"
)

// migrateSchema runs all pending migrations from the given version.
// The migration sequence is: 1→2→3→4→5→6→8→9→10→11→12→13→14→15→16→17→18→19→20→21
// (v7 never existed).
func (db *DB) migrateSchema(from int) error {
	conn := db.Conn()

	// Warn on unexpected version numbers.
	if from == 7 {
		log.WithField("from_version", from).Warn("Schema version 7 never existed; possible manual version corruption. Proceeding with migrations.")
	}
	if from > schemaVersion {
		log.WithFields(log.Fields{
			"from_version":   from,
			"schema_version": schemaVersion,
		}).Warn("Stored schema version exceeds expected; database may be from a newer build")
	}

	type migration struct {
		version int
		fn      func(conn *sql.DB) error
	}

	// Standard migrations that only need *sql.DB.
	standardMigrations := []migration{
		{2, migrateV1ToV2},
		{3, migrateV2ToV3},
		{4, migrateV3ToV4},
		{5, migrateV4ToV5},
		{6, migrateV5ToV6},
		{8, migrateV6ToV8},
		{9, migrateV8ToV9},
		{10, migrateV9ToV10},
		{11, migrateV10ToV11},
		{12, migrateV11ToV12},
		{13, migrateV12ToV13},
		{14, migrateV13ToV14},
		{15, migrateV14ToV15},
		{16, migrateV15ToV16},
		{17, migrateV16ToV17},
		{18, migrateV17ToV18},
	}

	for _, m := range standardMigrations {
		if from < m.version {
			if err := m.fn(conn); err != nil {
				return fmt.Errorf("migrate to v%d: %w", m.version, err)
			}
		}
	}

	// v19 requires *DB to instantiate UserTokenUsageService.
	if from < 19 {
		if err := migrateV18ToV19WithDB(db); err != nil {
			return fmt.Errorf("migrate to v19: %w", err)
		}
	}

	// Remaining standard migrations.
	lateMigrations := []migration{
		{20, migrateV19ToV20},
		{21, migrateV20ToV21},
		{22, migrateV21ToV22},
		{23, migrateV22ToV23},
		{24, migrateV23ToV24},
	}

	for _, m := range lateMigrations {
		if from < m.version {
			if err := m.fn(conn); err != nil {
				return fmt.Errorf("migrate to v%d: %w", m.version, err)
			}
		}
	}

	// v25 requires *DB to instantiate UserTokenUsageService (daily_token_usage + cached_tokens column).
	if from < 25 {
		if err := migrateV24ToV25WithDB(db); err != nil {
			return fmt.Errorf("migrate to v25: %w", err)
		}
	}

	// v26: migrate singleUser "default" sender IDs to "cli_user"
	if from < 26 {
		if err := migrateV25ToV26(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v26: %w", err)
		}
	}

	// v27: add max_context, max_output_tokens, thinking_mode to user_llm_subscriptions
	if from < 27 {
		if err := migrateV26ToV27(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v27: %w", err)
		}
	}

	// v28: add reasoning_content to session_messages
	if from < 28 {
		if err := migrateV27ToV28(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v28: %w", err)
		}
	}

	// v29: add cached_models to user_llm_subscriptions
	if from < 29 {
		if err := migrateV28ToV29(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v29: %w", err)
		}
	}

	// v30: add user_chats table for multi-chatroom support
	if from < 30 {
		if err := migrateV29ToV30(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v30: %w", err)
		}
	}

	// v31: add context_tokens to session_messages for exact per-message token accounting
	if from < 31 {
		if err := migrateV30ToV31(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v31: %w", err)
		}
	}

	// v32: add per_model_configs to user_llm_subscriptions for per-model token settings
	if from < 32 {
		if err := migrateV31ToV32(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v32: %w", err)
		}
	}

	// v33: clean orphaned rows from tables with foreign keys to tenants.
	// Before this version, PRAGMA foreign_keys was OFF, so ON DELETE CASCADE never fired.
	// This migration removes all orphaned data and then VACUUMs to reclaim disk space.
	if from < 33 {
		if err := migrateV32ToV33(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v33: %w", err)
		}
	}

	// v34: add subscription_id and model to tenants (session→subscription mapping).
	if from < 34 {
		if err := migrateV33ToV34(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v34: %w", err)
		}
	}

	// v35: extract model data into subscription_models table.
	if from < 35 {
		if err := migrateV34ToV35(db); err != nil {
			return fmt.Errorf("migrate to v35: %w", err)
		}
	}

	// v36: add api_type column to user_llm_subscriptions
	if from < 36 {
		if err := migrateV35ToV36(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v36: %w", err)
		}
	}

	// v37: add api_type column to subscription_models table
	if from < 37 {
		if err := migrateV36ToV37(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v37: %w", err)
		}
	}

	// v38: add runner_id to tenants (session→runner binding)
	if from < 38 {
		if err := migrateV37ToV38(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v38: %w", err)
		}
	}

	// v39: model-first subscription redesign foundation.
	// Adds subscription_models.enabled (model disable), user_default_model table,
	// backfills concrete model rows for tenants-referenced (sub, model) pairs, and
	// seeds per-user default model selection.
	if from < 39 {
		if err := migrateV38ToV39(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v39: %w", err)
		}
	}

	// v40: subscription-level enabled flag. A disabled subscription stops
	// contributing models to the picker without deleting its credentials.
	if from < 40 {
		if err := migrateV39ToV40(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v40: %w", err)
		}
	}

	// v41: drop the legacy user_llm_configs table (dead since v24).
	if from < 41 {
		if err := migrateV40ToV41(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v41: %w", err)
		}
	}

	// v42: drop the redundant per_model_configs JSON column (subscription_models
	// table is the sole source for per-model config).
	if from < 42 {
		if err := migrateV41ToV42(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v42: %w", err)
		}
	}

	// v43: drop the redundant is_default column (default derived from user_default_model).
	if from < 43 {
		if err := migrateV42ToV43(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v43: %w", err)
		}
	}

	// v44: add is_system column to user_llm_subscriptions. A system subscription
	// (sender_id="__system__", is_system=1) is reconciled from config/env at boot
	// and acts as the shared default/fallback LLM source visible to all users.
	if from < 44 {
		if err := migrateV43ToV44(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v44: %w", err)
		}
	}

	// v45: Canonical User Identity system.
	// Creates `users` + `user_identities` + `link_codes` tables, adds `user_id INTEGER`
	// columns to all asset tables, and backfills from existing sender_id-based data.
	if from < 45 {
		if err := migrateV44ToV45(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v45: %w", err)
		}
	}

	// v46: Re-backfill user_id for rows added after v45 migration.
	// The v45 migration backfills user_id from sender_id → user_identities.
	// However, subscriptions/settings added AFTER v45 (via old code that
	// doesn't write user_id) will have user_id=0. This migration re-runs
	// the same backfill to catch any rows that were missed.
	if from < 46 {
		if err := migrateV45ToV46(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v46: %w", err)
		}
	}

	// v47: pending_resumes table for graceful shutdown agent loop resume.
	if from < 47 {
		if err := migrateV46ToV47(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v47: %w", err)
		}
	}

	// v48: add sort_order column to user_chats for drag-and-drop reordering.
	if from < 48 {
		if err := migrateV47ToV48(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v48: %w", err)
		}
	}

	// v49: fix cancelled-turn messages that were incorrectly marked display_only=1.
	// These messages carry Detail (iteration history) that GetAllMessages must
	// return so ConvertMessagesToHistory can parse them. With display_only=1,
	// the detail is lost and the UI renders duplicate/merged turns.
	if from < 49 {
		if err := migrateV48ToV49(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v49: %w", err)
		}
	}

	// v50: add turn_id column to session_messages for turn-scoped dedup.
	// IncrementalPersist writes mid-turn messages; reload fetches them as
	// committed history while the live store still has the same turn's
	// progress. Without turn_id, the frontend can't tell if a committed
	// message is from the current turn (suppress liveMessage) or a previous
	// turn (show both). This caused the "history and live duplicate" bug.
	if from < 50 {
		if err := migrateV49ToV50(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v50: %w", err)
		}
	}

	// v51: upgrade Detail JSON iteration numbers from 0-based to 1-based.
	// The engine's Run loop now uses 1-based iteration numbers (first = 1).
	// Old Detail JSON had 0-based numbers (first = 0). This migration rewrites
	// all Detail JSON to be 1-based, ensuring consistency across old and new data.
	if from < 51 {
		if err := migrateV50ToV51(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v51: %w", err)
		}
	}

	// v52: make session_messages an append-only history log by adding
	// record_type, target_history_id, and record_data columns. Existing rows
	// are the migration baseline and remain ordinary message records.
	if from < 52 {
		if err := migrateV51ToV52(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v52: %w", err)
		}
	}

	// v53: move session CWD persistence from files (~/.xbot/session_cwd/*.txt)
	// into the tenants table. File-based CWD was unreliable (Cd in
	// sessionless/SubAgent context only mutated in-memory InitialCWD, and the
	// file key could mismatch the tenant's channel:chatID), so after a restart
	// every session fell back to "~". The DB is authoritative.
	if from < 53 {
		if err := migrateV52ToV53(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v53: %w", err)
		}
	}

	// v54: structured iteration history table. Replaces Detail JSON for
	// iteration data — every intermediate assistant message now has its
	// iteration record (iter id, reasoning, tools) in a dedicated table,
	// not just the final assistant message's Detail blob. This fixes the
	// "missing iterations after reload" bug where intermediate tool_calls
	// assistant messages had no Detail (iter id lost).
	//
	// NOTE: v54 was previously used for identity merge. This migration uses v55
	// to avoid collision — databases already at v54 (from identity merge)
	// will run this migration and create the iteration_history table.
	if from < 55 {
		if err := migrateV54ToV55(db.Conn()); err != nil {
			return fmt.Errorf("migrate to v55: %w", err)
		}
	}

	// v56: remove FK constraint from iteration_history (message_id=0 is valid —
	// iteration records are linked by turn_id, not message_id). The FK
	// constraint caused "FOREIGN KEY constraint failed" when writeIterationHistory
	// inserted message_id=0 (no associated session_messages row). This silently
	// dropped ALL iteration_history writes — iterations 1-38+ were lost.
	if from < 56 {
		if err := migrateV55ToV56(conn); err != nil {
			return fmt.Errorf("migrate to v56: %w", err)
		}
	}

	// v57: per-iteration LLM metrics (tokens / TTFT / tokens-per-sec / total ms)
	// on iteration_history. Enables the iteration-stats plugin to show per-
	// iteration token count & timing even after a reload.
	if from < 57 {
		if err := migrateV56ToV57(conn); err != nil {
			return fmt.Errorf("migrate to v57: %w", err)
		}
	}

	// v58: add per-iteration TPOT (time per output token) to iteration_history.
	if from < 58 {
		if err := migrateV57ToV58(conn); err != nil {
			return fmt.Errorf("migrate to v58: %w", err)
		}
	}

	// v59: add per-iteration LLM usage (input/prompt-cache-hit tokens + model)
	// to iteration_history. Enables per-session / per-model / per-day usage
	// aggregation (cache hit rate, input vs output split) straight from the
	// iteration table — no separate session-level ledger needed.
	if from < 59 {
		if err := migrateV58ToV59(conn); err != nil {
			return fmt.Errorf("migrate to v59: %w", err)
		}
	}

	// v60: partial index for session_messages control records
	// (record_type != 'message': ask_question/ask_answer/mask/context_edit...).
	// The partial WHERE clause keeps the message hot-path INSERT zero-cost
	// (plain messages never enter the index) while control-record lookups
	// (ask_answer anti-join by tenant_id+record_type+target_history_id) hit
	// the index instead of a full tenant scan.
	if from < 60 {
		if err := migrateV59ToV60(conn); err != nil {
			return fmt.Errorf("migrate to v60: %w", err)
		}
	}

	// v61: two lookup-path indexes.
	// - idx_cron_jobs_user: ListJobsByUserID (web cron panel) filters by
	//   user_id; AddJob now persists the caller's canonical user id (M1).
	// - idx_sm_tenant_role_id: the ListUserChats preview subquery scans
	//   (tenant_id, role, id DESC) per tenant; the partial WHERE keeps tool rows
	//   out of the index.
	if from < 61 {
		if err := migrateV60ToV61(conn); err != nil {
			return fmt.Errorf("migrate to v61: %w", err)
		}
	}

	// v62: remove the system subscription (v44) and the is_system column —
	// model-subscription integration. The global fallback LLM returns to the
	// in-memory defaultLLM built from cfg.LLM; every persisted model reference
	// must carry its owning subscription id.
	if from < 62 {
		if err := migrateV61ToV62(db); err != nil {
			return fmt.Errorf("migrate to v62: %w", err)
		}
	}

	// v63: remove the multi-user identity system — the canonical user layer
	// (users/user_identities/link_codes) is dropped and all per-user data is
	// collapsed to the single operator ("cli_user" sender / user_id=1).
	if from < 63 {
		if err := migrateV62ToV63(db); err != nil {
			return fmt.Errorf("migrate to v63: %w", err)
		}
	}

	return nil
}

// migrateV61ToV62 removes the system subscription (v44) and its is_system
// column, and adds subscription_id to iteration_history — the
// model-subscription integration migration. The global fallback LLM returns to
// the in-memory defaultLLM built from cfg.LLM; every persisted model reference
// must carry its owning subscription id.
//
// Steps (order matters):
//  1. Back up the DB file once (VACUUM INTO, file DBs only). Failure aborts
//     the migration — DROP COLUMN is irreversible, user data safety first.
//  2. Reference cleanup: user_default_model / tenants / subscription_models /
//     user_identities rows pointing at the system subscription.
//  3. Delete the system row itself.
//  4. Tier cleanup: tier_vanguard/tier_balance/tier_swift values referencing
//     the deleted system subscription (system|model) or legacy bare model
//     names are re-resolved to a user-owned (subID|model) pair; unresolvable
//     values are removed so the tier fallback chain takes over.
//  5. Drop the is_system column (idempotent via columnExists).
//  6. Add iteration_history.subscription_id (idempotent via columnExists).
//
// Idempotent: re-running is a no-op (DELETEs match nothing, columnExists
// guards both ALTERs, already-rewritten tier values are skipped).
func migrateV61ToV62(db *DB) error {
	conn := db.Conn()

	// 1. One-time backup (file DBs only). VACUUM INTO requires the target not
	// to exist; a leftover backup from an interrupted run is kept as-is.
	if db.path != "" && db.path != ":memory:" {
		backup := db.path + ".pre-v62.bak"
		if _, statErr := os.Stat(backup); os.IsNotExist(statErr) {
			escaped := strings.ReplaceAll(backup, "'", "''")
			if _, err := conn.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
				return fmt.Errorf("migrate v61->v62 backup to %s: %w", backup, err)
			}
			log.WithField("backup", backup).Info("v62: database backed up before system-subscription removal")
		}
	}

	// 2. Reference cleanup (before row deletion so tier re-resolution in step 4
	// can never pick the system subscription as the new owner).
	if _, err := conn.Exec(`DELETE FROM user_default_model WHERE subscription_id = 'system'`); err != nil {
		return fmt.Errorf("migrate v61->v62 clean user_default_model: %w", err)
	}
	if _, err := conn.Exec(`UPDATE tenants SET subscription_id = '' WHERE subscription_id = 'system'`); err != nil {
		return fmt.Errorf("migrate v61->v62 clean tenants: %w", err)
	}
	if _, err := conn.Exec(`DELETE FROM subscription_models WHERE subscription_id = 'system'`); err != nil {
		return fmt.Errorf("migrate v61->v62 clean subscription_models: %w", err)
	}
	if _, err := conn.Exec(`DELETE FROM user_identities WHERE channel = 'system' AND channel_user_id = '__system__'`); err != nil {
		return fmt.Errorf("migrate v61->v62 clean user_identities: %w", err)
	}

	// 3. Delete the system row. id='system' alone is sufficient (the row id is
	// fixed by UpsertSystemSubscription) and stays valid after the is_system
	// column is dropped (idempotent re-runs would fail on the missing column).
	if _, err := conn.Exec(`DELETE FROM user_llm_subscriptions WHERE id = 'system'`); err != nil {
		return fmt.Errorf("migrate v61->v62 delete system subscription: %w", err)
	}

	// 4. Tier cleanup.
	if err := migrateTierValuesV62(conn); err != nil {
		return fmt.Errorf("migrate v61->v62 tier cleanup: %w", err)
	}

	// 5. Drop the is_system column.
	sysCol, err := columnExists(conn, "user_llm_subscriptions", "is_system")
	if err != nil {
		return fmt.Errorf("migrate v61->v62 check is_system: %w", err)
	}
	if sysCol {
		if _, err := conn.Exec(`ALTER TABLE user_llm_subscriptions DROP COLUMN is_system`); err != nil {
			return fmt.Errorf("migrate v61->v62 drop is_system: %w", err)
		}
	}

	// 6. iteration_history.subscription_id (model-subscription integration).
	subCol, err := columnExists(conn, "iteration_history", "subscription_id")
	if err != nil {
		return fmt.Errorf("migrate v61->v62 check subscription_id: %w", err)
	}
	if !subCol {
		if _, err := conn.Exec(`ALTER TABLE iteration_history ADD COLUMN subscription_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate v61->v62 add subscription_id: %w", err)
		}
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 62"); err != nil {
		return fmt.Errorf("migrate v61->v62 update version: %w", err)
	}
	log.Info("Database migrated to v62 (system subscription removed; iteration_history.subscription_id added)")
	return nil
}

// migrateTierValuesV62 rewrites tier_* settings values that reference the
// deleted system subscription (system|model) or are legacy bare model names
// into "subID|model" pairs owned by user subscriptions. Values whose model
// cannot be resolved to any subscription are deleted (the tier fallback
// chain takes over). Runs AFTER the system row deletion so the system
// subscription is never picked as the new owner.
func migrateTierValuesV62(conn *sql.DB) error {
	rows, err := conn.Query(`SELECT rowid, value FROM user_settings WHERE key IN ('tier_vanguard','tier_balance','tier_swift')`)
	if err != nil {
		return fmt.Errorf("list tier settings: %w", err)
	}
	type tierRow struct {
		rowid int64
		value string
	}
	var tiers []tierRow
	for rows.Next() {
		var r tierRow
		if err := rows.Scan(&r.rowid, &r.value); err != nil {
			rows.Close()
			return fmt.Errorf("scan tier setting: %w", err)
		}
		tiers = append(tiers, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, t := range tiers {
		val := strings.TrimSpace(t.value)
		if val == "" {
			continue
		}
		if idx := strings.Index(val, "|"); idx >= 0 {
			if val[:idx] != "system" {
				continue // already a user-owned pair — leave as-is
			}
			// system|model → bare model, re-resolve below.
			val = val[idx+1:]
			if val == "" {
				if _, err := conn.Exec(`DELETE FROM user_settings WHERE rowid = ?`, t.rowid); err != nil {
					return fmt.Errorf("delete empty system tier value: %w", err)
				}
				continue
			}
		}
		// Bare model name → resolve an owning subscription.
		owner := resolveTierOwnerV62(conn, val)
		if owner == "" {
			if _, err := conn.Exec(`DELETE FROM user_settings WHERE rowid = ?`, t.rowid); err != nil {
				return fmt.Errorf("delete unresolvable tier value: %w", err)
			}
			log.WithField("model", val).Info("v62: tier value had no owning subscription; removed")
			continue
		}
		if _, err := conn.Exec(`UPDATE user_settings SET value = ?, updated_at = ? WHERE rowid = ?`,
			owner+"|"+val, time.Now().Unix(), t.rowid); err != nil {
			return fmt.Errorf("rewrite tier value: %w", err)
		}
		log.WithFields(log.Fields{"owner": owner, "model": val}).Info("v62: tier value re-paired with owning subscription")
	}
	return nil
}

// resolveTierOwnerV62 finds a subscription that owns the model (SQL-level
// mirror of ResolveSubscriptionForModel's passes): enabled subscription_models
// rows first, then subscription default models, then cached model lists. The
// system row is already deleted when this runs.
func resolveTierOwnerV62(conn *sql.DB, model string) string {
	var owner string
	// Pass 1: enabled subscription_models rows.
	if err := conn.QueryRow(`SELECT subscription_id FROM subscription_models WHERE model = ? AND enabled = 1 LIMIT 1`, model).Scan(&owner); err == nil {
		return owner
	}
	// Pass 2: subscription default model.
	if err := conn.QueryRow(`SELECT id FROM user_llm_subscriptions WHERE model = ? AND enabled = 1 LIMIT 1`, model).Scan(&owner); err == nil {
		return owner
	}
	// Pass 3: cached models (JSON array — the quoted pattern is an exact match).
	pattern := `%"` + model + `"%`
	if err := conn.QueryRow(`SELECT id FROM user_llm_subscriptions WHERE enabled = 1 AND cached_models LIKE ? LIMIT 1`, pattern).Scan(&owner); err == nil {
		return owner
	}
	return ""
}

// migrateV62ToV63 removes the multi-user identity system: the canonical user
// layer (users/user_identities/link_codes) is dropped and all per-user data
// is collapsed to the single operator ("cli_user" sender / user_id=1).
//
// What changes:
//   - identity tables: users, user_identities, link_codes → DROP TABLE
//   - INTEGER canonical columns → DROP: tenants.owner_user_id,
//     runners.owner_user_id, user_llm_subscriptions.user_id,
//     user_settings.user_id, user_default_model.user_id, user_chats.user_id,
//     cron_jobs.user_id, event_triggers.user_id
//   - TEXT sender collapse → 'cli_user' (conflict rows deleted/renamed
//     first to respect UNIQUE constraints): user_settings, user_chats,
//     runners, user_default_model, user_profiles, runner_tokens
//   - token usage: per-sender rows SUM-merged into the operator row
//   - xbot_short/long_term_memories.user_id → 1 (column KEPT — the memory
//     provider scopes on it; single operator = constant 1)
//
// Idempotent: every DELETE matches nothing on re-run, UNIQUE-conflict
// resolution is order-independent, columnExists guards every DROP, and
// DROP TABLE IF EXISTS never fails on re-run.
func migrateV62ToV63(db *DB) error {
	conn := db.Conn()

	// 1. One-time backup (file DBs only). VACUUM INTO requires the target
	// not to exist; a leftover backup from an interrupted run is kept as-is.
	if db.path != "" && db.path != ":memory:" {
		backup := db.path + ".pre-v63.bak"
		if _, statErr := os.Stat(backup); os.IsNotExist(statErr) {
			escaped := strings.ReplaceAll(backup, "'", "''")
			if _, err := conn.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
				return fmt.Errorf("migrate v62->v63 backup to %s: %w", backup, err)
			}
			log.WithField("backup", backup).Info("v63: database backed up before multi-user removal")
		}
	}

	// 2. Resolve UNIQUE conflicts BEFORE collapsing senders to 'cli_user'.
	//    The operator's own row always wins (cli_user is the canonical
	//    operator identity post-collapse).
	//    Every step is guarded by tableExists: old migration-test fixtures
	//    hand-build partial schemas (e.g. a v47 fixture without runners) —
	//    tables the DB never had are simply skipped.

	// 2a. user_settings UNIQUE(channel, sender_id, key): drop non-operator
	//     rows that collide with a cli_user row on (channel, key).
	if ok, err := tableExists(conn, "user_settings"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_settings: %w", err)
	} else if ok {
		if _, err := conn.Exec(`DELETE FROM user_settings WHERE sender_id != 'cli_user'
			AND EXISTS (SELECT 1 FROM user_settings o
				WHERE o.sender_id = 'cli_user'
				AND o.channel = user_settings.channel AND o.key = user_settings.key)`); err != nil {
			return fmt.Errorf("migrate v62->v63 dedupe user_settings: %w", err)
		}
	}

	// 2b. user_chats UNIQUE(channel, sender_id, chat_id): drop non-operator
	//     rows colliding on (channel, chat_id) — keep the operator's row.
	if ok, err := tableExists(conn, "user_chats"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_chats: %w", err)
	} else if ok {
		if _, err := conn.Exec(`DELETE FROM user_chats WHERE sender_id != 'cli_user'
			AND EXISTS (SELECT 1 FROM user_chats o
				WHERE o.sender_id = 'cli_user'
				AND o.channel = user_chats.channel AND o.chat_id = user_chats.chat_id)`); err != nil {
			return fmt.Errorf("migrate v62->v63 dedupe user_chats: %w", err)
		}
	}

	// 2c. runners UNIQUE(user_id, name): rename rows that would collide with
	//     the operator's runner names (suffix with the old user_id).
	if ok, err := tableExists(conn, "runners"); err != nil {
		return fmt.Errorf("migrate v62->v63 check runners: %w", err)
	} else if ok {
		if _, err := conn.Exec(`UPDATE runners SET name = name || '_' || user_id
			WHERE user_id != 'cli_user'
			AND EXISTS (SELECT 1 FROM runners o WHERE o.user_id = 'cli_user' AND o.name = runners.name)`); err != nil {
			return fmt.Errorf("migrate v62->v63 dedupe runners: %w", err)
		}
	}

	// 2d. user_default_model PK=sender_id: keep the freshest row only
	//     (ORDER BY updated_at DESC — mirrors the old multi-row tolerance of
	//     GetUserDefaultModelByUserID).
	if ok, err := tableExists(conn, "user_default_model"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_default_model: %w", err)
	} else if ok {
		if _, err := conn.Exec(`DELETE FROM user_default_model WHERE sender_id != (
			SELECT sender_id FROM user_default_model ORDER BY updated_at DESC LIMIT 1)`); err != nil {
			return fmt.Errorf("migrate v62->v63 dedupe user_default_model: %w", err)
		}
	}

	// 2e. user_profiles PK=sender_id: keep the operator's row; when absent,
	//     promote one surviving row to the operator identity, then drop the rest.
	if ok, err := tableExists(conn, "user_profiles"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_profiles: %w", err)
	} else if ok {
		if err := collapseSingleSenderTable(conn, "user_profiles", "sender_id"); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse user_profiles: %w", err)
		}
	}

	// 2f. runner_tokens PK=user_id: same single-row collapse.
	if ok, err := tableExists(conn, "runner_tokens"); err != nil {
		return fmt.Errorf("migrate v62->v63 check runner_tokens: %w", err)
	} else if ok {
		if err := collapseSingleSenderTable(conn, "runner_tokens", "user_id"); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse runner_tokens: %w", err)
		}
	}

	// 2g. user_token_usage PK=sender_id: SUM-merge non-operator rows into the
	//     operator row (UPSERT), then delete them.
	//     HAVING COUNT(*) > 0 guards the empty case — an aggregate SELECT
	//     without GROUP BY emits one NULL row on an empty set, which would
	//     violate the NOT NULL columns.
	if ok, err := tableExists(conn, "user_token_usage"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_token_usage: %w", err)
	} else if ok {
		if _, err := conn.Exec(`INSERT INTO user_token_usage
				(sender_id, input_tokens, output_tokens, total_tokens, cached_tokens, conversation_count, llm_call_count, updated_at)
			SELECT 'cli_user', SUM(input_tokens), SUM(output_tokens), SUM(total_tokens), SUM(cached_tokens),
				SUM(conversation_count), SUM(llm_call_count), MAX(updated_at)
			FROM user_token_usage WHERE sender_id != 'cli_user'
			HAVING COUNT(*) > 0
			ON CONFLICT(sender_id) DO UPDATE SET
				input_tokens = user_token_usage.input_tokens + excluded.input_tokens,
				output_tokens = user_token_usage.output_tokens + excluded.output_tokens,
				total_tokens = user_token_usage.total_tokens + excluded.total_tokens,
				cached_tokens = user_token_usage.cached_tokens + excluded.cached_tokens,
				conversation_count = user_token_usage.conversation_count + excluded.conversation_count,
				llm_call_count = user_token_usage.llm_call_count + excluded.llm_call_count,
				updated_at = excluded.updated_at`); err != nil {
			return fmt.Errorf("migrate v62->v63 merge user_token_usage: %w", err)
		}
		if _, err := conn.Exec(`DELETE FROM user_token_usage WHERE sender_id != 'cli_user'`); err != nil {
			return fmt.Errorf("migrate v62->v63 prune user_token_usage: %w", err)
		}
	}

	// 2h. daily_token_usage PK(date, sender_id, model): SUM-merge per
	//     (date, model) into the operator row, then delete the rest.
	if ok, err := tableExists(conn, "daily_token_usage"); err != nil {
		return fmt.Errorf("migrate v62->v63 check daily_token_usage: %w", err)
	} else if ok {
		if _, err := conn.Exec(`INSERT INTO daily_token_usage
				(date, sender_id, model, input_tokens, output_tokens, cached_tokens, conversation_count, llm_call_count, updated_at)
			SELECT date, 'cli_user', model, SUM(input_tokens), SUM(output_tokens), SUM(cached_tokens),
				SUM(conversation_count), SUM(llm_call_count), MAX(updated_at)
			FROM daily_token_usage WHERE sender_id != 'cli_user' GROUP BY date, model
			ON CONFLICT(date, sender_id, model) DO UPDATE SET
				input_tokens = daily_token_usage.input_tokens + excluded.input_tokens,
				output_tokens = daily_token_usage.output_tokens + excluded.output_tokens,
				cached_tokens = daily_token_usage.cached_tokens + excluded.cached_tokens,
				conversation_count = daily_token_usage.conversation_count + excluded.conversation_count,
				llm_call_count = daily_token_usage.llm_call_count + excluded.llm_call_count,
				updated_at = excluded.updated_at`); err != nil {
			return fmt.Errorf("migrate v62->v63 merge daily_token_usage: %w", err)
		}
		if _, err := conn.Exec(`DELETE FROM daily_token_usage WHERE sender_id != 'cli_user'`); err != nil {
			return fmt.Errorf("migrate v62->v63 prune daily_token_usage: %w", err)
		}
	}

	// 3. Collapse senders to the operator identity. The UNIQUE conflicts
	//    were resolved in step 2 — these UPDATEs cannot violate constraints.
	//    Subscriptions: the legacy sender_id column (v22-era creator sender)
	//    is the List(senderID) query dimension post-v63 — collapse it too so
	//    the operator sees every subscription regardless of who created it.
	if ok, err := tableExists(conn, "user_llm_subscriptions"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_llm_subscriptions: %w", err)
	} else if ok {
		if _, err := conn.Exec(`UPDATE user_llm_subscriptions SET sender_id = 'cli_user' WHERE sender_id != 'cli_user'`); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse subscription senders: %w", err)
		}
	}
	if ok, err := tableExists(conn, "user_settings"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_settings: %w", err)
	} else if ok {
		if _, err := conn.Exec(`UPDATE user_settings SET sender_id = 'cli_user' WHERE sender_id != 'cli_user'`); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse user_settings sender: %w", err)
		}
	}
	if ok, err := tableExists(conn, "user_chats"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_chats: %w", err)
	} else if ok {
		if _, err := conn.Exec(`UPDATE user_chats SET sender_id = 'cli_user' WHERE sender_id != 'cli_user'`); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse user_chats sender: %w", err)
		}
	}
	if ok, err := tableExists(conn, "runners"); err != nil {
		return fmt.Errorf("migrate v62->v63 check runners: %w", err)
	} else if ok {
		if _, err := conn.Exec(`UPDATE runners SET user_id = 'cli_user' WHERE user_id != 'cli_user'`); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse runners user: %w", err)
		}
	}
	if ok, err := tableExists(conn, "user_default_model"); err != nil {
		return fmt.Errorf("migrate v62->v63 check user_default_model: %w", err)
	} else if ok {
		if _, err := conn.Exec(`UPDATE user_default_model SET sender_id = 'cli_user' WHERE sender_id != 'cli_user'`); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse user_default_model sender: %w", err)
		}
	}

	// 4. xbot memories: collapse the canonical user_id to the single
	//    operator (rows UNION — no unique constraint on user_id).
	//    The column is KEPT: memory scopeWhere reads it; every row is now
	//    the same operator (user_id=1).
	if ok, err := tableExists(conn, "xbot_short_term_memories"); err != nil {
		return fmt.Errorf("migrate v62->v63 check xbot_short_term_memories: %w", err)
	} else if ok {
		if _, err := conn.Exec(`UPDATE xbot_short_term_memories SET user_id = 1 WHERE user_id != 1`); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse xbot_short_term_memories: %w", err)
		}
	}
	if ok, err := tableExists(conn, "xbot_long_term_memories"); err != nil {
		return fmt.Errorf("migrate v62->v63 check xbot_long_term_memories: %w", err)
	} else if ok {
		if _, err := conn.Exec(`UPDATE xbot_long_term_memories SET user_id = 1 WHERE user_id != 1`); err != nil {
			return fmt.Errorf("migrate v62->v63 collapse xbot_long_term_memories: %w", err)
		}
	}

	// 5. Drop the canonical INTEGER columns (guarded — idempotent re-runs).
	// cron_jobs.user_id must drop its index first (idx_cron_jobs_user
	// references the column — SQLite refuses DROP COLUMN while an index
	// depends on it).
	if ok, err := tableExists(conn, "cron_jobs"); err != nil {
		return fmt.Errorf("migrate v62->v63 check cron_jobs: %w", err)
	} else if ok {
		if _, err := conn.Exec(`DROP INDEX IF EXISTS idx_cron_jobs_user`); err != nil {
			return fmt.Errorf("migrate v62->v63 drop idx_cron_jobs_user: %w", err)
		}
	}
	userIDCols := [][2]string{
		{"tenants", "owner_user_id"},
		{"runners", "owner_user_id"},
		{"user_llm_subscriptions", "user_id"},
		{"user_settings", "user_id"},
		{"user_default_model", "user_id"},
		{"user_chats", "user_id"},
		{"cron_jobs", "user_id"},
		{"event_triggers", "user_id"},
	}
	for _, tc := range userIDCols {
		exists, err := columnExists(conn, tc[0], tc[1])
		if err != nil {
			return fmt.Errorf("migrate v62->v63 check %s.%s: %w", tc[0], tc[1], err)
		}
		if !exists {
			continue
		}
		if _, err := conn.Exec(fmt.Sprintf(`ALTER TABLE %s DROP COLUMN %s`, tc[0], tc[1])); err != nil {
			return fmt.Errorf("migrate v62->v63 drop %s.%s: %w", tc[0], tc[1], err)
		}
	}

	// 6. Drop the identity tables (children first — they FK-reference users).
	for _, table := range []string{"user_identities", "link_codes", "users"} {
		if _, err := conn.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			return fmt.Errorf("migrate v62->v63 drop %s: %w", table, err)
		}
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 63"); err != nil {
		return fmt.Errorf("migrate v62->v63 update version: %w", err)
	}
	log.Info("Database migrated to v63 (multi-user identity system removed — single operator)")
	return nil
}

// collapseSingleSenderTable keeps exactly one row of a sender-keyed table,
// normalized to the operator 'cli_user'. The operator's own row wins; when
// absent, the first surviving row (lowest rowid) is promoted. All other
// rows are deleted. col is the TEXT sender column ('sender_id' for
// user_profiles, 'user_id' for runner_tokens).
func collapseSingleSenderTable(conn *sql.DB, table, col string) error {
	// Promote one row to the operator identity when the operator has none.
	if _, err := conn.Exec(fmt.Sprintf(`UPDATE %s SET %s = 'cli_user' WHERE rowid = (
		SELECT rowid FROM %s WHERE %s != 'cli_user' ORDER BY rowid LIMIT 1)
		AND NOT EXISTS (SELECT 1 FROM %s WHERE %s = 'cli_user')`, table, col, table, col, table, col)); err != nil {
		return fmt.Errorf("promote %s operator row: %w", table, err)
	}
	// Drop every non-operator row.
	if _, err := conn.Exec(fmt.Sprintf(`DELETE FROM %s WHERE %s != 'cli_user'`, table, col)); err != nil {
		return fmt.Errorf("prune %s: %w", table, err)
	}
	return nil
}

// tableExists reports whether the table exists in the current schema.
// Migration v63 guards every per-table step with this: old fixtures
// (hand-built partial schemas in migration tests, e.g. a v47 fixture
// without runners) skip the steps for tables they never had.
func tableExists(conn *sql.DB, table string) (bool, error) {
	var count int
	if err := conn.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table,
	).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// migrateV60ToV61 adds the user_id index on cron_jobs (ListJobsByUserID lookup
// path, backing the web cron panel) and the partial role index on
// session_messages (ListUserChats preview subquery).
// Idempotent: CREATE INDEX IF NOT EXISTS (safe to re-run on a schema.go-built
// DB that already has both indexes from the v61 DDL).
func migrateV60ToV61(conn *sql.DB) error {
	if _, err := conn.Exec(`CREATE INDEX IF NOT EXISTS idx_cron_jobs_user ON cron_jobs(user_id)`); err != nil {
		return fmt.Errorf("migrate v60->v61 create idx_cron_jobs_user: %w", err)
	}
	if _, err := conn.Exec(`CREATE INDEX IF NOT EXISTS idx_sm_tenant_role_id ON session_messages(tenant_id, role, id) WHERE role IN ('user','assistant')`); err != nil {
		return fmt.Errorf("migrate v60->v61 create idx_sm_tenant_role_id: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 61"); err != nil {
		return fmt.Errorf("migrate v60->v61 update version: %w", err)
	}
	log.Info("Database migrated to v61 (idx_cron_jobs_user + idx_sm_tenant_role_id)")
	return nil
}

// migrateV59ToV60 creates the partial index for session_messages control
// records. Only non-'message' rows (ask_question/ask_answer answers, mask
// markers, context snapshots) enter the index, so plain-message INSERTs pay
// zero index-maintenance cost. Control-record lookups by
// (tenant_id, record_type, target_history_id) — the ask_answer anti-join in
// AppendAskAnswerWithUserMessage and Replay — use this index.
// Idempotent: CREATE INDEX IF NOT EXISTS (safe to re-run on a schema.go-built
// DB that already has the index from the v60 DDL).
func migrateV59ToV60(conn *sql.DB) error {
	if _, err := conn.Exec(`CREATE INDEX IF NOT EXISTS idx_sm_tenant_record ON session_messages(tenant_id, record_type, target_history_id) WHERE record_type != 'message'`); err != nil {
		return fmt.Errorf("migrate v59->v60 create partial index: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 60"); err != nil {
		return fmt.Errorf("migrate v59->v60 update version: %w", err)
	}
	log.Info("Database migrated to v60 (partial index for control records)")
	return nil
}

// migrateV57ToV58 adds the per-iteration TPOT column to iteration_history.
// SQLite ALTER TABLE ADD COLUMN with a NOT NULL DEFAULT is cheap and preserves
// existing rows (tpot_ms defaults to 0 for pre-v58 iterations). Idempotent: the
// column may already exist when a test/fixture set schema_version to 57 but the
// table was created by the current createSchema DDL (which already includes it).
func migrateV57ToV58(conn *sql.DB) error {
	exists, err := columnExists(conn, "iteration_history", "tpot_ms")
	if err == nil && !exists {
		if _, err := conn.Exec("ALTER TABLE iteration_history ADD COLUMN tpot_ms INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("migrate v57->v58 add tpot_ms: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 58"); err != nil {
		return fmt.Errorf("migrate v57->v58 update version: %w", err)
	}
	log.Info("Database migrated to v58 (added tpot_ms to iteration_history)")
	return nil
}

// migrateV58ToV59 adds per-iteration LLM usage columns to iteration_history:
// input_tokens (prompt tokens), cached_tokens (prompt-cache hit tokens), and
// model (LLM model name used for that iteration). This makes iteration_history
// the single source for usage/perf aggregation — per-session, per-model,
// per-day — without a separate session-level ledger.
// Idempotent: columns may already exist when a test fixture set
// schema_version to 58 but the table was created by the current createSchema
// DDL (which already includes them).
func migrateV58ToV59(conn *sql.DB) error {
	for _, c := range []struct{ name, ddl string }{
		{"input_tokens", "ALTER TABLE iteration_history ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0"},
		{"cached_tokens", "ALTER TABLE iteration_history ADD COLUMN cached_tokens INTEGER NOT NULL DEFAULT 0"},
		{"model", "ALTER TABLE iteration_history ADD COLUMN model TEXT NOT NULL DEFAULT ''"},
	} {
		exists, err := columnExists(conn, "iteration_history", c.name)
		if err == nil && !exists {
			if _, err := conn.Exec(c.ddl); err != nil {
				return fmt.Errorf("migrate v58->v59 add %s: %w", c.name, err)
			}
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 59"); err != nil {
		return fmt.Errorf("migrate v58->v59 update version: %w", err)
	}
	log.Info("Database migrated to v59 (added input_tokens/cached_tokens/model to iteration_history)")
	return nil
}

// migrateV56ToV57 adds per-iteration LLM metrics columns to iteration_history.
// SQLite ALTER TABLE ADD COLUMN with a NOT NULL DEFAULT is cheap and preserves
// existing rows (metrics default to 0 for pre-v57 iterations). Idempotent: the
// columns may already exist when a test/fixture set schema_version to 56 but the
// table was created by the current createSchema DDL (which already includes them).
func migrateV56ToV57(conn *sql.DB) error {
	cols := []string{"tokens", "ttft_ms", "tokens_per_sec", "total_ms"}
	for _, c := range cols {
		exists, err := columnExists(conn, "iteration_history", c)
		if err == nil && !exists {
			if _, err := conn.Exec(fmt.Sprintf("ALTER TABLE iteration_history ADD COLUMN %s INTEGER NOT NULL DEFAULT 0", c)); err != nil {
				return fmt.Errorf("migrate v56->v57 add %s: %w", c, err)
			}
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 57"); err != nil {
		return fmt.Errorf("migrate v56->v57 update version: %w", err)
	}
	log.Info("Database migrated to v57 (added per-iteration metrics to iteration_history)")
	return nil
}

// migrateV55ToV56 recreates iteration_history without the FK constraint.
// The old table had FOREIGN KEY (message_id) REFERENCES session_messages(id)
// which rejected message_id=0 (valid value — iterations are linked by
// turn_id, not message_id). SQLite cannot ALTER TABLE to drop FK, so we
// recreate the table.
func migrateV55ToV56(conn *sql.DB) error {
	migration := `
	-- Save existing data
	CREATE TABLE IF NOT EXISTS _iteration_history_backup AS SELECT * FROM iteration_history;
	-- Drop old table (has FK constraint)
	DROP TABLE IF EXISTS iteration_history;
	-- Recreate without FK
	CREATE TABLE iteration_history (
id INTEGER PRIMARY KEY AUTOINCREMENT,
message_id INTEGER NOT NULL DEFAULT 0,
tenant_id INTEGER NOT NULL,
turn_id INTEGER NOT NULL DEFAULT 0,
iteration INTEGER NOT NULL,
content TEXT NOT NULL DEFAULT '',
reasoning TEXT NOT NULL DEFAULT '',
tools TEXT NOT NULL DEFAULT '[]',
created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_iter_history_msg ON iteration_history(message_id);
CREATE INDEX IF NOT EXISTS idx_iter_history_turn ON iteration_history(tenant_id, turn_id);
-- Restore data (skip FK-violating rows — message_id=0 is now valid)
INSERT INTO iteration_history SELECT * FROM _iteration_history_backup;
-- Cleanup
DROP TABLE _iteration_history_backup;
UPDATE schema_version SET version = 56;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v55->v56: %w", err)
	}
	log.Info("Database migrated to v56 (removed FK constraint from iteration_history)")
	return nil
}

// migrateV54ToV55 creates the iteration_history table for structured
// iteration storage. Existing Detail JSON is left as-is for backward compat.
func migrateV54ToV55(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS iteration_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id INTEGER NOT NULL DEFAULT 0,
  tenant_id INTEGER NOT NULL,
  turn_id INTEGER NOT NULL DEFAULT 0,
  iteration INTEGER NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  reasoning TEXT NOT NULL DEFAULT '',
  tools TEXT NOT NULL DEFAULT '[]',
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_iter_history_msg ON iteration_history(message_id);
CREATE INDEX IF NOT EXISTS idx_iter_history_turn ON iteration_history(tenant_id, turn_id);
UPDATE schema_version SET version = 55;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v54->v55: %w", err)
	}
	log.Info("Database migrated to v55 (added iteration_history table)")
	return nil
}

// migrateV1ToV2 adds the user_profiles table.
func migrateV1ToV2(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS user_profiles (
    sender_id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    profile TEXT NOT NULL DEFAULT '',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
UPDATE schema_version SET version = 2;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v1->v2: %w", err)
	}
	log.Info("Database migrated to v2 (added user_profiles)")
	return nil
}

// migrateV2ToV3 adds core_memory_blocks, archival_memory, and event_history_fts.
func migrateV2ToV3(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS core_memory_blocks (
    tenant_id INTEGER NOT NULL,
    block_name TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    char_limit INTEGER NOT NULL DEFAULT 2000,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, block_name),
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS archival_memory (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    content TEXT NOT NULL,
    embedding BLOB,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_archival_memory_tenant ON archival_memory(tenant_id);

CREATE VIRTUAL TABLE IF NOT EXISTS event_history_fts USING fts5(
    entry,
    content='event_history',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS event_history_ai AFTER INSERT ON event_history BEGIN
    INSERT INTO event_history_fts(rowid, entry) VALUES (new.id, new.entry);
END;

UPDATE schema_version SET version = 3;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v2->v3: %w", err)
	}

	// Backfill FTS index from existing event_history entries
	if _, err := conn.Exec(`INSERT INTO event_history_fts(rowid, entry) SELECT id, entry FROM event_history`); err != nil {
		log.WithError(err).Warn("Failed to backfill event_history_fts (may already be populated)")
	}

	log.Info("Database migrated to v3 (added core_memory_blocks, archival_memory, event_history_fts)")
	return nil
}

// migrateV3ToV4 adds the cron_jobs table.
func migrateV3ToV4(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS cron_jobs (
    id TEXT PRIMARY KEY,
    message TEXT NOT NULL,
    channel TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    sender_id TEXT NOT NULL DEFAULT '',
    cron_expr TEXT,
    every_seconds INTEGER DEFAULT 0,
    delay_seconds INTEGER DEFAULT 0,
    at TEXT,
    created_at DATETIME NOT NULL,
    next_run DATETIME NOT NULL,
    one_shot INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_next_run ON cron_jobs(next_run);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_sender ON cron_jobs(sender_id);

UPDATE schema_version SET version = 4;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v3->v4: %w", err)
	}
	log.Info("Database migrated to v4 (added cron_jobs)")
	return nil
}

// migrateV4ToV5 adds last_trigger column to cron_jobs.
func migrateV4ToV5(conn *sql.DB) error {
	// Check if column already exists before adding
	exists, err := columnExists(conn, "cron_jobs", "last_trigger")
	if err == nil && !exists {
		// Column doesn't exist, add it
		_, err = conn.Exec("ALTER TABLE cron_jobs ADD COLUMN last_trigger DATETIME")
		if err != nil {
			return fmt.Errorf("migrate v4->v5: %w", err)
		}
		log.Info("Database migrated to v5 (added last_trigger to cron_jobs)")
	}
	// Always update version even if column exists (for fresh databases)
	if _, err := conn.Exec("UPDATE schema_version SET version = 5"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v5")
	return nil
}

// migrateV5ToV6 adds the user_llm_configs table.
func migrateV5ToV6(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS user_llm_configs (
    sender_id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    base_url TEXT NOT NULL,
    api_key TEXT NOT NULL,
    model TEXT,
    user_id TEXT,
    enterprise_id TEXT,
    domain TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

UPDATE schema_version SET version = 6;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v5->v6: %w", err)
	}
	log.Info("Database migrated to v6 (added user_llm_configs)")
	return nil
}

// migrateV6ToV8 adds user_id to core_memory_blocks with correct PRIMARY KEY.
// SQLite's ALTER TABLE ADD COLUMN doesn't modify existing PRIMARY KEY.
// Must recreate table to update PRIMARY KEY from (tenant_id, block_name) to (tenant_id, block_name, user_id).
func migrateV6ToV8(conn *sql.DB) error {
	// Step 1: Create new table with correct PRIMARY KEY
	_, err := conn.Exec(`
		CREATE TABLE IF NOT EXISTS core_memory_blocks_new (
			tenant_id INTEGER NOT NULL,
			block_name TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			char_limit INTEGER NOT NULL DEFAULT 2000,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (tenant_id, block_name, user_id),
			FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate v6->v8: create new table: %w", err)
	}

	// Step 2: Copy data from old table (user_id defaults to '' for existing rows)
	_, err = conn.Exec(`
		INSERT INTO core_memory_blocks_new (tenant_id, block_name, user_id, content, char_limit, updated_at)
		SELECT tenant_id, block_name, '', content, char_limit, updated_at
		FROM core_memory_blocks
	`)
	if err != nil {
		return fmt.Errorf("migrate v6->v8: copy data: %w", err)
	}

	// Step 3: Drop old table
	_, err = conn.Exec("DROP TABLE core_memory_blocks")
	if err != nil {
		return fmt.Errorf("migrate v6->v8: drop old table: %w", err)
	}

	// Step 4: Rename new table to original name
	_, err = conn.Exec("ALTER TABLE core_memory_blocks_new RENAME TO core_memory_blocks")
	if err != nil {
		return fmt.Errorf("migrate v6->v8: rename table: %w", err)
	}

	log.Info("Database migrated to v8 (added user_id with correct PRIMARY KEY to core_memory_blocks)")

	// Update schema version
	if _, err := conn.Exec("UPDATE schema_version SET version = 8"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	return nil
}

// migrateV8ToV9 fixes incorrect PRIMARY KEY from buggy v6->v8 migration.
// The buggy migration added user_id column but didn't update PRIMARY KEY.
// This caused PRIMARY KEY to remain (tenant_id, block_name) instead of (tenant_id, block_name, user_id).
func migrateV8ToV9(conn *sql.DB) error {
	// Check if PRIMARY KEY is correct by inspecting pragma_table_info
	var pkCount int
	err := conn.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('core_memory_blocks') WHERE pk > 0
	`).Scan(&pkCount)
	if err != nil {
		return fmt.Errorf("migrate v8->v9: check primary key: %w", err)
	}

	// If pkCount is 2, PRIMARY KEY is wrong (tenant_id, block_name)
	// If pkCount is 3, PRIMARY KEY is correct (tenant_id, block_name, user_id)
	if pkCount == 2 {
		log.Warn("Detected incorrect PRIMARY KEY (2 columns), rebuilding core_memory_blocks table...")

		// Step 1: Create new table with correct PRIMARY KEY
		_, err = conn.Exec(`
			CREATE TABLE core_memory_blocks_new (
				tenant_id INTEGER NOT NULL,
				block_name TEXT NOT NULL,
				user_id TEXT NOT NULL DEFAULT '',
				content TEXT NOT NULL DEFAULT '',
				char_limit INTEGER NOT NULL DEFAULT 2000,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (tenant_id, block_name, user_id),
				FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
			)
		`)
		if err != nil {
			return fmt.Errorf("migrate v8->v9: create new table: %w", err)
		}

		// Step 2: Copy existing data (user_id may already exist or default to '')
		_, err = conn.Exec(`
			INSERT INTO core_memory_blocks_new (tenant_id, block_name, user_id, content, char_limit, updated_at)
			SELECT tenant_id, block_name, COALESCE(user_id, ''), content, char_limit, updated_at
			FROM core_memory_blocks
		`)
		if err != nil {
			return fmt.Errorf("migrate v8->v9: copy data: %w", err)
		}

		// Step 3: Drop old table
		_, err = conn.Exec("DROP TABLE core_memory_blocks")
		if err != nil {
			return fmt.Errorf("migrate v8->v9: drop old table: %w", err)
		}

		// Step 4: Rename new table
		_, err = conn.Exec("ALTER TABLE core_memory_blocks_new RENAME TO core_memory_blocks")
		if err != nil {
			return fmt.Errorf("migrate v8->v9: rename table: %w", err)
		}

		log.Info("Database migrated to v9 (fixed PRIMARY KEY to include user_id)")
	} else {
		log.WithField("pk_count", pkCount).Info("PRIMARY KEY already correct, skipping v9 rebuild")
	}

	// Update schema version
	if _, err := conn.Exec("UPDATE schema_version SET version = 9"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	return nil
}

// migrateV9ToV10 adds max_context column to user_llm_configs.
func migrateV9ToV10(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_configs", "max_context")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE user_llm_configs ADD COLUMN max_context INTEGER DEFAULT 0")
		if err != nil {
			return fmt.Errorf("migrate v9->v10: %w", err)
		}
		log.Info("Database migrated to v10 (added max_context to user_llm_configs)")
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 10"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	return nil
}

// migrateV10ToV11 adds thinking_mode column to user_llm_configs.
func migrateV10ToV11(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_configs", "thinking_mode")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE user_llm_configs ADD COLUMN thinking_mode TEXT DEFAULT ''")
		if err != nil {
			return fmt.Errorf("migrate v10->v11: %w", err)
		}
		log.Info("Database migrated to v11 (added thinking_mode to user_llm_configs)")
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 11"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	return nil
}

// migrateV11ToV12 removes CodeBuddy-specific columns from user_llm_configs.
func migrateV11ToV12(conn *sql.DB) error {
	_, err := conn.Exec(`
		CREATE TABLE user_llm_configs_new (
			sender_id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			base_url TEXT NOT NULL,
			api_key TEXT NOT NULL,
			model TEXT,
			max_context INTEGER DEFAULT 0,
			thinking_mode TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return fmt.Errorf("migrate v11->v12: create new table: %w", err)
	}

	_, err = conn.Exec(`
		INSERT INTO user_llm_configs_new
		(sender_id, provider, base_url, api_key, model, max_context, thinking_mode, created_at, updated_at)
		SELECT sender_id, provider, base_url, api_key, model, COALESCE(max_context, 0), COALESCE(thinking_mode, ''), created_at, updated_at
		FROM user_llm_configs;
	`)
	if err != nil {
		return fmt.Errorf("migrate v11->v12: copy data: %w", err)
	}

	_, err = conn.Exec(`DROP TABLE user_llm_configs;`)
	if err != nil {
		return fmt.Errorf("migrate v11->v12: drop old table: %w", err)
	}

	_, err = conn.Exec(`ALTER TABLE user_llm_configs_new RENAME TO user_llm_configs;`)
	if err != nil {
		return fmt.Errorf("migrate v11->v12: rename table: %w", err)
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 12"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v12 (removed CodeBuddy columns)")
	return nil
}

// migrateV12ToV13 adds shared_registry and user_settings tables.
func migrateV12ToV13(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS shared_registry (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL CHECK(type IN ('skill', 'agent')),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    author      TEXT NOT NULL,
    tags        TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL,
    sharing     TEXT NOT NULL DEFAULT 'private' CHECK(sharing IN ('private', 'public')),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_shared_type_sharing ON shared_registry(type, sharing);
CREATE INDEX IF NOT EXISTS idx_shared_author ON shared_registry(author);

CREATE TABLE IF NOT EXISTS user_settings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    channel    TEXT NOT NULL,
    sender_id  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    UNIQUE(channel, sender_id, key)
);
CREATE INDEX IF NOT EXISTS idx_user_settings_sender ON user_settings(channel, sender_id);

UPDATE schema_version SET version = 13;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v12->v13: %w", err)
	}
	log.Info("Database migrated to v13 (added shared_registry, user_settings)")
	return nil
}

// migrateV13ToV14 adds UNIQUE(type, name, author) constraint to shared_registry.
func migrateV13ToV14(conn *sql.DB) error {
	_, err := conn.Exec(`
		CREATE TABLE shared_registry_new (
		    id          INTEGER PRIMARY KEY AUTOINCREMENT,
		    type        TEXT NOT NULL CHECK(type IN ('skill', 'agent')),
		    name        TEXT NOT NULL,
		    description TEXT NOT NULL DEFAULT '',
		    author      TEXT NOT NULL,
		    tags        TEXT NOT NULL DEFAULT '',
		    source_path TEXT NOT NULL,
		    sharing     TEXT NOT NULL DEFAULT 'private' CHECK(sharing IN ('private', 'public')),
		    created_at  INTEGER NOT NULL,
		    updated_at  INTEGER NOT NULL,
		    UNIQUE(type, name, author)
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate v13->v14: create new table: %w", err)
	}

	_, err = conn.Exec(`
		INSERT INTO shared_registry_new (id, type, name, description, author, tags, source_path, sharing, created_at, updated_at)
		SELECT id, type, name, description, author, tags, source_path, sharing, created_at, updated_at
		FROM shared_registry
	`)
	if err != nil {
		return fmt.Errorf("migrate v13->v14: copy data: %w", err)
	}

	_, err = conn.Exec("DROP TABLE shared_registry")
	if err != nil {
		return fmt.Errorf("migrate v13->v14: drop old table: %w", err)
	}

	_, err = conn.Exec("ALTER TABLE shared_registry_new RENAME TO shared_registry")
	if err != nil {
		return fmt.Errorf("migrate v13->v14: rename table: %w", err)
	}

	_, err = conn.Exec("CREATE INDEX IF NOT EXISTS idx_shared_type_sharing ON shared_registry(type, sharing)")
	if err != nil {
		return fmt.Errorf("migrate v13->v14: create index: %w", err)
	}

	_, err = conn.Exec("CREATE INDEX IF NOT EXISTS idx_shared_author ON shared_registry(author)")
	if err != nil {
		return fmt.Errorf("migrate v13->v14: create index: %w", err)
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 14"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v14 (added UNIQUE constraint to shared_registry)")
	return nil
}

// migrateV14ToV15 adds the runner_tokens table.
func migrateV14ToV15(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS runner_tokens (
    user_id     TEXT PRIMARY KEY,
    token       TEXT NOT NULL,
    mode        TEXT NOT NULL DEFAULT 'native',
    docker_image TEXT NOT NULL DEFAULT '',
    workspace   TEXT NOT NULL DEFAULT '/workspace',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v14->v15: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 15"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v15 (added runner_tokens)")
	return nil
}

// migrateV15ToV16 adds the web_users table.
func migrateV15ToV16(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS web_users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE,
    password   TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v15->v16: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 16"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v16 (added web_users)")
	return nil
}

// migrateV16ToV17 adds the runners table and migrates existing runner_tokens data.
func migrateV16ToV17(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS runners (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    token        TEXT    NOT NULL UNIQUE,
    mode         TEXT    NOT NULL DEFAULT 'native',
    docker_image TEXT    NOT NULL DEFAULT 'ubuntu:22.04',
    workspace    TEXT    NOT NULL DEFAULT '',
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, name)
);
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v16->v17: %w", err)
	}

	// Migrate existing runner_tokens entries into runners table.
	// Each existing user gets a runner named "default".
	_, err := conn.Exec(`
		INSERT OR IGNORE INTO runners (user_id, name, token, mode, docker_image, workspace, created_at)
		SELECT user_id, 'default', token, mode, docker_image, workspace, created_at
		FROM runner_tokens
	`)
	if err != nil {
		log.WithError(err).Warn("Failed to migrate runner_tokens to runners table")
	}

	// Set active runner for existing users.
	_, err = conn.Exec(`
		INSERT OR IGNORE INTO user_settings (channel, sender_id, key, value, updated_at)
		SELECT 'web', user_id, 'active_runner', 'default', CAST(strftime('%s','now') AS INTEGER)
		FROM runner_tokens
	`)
	if err != nil {
		log.WithError(err).Warn("Failed to set active_runner for migrated users")
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 17"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v17 (added runners table, migrated runner_tokens)")
	return nil
}

// migrateV17ToV18 adds display_only column to session_messages.
func migrateV17ToV18(conn *sql.DB) error {
	exists, err := columnExists(conn, "session_messages", "display_only")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE session_messages ADD COLUMN display_only INTEGER DEFAULT 0")
		if err != nil {
			return fmt.Errorf("migrate v17->v18: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 18"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v18 (added display_only to session_messages)")
	return nil
}

// migrateV18ToV19WithDB adds the user_token_usage table via UserTokenUsageService.
// This migration requires *DB rather than just *sql.DB because it instantiates a service.
func migrateV18ToV19WithDB(db *DB) error {
	svc := NewUserTokenUsageService(db)
	if err := svc.createTable(db.Conn()); err != nil {
		return fmt.Errorf("migrate v18->v19: %w", err)
	}
	if _, err := db.Conn().Exec("UPDATE schema_version SET version = 19"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v19 (added user_token_usage)")
	return nil
}

// migrateV19ToV20 adds token tracking fields to tenant_state.
func migrateV19ToV20(conn *sql.DB) error {
	if _, err := conn.Exec("ALTER TABLE tenant_state ADD COLUMN last_prompt_tokens INTEGER DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate v19->v20: %w", err)
	}
	if _, err := conn.Exec("ALTER TABLE tenant_state ADD COLUMN last_completion_tokens INTEGER DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate v19->v20: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 20"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v20 (added token tracking to tenant_state)")
	return nil
}

// migrateV20ToV21 adds LLM fields to runners table.
func migrateV20ToV21(conn *sql.DB) error {
	if _, err := conn.Exec("ALTER TABLE runners ADD COLUMN llm_provider TEXT NOT NULL DEFAULT ''"); err != nil {
		// Column may already exist in fresh DB (created with v21+ schema).
		// Skip if error is "duplicate column name".
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v20->v21: %w", err)
		}
	}
	if _, err := conn.Exec("ALTER TABLE runners ADD COLUMN llm_api_key TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v20->v21: %w", err)
		}
	}
	if _, err := conn.Exec("ALTER TABLE runners ADD COLUMN llm_model TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v20->v21: %w", err)
		}
	}
	if _, err := conn.Exec("ALTER TABLE runners ADD COLUMN llm_base_url TEXT NOT NULL DEFAULT ''"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v20->v21: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 21"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v21 (added LLM fields to runners)")
	return nil
}

// migrateV21ToV22 adds the event_triggers table.
func migrateV21ToV22(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS event_triggers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    event_type  TEXT NOT NULL DEFAULT 'webhook',
    channel     TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    sender_id   TEXT NOT NULL,
    message_tpl TEXT NOT NULL,
    secret      TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    one_shot    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    last_fired  TEXT,
    fire_count  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_event_triggers_sender ON event_triggers(sender_id);
CREATE INDEX IF NOT EXISTS idx_event_triggers_type ON event_triggers(event_type, enabled);
UPDATE schema_version SET version = 22;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v21->v22: %w", err)
	}
	log.Info("Database migrated to v22 (added event_triggers)")
	return nil
}

func migrateV22ToV23(conn *sql.DB) error {
	migration := `
CREATE TABLE IF NOT EXISTS user_llm_subscriptions (
    id          TEXT PRIMARY KEY,
    sender_id   TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    provider    TEXT NOT NULL DEFAULT 'openai',
    base_url    TEXT NOT NULL DEFAULT '',
    api_key     TEXT NOT NULL DEFAULT '',
    model       TEXT NOT NULL DEFAULT '',
    is_default  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_llm_subs_sender ON user_llm_subscriptions(sender_id);
UPDATE schema_version SET version = 23;
`
	if _, err := conn.Exec(migration); err != nil {
		return fmt.Errorf("migrate v22->v23: %w", err)
	}
	log.Info("Database migrated to v23 (added user_llm_subscriptions)")
	return nil
}

// migrateV23ToV24 migrates existing user_llm_configs data into user_llm_subscriptions.
// This is a one-time migration — after this, user_llm_subscriptions is the sole source of truth.
func migrateV23ToV24(conn *sql.DB) error {
	// Copy any rows from old table that don't already have a matching subscription.
	// Match by (sender_id, provider) to avoid duplicates.
	migrate := `
INSERT OR IGNORE INTO user_llm_subscriptions (id, sender_id, name, provider, base_url, api_key, model, is_default, created_at, updated_at)
SELECT
    'sub_' || LOWER(HEX(RANDOMBLOB(8))),
    u.sender_id,
    COALESCE(u.provider, 'openai'),
    COALESCE(u.provider, 'openai'),
    u.base_url,
    u.api_key,
    u.model,
    1,
    u.created_at,
    u.updated_at
FROM user_llm_configs u
WHERE u.sender_id IS NOT NULL
  AND u.sender_id != ''
  AND NOT EXISTS (
      SELECT 1 FROM user_llm_subscriptions s
      WHERE s.sender_id = u.sender_id AND s.provider = COALESCE(u.provider, 'openai')
  );
`
	if _, err := conn.Exec(migrate); err != nil {
		return fmt.Errorf("migrate v23->v24 data: %w", err)
	}

	var count int
	conn.QueryRow("SELECT COUNT(*) FROM user_llm_subscriptions").Scan(&count)

	if _, err := conn.Exec("UPDATE schema_version SET version = 24"); err != nil {
		return fmt.Errorf("migrate v23->v24 version: %w", err)
	}
	log.WithField("subscriptions", count).Info("Database migrated to v24 (user_llm_configs → user_llm_subscriptions)")
	return nil
}

// migrateV24ToV25WithDB adds daily_token_usage table and cached_tokens column.
func migrateV24ToV25WithDB(db *DB) error {
	conn := db.Conn()
	svc := NewUserTokenUsageService(db)

	// Add cached_tokens column to existing user_token_usage (if not present)
	if err := svc.addCachedTokensColumn(conn); err != nil {
		return fmt.Errorf("add cached_tokens column: %w", err)
	}

	// Create daily_token_usage table
	if err := svc.createDailyTable(conn); err != nil {
		return fmt.Errorf("create daily_token_usage: %w", err)
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 25"); err != nil {
		return fmt.Errorf("migrate v24->v25 version: %w", err)
	}
	log.Info("Database migrated to v25 (daily_token_usage + cached_tokens)")
	return nil
}

// migrateV25ToV26 migrates "default" sender IDs to "cli_user".
// This is a one-time migration for CLI single-user mode data that was previously
// stored under the normalized "default" sender ID.
func migrateV25ToV26(conn *sql.DB) error {
	const oldID = "default"
	const newID = "cli_user"

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Tables with sender_id column
	senderIDTables := []string{
		"user_profiles",
		"cron_jobs",
		"user_llm_configs",
		"user_settings",
		"user_token_usage",
		"daily_token_usage",
		"event_triggers",
		"user_llm_subscriptions",
	}
	for _, table := range senderIDTables {
		_, err := tx.Exec(
			fmt.Sprintf(`UPDATE %s SET sender_id = ? WHERE sender_id = ?`, table),
			newID, oldID,
		)
		if err != nil {
			// Table might not exist on fresh installs — ignore
			log.WithField("table", table).WithError(err).Debug("v26 migration: skipping table")
		}
	}

	// Tables with user_id column
	userIDTables := []string{
		"core_memory_blocks",
		"runners",
	}
	for _, table := range userIDTables {
		_, err := tx.Exec(
			fmt.Sprintf(`UPDATE %s SET user_id = ? WHERE user_id = ?`, table),
			newID, oldID,
		)
		if err != nil {
			log.WithField("table", table).WithError(err).Debug("v26 migration: skipping table")
		}
	}

	// Update version stamp inside the same transaction
	if _, err := tx.Exec("UPDATE schema_version SET version = 26"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	log.Info("Database migrated to v26: sender_id 'default' → 'cli_user'")
	return nil
}

// migrateV26ToV27 adds max_context, max_output_tokens, thinking_mode columns
// to user_llm_subscriptions so these settings are persisted to DB.
func migrateV26ToV27(conn *sql.DB) error {
	cols := []struct {
		name string
		def  string
	}{
		{"max_context", "INTEGER DEFAULT 0"},
		{"max_output_tokens", "INTEGER DEFAULT 0"},
		{"thinking_mode", "TEXT DEFAULT ''"},
	}
	for _, c := range cols {
		exists, err := columnExists(conn, "user_llm_subscriptions", c.name)
		if err == nil && !exists {
			_, err = conn.Exec(fmt.Sprintf("ALTER TABLE user_llm_subscriptions ADD COLUMN %s %s", c.name, c.def))
			if err != nil {
				return fmt.Errorf("migrate v26->v27 add %s: %w", c.name, err)
			}
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 27"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v27: added max_context, max_output_tokens, thinking_mode to user_llm_subscriptions")
	return nil
}

// migrateV27ToV28 adds reasoning_content column to session_messages
// so the model's thinking chain persists across restarts.
func migrateV27ToV28(conn *sql.DB) error {
	exists, err := columnExists(conn, "session_messages", "reasoning_content")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE session_messages ADD COLUMN reasoning_content TEXT DEFAULT ''")
		if err != nil {
			return fmt.Errorf("migrate v27->v28 add reasoning_content: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 28"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v28: added reasoning_content to session_messages")
	return nil
}

// migrateV28ToV29 adds cached_models column to user_llm_subscriptions
// for per-subscription model list caching.
func migrateV28ToV29(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_subscriptions", "cached_models")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE user_llm_subscriptions ADD COLUMN cached_models TEXT NOT NULL DEFAULT ''")
		if err != nil {
			return fmt.Errorf("migrate v28->v29 add cached_models: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 29"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v29: added cached_models to user_llm_subscriptions")
	return nil
}

// migrateV29ToV30 adds user_chats table for multi-chatroom support.
func migrateV29ToV30(conn *sql.DB) error {
	_, err := conn.Exec(`
	CREATE TABLE IF NOT EXISTS user_chats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel TEXT NOT NULL,
		sender_id TEXT NOT NULL,
		chat_id TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(channel, sender_id, chat_id)
	);
	CREATE INDEX IF NOT EXISTS idx_user_chats_sender ON user_chats(channel, sender_id);
	`)
	if err != nil {
		return fmt.Errorf("migrate v29->v30 create user_chats: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 30"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v30: added user_chats table")
	return nil
}

// migrateV30ToV31 adds context_tokens column to session_messages.
// This stores the exact API prompt_tokens value at the time each user message
// was sent, enabling precise token accounting without estimation.
// Rewind uses this value to restore accurate token state.
func migrateV30ToV31(conn *sql.DB) error {
	if _, err := conn.Exec("ALTER TABLE session_messages ADD COLUMN context_tokens INTEGER DEFAULT 0"); err != nil {
		// "duplicate column name" is OK — means the column already exists (fresh DB from schema.go)
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v30->v31 add context_tokens: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 31"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v31: added context_tokens to session_messages")
	return nil
}

// migrateV31ToV32 adds per_model_configs column to user_llm_subscriptions.
// This stores per-model token overrides as JSON: {"model-name": {"max_output_tokens": N, "max_context": N}}
// When a model has a per-model config, it takes priority over the subscription-level defaults.
func migrateV31ToV32(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_subscriptions", "per_model_configs")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE user_llm_subscriptions ADD COLUMN per_model_configs TEXT NOT NULL DEFAULT '{}'")
		if err != nil {
			return fmt.Errorf("migrate v31->v32 add per_model_configs: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 32"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v32: added per_model_configs to user_llm_subscriptions")
	return nil
}

// orphanTables lists all tables that have a tenant_id foreign key to tenants(id).
// Used by migrateV32ToV33 to clean up orphaned rows left by disabled foreign keys.
var orphanTables = []string{
	"session_messages",
	"tenant_state",
	"core_memory_blocks",
	"long_term_memory",
	"event_history",
	"archival_memory",
}

// migrateV32ToV33 cleans orphaned rows from all tables with foreign keys to tenants.
// Before v33, PRAGMA foreign_keys was OFF, so ON DELETE CASCADE never fired when tenants
// were deleted. This left behind orphaned rows (tenant_id pointing to non-existent tenants)
// that accumulated over time, sometimes comprising 77%+ of total rows in session_messages.
//
// The migration:
//  1. Deletes all orphaned rows from FK-linked tables.
//  2. Runs VACUUM to reclaim the freed disk space back to the OS.
//  3. Enables foreign_keys pragma for the current connection (also set in Open() for future).
func migrateV32ToV33(conn *sql.DB) error {
	// Enable foreign keys so CASCADE works for future deletes.
	if _, err := conn.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	// Ensure the shared tenant (id=0) exists for core_memory human blocks.
	// Human blocks are stored at tenant_id=0 as shared cross-tenant data.
	// Without this row, FK constraints on core_memory_blocks would block
	// any InitBlocks call that creates human blocks.
	conn.Exec("INSERT OR IGNORE INTO tenants (id, channel, chat_id, created_at, last_active_at) VALUES (0, '_shared', '_shared', datetime('now'), datetime('now'))")

	// Clean orphaned rows from each FK-linked table.
	totalOrphans := 0
	for _, table := range orphanTables {
		result, err := conn.Exec(
			fmt.Sprintf("DELETE FROM %s WHERE tenant_id NOT IN (SELECT id FROM tenants)", table),
		)
		if err != nil {
			// Table might not exist in older DBs; skip silently.
			log.WithError(err).WithField("table", table).Debug("Skipping orphan cleanup for table")
			continue
		}
		rows, _ := result.RowsAffected()
		if rows > 0 {
			totalOrphans += int(rows)
			log.WithFields(log.Fields{
				"table":  table,
				"orphan": rows,
			}).Info("Cleaned orphaned rows from table")
		}
	}

	// Also clean orphaned event_history_fts (virtual table matching event_history).
	// FTS tables don't have FK constraints, but their rows mirror event_history orphans.
	if _, err := conn.Exec("DELETE FROM event_history_fts WHERE rowid NOT IN (SELECT id FROM event_history)"); err != nil {
		log.WithError(err).Debug("Skipping orphan cleanup for event_history_fts")
	}

	if totalOrphans > 0 {
		log.WithField("total_orphans", totalOrphans).Info("Running VACUUM to reclaim disk space after orphan cleanup")
		if _, err := conn.Exec("VACUUM"); err != nil {
			// VACUUM failure is non-fatal: data is cleaned, just space not reclaimed.
			log.WithError(err).Warn("VACUUM failed after orphan cleanup (space not reclaimed)")
		}
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 33"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.WithField("orphan_rows_cleaned", totalOrphans).Info("Database migrated to v33: cleaned orphaned data, enabled foreign keys")
	return nil
}

// migrateV33ToV34 adds subscription_id and model columns to the tenants table
// so the backend can persist which subscription a session uses. Previously this
// mapping only existed in LLMFactory's in-memory cache (lost on restart) and
// in the CLI's local sessions.json (unavailable to other clients).
func migrateV33ToV34(conn *sql.DB) error {
	_, err := conn.Exec(`
		ALTER TABLE tenants ADD COLUMN subscription_id TEXT DEFAULT '';
		ALTER TABLE tenants ADD COLUMN model TEXT DEFAULT '';
	`)
	if err != nil {
		return fmt.Errorf("add subscription columns: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 34"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v34: added subscription_id/model to tenants")
	return nil
}

// migrateV34ToV35 extracts model-level attributes from user_llm_subscriptions
// into a proper subscription_models table. The old columns (model, max_context,
// max_output_tokens, thinking_mode, cached_models, per_model_configs) remain
// on the subscription row for backward compatibility during the transition.
func migrateV34ToV35(db *DB) error {
	conn := db.Conn()

	// 1. Create subscription_models table
	if _, err := conn.Exec(`
		CREATE TABLE IF NOT EXISTS subscription_models (
			id                TEXT PRIMARY KEY,
			subscription_id   TEXT NOT NULL REFERENCES user_llm_subscriptions(id) ON DELETE CASCADE,
			model             TEXT NOT NULL,
			max_context       INTEGER NOT NULL DEFAULT 0,
			max_output_tokens INTEGER NOT NULL DEFAULT 0,
			thinking_mode     TEXT NOT NULL DEFAULT '',
			created_at        TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_sub_models_sub ON subscription_models(subscription_id);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_sub_models_uniq ON subscription_models(subscription_id, model);
	`); err != nil {
		return fmt.Errorf("create subscription_models: %w", err)
	}

	// 2. Migrate default model from each subscription row
	if _, err := conn.Exec(`
		INSERT OR IGNORE INTO subscription_models (id, subscription_id, model, max_context, max_output_tokens, thinking_mode)
		SELECT lower(hex(randomblob(16))), id, model, COALESCE(max_context, 0), COALESCE(max_output_tokens, 0), COALESCE(thinking_mode, '')
		FROM user_llm_subscriptions
		WHERE model IS NOT NULL AND model != '';
	`); err != nil {
		return fmt.Errorf("migrate default models: %w", err)
	}

	// 3. Migrate per_model_configs JSON into subscription_models rows.
	// Guard: the column was dropped in v42, so skip this step once it's gone
	// (keeps the migration idempotent when re-run on a post-v42 schema).
	pmcColExists, _ := columnExists(conn, "user_llm_subscriptions", "per_model_configs")
	if pmcColExists {
		// IMPORTANT: collect all rows first, then execute inserts. SQLite's
		// single-connection pool cannot run conn.Exec while rows.Next() is
		// iterating — that would deadlock and freeze the entire startup.
		rows, err := conn.Query(`
			SELECT id, COALESCE(per_model_configs, '{}') FROM user_llm_subscriptions
			WHERE per_model_configs IS NOT NULL AND per_model_configs != '' AND per_model_configs != '{}'
		`)
		if err != nil {
			return fmt.Errorf("query per_model_configs: %w", err)
		}

		type pmcRow struct {
			subID   string
			jsonStr string
		}
		var pmcRows []pmcRow
		for rows.Next() {
			var r pmcRow
			if err := rows.Scan(&r.subID, &r.jsonStr); err != nil {
				rows.Close()
				return fmt.Errorf("scan per_model_configs row: %w", err)
			}
			pmcRows = append(pmcRows, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("rows iteration: %w", err)
		}

		// Now process collected rows (no conn.Exec inside a rows loop).
		for _, r := range pmcRows {
			if r.jsonStr == "" || r.jsonStr == "{}" {
				continue
			}
			var pmc map[string]struct {
				MaxContext      int `json:"max_context,omitempty"`
				MaxOutputTokens int `json:"max_output_tokens,omitempty"`
			}
			if err := json.Unmarshal([]byte(r.jsonStr), &pmc); err != nil {
				log.WithError(err).WithField("sub_id", r.subID).Warn("v35: failed to parse per_model_configs, skipping")
				continue
			}
			for modelName, cfg := range pmc {
				if modelName == "" {
					continue
				}
				_, err := conn.Exec(`
				INSERT INTO subscription_models (id, subscription_id, model, max_context, max_output_tokens)
				VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?)
				ON CONFLICT(subscription_id, model) DO UPDATE SET
					max_context = COALESCE(excluded.max_context, max_context),
					max_output_tokens = COALESCE(excluded.max_output_tokens, max_output_tokens)
			`, r.subID, modelName, cfg.MaxContext, cfg.MaxOutputTokens)
				if err != nil {
					log.WithError(err).WithFields(log.Fields{
						"sub_id": r.subID, "model": modelName,
					}).Warn("v35: failed to upsert per_model_config row")
				}
			}
		}
	}

	// 4. Add model_id to tenants (ignore error if column already exists)
	conn.Exec("ALTER TABLE tenants ADD COLUMN model_id TEXT DEFAULT ''")

	// 5. Bump version
	if _, err := conn.Exec("UPDATE schema_version SET version = 35"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v35: added subscription_models table")
	return nil
}

// migrateV35ToV36 adds api_type column to user_llm_subscriptions.
// This column stores the API endpoint type: "" (default=chat_completions) or "responses".
func migrateV35ToV36(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_subscriptions", "api_type")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE user_llm_subscriptions ADD COLUMN api_type TEXT DEFAULT ''")
		if err != nil {
			return fmt.Errorf("migrate v35->v36 add api_type: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 36"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v36: added api_type column to user_llm_subscriptions")
	return nil
}

// migrateV36ToV37 adds api_type column to subscription_models table.
// This enables per-model API type overrides (e.g. gpt-4o uses chat_completions
// while o3 uses responses API within the same subscription).
func migrateV36ToV37(conn *sql.DB) error {
	exists, err := columnExists(conn, "subscription_models", "api_type")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE subscription_models ADD COLUMN api_type TEXT NOT NULL DEFAULT ''")
		if err != nil {
			return fmt.Errorf("migrate v36->v37 add api_type: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 37"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v37: added api_type column to subscription_models")
	return nil
}

// migrateV37ToV38 adds runner_id to tenants for session-runner binding.
func migrateV37ToV38(conn *sql.DB) error {
	exists, err := columnExists(conn, "tenants", "runner_id")
	if err == nil && !exists {
		_, err = conn.Exec("ALTER TABLE tenants ADD COLUMN runner_id TEXT DEFAULT ''")
		if err != nil {
			return fmt.Errorf("migrate v37->v38 add runner_id: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 38"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v38: added runner_id to tenants")
	return nil
}

// migrateV38ToV39 lays the DB foundation for the model-first subscription redesign:
//
//  1. Adds subscription_models.enabled (default 1) so individual models can be
//     disabled independently of their subscription.
//  2. Creates user_default_model, storing each user's default (subscription, model)
//     used to resolve LLM for new sessions (replaces the implicit
//     user_llm_subscriptions.model "current model" semantics).
//  3. Backfills subscription_models rows for every (subscription_id, model) pair
//     referenced in tenants that lacks a row. This makes existing per-session
//     selections concrete, disable-able model entities. Config defaults to 0
//     (resolution falls back to subscription defaults). This is safe because the
//     v35 migration already moved all non-empty per_model_configs JSON entries
//     into rows, so missing rows have no real config to clobber.
//  4. Seeds user_default_model from each user's default subscription. When the
//     default subscription's model is empty, falls back to the most-recently-active
//     tenant's model for that subscription; if none exists, the user is skipped
//     (ResolveLLM will fall back to the system default until they pick a model).
//
// This migration is purely additive: no existing column is dropped or narrowed,
// so the pre-redesign code paths keep working unchanged.
func migrateV38ToV39(conn *sql.DB) error {
	// 1. enabled column on subscription_models.
	exists, err := columnExists(conn, "subscription_models", "enabled")
	if err == nil && !exists {
		if _, err := conn.Exec("ALTER TABLE subscription_models ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1"); err != nil {
			return fmt.Errorf("migrate v38->v39 add subscription_models.enabled: %w", err)
		}
	}

	// 2. user_default_model table.
	if _, err := conn.Exec(`
CREATE TABLE IF NOT EXISTS user_default_model (
    sender_id       TEXT PRIMARY KEY,
    subscription_id TEXT NOT NULL,
    model           TEXT NOT NULL DEFAULT '',
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);`); err != nil {
		return fmt.Errorf("migrate v38->v39 create user_default_model: %w", err)
	}

	// 3. Backfill concrete model rows for tenants-referenced (sub, model) pairs.
	if _, err := conn.Exec(`
INSERT OR IGNORE INTO subscription_models (id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, enabled)
SELECT lower(hex(randomblob(16))), t.subscription_id, t.model, 0, 0, '', '', 1
FROM tenants t
WHERE t.subscription_id != '' AND t.model != ''
  AND NOT EXISTS (
      SELECT 1 FROM subscription_models sm
      WHERE sm.subscription_id = t.subscription_id AND sm.model = t.model
  )
GROUP BY t.subscription_id, t.model;`); err != nil {
		return fmt.Errorf("migrate v38->v39 backfill subscription_models: %w", err)
	}

	// 4. Seed user_default_model from each user's default subscription.
	// Guard: the is_default column was dropped in v43, so skip this seed once it's
	// gone (user_default_model is already authoritative by then). Keeps the
	// migration idempotent when re-run on a post-v43 schema.
	exists, err = columnExists(conn, "user_llm_subscriptions", "is_default")
	if err == nil && exists {
		if _, err := conn.Exec(`
INSERT OR REPLACE INTO user_default_model (sender_id, subscription_id, model, updated_at)
SELECT s.sender_id, s.id,
    COALESCE(NULLIF(s.model, ''),
        (SELECT t.model FROM tenants t
         WHERE t.subscription_id = s.id AND t.model != ''
         ORDER BY t.last_active_at DESC LIMIT 1)),
    datetime('now')
FROM user_llm_subscriptions s
WHERE s.is_default = 1
  AND (s.model != '' OR EXISTS (
      SELECT 1 FROM tenants t WHERE t.subscription_id = s.id AND t.model != ''));`); err != nil {
			return fmt.Errorf("migrate v38->v39 seed user_default_model: %w", err)
		}
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 39"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v39: subscription_models.enabled + user_default_model + model backfill")
	return nil
}

// migrateV39ToV40 adds the subscription-level enabled flag (default 1). A disabled
// subscription stops contributing models to the picker without losing credentials.
// Purely additive.
func migrateV39ToV40(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_subscriptions", "enabled")
	if err == nil && !exists {
		if _, err := conn.Exec("ALTER TABLE user_llm_subscriptions ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1"); err != nil {
			return fmt.Errorf("migrate v39->v40 add user_llm_subscriptions.enabled: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 40"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v40: user_llm_subscriptions.enabled")
	return nil
}

// migrateV40ToV41 drops the legacy user_llm_configs table. Its data was migrated
// into user_llm_subscriptions in v24, and no code path reads/writes it anymore.
func migrateV40ToV41(conn *sql.DB) error {
	if _, err := conn.Exec("DROP TABLE IF EXISTS user_llm_configs"); err != nil {
		return fmt.Errorf("migrate v40->v41 drop user_llm_configs: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 41"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v41: dropped legacy user_llm_configs table")
	return nil
}

// migrateV41ToV42 drops the redundant per_model_configs JSON column from
// user_llm_subscriptions. Per-model config now lives solely in the
// subscription_models table (authoritative since v35). The JSON column was a
// stale duplicate and incomplete (no ThinkingMode). Uses ALTER TABLE DROP
// COLUMN (SQLite >= 3.35, provided by modernc.org/sqlite).
func migrateV41ToV42(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_subscriptions", "per_model_configs")
	if err == nil && exists {
		if _, err := conn.Exec("ALTER TABLE user_llm_subscriptions DROP COLUMN per_model_configs"); err != nil {
			return fmt.Errorf("migrate v41->v42 drop per_model_configs: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 42"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v42: dropped redundant per_model_configs JSON column")
	return nil
}

// migrateV42ToV43 drops the is_default column from user_llm_subscriptions. The
// default subscription is now derived from user_default_model (seeded in v39),
// making the per-row is_default flag redundant. IsDefault stays as an in-memory
// read-side projection populated by GetDefault/List. Uses ALTER TABLE DROP
// COLUMN (SQLite >= 3.35, provided by modernc.org/sqlite).
func migrateV42ToV43(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_subscriptions", "is_default")
	if err == nil && exists {
		if _, err := conn.Exec("ALTER TABLE user_llm_subscriptions DROP COLUMN is_default"); err != nil {
			return fmt.Errorf("migrate v42->v43 drop is_default: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 43"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v43: dropped redundant is_default column (derived from user_default_model)")
	return nil
}

// migrateV43ToV44 adds the is_system column to user_llm_subscriptions. A system
// subscription row (is_system=1) is the shared default/fallback LLM reconciled
// from config/env at boot, visible to all users and read-only in the UI.
func migrateV43ToV44(conn *sql.DB) error {
	exists, err := columnExists(conn, "user_llm_subscriptions", "is_system")
	if err == nil && !exists {
		if _, err := conn.Exec("ALTER TABLE user_llm_subscriptions ADD COLUMN is_system INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("migrate v43->v44 add is_system: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 44"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v44: added is_system column to user_llm_subscriptions")
	return nil
}

// columnExists checks whether a column exists in a table using pragma_table_info.
// Returns (true, nil) if the column exists, (false, nil) if not, or (false, error) on query failure.
func columnExists(conn *sql.DB, table, column string) (bool, error) {
	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = ?", table)
	if err := conn.QueryRow(query, column).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// migrateV44ToV45 creates the canonical user identity system:
// - users table (id, display_name, role, created_at)
// - user_identities table (channel identity → canonical user mapping)
// - link_codes table (one-time codes for cross-channel linking)
// - Adds user_id INTEGER column to all asset tables + backfills from sender_id
//
// Roundtable-reviewed design: one-shot migration, no dual-column coexistence.
// NULL trap fix: __system__ subscription gets a system/__system__ identity.
// CASCADE fix: identities are migrated BEFORE source user deletion (merge phase).
func migrateV44ToV45(conn *sql.DB) error {
	// 1. Create users table
	if _, err := conn.Exec(`
	CREATE TABLE IF NOT EXISTS users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    display_name TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('admin', 'user')),
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		return fmt.Errorf("migrate v45 create users: %w", err)
	}

	// 2. Create user_identities table
	if _, err := conn.Exec(`
	CREATE TABLE IF NOT EXISTS user_identities (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL,
    channel         TEXT NOT NULL,
    channel_user_id TEXT NOT NULL,
    linked_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(channel, channel_user_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);`); err != nil {
		return fmt.Errorf("migrate v45 create user_identities: %w", err)
	}
	if _, err := conn.Exec(`CREATE INDEX IF NOT EXISTS idx_user_identities_user ON user_identities(user_id);`); err != nil {
		return fmt.Errorf("migrate v45 create user_identities index: %w", err)
	}

	// 3. Create link_codes table
	if _, err := conn.Exec(`
	CREATE TABLE IF NOT EXISTS link_codes (
    code        TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL,
    expires_at  TIMESTAMP NOT NULL,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);`); err != nil {
		return fmt.Errorf("migrate v45 create link_codes: %w", err)
	}

	// 4. Seed user 1 as admin (for existing cli_user/admin identities ONLY).
	// Do NOT use this ID for web users — each web user gets their own canonical user.
	if _, err := conn.Exec(`INSERT OR IGNORE INTO users (id, display_name, role) VALUES (1, 'Admin', 'admin');`); err != nil {
		return fmt.Errorf("migrate v45 seed admin user: %w", err)
	}

	// 5. Map CLI identities to user 1 (admin)
	if _, err := conn.Exec(`INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id) VALUES (1, 'cli', 'cli_user');`); err != nil {
		return fmt.Errorf("migrate v45 map cli_user: %w", err)
	}
	if _, err := conn.Exec(`INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id) VALUES (1, 'cli', 'admin');`); err != nil {
		return fmt.Errorf("migrate v45 map admin: %w", err)
	}

	// 6. Map __system__ subscription owner (NULL trap fix)
	if _, err := conn.Exec(`INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id) VALUES (1, 'system', '__system__');`); err != nil {
		return fmt.Errorf("migrate v45 map system: %w", err)
	}

	// 7. Map existing web users — each gets their OWN canonical user.
	// ALL web users are independent from the CLI admin (user_id=1).
	// Admin status is managed explicitly via the role management API,
	// NOT assumed by registration order.
	if _, err := conn.Exec(`
		INSERT OR IGNORE INTO users (display_name, role)
		SELECT username, 'user' FROM web_users;`); err != nil {
		return fmt.Errorf("migrate v45 seed web users: %w", err)
	}
	if _, err := conn.Exec(`
		INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
		SELECT u.id, 'web', 'web-' || CAST(w.id AS TEXT)
		FROM users u JOIN web_users w ON u.display_name = w.username
		WHERE 'web-' || CAST(w.id AS TEXT) NOT IN (SELECT channel_user_id FROM user_identities WHERE channel = 'web');`); err != nil {
		return fmt.Errorf("migrate v45 map web users: %w", err)
	}

	// 8. Map existing Feishu identities (from user_llm_subscriptions.sender_id)
	if _, err := conn.Exec(`
	INSERT OR IGNORE INTO users (display_name, role)
	SELECT DISTINCT sender_id, 'user' FROM user_llm_subscriptions
	WHERE sender_id LIKE 'ou_%'
	AND sender_id NOT IN (SELECT channel_user_id FROM user_identities WHERE channel = 'feishu');`); err != nil {
		return fmt.Errorf("migrate v45 seed feishu users: %w", err)
	}
	// 8b. Map existing Feishu identities — join back to user_llm_subscriptions
	// to get the real sender_id (ou_xxx) rather than relying on display_name.
	if _, err := conn.Exec(`
	INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
	SELECT u.id, 'feishu', uls.sender_id
	FROM users u
	JOIN user_llm_subscriptions uls ON uls.sender_id = u.display_name
	WHERE uls.sender_id LIKE 'ou_%'
	AND u.id NOT IN (SELECT user_id FROM user_identities WHERE channel = 'feishu');`); err != nil {
		return fmt.Errorf("migrate v45 map feishu users: %w", err)
	}

	// 9. Migrate Feishu-Web links (existing hack in user_settings)
	// Must run AFTER steps 7-8 so both identities exist.
	if _, err := conn.Exec(`
	INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id)
	SELECT ui.user_id, 'feishu', us.sender_id
	FROM user_settings us
	JOIN user_identities ui ON ui.channel = 'web'
    AND ui.channel_user_id = ('web-' || us.value)
	WHERE us.channel = 'feishu' AND us.key = 'web_user_id';`); err != nil {
		return fmt.Errorf("migrate v45 feishu-web links: %w", err)
	}

	// 10. Add user_id columns to asset tables
	assetTables := []struct {
		table  string
		column string
	}{
		{"user_llm_subscriptions", "user_id"},
		{"runners", "owner_user_id"},
		{"user_settings", "user_id"},
		{"user_default_model", "user_id"},
		{"user_chats", "user_id"},
		{"tenants", "owner_user_id"},
		{"cron_jobs", "user_id"},
		{"event_triggers", "user_id"},
	}
	for _, at := range assetTables {
		exists, err := columnExists(conn, at.table, at.column)
		if err != nil {
			return fmt.Errorf("migrate v45 check %s.%s: %w", at.table, at.column, err)
		}
		if !exists {
			if _, err := conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s INTEGER DEFAULT 0", at.table, at.column)); err != nil {
				return fmt.Errorf("migrate v45 add %s.%s: %w", at.table, at.column, err)
			}
		}
	}

	// 11. Backfill user_id columns
	// user_llm_subscriptions: match by sender_id → user_identities
	if _, err := conn.Exec(`
	UPDATE user_llm_subscriptions SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = user_llm_subscriptions.sender_id
    ORDER BY CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 WHEN 'system' THEN 3 ELSE 4 END
    LIMIT 1
	) WHERE user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v45 backfill subscriptions: %w", err)
	}

	// runners: user_id is TEXT (old), new column is owner_user_id INTEGER
	if _, err := conn.Exec(`
	UPDATE runners SET owner_user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = runners.user_id
    LIMIT 1
	) WHERE owner_user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v45 backfill runners: %w", err)
	}

	// user_settings: match by (channel, sender_id)
	if _, err := conn.Exec(`
	UPDATE user_settings SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel = user_settings.channel
    AND ui.channel_user_id = user_settings.sender_id
    LIMIT 1
	) WHERE user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v45 backfill user_settings: %w", err)
	}

	// user_default_model: match by sender_id
	if _, err := conn.Exec(`
	UPDATE user_default_model SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = user_default_model.sender_id
    ORDER BY CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 ELSE 3 END
    LIMIT 1
	) WHERE user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v45 backfill user_default_model: %w", err)
	}

	// user_chats: match by (channel, sender_id)
	if _, err := conn.Exec(`
	UPDATE user_chats SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel = user_chats.channel
    AND ui.channel_user_id = user_chats.sender_id
    LIMIT 1
	) WHERE user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v45 backfill user_chats: %w", err)
	}

	// tenants: match by (channel, chat_id)
	if _, err := conn.Exec(`
	UPDATE tenants SET owner_user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel = tenants.channel
    AND ui.channel_user_id = tenants.chat_id
    LIMIT 1
	) WHERE owner_user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v45 backfill tenants: %w", err)
	}

	// cron_jobs: match by sender_id
	if _, err := conn.Exec(`
	UPDATE cron_jobs SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = cron_jobs.sender_id
    ORDER BY CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 ELSE 3 END
    LIMIT 1
	) WHERE user_id = 0 AND sender_id != '';`); err != nil {
		return fmt.Errorf("migrate v45 backfill cron_jobs: %w", err)
	}

	// event_triggers: match by sender_id
	if _, err := conn.Exec(`
	UPDATE event_triggers SET user_id = (
    SELECT ui.user_id FROM user_identities ui
    WHERE ui.channel_user_id = event_triggers.sender_id
    ORDER BY CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 ELSE 3 END
    LIMIT 1
	) WHERE user_id = 0 AND sender_id != '';`); err != nil {
		return fmt.Errorf("migrate v45 backfill event_triggers: %w", err)
	}

	// 12. Update schema version
	if _, err := conn.Exec("UPDATE schema_version SET version = 45"); err != nil {
		return fmt.Errorf("migrate v45 update version: %w", err)
	}

	log.Info("Database migrated to v45: canonical user identity system (users + user_identities + link_codes + asset backfill)")
	return nil
}

// migrateV45ToV46 re-backfills user_id for rows added after v45 migration.
// The old code (before the canonical-user fix) didn't write user_id when
// adding subscriptions/settings, so rows added after v45 have user_id=0.
// This migration re-runs the same sender_id → user_identities JOIN to catch
// any rows that were missed.
func migrateV45ToV46(conn *sql.DB) error {
	// user_llm_subscriptions: re-backfill user_id=0 rows
	if _, err := conn.Exec(`
	UPDATE user_llm_subscriptions SET user_id = (
	SELECT ui.user_id FROM user_identities ui
	WHERE ui.channel_user_id = user_llm_subscriptions.sender_id
	ORDER BY CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 WHEN 'system' THEN 3 ELSE 4 END
	LIMIT 1
	) WHERE user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v46 backfill subscriptions: %w", err)
	}

	// user_settings: re-backfill user_id=0 rows
	if _, err := conn.Exec(`
	UPDATE user_settings SET user_id = (
	SELECT ui.user_id FROM user_identities ui
	WHERE ui.channel_user_id = user_settings.sender_id
	ORDER BY CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 WHEN 'system' THEN 3 ELSE 4 END
	LIMIT 1
	) WHERE user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v46 backfill user_settings: %w", err)
	}

	// user_default_model: re-backfill user_id=0 rows
	if _, err := conn.Exec(`
	UPDATE user_default_model SET user_id = (
	SELECT ui.user_id FROM user_identities ui
	WHERE ui.channel_user_id = user_default_model.sender_id
	ORDER BY CASE ui.channel WHEN 'cli' THEN 0 WHEN 'web' THEN 1 WHEN 'feishu' THEN 2 WHEN 'system' THEN 3 ELSE 4 END
	LIMIT 1
	) WHERE user_id = 0;`); err != nil {
		return fmt.Errorf("migrate v46 backfill user_default_model: %w", err)
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 46"); err != nil {
		return fmt.Errorf("migrate v46 update version: %w", err)
	}

	log.Info("Database migrated to v46: re-backfill user_id for rows added after v45")
	return nil
}

// migrateV46ToV47 creates the pending_resumes table for graceful shutdown
// agent loop resume. When xbot shuts down with active agent loops, the
// affected sessions are recorded here and resumed on next startup.
func migrateV47ToV48(conn *sql.DB) error {
	if _, err := conn.Exec("ALTER TABLE user_chats ADD COLUMN sort_order INTEGER DEFAULT 0"); err != nil {
		// Column may already exist (e.g. partial migration). Check and continue.
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v48 add sort_order: %w", err)
		}
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 48"); err != nil {
		return fmt.Errorf("migrate v48 update version: %w", err)
	}

	log.Info("Database migrated to v48: added sort_order column to user_chats")
	return nil
}

func migrateV48ToV49(conn *sql.DB) error {
	// Fix cancelled-turn messages that were incorrectly marked display_only=1.
	// handleCancelledRun persisted the [interrupted] assistant message with
	// DisplayOnly=true, but it carries Detail (iteration history) that
	// GetAllMessages must return for ConvertMessagesToHistory to work.
	// Without it, cancelled turns lose their iteration history and render
	// as duplicate/merged tool blocks.
	result, err := conn.Exec(`
		UPDATE session_messages
		SET display_only = 0
		WHERE role = 'assistant'
		  AND content = '[interrupted]'
		  AND detail IS NOT NULL AND detail != ''
		  AND display_only = 1
	`)
	if err != nil {
		return fmt.Errorf("migrate v49 fix display_only: %w", err)
	}
	rows, _ := result.RowsAffected()
	log.WithField("rows_affected", rows).Info("Database migrated to v49: fixed cancelled-turn display_only flag")

	if _, err := conn.Exec("UPDATE schema_version SET version = 49"); err != nil {
		return fmt.Errorf("migrate v49 update version: %w", err)
	}
	return nil
}

func migrateV49ToV50(conn *sql.DB) error {
	if _, err := conn.Exec("ALTER TABLE session_messages ADD COLUMN turn_id INTEGER DEFAULT 0"); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate v50 add turn_id: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 50"); err != nil {
		return fmt.Errorf("migrate v50 update version: %w", err)
	}
	log.Info("Database migrated to v50: added turn_id column to session_messages")
	return nil
}

// migrateV50ToV51 upgrades Detail JSON iteration numbers from 0-based to 1-based.
//
// Before v51, the engine's Run loop used 0-based iteration numbers (first = 0).
// Detail JSON in session_messages stored these as-is: [{"iteration":0,...}, {"iteration":1,...}].
// After v51, the engine uses 1-based numbers (first = 1). Old Detail JSON must be
// rewritten to match, otherwise ConvertMessagesToHistory renders old data with
// off-by-one iteration numbers.
//
// Strategy: Go-based row-by-row rewrite (safer than SQL JSON ops). For each
// Detail JSON, force iteration numbers to be 1-based sequential (1, 2, 3, ...).
// This is safe because:
//   - Well-formed Detail JSON already has sequential numbers — we just shift them.
//   - Malformed Detail (non-sequential) gets normalized to sequential.
//   - Empty/single-entry Detail is trivially correct.
func migrateV50ToV51(conn *sql.DB) error {
	type iterSnap struct {
		Iteration int             `json:"iteration"`
		Content   string          `json:"content,omitempty"`
		Reasoning string          `json:"reasoning,omitempty"`
		Tools     json.RawMessage `json:"tools"`
	}

	rows, err := conn.Query("SELECT id, detail FROM session_messages WHERE detail IS NOT NULL AND detail != ''")
	if err != nil {
		return fmt.Errorf("migrate v51 query: %w", err)
	}
	defer rows.Close()

	type update struct {
		id     int64
		detail string
	}
	var updates []update

	for rows.Next() {
		var id int64
		var detail string
		if err := rows.Scan(&id, &detail); err != nil {
			continue
		}
		var snaps []iterSnap
		if err := json.Unmarshal([]byte(detail), &snaps); err != nil {
			continue // skip malformed JSON
		}
		changed := false
		for i := range snaps {
			if snaps[i].Iteration != i+1 {
				changed = true
			}
			snaps[i].Iteration = i + 1 // force 1-based sequential
		}
		if !changed {
			continue
		}
		data, err := json.Marshal(snaps)
		if err != nil {
			continue
		}
		updates = append(updates, update{id: id, detail: string(data)})
	}
	rows.Close()

	for _, u := range updates {
		if _, err := conn.Exec("UPDATE session_messages SET detail = ? WHERE id = ?", u.detail, u.id); err != nil {
			log.WithError(err).WithField("id", u.id).Warn("migrate v51: failed to update detail JSON")
		}
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 51"); err != nil {
		return fmt.Errorf("migrate v51 update version: %w", err)
	}
	log.WithField("updated_rows", len(updates)).Info("Database migrated to v51: upgraded Detail JSON iteration numbers to 1-based")
	return nil
}

func migrateV46ToV47(conn *sql.DB) error {
	if _, err := conn.Exec(`
  CREATE TABLE IF NOT EXISTS pending_resumes (
   channel TEXT NOT NULL,
   chat_id TEXT NOT NULL,
   sender_id TEXT NOT NULL,
   content TEXT NOT NULL,
   created_at TEXT NOT NULL,
   PRIMARY KEY (channel, chat_id)
  );`); err != nil {
		return fmt.Errorf("create pending_resumes table: %w", err)
	}

	if _, err := conn.Exec("UPDATE schema_version SET version = 47"); err != nil {
		return fmt.Errorf("migrate v47 update version: %w", err)
	}

	log.Info("Database migrated to v47: added pending_resumes table")
	return nil
}

// migrateV51ToV52 makes session_messages an append-only history log by adding
// record_type, target_history_id, and record_data columns. Existing rows are
// the migration baseline and remain ordinary message records (record_type
// defaults to 'message').
func migrateV51ToV52(conn *sql.DB) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"record_type", "ALTER TABLE session_messages ADD COLUMN record_type TEXT NOT NULL DEFAULT 'message'"},
		{"target_history_id", "ALTER TABLE session_messages ADD COLUMN target_history_id INTEGER"},
		{"record_data", "ALTER TABLE session_messages ADD COLUMN record_data TEXT"}}
	for _, column := range columns {
		exists, err := columnExists(conn, "session_messages", column.name)
		if err != nil {
			return fmt.Errorf("check session_messages.%s: %w", column.name, err)
		}
		if !exists {
			if _, err := conn.Exec(column.ddl); err != nil {
				return fmt.Errorf("add session_messages.%s: %w", column.name, err)
			}
		}
	}
	if _, err := conn.Exec(`CREATE INDEX IF NOT EXISTS idx_session_messages_tenant_history ON session_messages(tenant_id, id)`); err != nil {
		return fmt.Errorf("create history index: %w", err)
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 52"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v52: append-only session history records")
	return nil
}

// migrateV52ToV53 moves session CWD persistence from files into the tenants
// table. File-based CWD was unreliable (Cd in sessionless/SubAgent context
// only mutated in-memory InitialCWD; file keys could mismatch the tenant's
// channel:chatID), so after a restart every session fell back to "~". The DB
// is now the single authoritative store (TUI may keep a local file view).
func migrateV52ToV53(conn *sql.DB) error {
	exists, err := columnExists(conn, "tenants", "cwd")
	if err != nil {
		return fmt.Errorf("check tenants.cwd: %w", err)
	}
	if !exists {
		if _, err := conn.Exec("ALTER TABLE tenants ADD COLUMN cwd TEXT DEFAULT ''"); err != nil {
			return fmt.Errorf("add tenants.cwd: %w", err)
		}
	}
	if _, err := conn.Exec("UPDATE schema_version SET version = 53"); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	log.Info("Database migrated to v53: session CWD persisted in tenants.cwd")
	return nil
}
