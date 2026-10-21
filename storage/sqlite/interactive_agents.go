package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// InteractiveAgentRecord 是 interactive_agents 表的一行。
//
// 只存「重建所需的最小事实」：可寻址身份（agent_key / parent_*）、角色与运行
// 参数、运行状态、以及重建 RunConfig 需要的上下文快照。messages 不在这里 ——
// 它们在各自子代理的 TenantSession 里（channel="agent"）。
type InteractiveAgentRecord struct {
	// AgentKey 是 AgentChannel 名，也是 SendMessage 的地址：`agent:<role>/<instance>`。
	AgentKey string
	// ParentChannel / ParentChatID 是「谁 spawn 了它」。主会话是 web/cli/feishu；
	// 孙代理的父是 channel="agent"、chatID="<role>/<instance>" —— 递归恢复据此展开整棵树。
	ParentChannel string
	ParentChatID  string
	RoleName      string
	Instance      string
	GroupID       string
	Task          string
	Background    bool
	// Status ∈ {idle, running, interrupted, unloaded}。
	Status string
	// TurnID 是该子代理当前 turn 的编号；恢复后必须接续（否则撞 TURN_ID_INVARIANT）。
	TurnID uint64
	// ModelName / SubID 决定恢复后用的模型 —— 必须沿用，否则同一会话突然换模型，
	// 历史 token 计费与 KV cache 全部错位。
	ModelName string
	SubID     string
	// OriginUserID / WorkspaceRoot / ParentAgentID 是 buildSubAgentRunConfig 的输入快照。
	OriginUserID  string
	WorkspaceRoot string
	ParentAgentID string
	LastError     string
	LastReply     string
	CreatedAt     string
	UpdatedAt     string
}

// InteractiveAgentStatus* 是 status 列的取值（字符串直接落库，便于人工排查）。
const (
	InteractiveAgentIdle        = "idle"
	InteractiveAgentRunning     = "running"
	InteractiveAgentInterrupted = "interrupted"
	InteractiveAgentUnloaded    = "unloaded"
)

// ErrInteractiveAgentNotFound 表示注册表里没有这个 agent_key。
var ErrInteractiveAgentNotFound = errors.New("interactive agent not found")

// InteractiveAgentService 是 interactive_agents 表的唯一读写入口。
type InteractiveAgentService struct {
	db *sql.DB
}

func NewInteractiveAgentService(db *sql.DB) *InteractiveAgentService {
	return &InteractiveAgentService{db: db}
}

const interactiveAgentColumns = `agent_key, parent_channel, parent_chat_id, role_name, instance, group_id,
	task, background, status, turn_id, model_name, sub_id, origin_user_id,
	workspace_root, parent_agent_id, COALESCE(last_error,''), COALESCE(last_reply,''),
	created_at, updated_at`

