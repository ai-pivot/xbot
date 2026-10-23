package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	// LastReplyAt 是「最近一次记录到新回复文本」的时刻（RFC3339 UTC）；没有回复时为空。
	LastReplyAt string
	CreatedAt   string
	UpdatedAt   string
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
	COALESCE(last_reply_at,''), created_at, updated_at`

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
	// last_reply_at 只在「回复文本真的变了」时前进（v77）：注册表在状态变更时
	// upsert，若每次都刷新时间戳，last_reply_at 会退化成一个 updated_at 副本。
	lastReplyAt := ""
	if rec.LastReply != "" {
		lastReplyAt = rec.UpdatedAt
	}
	_, err := s.db.Exec(`
		INSERT INTO interactive_agents (
			agent_key, parent_channel, parent_chat_id, role_name, instance, group_id,
			task, background, status, turn_id, model_name, sub_id, origin_user_id,
			workspace_root, parent_agent_id, last_error, last_reply, last_reply_at, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
			last_reply_at=CASE
				WHEN excluded.last_reply <> '' AND excluded.last_reply <> interactive_agents.last_reply
				THEN excluded.updated_at
				ELSE interactive_agents.last_reply_at END,
			updated_at=excluded.updated_at`,
		rec.AgentKey, rec.ParentChannel, rec.ParentChatID, rec.RoleName, rec.Instance, rec.GroupID,
		rec.Task, bg, rec.Status, rec.TurnID, rec.ModelName, rec.SubID,
		rec.OriginUserID, rec.WorkspaceRoot, rec.ParentAgentID, rec.LastError, rec.LastReply, lastReplyAt,
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

// orphanIdleChunk 是 MarkOrphanedIdleUnloaded 单条 SQL 里放多少个 live key。
// SQLite 的绑定参数上限是 999（新版本 32766），分块后活会话再多也不会撞上限。
const orphanIdleChunk = 400

// MarkOrphanedIdleUnloaded 把「注册行还在、但会话已经不存在」的 idle 记录标记为
// unloaded，返回改动条数。僵尸行（组织树把已销毁的会话当活的列出来）的唯一清理口。
//
// 判定僵尸要**同时**满足两条：
//
//  1. agent_key 不在 liveKeys 里 —— 进程内 interactiveSubAgents 没有这个实例；
//  2. tenants 表里查不到它对应的会话 —— destroyInteractiveSession 已经把会话库连
//     同租户一起删掉，这一行只剩空壳。
//
// 第 2 条不能省。只按「运行时没有」判定会把「重启前 idle、会话还在、下一条消息就能
// 恢复」的正常子代理一起判死；而 restoreInteractiveTree 会跳过 unloaded ⇒ 重启恢复
// 功能被自己对账关掉。（2026-10-23 生产库实测：75 条 idle 里 64 条是真空壳、
// 11 条是可恢复的正常会话。）
//
// 会话 key 的算法必须与 spawn / restore 用的 interactiveKey 完全同构：
//
//	"<parent_channel>:<parent_chat_id>/<role_name>[:<instance>]"
//
// （restoreOneAgent 用 interactiveKey(...) 打开 channel="agent" 的租户会话；这里用
// SQL 拼同一个串。拼错就等于把活会话认成僵尸，所以两处改动必须一起改。）
//
// 幂等：只改 status='idle' 的行，第二次执行影响 0 行。
func (s *InteractiveAgentService) MarkOrphanedIdleUnloaded(liveKeys []string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// interactive_agents 与 tenants 都在主库（PG 后端没有 interactive_agents，
	// 见 serverapp/callbacks.go 的注释），所以这里可以直连 tenants 判定会话是否还在。
	const stmt = `UPDATE interactive_agents SET status = ?, updated_at = ?
	               WHERE status = ?
	                 AND NOT EXISTS (
	                       SELECT 1 FROM tenants t
	                        WHERE t.channel = 'agent'
	                          AND t.chat_id = interactive_agents.parent_channel || ':' || interactive_agents.parent_chat_id
	                                        || '/' || interactive_agents.role_name
	                                        || CASE WHEN interactive_agents.instance <> '' THEN ':' || interactive_agents.instance ELSE '' END)`

	if len(liveKeys) == 0 {
		res, err := s.db.Exec(stmt, InteractiveAgentUnloaded, now, InteractiveAgentIdle)
		if err != nil {
			return 0, fmt.Errorf("mark orphaned idle agents unloaded: %w", err)
		}
		n, _ := res.RowsAffected()
		return n, nil
	}

	var total int64
	for start := 0; start < len(liveKeys); start += orphanIdleChunk {
		end := start + orphanIdleChunk
		if end > len(liveKeys) {
			end = len(liveKeys)
		}
		chunk := liveKeys[start:end]

		var ph strings.Builder
		args := make([]any, 0, len(chunk)+3)
		args = append(args, InteractiveAgentUnloaded, now, InteractiveAgentIdle)
		for i, k := range chunk {
			if i > 0 {
				ph.WriteByte(',')
			}
			ph.WriteByte('?')
			args = append(args, k)
		}
		res, err := s.db.Exec(stmt+` AND agent_key NOT IN (`+ph.String()+`)`, args...)
		if err != nil {
			return total, fmt.Errorf("mark orphaned idle agents unloaded: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

func scanInteractiveAgent(sc interface{ Scan(...any) error }) (InteractiveAgentRecord, error) {
	var rec InteractiveAgentRecord
	var bg int
	err := sc.Scan(
		&rec.AgentKey, &rec.ParentChannel, &rec.ParentChatID, &rec.RoleName, &rec.Instance, &rec.GroupID,
		&rec.Task, &bg, &rec.Status, &rec.TurnID, &rec.ModelName, &rec.SubID, &rec.OriginUserID,
		&rec.WorkspaceRoot, &rec.ParentAgentID, &rec.LastError, &rec.LastReply, &rec.LastReplyAt,
		&rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		return rec, err
	}
	rec.Background = bg != 0
	return rec, nil
}

// AgentActivity 是「活跃子代理」视图的一行（/api/agents 的 items 元素）。
type AgentActivity struct {
	AgentKey    string `json:"agent_key"`
	RoleName    string `json:"role_name"`
	Instance    string `json:"instance"`
	Status      string `json:"status"`
	UpdatedAt   string `json:"updated_at"`
	LastReplyAt string `json:"last_reply_at"`
}

// ListActive 返回最近活跃的子代理，按 updated_at 倒序。
//
// 「活跃」= updated_at >= cutoff。**默认排除 unloaded**：卸载掉的子代理不该再
// 占用户的视线（这是本接口的主要过滤语义，调用方可用 includeUnloaded 显式要回）。
//
// 只打主库：不触碰任何会话分库，也不重建子代理（纯读表）。
//
// 时间比较口径：updated_at 由 Upsert 写成 UTC RFC3339（秒精度、固定宽度），
// 所以 cutoff 也按同一 layout 格式化后做**字符串比较** —— 宽度相同的 UTC 串，
// 字符串序 == 时间序（这里不用 activityTimeLayout：那列不是本文件写的）。
func (s *InteractiveAgentService) ListActive(cutoff time.Time, includeUnloaded bool) ([]AgentActivity, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	query := `SELECT agent_key, role_name, instance, status, updated_at, COALESCE(last_reply_at,'')
	            FROM interactive_agents
	           WHERE updated_at >= ?`
	if !includeUnloaded {
		query += ` AND status <> '` + InteractiveAgentUnloaded + `'`
	}
	query += ` ORDER BY updated_at DESC, agent_key ASC`

	rows, err := s.db.Query(query, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("list active agents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]AgentActivity, 0, 64)
	for rows.Next() {
		var a AgentActivity
		if err := rows.Scan(&a.AgentKey, &a.RoleName, &a.Instance, &a.Status, &a.UpdatedAt, &a.LastReplyAt); err != nil {
			return nil, fmt.Errorf("scan active agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
