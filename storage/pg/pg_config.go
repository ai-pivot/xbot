package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"xbot/event"
	"xbot/storage/sqlite"
)

// pgConfigStore implements ConfigStore for PostgreSQL.
type pgConfigStore struct {
	pool *pgxpool.Pool
}

func cachedModelsJSON(sub *sqlite.LLMSubscription) string {
	if len(sub.CachedModels) == 0 {
		return ""
	}
	b, _ := json.Marshal(sub.CachedModels)
	return string(b)
}

func newPGConfigStore(pool *pgxpool.Pool) ConfigStore {
	return &pgConfigStore{pool: pool}
}

// ── LLM subscriptions ──

func (s *pgConfigStore) ListSubscriptions(senderID string) ([]*sqlite.LLMSubscription, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, sender_id, name, provider, base_url, api_key, model,
		        max_context, max_output_tokens, thinking_mode, cached_models,
		        api_type, enabled, created_at, updated_at
		 FROM user_llm_subscriptions WHERE sender_id = $1 ORDER BY created_at`, senderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*sqlite.LLMSubscription
	for rows.Next() {
		sub := &sqlite.LLMSubscription{}
		var cachedModelsStr sql.NullString
		if err := rows.Scan(&sub.ID, &sub.SenderID, &sub.Name, &sub.Provider,
			&sub.BaseURL, &sub.APIKey, &sub.Model, &sub.MaxContext,
			&sub.MaxOutputTokens, &sub.ThinkingMode, &cachedModelsStr,
			&sub.APIType, &sub.Enabled, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		if cachedModelsStr.Valid && cachedModelsStr.String != "" {
			json.Unmarshal([]byte(cachedModelsStr.String), &sub.CachedModels)
		}
		result = append(result, sub)
	}
	return result, nil
}

func (s *pgConfigStore) GetSubscription(id string) (*sqlite.LLMSubscription, error) {
	ctx := context.Background()
	sub := &sqlite.LLMSubscription{}
	var cachedModelsStr sql.NullString
	err := s.pool.QueryRow(ctx,
		`SELECT id, sender_id, name, provider, base_url, api_key, model,
		        max_context, max_output_tokens, thinking_mode, cached_models,
		        api_type, enabled, created_at, updated_at
		 FROM user_llm_subscriptions WHERE id = $1`, id).Scan(
		&sub.ID, &sub.SenderID, &sub.Name, &sub.Provider,
		&sub.BaseURL, &sub.APIKey, &sub.Model, &sub.MaxContext,
		&sub.MaxOutputTokens, &sub.ThinkingMode, &cachedModelsStr,
		&sub.APIType, &sub.Enabled, &sub.CreatedAt, &sub.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Every read path must decode this column. Forgetting it here left
	// CachedModels nil, and a nil []string is encoded by pgx as SQL NULL — which
	// the NOT NULL column rejects (23502) the moment the caller writes the row
	// back (see UpdateSubscription).
	if cachedModelsStr.Valid && cachedModelsStr.String != "" {
		json.Unmarshal([]byte(cachedModelsStr.String), &sub.CachedModels)
	}
	return sub, nil
}

func (s *pgConfigStore) GetDefaultSubscription(senderID string) (*sqlite.LLMSubscription, error) {
	ctx := context.Background()
	sub := &sqlite.LLMSubscription{}
	var cachedModelsStr sql.NullString
	err := s.pool.QueryRow(ctx,
		`SELECT s.id, s.sender_id, s.name, s.provider, s.base_url, s.api_key, s.model,
		        s.max_context, s.max_output_tokens, s.thinking_mode, s.cached_models,
		        s.api_type, s.enabled, s.created_at, s.updated_at
		 FROM user_llm_subscriptions s
		 INNER JOIN user_default_model d ON d.subscription_id = s.id
		 WHERE d.sender_id = $1`, senderID).Scan(
		&sub.ID, &sub.SenderID, &sub.Name, &sub.Provider,
		&sub.BaseURL, &sub.APIKey, &sub.Model, &sub.MaxContext,
		&sub.MaxOutputTokens, &sub.ThinkingMode, &cachedModelsStr,
		&sub.APIType, &sub.Enabled, &sub.CreatedAt, &sub.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if cachedModelsStr.Valid && cachedModelsStr.String != "" {
		json.Unmarshal([]byte(cachedModelsStr.String), &sub.CachedModels)
	}
	return sub, err
}

func (s *pgConfigStore) AddSubscription(sub *sqlite.LLMSubscription) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_llm_subscriptions
		 (id, sender_id, name, provider, base_url, api_key, model,
		  max_context, max_output_tokens, thinking_mode, cached_models, api_type, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		sub.ID, sub.SenderID, sub.Name, sub.Provider, sub.BaseURL,
		sub.APIKey, sub.Model, sub.MaxContext, sub.MaxOutputTokens,
		sub.ThinkingMode, cachedModelsJSON(sub), sub.APIType, sub.Enabled)
	return err
}

// UpdateSubscription rewrites the user-editable columns of a subscription.
//
// `cached_models` is deliberately NOT in the SET list. It is not user-editable —
// it is the API-discovered model cache — and SQLite's LLMSubscriptionService.Update
// does not write it either. Keeping the two backends identical here also removes a
// data-loss trap: a caller that loads a subscription, edits its name and writes it
// back must not wipe the cache. (The previous code passed `sub.CachedModels`
// straight into this TEXT column, so pgx encoded a nil []string as SQL NULL and the
// NOT NULL column rejected the statement — SQLSTATE 23502 on every edit.)
func (s *pgConfigStore) UpdateSubscription(sub *sqlite.LLMSubscription) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE user_llm_subscriptions SET
		 name=$1, provider=$2, base_url=$3, api_key=$4, model=$5,
		 max_context=$6, max_output_tokens=$7, thinking_mode=$8,
		 api_type=$9, enabled=$10, updated_at=now()
		 WHERE id=$11`,
		sub.Name, sub.Provider, sub.BaseURL, sub.APIKey, sub.Model,
		sub.MaxContext, sub.MaxOutputTokens, sub.ThinkingMode,
		sub.APIType, sub.Enabled, sub.ID)
	return err
}

