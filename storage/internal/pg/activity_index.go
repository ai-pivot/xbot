package pg

// activity_index.go — PG port of storage/sqlite/activity_index.go.
//
// activity_index redundantly stores a displayable summary of structured
// messages in the main DB so the cross-session activity feed is one index scan
// instead of opening every per-session DB.
//
// Boundaries (same as SQLite):
//   - The session DB remains the sole authority for message bodies; this table
//     only holds summaries.
//   - Only structured kinds are indexed (see common.ActivityKindIndexed); plain
//     text is intentionally excluded.
//   - created_at uses common.FormatActivityTime (fixed-width UTC text): string
//     order == time order, so ORDER BY created_at DESC walks the index.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	common "xbot/storage/common"
)

type activityStore struct {
	pool *pgxpool.Pool
}

func newActivityStore(pool *pgxpool.Pool) common.ActivityStore {
	return &activityStore{pool: pool}
}

// Insert writes one index row idempotently (UNIQUE(tenant_id, chat_id,
// message_id) => ON CONFLICT DO NOTHING).
//
// Call ordering: this must run after the message body has been persisted
// successfully. The index may lag, but it must never contain a row whose body
// does not exist — callers therefore log index failures instead of rolling back
// the original message (losing the body is unrecoverable data loss, losing one
// feed row is not).
func (a *activityStore) Insert(rec common.ActivityItem) error {
	if !common.ActivityKindIndexed(rec.Kind) {
		return nil
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = common.FormatActivityTime(time.Now())
	}
	_, err := a.pool.Exec(context.Background(),
		`INSERT INTO activity_index
		 (tenant_id, chat_id, channel, message_id, kind, sender_id, sender_name, summary, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (tenant_id, chat_id, message_id) DO NOTHING`,
		rec.TenantID, rec.ChatID, rec.Channel, rec.MessageID, rec.Kind,
		rec.SenderID, rec.SenderName, common.TruncateActivitySummary(rec.Summary), rec.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert activity index %s/%d: %w", rec.ChatID, rec.MessageID, err)
	}
	return nil
}

// normalizeActivityLimit keeps <=0 at the default and caps at the maximum.
func normalizeActivityLimit(limit int) int {
	if limit <= 0 {
		return common.ActivityLimitDefault
	}
	if limit > common.ActivityLimitMax {
		return common.ActivityLimitMax
	}
	return limit
}

// normalizeActivityWindow keeps <=0 at the 72h default.
func normalizeActivityWindow(d time.Duration) time.Duration {
	if d <= 0 {
		return common.ActivityWindowDefault
	}
	return d
}

// List returns the most recent activity (newest first) with the time window and
// kind filter applied. hasMore is decided by fetching one extra row instead of
// running COUNT.
func (a *activityStore) List(limit int, within time.Duration, kinds []string) ([]common.ActivityItem, bool, error) {
	limit = normalizeActivityLimit(limit)
	within = normalizeActivityWindow(within)
	cutoff := common.FormatActivityTime(time.Now().Add(-within))

	const selectClause = `
		SELECT tenant_id, chat_id, channel, message_id, kind, sender_id, sender_name, summary, created_at
		  FROM activity_index
		 WHERE created_at >= $1`
	const orderClause = ` ORDER BY created_at DESC, id DESC LIMIT `

	var (
		rows pgx.Rows
		err  error
	)
	// id DESC is only a deterministic tie-break for identical timestamps; it
	// does not change the newest-first semantics.
	if len(kinds) > 0 {
		rows, err = a.pool.Query(context.Background(),
			selectClause+` AND kind = ANY($2)`+orderClause+`$3`, cutoff, kinds, limit+1)
	} else {
		rows, err = a.pool.Query(context.Background(),
			selectClause+orderClause+`$2`, cutoff, limit+1)
	}
	if err != nil {
		return nil, false, fmt.Errorf("list activity index: %w", err)
	}
	defer rows.Close()

	out := make([]common.ActivityItem, 0, limit)
	for rows.Next() {
		var it common.ActivityItem
		if err := rows.Scan(&it.TenantID, &it.ChatID, &it.Channel, &it.MessageID, &it.Kind,
			&it.SenderID, &it.SenderName, &it.Summary, &it.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("scan activity row: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate activity rows: %w", err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// Count returns the total number of indexed rows (ops/tests only; not part of
// the feed query path).
func (a *activityStore) Count() (int64, error) {
	var n int64
	if err := a.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM activity_index`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count activity index: %w", err)
	}
	return n, nil
}
