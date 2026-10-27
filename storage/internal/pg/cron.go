package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	log "xbot/logger"
	"xbot/storage/sqlite"
)

// cronStore implements CronStore on PostgreSQL.
//
// The semantics mirror sqlite.CronService exactly (same ordering, same nil-on-
// missing, same legacy-import guard) so the scheduler behaves identically on both
// backends. Timestamps are native TIMESTAMPTZ here rather than the RFC3339 strings
// SQLite stores.
type cronStore struct {
	pool *pgxpool.Pool
}

func newCronStore(pool *pgxpool.Pool) *cronStore {
	return &cronStore{pool: pool}
}

// cronColumns is the shared projection. Nullable TEXT columns are COALESCEd:
// cron_expr/at are NULL for interval jobs, and scanning NULL into a plain string
// fails. last_trigger stays nullable (it is scanned into *time.Time).
const cronColumns = `id, message, channel, chat_id, COALESCE(sender_id, ''),
	COALESCE(cron_expr, ''), COALESCE(every_seconds, 0), COALESCE(delay_seconds, 0),
	COALESCE(at, ''), created_at, next_run, last_trigger, one_shot`

// scanCronJob reads one row. cron_expr/at are nullable, so they are COALESCEd —
// scanning NULL into a plain string fails.
func scanCronJob(row pgx.Row) (*sqlite.CronJob, error) {
	job := &sqlite.CronJob{}
	var createdAt, nextRun time.Time
	var lastTrigger *time.Time
	err := row.Scan(&job.ID, &job.Message, &job.Channel, &job.ChatID, &job.SenderID,
		&job.CronExpr, &job.EverySeconds, &job.DelaySeconds, &job.At,
		&createdAt, &nextRun, &lastTrigger, &job.OneShot)
	if err != nil {
		return nil, err
	}
	job.CreatedAt = createdAt
	job.NextRun = nextRun
	job.LastTrigger = lastTrigger
	return job, nil
}

func (s *cronStore) AddJob(job *sqlite.CronJob) error {
	ctx := context.Background()
	var cronExpr, at *string
	if job.CronExpr != "" {
		cronExpr = &job.CronExpr
	}
	if job.At != "" {
		at = &job.At
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cron_jobs (id, message, channel, chat_id, sender_id, cron_expr,
			every_seconds, delay_seconds, at, created_at, next_run, last_trigger, one_shot)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		job.ID, job.Message, job.Channel, job.ChatID, job.SenderID, cronExpr,
		job.EverySeconds, job.DelaySeconds, at, job.CreatedAt, job.NextRun, job.LastTrigger, job.OneShot)
	if err != nil {
		return fmt.Errorf("insert cron job: %w", err)
	}
	return nil
}

func (s *cronStore) RemoveJob(id string) error {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM cron_jobs WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete cron job: %w", err)
	}
	return nil
}

func (s *cronStore) GetJob(id string) (*sqlite.CronJob, error) {
	ctx := context.Background()
	job, err := scanCronJob(s.pool.QueryRow(ctx,
		`SELECT `+cronColumns+` FROM cron_jobs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan cron job: %w", err)
	}
	return job, nil
}

// listJobs is the shared query body: an optional WHERE plus an ORDER BY clause.
func (s *cronStore) listJobs(where, orderBy string, args ...any) ([]*sqlite.CronJob, error) {
	ctx := context.Background()
	query := `SELECT ` + cronColumns + ` FROM cron_jobs`
	if where != "" {
		query += ` WHERE ` + where
	}
	query += ` ORDER BY ` + orderBy
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query cron jobs: %w", err)
	}
	defer rows.Close()
	var jobs []*sqlite.CronJob
	for rows.Next() {
		job, err := scanCronJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan cron job row: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// ListJobs lists every job ordered by creation (the web cron panel's view).
func (s *cronStore) ListJobs() ([]*sqlite.CronJob, error) {
	return s.listJobs("", "created_at")
}

// ListAllJobs orders by next_run — what the scheduler iterates.
func (s *cronStore) ListAllJobs() ([]*sqlite.CronJob, error) {
	return s.listJobs("", "next_run")
}

func (s *cronStore) ListJobsByChannelChatID(channel, chatID string) ([]*sqlite.CronJob, error) {
	return s.listJobs("channel = $1 AND chat_id = $2", "created_at", channel, chatID)
}

func (s *cronStore) UpdateNextRun(id string, nextRun time.Time) error {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE cron_jobs SET next_run = $1 WHERE id = $2`, nextRun, id); err != nil {
		return fmt.Errorf("update cron job next_run: %w", err)
	}
	return nil
}

func (s *cronStore) UpdateLastTrigger(id string, lastTrigger time.Time) error {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE cron_jobs SET last_trigger = $1 WHERE id = $2`, lastTrigger, id); err != nil {
		return fmt.Errorf("update cron job last_trigger: %w", err)
	}
	return nil
}

// MigrateFromJSON imports the legacy cron.json once, matching the SQLite rule:
// if any job already exists the import is skipped, so repeated starts are safe.
func (s *cronStore) MigrateFromJSON(dataDir string) error {
	existing, err := s.ListAllJobs()
	if err != nil {
		return fmt.Errorf("check existing jobs: %w", err)
	}
	if len(existing) > 0 {
		log.Info("Cron jobs already in database, skipping JSON migration")
		return nil
	}

	newPath := filepath.Join(dataDir, ".xbot", "cron.json")
	oldPath := filepath.Join(dataDir, "cron.json")
	var jsonPath string
	if _, err := os.Stat(newPath); err == nil {
		jsonPath = newPath
	} else if _, err := os.Stat(oldPath); err == nil {
		jsonPath = oldPath
	} else {
		log.Info("No cron.json file found, skipping migration")
		return nil
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("read cron.json: %w", err)
	}
	var oldJobs map[string]struct {
		ID           string `json:"id"`
		Message      string `json:"message"`
		Channel      string `json:"channel"`
		ChatID       string `json:"chat_id"`
		SenderID     string `json:"sender_id,omitempty"`
		CronExpr     string `json:"cron_expr,omitempty"`
		EverySeconds int    `json:"every_seconds,omitempty"`
		DelaySeconds int    `json:"delay_seconds,omitempty"`
		At           string `json:"at,omitempty"`
		CreatedAt    string `json:"created_at"`
	}
	if err := json.Unmarshal(data, &oldJobs); err != nil {
		return fmt.Errorf("parse cron.json: %w", err)
	}
	for _, j := range oldJobs {
		job := &sqlite.CronJob{
			ID: j.ID, Message: j.Message, Channel: j.Channel, ChatID: j.ChatID,
			SenderID: j.SenderID, CronExpr: j.CronExpr,
			EverySeconds: j.EverySeconds, DelaySeconds: j.DelaySeconds, At: j.At,
			CreatedAt: time.Now(), NextRun: time.Now(),
		}
		if err := s.AddJob(job); err != nil {
			log.WithError(err).WithField("id", j.ID).Warn("Failed to migrate cron job")
		}
	}
	log.WithField("count", len(oldJobs)).Info("Migrated cron jobs from JSON")
	return nil
}