func (s *pgConfigStore) RemoveSubscription(id string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `DELETE FROM user_llm_subscriptions WHERE id = $1`, id)
	return err
}

func (s *pgConfigStore) SetDefaultSubscription(id string) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var senderID string
	if err := tx.QueryRow(ctx, `SELECT sender_id FROM user_llm_subscriptions WHERE id = $1`, id).Scan(&senderID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO user_default_model (sender_id, subscription_id) VALUES ($1, $2)
		 ON CONFLICT (sender_id) DO UPDATE SET subscription_id = $2, updated_at = now()`,
		senderID, id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *pgConfigStore) RenameSubscription(id, name string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE user_llm_subscriptions SET name=$1, updated_at=now() WHERE id=$2`, name, id)
	return err
}

func (s *pgConfigStore) SetSubscriptionEnabled(id string, enabled bool) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE user_llm_subscriptions SET enabled=$1, updated_at=now() WHERE id=$2`, enabled, id)
	return err
}

// ── User profile + settings ──

func (s *pgConfigStore) GetUserProfile(senderID string) (string, string, error) {
	ctx := context.Background()
	var name, profile string
	err := s.pool.QueryRow(ctx,
		`SELECT name, profile FROM user_profiles WHERE sender_id = $1`, senderID).Scan(&name, &profile)
	if err == pgx.ErrNoRows {
		return "", "", nil
	}
	return name, profile, err
}

func (s *pgConfigStore) SaveUserProfile(senderID, name, profile string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_profiles (sender_id, name, profile) VALUES ($1, $2, $3)
		 ON CONFLICT (sender_id) DO UPDATE SET name=$2, profile=$3, updated_at=now()`,
		senderID, name, profile)
	return err
}

func (s *pgConfigStore) GetUserSettings(channel, senderID string) (map[string]string, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT key, value FROM user_settings WHERE channel = $1 AND sender_id = $2`,
		channel, senderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		result[k] = v
	}
	return result, nil
}

func (s *pgConfigStore) SetUserSetting(channel, senderID, key, value string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_settings (channel, sender_id, key, value, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (channel, sender_id, key) DO UPDATE SET value=$4, updated_at=$5`,
		channel, senderID, key, pgText(value), time.Now().Unix())
	return err
}

