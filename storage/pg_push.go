package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ─── Push targets (push_targets) — PostgreSQL backend ───

func (s *pgConfigStore) UpsertPushTarget(t *PushTarget) error {
	if t == nil {
		return fmt.Errorf("nil push target")
	}
	meta, err := json.Marshal(t.Metadata)
	if err != nil {
		return fmt.Errorf("marshal push metadata: %w", err)
	}
	if len(t.Metadata) == 0 {
		meta = []byte("{}")
	}
	now := time.Now()
	_, err = s.pool.Exec(context.Background(), `
INSERT INTO push_targets
    (user_id, sender_id, provider, platform, address, metadata, device_name, app_version, created_at, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (provider, address) DO UPDATE SET
    user_id = EXCLUDED.user_id,
    sender_id = EXCLUDED.sender_id,
    platform = EXCLUDED.platform,
    metadata = EXCLUDED.metadata,
    device_name = EXCLUDED.device_name,
    app_version = EXCLUDED.app_version,
    last_seen_at = EXCLUDED.last_seen_at`,
		t.UserID, t.SenderID, t.Provider, t.Platform, t.Address, string(meta),
		t.DeviceName, t.AppVersion, now, now)
	if err != nil {
		return fmt.Errorf("upsert push target: %w", err)
	}
	return nil
}

func (s *pgConfigStore) DeletePushTarget(userID int64, provider, address string) error {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM push_targets WHERE user_id = $1 AND provider = $2 AND address = $3`,
		userID, provider, address)
	if err != nil {
		return fmt.Errorf("delete push target: %w", err)
	}
	return nil
}

func (s *pgConfigStore) DeletePushTargetByID(id int64) error {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM push_targets WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete push target by id: %w", err)
	}
	return nil
}

func (s *pgConfigStore) ListPushTargetsBySender(senderID string) ([]PushTarget, error) {
	rows, err := s.pool.Query(context.Background(), `
SELECT id, user_id, sender_id, provider, platform, address, metadata,
       device_name, app_version, created_at, last_seen_at
FROM push_targets WHERE sender_id = $1 ORDER BY id`, senderID)
	if err != nil {
		return nil, fmt.Errorf("list push targets: %w", err)
	}
	defer rows.Close()

	out := make([]PushTarget, 0, 4)
	for rows.Next() {
		var t PushTarget
		var meta []byte
		if err := rows.Scan(&t.ID, &t.UserID, &t.SenderID, &t.Provider, &t.Platform,
			&t.Address, &meta, &t.DeviceName, &t.AppVersion, &t.CreatedAt, &t.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan push target: %w", err)
		}
		t.Metadata = map[string]string{}
		if len(meta) > 0 {
			if err := json.Unmarshal(meta, &t.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal push metadata: %w", err)
			}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate push targets: %w", err)
	}
	return out, nil
}

func (s *pgConfigStore) TouchPushTargets(ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(context.Background(),
		`UPDATE push_targets SET last_seen_at = $1 WHERE id = ANY($2::bigint[])`, at, ids)
	if err != nil {
		return fmt.Errorf("touch push targets: %w", err)
	}
	return nil
}
