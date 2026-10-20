package storage

import (
	"fmt"

	"xbot/llm"
)

// copySessionMessages materialises the source session's *replayed conversation*
// into the destination session.
//
// ⛔ This must not be a raw `INSERT … SELECT` of session_messages. The table is an
// append-only log of everything the session ever did; the conversation is a
// derived view of it (Replay: collapse to the latest compression checkpoint,
// apply masks/context edits, drop display-only rows). Two consequences:
//
//   - Compression summaries live inside the compress record's `record_data` JSON,
//     not as rows, so the replayed state cannot be reconstructed from rows.
//   - display-only rows are not conversation at all: cron notifications, command
//     output, and other synthetic tool pairs. Copying them made a fork inherit the
//     source session's scheduled notifications — the agent then saw
//     "A scheduled cron job fired" in its inherited context.
//
// So the rows go through the process by design. What is worth optimising is the
// write side (one batched insert) and, where a backend can, the pure-copy half
// (iteration_history) — see each backend's CopySessionFrom.
func copySessionMessages(src, dst SessionStore, srcTenantID, dstTenantID int64) ([]llm.ChatMessage, error) {
	msgs, err := src.GetAllMessages(srcTenantID)
	if err != nil {
		return nil, fmt.Errorf("read source messages: %w", err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	msgs = llm.SanitizeMessages(msgs)
	if len(msgs) == 0 {
		return nil, nil
	}
	for i := range msgs {
		msgs[i].ID = 0 // destination re-assigns ids; turn ids are preserved
	}
	if _, err := dst.AppendMessages(dstTenantID, msgs); err != nil {
		return nil, fmt.Errorf("write forked messages: %w", err)
	}
	return msgs, nil
}

// copySessionIterations copies iteration_history rows, which *are* a pure copy:
// no replay view, no derivation. Backends that can express it as
// `INSERT … SELECT` should (PG does); backends whose sessions live in separate
// files read and append instead (SQLite v71).
func copySessionIterations(src, dst SessionStore, srcTenantID, dstTenantID int64) error {
	recs, err := src.GetAllIterationHistory(srcTenantID)
	if err != nil {
		// Iteration detail is a rendering nicety, not context data — the forked
		// messages are still a valid prefix for the LLM.
		return err
	}
	for i := range recs {
		recs[i].MessageID = 0
	}
	return dst.AppendIterationHistories(dstTenantID, recs)
}