func (s *pgConfigStore) DeleteUserSetting(channel, senderID, key string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_settings WHERE channel = $1 AND sender_id = $2 AND key = $3`,
		channel, senderID, key)
	return err
}

// ── Token usage ──

func (s *pgConfigStore) RecordUsage(senderID, model string, inputTokens, outputTokens, cachedTokens, convCount, llmCallCount int) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx,
		`INSERT INTO user_token_usage (sender_id, input_tokens, output_tokens, total_tokens, cached_tokens, conversation_count, llm_call_count)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (sender_id) DO UPDATE SET
		  input_tokens = user_token_usage.input_tokens + $2,
		  output_tokens = user_token_usage.output_tokens + $3,
		  total_tokens = user_token_usage.total_tokens + $4,
		  cached_tokens = user_token_usage.cached_tokens + $5,
		  conversation_count = user_token_usage.conversation_count + $6,
		  llm_call_count = user_token_usage.llm_call_count + $7,
		  updated_at = now()`,
		senderID, inputTokens, outputTokens, inputTokens+outputTokens, cachedTokens, convCount, llmCallCount)
	if err != nil {
		return err
	}
	date := time.Now().Format("2006-01-02")
	_, err = tx.Exec(ctx,
		`INSERT INTO daily_token_usage (date, sender_id, model, input_tokens, output_tokens, cached_tokens, conversation_count, llm_call_count)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (date, sender_id, model) DO UPDATE SET
		  input_tokens = daily_token_usage.input_tokens + $4,
		  output_tokens = daily_token_usage.output_tokens + $5,
		  cached_tokens = daily_token_usage.cached_tokens + $6,
		  conversation_count = daily_token_usage.conversation_count + $7,
		  llm_call_count = daily_token_usage.llm_call_count + $8,
		  updated_at = now()`,
		date, senderID, model, inputTokens, outputTokens, cachedTokens, convCount, llmCallCount)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *pgConfigStore) GetUsage(senderID string) (*sqlite.UserTokenUsage, error) {
	ctx := context.Background()
	u := &sqlite.UserTokenUsage{}
	err := s.pool.QueryRow(ctx,
		`SELECT sender_id, input_tokens, output_tokens, total_tokens,
		        cached_tokens, conversation_count, llm_call_count
		 FROM user_token_usage WHERE sender_id = $1`, senderID).Scan(
		&u.SenderID, &u.InputTokens, &u.OutputTokens, &u.TotalTokens,
		&u.CachedTokens, &u.ConversationCount, &u.LLMCallCount)
	if err == pgx.ErrNoRows {
		return &sqlite.UserTokenUsage{SenderID: senderID}, nil
	}
	return u, err
}

func (s *pgConfigStore) GetDailyUsageSummary(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	return s.GetDailyUsage(senderID, days)
}
func (s *pgConfigStore) GetAllUsage() ([]sqlite.UserTokenUsage, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT sender_id, input_tokens, output_tokens, total_tokens, cached_tokens, conversation_count, llm_call_count
		 FROM user_token_usage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []sqlite.UserTokenUsage
	for rows.Next() {
		var u sqlite.UserTokenUsage
		if err := rows.Scan(&u.SenderID, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.CachedTokens, &u.ConversationCount, &u.LLMCallCount); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, nil
}
func (s *pgConfigStore) GetDailyUsage(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT date, sender_id, model, input_tokens, output_tokens,
		        cached_tokens, conversation_count, llm_call_count
		 FROM daily_token_usage
		 WHERE sender_id = $1 AND date >= to_char(CURRENT_DATE - $2::int, 'YYYY-MM-DD')
		 ORDER BY date DESC`,
		senderID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []sqlite.DailyTokenUsage
	for rows.Next() {
		var d sqlite.DailyTokenUsage
		if err := rows.Scan(&d.Date, &d.SenderID, &d.Model, &d.InputTokens,
			&d.OutputTokens, &d.CachedTokens, &d.ConversationCount, &d.LLMCallCount); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, nil
}

// ── Cron ──

func (s *pgConfigStore) AddCronJob(job *sqlite.CronJob) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cron_jobs (id, message, channel, chat_id, sender_id, cron_expr,
		 every_seconds, delay_seconds, at, created_at, next_run, one_shot)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now(),$10,$11)`,
		job.ID, job.Message, job.Channel, job.ChatID, job.SenderID,
		job.CronExpr, job.EverySeconds, job.DelaySeconds, job.At,
		job.NextRun, job.OneShot)
	return err
}

func (s *pgConfigStore) RemoveCronJob(id string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `DELETE FROM cron_jobs WHERE id = $1`, id)
	return err
}

func (s *pgConfigStore) GetCronJob(id string) (*sqlite.CronJob, error) {
	ctx := context.Background()
	job := &sqlite.CronJob{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, message, channel, chat_id, sender_id, cron_expr,
		        every_seconds, delay_seconds, at, next_run, last_trigger, one_shot
		 FROM cron_jobs WHERE id = $1`, id).Scan(
		&job.ID, &job.Message, &job.Channel, &job.ChatID, &job.SenderID,
		&job.CronExpr, &job.EverySeconds, &job.DelaySeconds, &job.At,
		&job.NextRun, &job.LastTrigger, &job.OneShot)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return job, err
}

