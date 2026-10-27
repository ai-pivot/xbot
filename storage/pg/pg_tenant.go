package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/storage/sqlite"
)

type pgTenantStore struct {
	pool *pgxpool.Pool
}

func newPGTenantStore(pool *pgxpool.Pool) TenantStore {
	return &pgTenantStore{pool: pool}
}

func (s *pgTenantStore) GetOrCreateTenantID(channel, chatID string) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO tenants (channel, chat_id) VALUES ($1, $2)
		 ON CONFLICT (channel, chat_id) DO UPDATE SET last_active_at = now()
		 RETURNING id`, channel, chatID).Scan(&id)
	return id, err
}

func (s *pgTenantStore) GetTenantIDByChannelChatID(channel, chatID string) (int64, error) {
	ctx := context.Background()
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM tenants WHERE channel = $1 AND chat_id = $2`, channel, chatID).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (s *pgTenantStore) GetTenantInfo(tenantID int64) (string, string, error) {
	ctx := context.Background()
	var ch, cid string
	err := s.pool.QueryRow(ctx,
		`SELECT channel, chat_id FROM tenants WHERE id = $1`, tenantID).Scan(&ch, &cid)
	return ch, cid, err
}

func (s *pgTenantStore) DeleteTenant(tenantID int64) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	return err
}

func (s *pgTenantStore) DeleteTenantHistory(tenantID int64) error {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM iteration_history WHERE tenant_id = $1`, tenantID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM session_messages WHERE tenant_id = $1`, tenantID)
	return err
}

func (s *pgTenantStore) TouchTenantID(channel, chatID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE tenants SET last_active_at = now() WHERE channel = $1 AND chat_id = $2`,
		channel, chatID)
	return err
}

func (s *pgTenantStore) SetTenantDBPath(tenantID int64, dbPath string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE tenants SET db_path = $1 WHERE id = $2`, dbPath, tenantID)
	return err
}

func (s *pgTenantStore) SetTenantMigrated(tenantID int64) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE tenants SET migrated = true WHERE id = $1`, tenantID)
	return err
}

func (s *pgTenantStore) GetTenantDBInfo(tenantID int64) (sqlite.TenantDBInfo, error) {
	ctx := context.Background()
	var info sqlite.TenantDBInfo
	var migrated bool
	err := s.pool.QueryRow(ctx,
		`SELECT db_path, migrated FROM tenants WHERE id = $1`, tenantID).Scan(&info.DBPath, &migrated)
	if err != nil {
		return info, err
	}
	info.Migrated = migrated
	return info, nil
}

func (s *pgTenantStore) SetTenantSubscription(channel, chatID, subscriptionID, model string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE tenants SET subscription_id = $1, model = $2 WHERE channel = $3 AND chat_id = $4`,
		subscriptionID, model, channel, chatID)
	return err
}

func (s *pgTenantStore) GetTenantSubscription(channel, chatID string) (string, string, error) {
	ctx := context.Background()
	var subID, model string
	err := s.pool.QueryRow(ctx,
		`SELECT subscription_id, model FROM tenants WHERE channel = $1 AND chat_id = $2`,
		channel, chatID).Scan(&subID, &model)
	if err == pgx.ErrNoRows {
		return "", "", nil
	}
	return subID, model, err
}

func (s *pgTenantStore) SetTenantRunner(channel, chatID, runnerID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE tenants SET runner_id = $1 WHERE channel = $2 AND chat_id = $3`,
		runnerID, channel, chatID)
	return err
}

func (s *pgTenantStore) GetTenantRunner(channel, chatID string) (string, error) {
	ctx := context.Background()
	var runnerID string
	err := s.pool.QueryRow(ctx,
		`SELECT runner_id FROM tenants WHERE channel = $1 AND chat_id = $2`,
		channel, chatID).Scan(&runnerID)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return runnerID, err
}

func (s *pgTenantStore) ClearSubscriptionFromTenants(subID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE tenants SET subscription_id = '', model = '' WHERE subscription_id = $1`, subID)
	return err
}

func (s *pgTenantStore) SetTenantCWD(tenantID int64, cwd string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE tenants SET cwd = $1 WHERE id = $2`, cwd, tenantID)
	return err
}

func (s *pgTenantStore) GetTenantCWD(tenantID int64) (string, error) {
	ctx := context.Background()
	var cwd string
	err := s.pool.QueryRow(ctx, `SELECT cwd FROM tenants WHERE id = $1`, tenantID).Scan(&cwd)
	return cwd, err
}

func (s *pgTenantStore) SetTenantPreview(tenantID int64, content string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `UPDATE tenants SET preview = $1 WHERE id = $2`, pgText(content), tenantID)
	return err
}

func (s *pgTenantStore) CreateChat(channel, senderID, label string) (string, error) {
	ctx := context.Background()
	chatID := fmt.Sprintf("chat_%016X", time.Now().UnixNano())
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES ($1, $2, $3, $4)`,
		channel, senderID, chatID, label)
	return chatID, err
}

func (s *pgTenantStore) DeleteChat(channel, senderID, chatID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_chats WHERE channel = $1 AND sender_id = $2 AND chat_id = $3`,
		channel, senderID, chatID)
	return err
}

func (s *pgTenantStore) RenameChat(channel, senderID, chatID, label string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE user_chats SET label = $1 WHERE channel = $2 AND sender_id = $3 AND chat_id = $4`,
		label, channel, senderID, chatID)
	return err
}