// Upsert 写入或更新一条记录（保留 created_at）。
func (s *InteractiveAgentService) Upsert(rec InteractiveAgentRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if rec.CreatedAt == "" {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	bg := 0
	if rec.Background {
		bg = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO interactive_agents (
			agent_key, parent_channel, parent_chat_id, role_name, instance, group_id,
			task, background, status, turn_id, model_name, sub_id, origin_user_id,
			workspace_root, parent_agent_id, last_error, last_reply, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(agent_key) DO UPDATE SET
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
			updated_at=excluded.updated_at`,
		rec.AgentKey, rec.ParentChannel, rec.ParentChatID, rec.RoleName, rec.Instance, rec.GroupID,
		rec.Task, bg, rec.Status, rec.TurnID, rec.ModelName, rec.SubID,
		rec.OriginUserID, rec.WorkspaceRoot, rec.ParentAgentID, rec.LastError, rec.LastReply,
		rec.CreatedAt, rec.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert interactive agent %s: %w", rec.AgentKey, err)
	}
	return nil
}

// Get 按 agent_key 读一条。不存在时返回 ErrInteractiveAgentNotFound。
func (s *InteractiveAgentService) Get(agentKey string) (*InteractiveAgentRecord, error) {
	row := s.db.QueryRow(`SELECT `+interactiveAgentColumns+` FROM interactive_agents WHERE agent_key = ?`, agentKey)
	rec, err := scanInteractiveAgent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInteractiveAgentNotFound
		}
		return nil, err
	}
	return &rec, nil
}

// ListChildAgents 返回某个会话下属的全部子代理（含 unloaded，调用方决定是否跳过）。
// 按 created_at 升序，保证恢复顺序稳定（父先于孙）。
func (s *InteractiveAgentService) ListChildAgents(parentChannel, parentChatID string) ([]InteractiveAgentRecord, error) {
	rows, err := s.db.Query(
		`SELECT `+interactiveAgentColumns+` FROM interactive_agents
		  WHERE parent_channel = ? AND parent_chat_id = ?
		  ORDER BY created_at ASC`, parentChannel, parentChatID)
	if err != nil {
		return nil, fmt.Errorf("list child agents of %s:%s: %w", parentChannel, parentChatID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []InteractiveAgentRecord
	for rows.Next() {
		rec, err := scanInteractiveAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// MarkStatus 只更新状态与 turn_id（其他字段不动）—— 供 Run 开始/结束、unload、
// 重启后标记 interrupted 使用。
func (s *InteractiveAgentService) MarkStatus(agentKey, status string, turnID uint64) error {
	_, err := s.db.Exec(
		`UPDATE interactive_agents SET status = ?, turn_id = ?, updated_at = ? WHERE agent_key = ?`,
		status, turnID, time.Now().UTC().Format(time.RFC3339), agentKey)
	if err != nil {
		return fmt.Errorf("mark interactive agent %s %s: %w", agentKey, status, err)
	}
	return nil
}

// MarkAllRunningAsInterrupted 把所有 running 记录改成 interrupted，返回改动条数。
//
// 进程启动时调用：上一个进程里正在跑的子代理，其 goroutine 已经随进程消失，
// 不可能「恢复成正在计算」；标记为 interrupted 后，由递归恢复在第一次 tick
// 时用 InjectInboundResume 续上同一个 turn。
func (s *InteractiveAgentService) MarkAllRunningAsInterrupted() (int64, error) {
	res, err := s.db.Exec(
		`UPDATE interactive_agents SET status = ?, updated_at = ? WHERE status = ?`,
		InteractiveAgentInterrupted, time.Now().UTC().Format(time.RFC3339), InteractiveAgentRunning)
	if err != nil {
		return 0, fmt.Errorf("mark running agents interrupted: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkUnloadedCascade 把一个 agent 及其所有后代标记为 unloaded（级联卸载）。
func (s *InteractiveAgentService) MarkUnloadedCascade(agentKey string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	// 递归 CTE：从 agentKey 出发，沿 parent_channel="agent" + parent_chat_id 展开。
	_, err := s.db.Exec(`
		WITH RECURSIVE tree(key) AS (
			SELECT ?
			UNION ALL
			SELECT a.agent_key FROM interactive_agents a
			  JOIN tree t ON a.parent_channel = 'agent'
			 AND a.parent_chat_id = substr(t.key, length('agent:') + 1)
		)
		UPDATE interactive_agents SET status = ?, updated_at = ?
		 WHERE agent_key IN (SELECT key FROM tree)`,
		agentKey, InteractiveAgentUnloaded, now)
	if err != nil {
		return fmt.Errorf("cascade unload %s: %w", agentKey, err)
	}
	return nil
}

func scanInteractiveAgent(sc interface{ Scan(...any) error }) (InteractiveAgentRecord, error) {
	var rec InteractiveAgentRecord
	var bg int
	err := sc.Scan(
		&rec.AgentKey, &rec.ParentChannel, &rec.ParentChatID, &rec.RoleName, &rec.Instance, &rec.GroupID,
		&rec.Task, &bg, &rec.Status, &rec.TurnID, &rec.ModelName, &rec.SubID, &rec.OriginUserID,
		&rec.WorkspaceRoot, &rec.ParentAgentID, &rec.LastError, &rec.LastReply,
		&rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		return rec, err
	}
	rec.Background = bg != 0
	return rec, nil
}