func (s *pgConfigStore) ListCronJobsBySender(senderID string) ([]*sqlite.CronJob, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, message, channel, chat_id, sender_id, cron_expr,
		        every_seconds, delay_seconds, at, next_run, last_trigger, one_shot
		 FROM cron_jobs WHERE sender_id = $1 ORDER BY created_at`, senderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

func (s *pgConfigStore) ListAllCronJobs() ([]*sqlite.CronJob, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, message, channel, chat_id, sender_id, cron_expr,
		        every_seconds, delay_seconds, at, next_run, last_trigger, one_shot
		 FROM cron_jobs ORDER BY next_run`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

func (s *pgConfigStore) ListCronJobsByChannelChatID(channel, chatID string) ([]*sqlite.CronJob, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, message, channel, chat_id, sender_id, cron_expr,
		        every_seconds, delay_seconds, at, next_run, last_trigger, one_shot
		 FROM cron_jobs WHERE channel = $1 AND chat_id = $2`, channel, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

func (s *pgConfigStore) UpdateCronNextRun(id string, nextRun time.Time) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE cron_jobs SET next_run = $1 WHERE id = $2`, nextRun, id)
	return err
}

func scanCronJobs(rows pgxRows) ([]*sqlite.CronJob, error) {
	var result []*sqlite.CronJob
	for rows.Next() {
		job := &sqlite.CronJob{}
		if err := rows.Scan(&job.ID, &job.Message, &job.Channel, &job.ChatID,
			&job.SenderID, &job.CronExpr, &job.EverySeconds, &job.DelaySeconds,
			&job.At, &job.NextRun, &job.LastTrigger, &job.OneShot); err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, nil
}

// ── Triggers ──

func (s *pgConfigStore) AddTrigger(t *event.Trigger) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO event_triggers (id, name, event_type, channel, chat_id,
		 sender_id, message_tpl, secret, enabled, one_shot, created_at, fire_count)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now(),0)`,
		t.ID, t.Name, t.EventType, t.Channel, t.ChatID,
		t.SenderID, t.MessageTpl, t.Secret, t.Enabled, t.OneShot)
	return err
}

func (s *pgConfigStore) RemoveTrigger(id string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `DELETE FROM event_triggers WHERE id = $1`, id)
	return err
}

func (s *pgConfigStore) GetTrigger(id string) (*event.Trigger, error) {
	ctx := context.Background()
	t := &event.Trigger{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, event_type, channel, chat_id, sender_id,
		        message_tpl, secret, enabled, one_shot, fire_count
		 FROM event_triggers WHERE id = $1`, id).Scan(
		&t.ID, &t.Name, &t.EventType, &t.Channel, &t.ChatID,
		&t.SenderID, &t.MessageTpl, &t.Secret, &t.Enabled, &t.OneShot, &t.FireCount)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *pgConfigStore) ListTriggersByEventType(eventType string) ([]*event.Trigger, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, event_type, channel, chat_id, sender_id,
		        message_tpl, secret, enabled, one_shot, fire_count
		 FROM event_triggers WHERE event_type = $1 AND enabled = true`, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTriggers(rows)
}

func (s *pgConfigStore) ListTriggersBySender(senderID string) ([]*event.Trigger, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, event_type, channel, chat_id, sender_id,
		        message_tpl, secret, enabled, one_shot, fire_count
		 FROM event_triggers WHERE sender_id = $1`, senderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTriggers(rows)
}

