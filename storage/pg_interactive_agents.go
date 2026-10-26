package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/storage/sqlite"
)

// pgInteractiveAgentStore implements InteractiveAgentStore on PostgreSQL.
//
// Semantics mirror sqlite.InteractiveAgentService exactly (same ordering, same
// last_reply_at rule, same not-found sentinel). Timestamps stay RFC3339 TEXT on
// purpose: the registry compares and orders them as fixed-width UTC strings
// (see sqlite.ListActive), so both backends must produce the same values.
type pgInteractiveAgentStore struct {
	pool *pgxpool.Pool
}

func newPGInteractiveAgentStore(pool *pgxpool.Pool) *pgInteractiveAgentStore {
	return &pgInteractiveAgentStore{pool: pool}
}

const pgInteractiveAgentColumns = `agent_key, parent_channel, parent_chat_id, role_name, instance,
	group_id, task, background, status, turn_id, model_name, sub_id, origin_user_id,
	workspace_root, parent_agent_id, last_error, last_reply, last_reply_at,
	created_at, updated_at`

func scanPGInteractiveAgent(row pgx.Row) (sqlite.InteractiveAgentRecord, error) {
	var rec sqlite.InteractiveAgentRecord
	err := row.Scan(
		&rec.AgentKey, &rec.ParentChannel, &rec.ParentChatID, &rec.RoleName, &rec.Instance,
		&rec.GroupID, &rec.Task, &rec.Background, &rec.Status, &rec.TurnID,
		&rec.ModelName, &rec.SubID, &rec.OriginUserID, &rec.WorkspaceRoot,
		&rec.ParentAgentID, &rec.LastError, &rec.LastReply, &rec.LastReplyAt,
		&rec.CreatedAt, &rec.UpdatedAt)
	return rec, err
}