func (s *pgTenantStore) ListUserChats(channel, senderID, currentChatID string, offset, limit int) ([]sqlite.UserChatWithPreview, bool, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT uc.chat_id, uc.label, uc.created_at, uc.sort_order,
		        COALESCE(t.preview, '') AS preview,
		        COALESCE(t.last_active_at, uc.created_at) AS last_active
		 FROM user_chats uc
		 LEFT JOIN tenants t ON t.channel = uc.channel AND t.chat_id = uc.chat_id
		 WHERE uc.channel = $1 AND uc.sender_id = $2
		 ORDER BY last_active DESC NULLS LAST, uc.sort_order ASC, uc.created_at ASC
		 LIMIT $3 OFFSET $4`,
		channel, senderID, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var result []sqlite.UserChatWithPreview
	for rows.Next() {
		var r sqlite.UserChatWithPreview
		var createdAt, lastActive time.Time
		if err := rows.Scan(&r.ChatID, &r.Label, &createdAt, &r.SortOrder, &r.Preview, &lastActive); err != nil {
			return nil, false, err
		}
		r.LastActive = lastActive
		r.CreatedAt = createdAt
		r.IsCurrent = r.ChatID == currentChatID
		result = append(result, r)
	}
	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}
	return result, hasMore, nil
}

func (s *pgTenantStore) UpdateChatSortOrders(channel, senderID string, orders map[string]int) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	for chatID, order := range orders {
		if _, err := tx.Exec(ctx,
			`UPDATE user_chats SET sort_order = $1 WHERE channel = $2 AND sender_id = $3 AND chat_id = $4`,
			order, channel, senderID, chatID); err != nil {
			return fmt.Errorf("update sort_order for %s: %w", chatID, err)
		}
	}
	return tx.Commit(ctx)
}

func (s *pgTenantStore) ListDistinctChannels() ([]string, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT channel FROM tenants WHERE chat_id != '_shared' ORDER BY channel`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (s *pgTenantStore) ListTenants() ([]sqlite.TenantInfo, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT t.id, t.channel, t.chat_id, COALESCE(c.label, '') AS label,
		        t.subscription_id, t.model, t.created_at, t.last_active_at,
		        COALESCE(t.preview, '') AS preview, COALESCE(c.sort_order, 0) AS sort_order
		 FROM tenants t
		 LEFT JOIN user_chats c ON c.channel = t.channel AND c.chat_id = t.chat_id
		 WHERE t.channel != '_shared'
		 ORDER BY t.last_active_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	var tenants []sqlite.TenantInfo
	for rows.Next() {
		var t sqlite.TenantInfo
		if err := rows.Scan(&t.ID, &t.Channel, &t.ChatID, &t.Label,
			&t.SubscriptionID, &t.Model, &t.CreatedAt, &t.LastActiveAt,
			&t.Preview, &t.SortOrder); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
}

func (s *pgTenantStore) GetSenderForChat(channel, chatID string) (string, error) {
	ctx := context.Background()
	var senderID string
	err := s.pool.QueryRow(ctx,
		`SELECT sender_id FROM user_chats WHERE channel = $1 AND chat_id = $2 ORDER BY created_at DESC LIMIT 1`,
		channel, chatID).Scan(&senderID)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return senderID, err
}

// GetChatOwner resolves the channel and sender owning a session. chat_id is unique
// in practice, so the lookup spans channels; empty results mean "not registered".
func (s *pgTenantStore) GetChatOwner(chatID string) (string, string, error) {
	ctx := context.Background()
	var channel, senderID string
	err := s.pool.QueryRow(ctx,
		`SELECT channel, sender_id FROM user_chats WHERE chat_id = $1
		 ORDER BY created_at DESC LIMIT 1`, chatID).Scan(&channel, &senderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get chat owner: %w", err)
	}
	return channel, senderID, nil
}

// GetChatLabel returns the user-assigned label of a session ("" when unset or
// unknown) — the backend-agnostic replacement for the direct user_chats SELECT.
func (s *pgTenantStore) GetChatLabel(channel, chatID string) (string, error) {
	ctx := context.Background()
	var label string
	err := s.pool.QueryRow(ctx,
		`SELECT label FROM user_chats WHERE channel = $1 AND chat_id = $2 AND label != ''
		 ORDER BY created_at DESC LIMIT 1`,
		channel, chatID).Scan(&label)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get chat label: %w", err)
	}
	return label, nil
}

func (s *pgTenantStore) AddPendingResume(channel, chatID, senderID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO pending_resumes (channel, chat_id, sender_id, content)
		 VALUES ($1, $2, $3, '')
		 ON CONFLICT (channel, chat_id) DO UPDATE SET sender_id = $3`,
		channel, chatID, senderID)
	return err
}

func (s *pgTenantStore) GetSessionSenderID(channel, chatID string) (string, error) {
	ctx := context.Background()
	var senderID string
	err := s.pool.QueryRow(ctx,
		`SELECT sender_id FROM user_chats WHERE channel = $1 AND chat_id = $2 ORDER BY created_at DESC LIMIT 1`,
		channel, chatID).Scan(&senderID)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return senderID, err
}

func (s *pgTenantStore) ListPendingResumes() ([]sqlite.PendingResume, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT channel, chat_id, sender_id, created_at::text FROM pending_resumes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []sqlite.PendingResume
	for rows.Next() {
		var r sqlite.PendingResume
		if err := rows.Scan(&r.Channel, &r.ChatID, &r.SenderID, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}

func (s *pgTenantStore) ClearPendingResume(channel, chatID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `DELETE FROM pending_resumes WHERE channel = $1 AND chat_id = $2`, channel, chatID)
	return err
}