func (s *pgConfigStore) UpdateTriggerEnabled(id string, enabled bool) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE event_triggers SET enabled = $1 WHERE id = $2`, enabled, id)
	return err
}

func (s *pgConfigStore) RecordTriggerFire(id string, at time.Time) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE event_triggers SET last_fired = $1, fire_count = fire_count + 1 WHERE id = $2`, at, id)
	return err
}

func scanTriggers(rows pgxRows) ([]*event.Trigger, error) {
	var result []*event.Trigger
	for rows.Next() {
		t := &event.Trigger{}
		if err := rows.Scan(&t.ID, &t.Name, &t.EventType, &t.Channel, &t.ChatID,
			&t.SenderID, &t.MessageTpl, &t.Secret, &t.Enabled, &t.OneShot, &t.FireCount); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, nil
}

// ── Sharing ──

func (s *pgConfigStore) CreateSharedArtifact(a *sqlite.SharedArtifact) error {
	ctx := context.Background()
	if a.Token == "" {
		a.Token = fmt.Sprintf("share_%d_%d", time.Now().UnixNano(), a.CreatedBy)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO shared_artifacts (token, plugin_id, content_type, payload, title, created_at, created_by)
		 VALUES ($1,$2,$3,$4,$5,now(),$6)`,
		a.Token, a.PluginID, a.ContentType, a.Payload, a.Title, a.CreatedBy)
	return err
}

func (s *pgConfigStore) GetSharedArtifact(token string) (*sqlite.SharedArtifact, error) {
	ctx := context.Background()
	a := &sqlite.SharedArtifact{}
	err := s.pool.QueryRow(ctx,
		`SELECT token, plugin_id, content_type, payload, title, created_at::text, created_by
		 FROM shared_artifacts
		 WHERE token = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`,
		token).Scan(&a.Token, &a.PluginID, &a.ContentType, &a.Payload, &a.Title, &a.CreatedAt, &a.CreatedBy)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return a, err
}

func (s *pgConfigStore) RevokeSharedArtifact(token string, createdBy int64) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE shared_artifacts SET revoked_at = now()
		 WHERE token = $1 AND created_by = $2`, token, createdBy)
	return err
}

func (s *pgConfigStore) ListSharedArtifactsByCreator(createdBy int64) ([]sqlite.SharedArtifact, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT token, plugin_id, content_type, payload, title, created_at::text, created_by
		 FROM shared_artifacts WHERE created_by = $1 AND revoked_at IS NULL
		 ORDER BY created_at DESC`, createdBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []sqlite.SharedArtifact
	for rows.Next() {
		var a sqlite.SharedArtifact
		if err := rows.Scan(&a.Token, &a.PluginID, &a.ContentType, &a.Payload,
			&a.Title, &a.CreatedAt, &a.CreatedBy); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, nil
}

// ── Web UI authentication (web_users) ──
//
// PG-specific SQL: $N placeholders, identity-generated ids (RETURNING id —
// no LastInsertId), TIMESTAMPTZ created_at cast to text so it scans into a
// plain string like the SQLite adapter does.

func (s *pgConfigStore) CreateWebUser(username, hashedPassword string) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO web_users (username, password) VALUES ($1, $2) RETURNING id`,
		username, hashedPassword).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *pgConfigStore) GetWebUserForLogin(username string) (int64, string, error) {
	ctx := context.Background()
	var id int64
	var hashed string
	err := s.pool.QueryRow(ctx,
		`SELECT id, password FROM web_users WHERE username = $1`, username).Scan(&id, &hashed)
	if err != nil {
		return 0, "", err
	}
	return id, hashed, nil
}

