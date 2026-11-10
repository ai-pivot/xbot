package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrGroupNotFound / ErrGroupExists / ErrNotGroupMember 是群组服务的领域错误，
// Web/Agent 层据此映射 HTTP 状态码或工具错误码。
// GroupService 是 groups/group_members 两张小表的唯一读写入口。
//
// 设计约束（docs/design/group-hardening.md §4）：
//   - 只有元数据，没有群级消息表；
//   - 群发消息仍然「遍历 alive 成员各写一份」，落在各成员会话库；
//   - 状态只有 alive/left（运行时的 busy 由事件推导，不落库）。
type GroupService struct {
	db *sql.DB
}

func NewGroupService(db *sql.DB) *GroupService {
	return &GroupService{db: db}
}

// CreateGroup 写入群组与初始成员（owner 自动成为 alive 成员），单事务。
func (s *GroupService) CreateGroup(ctx context.Context, id, name, owner string, members []string) (*GroupRow, error) {
	if id == "" || name == "" || owner == "" {
		return nil, fmt.Errorf("group id, name and owner are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO groups (id, name, owner, created_at) VALUES (?, ?, ?, ?)`,
		id, name, owner, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrGroupExists
		}
		return nil, err
	}
	// owner 先行，成员去重；owner 重复时保留一次。
	seen := map[string]bool{owner: true}
	if err := insertGroupMember(ctx, tx, id, owner, now); err != nil {
		return nil, err
	}
	for _, m := range members {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		if err := insertGroupMember(ctx, tx, id, m, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &GroupRow{ID: id, Name: name, Owner: owner, CreatedAt: now, MemberCount: len(seen)}, nil
}

func insertGroupMember(ctx context.Context, tx *sql.Tx, groupID, addr, now string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO group_members (group_id, member_addr, status, joined_at)
		 VALUES (?, ?, 'alive', ?)
		 ON CONFLICT(group_id, member_addr) DO UPDATE SET status='alive'`,
		groupID, addr, now)
	return err
}

// AddMember 幂等地把成员加回 alive（左成员重新加入时复用同一行）。
func (s *GroupService) AddMember(ctx context.Context, groupID, addr string) (*GroupMemberRow, error) {
	if groupID == "" || addr == "" {
		return nil, fmt.Errorf("group id and member address are required")
	}
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO group_members (group_id, member_addr, status, joined_at)
		 VALUES (?, ?, 'alive', ?)
		 ON CONFLICT(group_id, member_addr) DO UPDATE SET status='alive'`,
		groupID, addr, now)
	if err != nil {
		return nil, err
	}
	row := &GroupMemberRow{GroupID: groupID, MemberAddr: addr, Status: "alive", JoinedAt: now}
	enrichMember(row)
	return row, nil
}

// GetGroup 按 id 读群组（附成员数）。
func (s *GroupService) GetGroup(ctx context.Context, id string) (*GroupRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT g.id, g.name, g.owner, g.created_at,
		        (SELECT COUNT(*) FROM group_members m WHERE m.group_id = g.id AND m.status='alive')
		   FROM groups g WHERE g.id = ?`, id)
	var g GroupRow
	if err := row.Scan(&g.ID, &g.Name, &g.Owner, &g.CreatedAt, &g.MemberCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	return &g, nil
}

// ListGroupsFor 返回「我可见」的群：我是群主或是 alive 成员。
func (s *GroupService) ListGroupsFor(ctx context.Context, addr string) ([]GroupRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT g.id, g.name, g.owner, g.created_at,
		        (SELECT COUNT(*) FROM group_members m2 WHERE m2.group_id = g.id AND m2.status='alive')
		   FROM groups g
		  WHERE g.owner = ?
		     OR EXISTS (SELECT 1 FROM group_members m
		                 WHERE m.group_id = g.id AND m.member_addr = ? AND m.status='alive')
		  ORDER BY g.created_at DESC, g.id`, addr, addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]GroupRow, 0, 8)
	for rows.Next() {
		var g GroupRow
		if err := rows.Scan(&g.ID, &g.Name, &g.Owner, &g.CreatedAt, &g.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListMembers 返回群成员（含 left，供组织树展示历史成员）。
func (s *GroupService) ListMembers(ctx context.Context, groupID string) ([]GroupMemberRow, error) {
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT group_id, member_addr, status, joined_at
		   FROM group_members WHERE group_id = ?
		  ORDER BY CASE status WHEN 'alive' THEN 0 ELSE 1 END, joined_at, member_addr`, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]GroupMemberRow, 0, 8)
	for rows.Next() {
		var m GroupMemberRow
		if err := rows.Scan(&m.GroupID, &m.MemberAddr, &m.Status, &m.JoinedAt); err != nil {
			return nil, err
		}
		enrichMember(&m)
		out = append(out, m)
	}
	return out, rows.Err()
}

// IsAliveMember 校验地址是否为 alive 成员（owner 视为成员）。
func (s *GroupService) IsAliveMember(ctx context.Context, groupID, addr string) (bool, error) {
	g, err := s.GetGroup(ctx, groupID)
	if err != nil {
		return false, err
	}
	if g.Owner == addr {
		return true, nil
	}
	var status string
	err = s.db.QueryRowContext(ctx,
		`SELECT status FROM group_members WHERE group_id = ? AND member_addr = ?`,
		groupID, addr).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "alive", nil
}

// enrichMember 从 `agent:<role>/<instance>` 派生可读名称；非 agent 地址原样返回。
func enrichMember(m *GroupMemberRow) {
	addr := m.MemberAddr
	if strings.HasPrefix(addr, "agent:") {
		rest := strings.TrimPrefix(addr, "agent:")
		if i := strings.Index(rest, ":"); i >= 0 {
			rest = rest[:i]
		}
		role, instance := rest, ""
		if i := strings.Index(rest, "/"); i >= 0 {
			role, instance = rest[:i], rest[i+1:]
		}
		m.Role = role
		m.Instance = instance
		m.Name = role
		if instance != "" {
			m.Name = role + "/" + instance
		}
		return
	}
	m.Name = addr
}
