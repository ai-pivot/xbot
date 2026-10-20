package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/event"
)

// pgTriggerStore implements event.TriggerStore on PostgreSQL.
//
// Same semantics as sqlite.TriggerService (nil-on-missing, enabled/one_shot as
// native BOOLEAN, last_fired nullable) so the webhook router behaves identically.
type pgTriggerStore struct {
	pool *pgxpool.Pool
}

func newPGTriggerStore(pool *pgxpool.Pool) *pgTriggerStore {
	return &pgTriggerStore{pool: pool}
}

const pgTriggerColumns = `id, name, event_type, channel, chat_id, sender_id, message_tpl,
	secret, enabled, one_shot, created_at, last_fired, fire_count`

func scanPGTrigger(row pgx.Row) (*event.Trigger, error) {
	t := &event.Trigger{}
	var lastFired *time.Time
	err := row.Scan(&t.ID, &t.Name, &t.EventType, &t.Channel, &t.ChatID, &t.SenderID,
		&t.MessageTpl, &t.Secret, &t.Enabled, &t.OneShot, &t.CreatedAt, &lastFired, &t.FireCount)
	if err != nil {
		return nil, err
	}
	t.LastFired = lastFired
	return t, nil
}

func (s *pgTriggerStore) AddTrigger(t *event.Trigger) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO event_triggers (id, name, event_type, channel, chat_id, sender_id,
			message_tpl, secret, enabled, one_shot, created_at, last_fired, fire_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		t.ID, t.Name, t.EventType, t.Channel, t.ChatID, t.SenderID,
		t.MessageTpl, t.Secret, t.Enabled, t.OneShot, t.CreatedAt, t.LastFired, t.FireCount)
	if err != nil {
		return fmt.Errorf("insert event trigger: %w", err)
	}
	return nil
}

func (s *pgTriggerStore) RemoveTrigger(id string) error {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM event_triggers WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete event trigger: %w", err)
	}
	return nil
}

// GetTrigger returns (nil, nil) when the trigger does not exist.
func (s *pgTriggerStore) GetTrigger(id string) (*event.Trigger, error) {
	ctx := context.Background()
	t, err := scanPGTrigger(s.pool.QueryRow(ctx,
		`SELECT `+pgTriggerColumns+` FROM event_triggers WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan event trigger: %w", err)
	}
	return t, nil
}

func (s *pgTriggerStore) listTriggers(where, orderBy string, args ...any) ([]*event.Trigger, error) {
	ctx := context.Background()
	query := `SELECT ` + pgTriggerColumns + ` FROM event_triggers`
	if where != "" {
		query += ` WHERE ` + where
	}
	query += ` ORDER BY ` + orderBy
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query event triggers: %w", err)
	}
	defer rows.Close()
	var out []*event.Trigger
	for rows.Next() {
		t, err := scanPGTrigger(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event trigger row: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *pgTriggerStore) ListByEventType(eventType string) ([]*event.Trigger, error) {
	return s.listTriggers("event_type = $1 AND enabled = true", "created_at", eventType)
}

func (s *pgTriggerStore) ListBySender(senderID string) ([]*event.Trigger, error) {
	return s.listTriggers("sender_id = $1", "created_at", senderID)
}

func (s *pgTriggerStore) UpdateEnabled(id string, enabled bool) error {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		`UPDATE event_triggers SET enabled = $1 WHERE id = $2`, enabled, id); err != nil {
		return fmt.Errorf("update event trigger enabled: %w", err)
	}
	return nil
}

// RecordFire stamps the fire time and increments the counter.
//
// Disabling a one-shot trigger is the ROUTER's responsibility (see
// event.Router.dispatchOne, which calls UpdateEnabled after RecordFire) — keeping
// that out of the store makes both backends behave identically and keeps the
// store's single job simple.
func (s *pgTriggerStore) RecordFire(id string, at time.Time) error {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `
		UPDATE event_triggers
		SET last_fired = $1, fire_count = fire_count + 1
		WHERE id = $2`, at, id); err != nil {
		return fmt.Errorf("record event trigger fire: %w", err)
	}
	return nil
}