func (s *pgConfigStore) ListWebUsers() ([]WebUserInfo, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, username, created_at::text FROM web_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []WebUserInfo
	for rows.Next() {
		var u WebUserInfo
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if users == nil {
		users = []WebUserInfo{}
	}
	return users, nil
}

func (s *pgConfigStore) DeleteWebUser(username string) error {
	ctx := context.Background()
	tag, err := s.pool.Exec(ctx, `DELETE FROM web_users WHERE username = $1`, username)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user %q not found", username)
	}
	return nil
}

func (s *pgConfigStore) CountWebUsers() (int, error) {
	ctx := context.Background()
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM web_users`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// SubscriptionBackend
func (s *pgConfigStore) GetModel(subID, model string) (*sqlite.SubscriptionModel, error) {
	ctx := context.Background()
	m := &sqlite.SubscriptionModel{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, enabled, vision, vision_detail
		 FROM subscription_models WHERE subscription_id = $1 AND model = $2`, subID, model).Scan(
		&m.ID, &m.SubscriptionID, &m.Model, &m.MaxContext, &m.MaxOutputTokens,
		&m.ThinkingMode, &m.APIType, &m.Enabled, &m.Vision, &m.VisionDetail)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return m, err
}
func (s *pgConfigStore) GetModels(subID string) ([]*sqlite.SubscriptionModel, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, enabled, vision, vision_detail
		 FROM subscription_models WHERE subscription_id = $1`, subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*sqlite.SubscriptionModel
	for rows.Next() {
		m := &sqlite.SubscriptionModel{}
		if err := rows.Scan(&m.ID, &m.SubscriptionID, &m.Model, &m.MaxContext, &m.MaxOutputTokens,
			&m.ThinkingMode, &m.APIType, &m.Enabled, &m.Vision, &m.VisionDetail); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, nil
}
func (s *pgConfigStore) EnsureModel(subID, model string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO subscription_models (id, subscription_id, model, enabled)
		 VALUES ($1, $2, $3, true)
		 ON CONFLICT (subscription_id, model) DO NOTHING`,
		fmt.Sprintf("%s-%s", subID, model), subID, model)
	return err
}
func (s *pgConfigStore) SetModelEnabled(subID, model string, enabled bool) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE subscription_models SET enabled = $1 WHERE subscription_id = $2 AND model = $3`,
		enabled, subID, model)
	return err
}
func (s *pgConfigStore) GetUserDefaultModel(senderID string) (*sqlite.UserDefaultModel, error) {
	ctx := context.Background()
	m := &sqlite.UserDefaultModel{}
	err := s.pool.QueryRow(ctx,
		`SELECT sender_id, subscription_id, model FROM user_default_model WHERE sender_id = $1`, senderID).Scan(
		&m.SenderID, &m.SubscriptionID, &m.Model)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return m, err
}
func (s *pgConfigStore) SetUserDefaultModel(senderID, subID, model string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_default_model (sender_id, subscription_id, model)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (sender_id) DO UPDATE SET subscription_id = $2, model = $3, updated_at = now()`,
		senderID, subID, model)
	return err
}
func (s *pgConfigStore) ListAllSubscriptions() ([]*sqlite.LLMSubscription, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT id, sender_id, name, provider, base_url, api_key, model,
		        max_context, max_output_tokens, thinking_mode, cached_models,
		        api_type, enabled, created_at, updated_at
		 FROM user_llm_subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*sqlite.LLMSubscription
	for rows.Next() {
		sub := &sqlite.LLMSubscription{}
		var cachedModelsStr sql.NullString
		if err := rows.Scan(&sub.ID, &sub.SenderID, &sub.Name, &sub.Provider,
			&sub.BaseURL, &sub.APIKey, &sub.Model, &sub.MaxContext,
			&sub.MaxOutputTokens, &sub.ThinkingMode, &cachedModelsStr,
			&sub.APIType, &sub.Enabled, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		if cachedModelsStr.Valid && cachedModelsStr.String != "" {
			json.Unmarshal([]byte(cachedModelsStr.String), &sub.CachedModels)
		}
		result = append(result, sub)
	}
	return result, nil
}

func (s *pgConfigStore) GetDefault(senderID string) (*sqlite.LLMSubscription, error) {
	return s.GetDefaultSubscription(senderID)
}
func (s *pgConfigStore) List(senderID string) ([]*sqlite.LLMSubscription, error) {
	return s.ListSubscriptions(senderID)
}
func (s *pgConfigStore) ListAll() ([]*sqlite.LLMSubscription, error) {
	return s.ListAllSubscriptions()
}
func (s *pgConfigStore) UpsertModel(subID, model string, maxCtx, maxOut int, thinking, apiType string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO subscription_models (id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, true)
		 ON CONFLICT (subscription_id, model) DO UPDATE SET max_context = $4, max_output_tokens = $5, thinking_mode = $6, api_type = $7`,
		fmt.Sprintf("%s-%s", subID, model), subID, model, maxCtx, maxOut, thinking, apiType)
	return err
}

func (s *pgConfigStore) Add(sub *sqlite.LLMSubscription) error    { return s.AddSubscription(sub) }
func (s *pgConfigStore) Update(sub *sqlite.LLMSubscription) error { return s.UpdateSubscription(sub) }
func (s *pgConfigStore) Remove(id string) error                   { return s.RemoveSubscription(id) }
func (s *pgConfigStore) Rename(id, name string) error             { return s.RenameSubscription(id, name) }
func (s *pgConfigStore) SetDefault(id string) error               { return s.SetDefaultSubscription(id) }
func (s *pgConfigStore) SetModel(id, model string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE user_llm_subscriptions SET model = $1, updated_at = now() WHERE id = $2`, model, id)
	return err
}
func (s *pgConfigStore) SetModelMaxContext(subID, model string, maxCtx int) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE subscription_models SET max_context = $1 WHERE subscription_id = $2 AND model = $3`, maxCtx, subID, model)
	return err
}
func (s *pgConfigStore) SetModelMaxOutput(subID, model string, maxOut int) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE subscription_models SET max_output_tokens = $1 WHERE subscription_id = $2 AND model = $3`, maxOut, subID, model)
	return err
}
func (s *pgConfigStore) RemoveModel(subID, model string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `DELETE FROM subscription_models WHERE subscription_id = $1 AND model = $2`, subID, model)
	return err
}
func (s *pgConfigStore) Get(id string) (*sqlite.LLMSubscription, error) { return s.GetSubscription(id) }
func (s *pgConfigStore) ClearUserDefaultModel(senderID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `DELETE FROM user_default_model WHERE sender_id = $1`, senderID)
	return err
}