func (s *pgInteractiveAgentStore) Upsert(rec sqlite.InteractiveAgentRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if rec.CreatedAt == "" {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	lastReplyAt := ""
	if rec.LastReply != "" {
		lastReplyAt = rec.UpdatedAt
	}
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO interactive_agents (
			agent_key, parent_channel, parent_chat_id, role_name, instance, group_id,
			task, background, status, turn_id, model_name, sub_id, origin_user_id,
			workspace_root, parent_agent_id, last_error, last_reply, last_reply_at,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT (agent_key) DO UPDATE SET
			parent_channel=excluded.parent_channel,
			parent_chat_id=excluded.parent_chat_id,
			role_name=excluded.role_name,
			instance=excluded.instance,
			group_id=excluded.group_id,
			task=excluded.task,
			background=excluded.background,
			status=excluded.status,
			turn_id=excluded.turn_id,
			model_name=excluded.model_name,
			sub_id=excluded.sub_id,
			origin_user_id=excluded.origin_user_id,
			workspace_root=excluded.workspace_root,
			parent_agent_id=excluded.parent_agent_id,
			last_error=excluded.last_error,
			last_reply=excluded.last_reply,
			last_reply_at=CASE
				WHEN excluded.last_reply <> '' AND excluded.last_reply <> interactive_agents.last_reply
				THEN excluded.updated_at
				ELSE interactive_agents.last_reply_at END,
			updated_at=excluded.updated_at`,
		rec.AgentKey, rec.ParentChannel, rec.ParentChatID, rec.RoleName, rec.Instance,
		rec.GroupID, rec.Task, rec.Background, rec.Status, rec.TurnID,
		rec.ModelName, rec.SubID, rec.OriginUserID, rec.WorkspaceRoot,
		rec.ParentAgentID, rec.LastError, rec.LastReply, lastReplyAt,
		rec.CreatedAt, rec.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert interactive agent %s: %w", rec.AgentKey, err)
	}
	return nil
}

func (s *pgInteractiveAgentStore) Get(agentKey string) (*sqlite.InteractiveAgentRecord, error) {
	rec, err := scanPGInteractiveAgent(s.pool.QueryRow(context.Background(),
		`SELECT `+pgInteractiveAgentColumns+` FROM interactive_agents WHERE agent_key = $1`, agentKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sqlite.ErrInteractiveAgentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get interactive agent %s: %w", agentKey, err)
	}
	return &rec, nil
}

func (s *pgInteractiveAgentStore) ListChildAgents(parentChannel, parentChatID string) ([]sqlite.InteractiveAgentRecord, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT `+pgInteractiveAgentColumns+` FROM interactive_agents
		  WHERE parent_channel = $1 AND parent_chat_id = $2
		  ORDER BY created_at ASC`, parentChannel, parentChatID)
	if err != nil {
		return nil, fmt.Errorf("list child agents of %s:%s: %w", parentChannel, parentChatID, err)
	}
	defer rows.Close()

	var out []sqlite.InteractiveAgentRecord
	for rows.Next() {
		rec, err := scanPGInteractiveAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *pgInteractiveAgentStore) MarkStatus(agentKey, status string, turnID uint64) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE interactive_agents SET status = $1, turn_id = $2, updated_at = $3 WHERE agent_key = $4`,
		status, turnID, time.Now().UTC().Format(time.RFC3339), agentKey)
	if err != nil {
		return fmt.Errorf("mark interactive agent %s %s: %w", agentKey, status, err)
	}
	return nil
}

func (s *pgInteractiveAgentStore) MarkAllRunningAsInterrupted() (int64, error) {
	res, err := s.pool.Exec(context.Background(),
		`UPDATE interactive_agents SET status = $1, updated_at = $2 WHERE status = $3`,
		sqlite.InteractiveAgentInterrupted, time.Now().UTC().Format(time.RFC3339),
		sqlite.InteractiveAgentRunning)
	if err != nil {
		return 0, fmt.Errorf("mark running agents interrupted: %w", err)
	}
	return res.RowsAffected(), nil
}

func (s *pgInteractiveAgentStore) MarkUnloadedCascade(agentKey string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.pool.Exec(context.Background(), `
		WITH RECURSIVE tree(key) AS (
			SELECT $1::text
			UNION ALL
			SELECT a.agent_key FROM interactive_agents a
			  JOIN tree t ON a.parent_channel = 'agent'
			 AND a.parent_chat_id = substr(t.key, length('agent:') + 1)
		)
		UPDATE interactive_agents SET status = $2, updated_at = $3
		 WHERE agent_key IN (SELECT key FROM tree)`,
		agentKey, sqlite.InteractiveAgentUnloaded, now)
	if err != nil {
		return fmt.Errorf("cascade unload %s: %w", agentKey, err)
	}
	return nil
}

const pgOrphanIdleChunk = 400

func (s *pgInteractiveAgentStore) MarkOrphanedIdleUnloaded(liveKeys []string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	const stmt = `UPDATE interactive_agents SET status = $1, updated_at = $2
	               WHERE status = $3
	                 AND NOT EXISTS (
	                       SELECT 1 FROM tenants t
	                        WHERE t.channel = 'agent'
	                          AND t.chat_id = interactive_agents.parent_channel || ':' || interactive_agents.parent_chat_id
	                                        || '/' || interactive_agents.role_name
	                                        || CASE WHEN interactive_agents.instance <> '' THEN ':' || interactive_agents.instance ELSE '' END)`

	if len(liveKeys) == 0 {
		res, err := s.pool.Exec(context.Background(), stmt, sqlite.InteractiveAgentUnloaded, now, sqlite.InteractiveAgentIdle)
		if err != nil {
			return 0, fmt.Errorf("mark orphaned idle agents unloaded: %w", err)
		}
		return res.RowsAffected(), nil
	}

	var total int64
	for start := 0; start < len(liveKeys); start += pgOrphanIdleChunk {
		end := start + pgOrphanIdleChunk
		if end > len(liveKeys) {
			end = len(liveKeys)
		}
		chunk := liveKeys[start:end]

		var ph strings.Builder
		args := make([]any, 0, len(chunk)+3)
		args = append(args, sqlite.InteractiveAgentUnloaded, now, sqlite.InteractiveAgentIdle)
		for i, k := range chunk {
			if i > 0 {
				ph.WriteByte(',')
			}
			ph.WriteByte('$')
			ph.WriteString(fmt.Sprintf("%d", len(args)+1))
			args = append(args, k)
		}
		res, err := s.pool.Exec(context.Background(),
			stmt+` AND agent_key NOT IN (`+ph.String()+`)`, args...)
		if err != nil {
			return total, fmt.Errorf("mark orphaned idle agents unloaded: %w", err)
		}
		total += res.RowsAffected()
	}
	return total, nil
}

func (s *pgInteractiveAgentStore) ListActive(cutoff time.Time, includeUnloaded bool) ([]sqlite.AgentActivity, error) {
	query := `SELECT agent_key, role_name, instance, status, updated_at, last_reply_at
	            FROM interactive_agents
	           WHERE updated_at >= $1`
	if !includeUnloaded {
		query += ` AND status <> '` + sqlite.InteractiveAgentUnloaded + `'`
	}
	query += ` ORDER BY updated_at DESC, agent_key ASC`

	rows, err := s.pool.Query(context.Background(), query, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("list active agents: %w", err)
	}
	defer rows.Close()

	out := make([]sqlite.AgentActivity, 0, 64)
	for rows.Next() {
		var a sqlite.AgentActivity
		if err := rows.Scan(&a.AgentKey, &a.RoleName, &a.Instance, &a.Status, &a.UpdatedAt, &a.LastReplyAt); err != nil {
			return nil, fmt.Errorf("scan active agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

var _ InteractiveAgentStore = (*pgInteractiveAgentStore)(nil)
