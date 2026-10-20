package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/storage/sqlite"
)

// ─── Per-session ID offsetting ───
//
// Each per-session SQLite database has its OWN autoincrement sequences: every
// session_messages.id and iteration_history.id starts at 1. Merging them into one
// PostgreSQL table therefore collides on the primary key, and — worse — silently
// renumbering would break every internal reference:
//
//	session_messages.target_history_id   → another message in the same DB
//	iteration_history.message_id         → a message in the same DB
//	record_data JSON (compress/prune)    → ContextSnapshot.HistoryIDs
//	record_data JSON (mask/context_edit) → MaskMutations/MessageMutations targets
//	record_data JSON (ask_question/answer) → ToolHistoryID
//
// So each session DB gets a contiguous, non-overlapping id window and EVERY
// reference inside that DB is shifted by the same amount. Referential integrity is
// preserved exactly; only the absolute numbers change.

// idWindow is the offset applied to one session database.
type idWindow struct {
	msgOffset  int64 // added to session_messages.id and its references
	iterOffset int64 // added to iteration_history.id and its references
}

// copySessionDataWithOffsets merges every per-session database, assigning each a
// disjoint id window.
func copySessionDataWithOffsets(ctx context.Context, pool *pgxpool.Pool, sessions []sessionDB) error {
	fmt.Println("\n── session databases ──")
	var totalMsgs, totalIters int
	var nextMsgID, nextIterID int64 = 1, 1

	for _, s := range sessions {
		w := idWindow{msgOffset: nextMsgID - 1, iterOffset: nextIterID - 1}

		nMsg, maxMsg, err := copySessionTable(ctx, s.db, pool, "session_messages", w, "msg")
		if err != nil {
			return fmt.Errorf("copy session_messages for tenant %d (%s): %w", s.tenantID, s.path, err)
		}
		nIter, maxIter, err := copySessionTable(ctx, s.db, pool, "iteration_history", w, "iter")
		if err != nil {
			return fmt.Errorf("copy iteration_history for tenant %d (%s): %w", s.tenantID, s.path, err)
		}
		nextMsgID += maxMsg
		nextIterID += maxIter
		totalMsgs += nMsg
		totalIters += nIter
		if nMsg > 0 || nIter > 0 {
			fmt.Printf("  tenant %-6d %6d msgs  %6d iters  (ids %d.. / %d..)\n",
				s.tenantID, nMsg, nIter, w.msgOffset+1, w.iterOffset+1)
		}
	}
	fmt.Printf("  %-13s %6d msgs  %6d iters\n", "TOTAL", totalMsgs, totalIters)
	return nil
}

// copySessionTable copies one table from one session DB, applying the id window.
// Returns (rows copied, id span used).
func copySessionTable(ctx context.Context, src *sql.DB, pool *pgxpool.Pool, table string, w idWindow, kind string) (int, int64, error) {
	srcCols, dstCols, dstTypes, err := planWithTypes(ctx, src, pool, table)
	if err != nil {
		return 0, 0, err
	}
	if len(srcCols) == 0 {
		return 0, 0, nil
	}

	rows, err := src.QueryContext(ctx,
		"SELECT "+joinQuoted(srcCols)+" FROM "+table)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	offset := w.msgOffset
	if kind == "iter" {
		offset = w.iterOffset
	}

	insertSQL := buildInsertSQL(table, dstCols, true)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	n := 0
	var maxLocal int64
	batch := &pgx.Batch{}
	for rows.Next() {
		vals := make([]any, len(srcCols))
		ptrs := make([]any, len(srcCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, 0, fmt.Errorf("scan row: %w", err)
		}
		converted := make([]any, len(vals))
		for i := range vals {
			col := srcCols[i]
			v := vals[i]
			// Shift this DB's own ids and its internal references.
			switch col {
			case "id":
				if local, ok := toInt64(v); ok {
					if local > maxLocal {
						maxLocal = local
					}
					v = local + offset
				}
			case "target_history_id", "message_id":
				if local, ok := toInt64(v); ok && local > 0 {
					v = local + offset
				}
			case "record_data":
				v = shiftRecordData(v, w)
			}
			converted[i] = convertValue(dstTypes[i], col, v)
		}
		batch.Queue(insertSQL, converted...)
		n++
		if batch.Len() >= 500 {
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return 0, 0, fmt.Errorf("insert batch into %s: %w", table, err)
			}
			batch = &pgx.Batch{}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return 0, 0, fmt.Errorf("insert final batch into %s: %w", table, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return n, maxLocal, nil
}

// shiftRecordData rewrites the history ids embedded in a control record's JSON so
// they keep pointing at the same rows after the window is applied. Unknown or
// malformed payloads are returned untouched (they carry no ids we understand).
func shiftRecordData(v any, w idWindow) any {
	var raw []byte
	switch t := v.(type) {
	case string:
		raw = []byte(t)
	case []byte:
		raw = t
	default:
		return v
	}
	if len(raw) == 0 {
		return v
	}

	// Compression / prune snapshot: ContextSnapshot{HistoryIDs, PendingAskUser}.
	var snap sqlite.ContextSnapshot
	if err := json.Unmarshal(raw, &snap); err == nil && snap.HistoryIDs != nil {
		for i, id := range snap.HistoryIDs {
			if id > 0 {
				snap.HistoryIDs[i] = id + w.msgOffset
			}
		}
		if snap.PendingAskUser != nil {
			if snap.PendingAskUser.HistoryID > 0 {
				snap.PendingAskUser.HistoryID += w.msgOffset
			}
			if snap.PendingAskUser.ToolHistoryID > 0 {
				snap.PendingAskUser.ToolHistoryID += w.msgOffset
			}
		}
		if out, err := json.Marshal(snap); err == nil {
			return string(out)
		}
		return v
	}

	// Mask / context-edit mutations.
	var masks sqlite.MaskMutations
	if err := json.Unmarshal(raw, &masks); err == nil && masks.Mutations != nil {
		for i := range masks.Mutations {
			if masks.Mutations[i].TargetHistoryID > 0 {
				masks.Mutations[i].TargetHistoryID += w.msgOffset
			}
		}
		if out, err := json.Marshal(masks); err == nil {
			return string(out)
		}
		return v
	}
	var edits sqlite.MessageMutations
	if err := json.Unmarshal(raw, &edits); err == nil && edits.Mutations != nil {
		for i := range edits.Mutations {
			if edits.Mutations[i].TargetHistoryID > 0 {
				edits.Mutations[i].TargetHistoryID += w.msgOffset
			}
		}
		if out, err := json.Marshal(edits); err == nil {
			return string(out)
		}
		return v
	}

	// AskUser question / answer: the tool row it targets.
	var q sqlite.AskQuestionRecord
	if err := json.Unmarshal(raw, &q); err == nil && q.ToolHistoryID > 0 {
		q.ToolHistoryID += w.msgOffset
		if out, err := json.Marshal(q); err == nil {
			return string(out)
		}
		return v
	}
	var a sqlite.AskAnswerRecord
	if err := json.Unmarshal(raw, &a); err == nil && a.ToolHistoryID > 0 {
		a.ToolHistoryID += w.msgOffset
		if out, err := json.Marshal(a); err == nil {
			return string(out)
		}
		return v
	}
	return v
}

func joinQuoted(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += `"` + c + `"`
	}
	return out
}