func (s *pgConfigStore) SetModelVisionConfig(subID, model string, vision bool, visionDetail string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE subscription_models SET vision = $1, vision_detail = $2 WHERE subscription_id = $3 AND model = $4`, vision, visionDetail, subID, model)
	return err
}

// UpdatePerModelConfigs replaces ALL per-model overrides for a subscription —
// the same contract as SQLite: the provided map is the complete new set (rows
// absent from it are dropped, missing rows are created), and api_type carries
// cfg.APIType (thinking_mode is not part of PerModelConfig).
func (s *pgConfigStore) UpdatePerModelConfigs(id string, configs map[string]sqlite.PerModelConfig) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM subscription_models WHERE subscription_id = $1`, id); err != nil {
		return fmt.Errorf("clear subscription_models: %w", err)
	}
	for model, cfg := range configs {
		if _, err := tx.Exec(ctx, `INSERT INTO subscription_models
			(id, subscription_id, model, max_context, max_output_tokens, thinking_mode, api_type, vision, vision_detail)
			VALUES (md5(random()::text || clock_timestamp()::text), $1, $2, $3, $4, '', $5, $6, $7)
			ON CONFLICT (subscription_id, model) DO UPDATE SET
				max_context = excluded.max_context,
				max_output_tokens = excluded.max_output_tokens,
				thinking_mode = excluded.thinking_mode,
				api_type = excluded.api_type,
				updated_at = now()`,
			id, model, cfg.MaxContext, cfg.MaxOutputTokens, cfg.APIType, cfg.Vision, cfg.VisionDetail); err != nil {
			return fmt.Errorf("upsert model %s: %w", model, err)
		}
	}
	return tx.Commit(ctx)
}
