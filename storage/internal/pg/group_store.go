package pg

// group_store.go — PG port of storage/sqlite/group_store.go.
//
// Groups are metadata-only (no group message table); messages are written
// per-member into their own session stores. Status is only alive/left — runtime
// busy state is derived from events and never persisted.
//
// Type/error names resolve through storage/common so this file does not depend
// on aliases.go being extended.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	common "xbot/storage/common"
)

type groupStore struct {
	pool *pgxpool.Pool
}

func newGroupStore(pool *pgxpool.Pool) common.GroupStore {
	return &groupStore{pool: pool}
}

// CreateGroup writes the group and its initial members (the owner is
// automatically an alive member) in a single transaction.
func (s *groupStore) CreateGroup(ctx context.Context, id, name, owner string, members []string) (*common.GroupRow, error) {
	if id == "" || name == "" || owner == "" {
		return nil, fmt.Errorf("group id, name and owner are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.Exec(ctx,
		`INSERT INTO groups (id, name, owner, created_at) VALUES ($1, $2, $3, $4)`,
		id, name, owner, now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, common.ErrGroupExists
		}
		return nil, err
	}
	// Owner first, members deduplicated; a repeated owner is kept once.
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
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &common.GroupRow{ID: id, Name: name, Owner: owner, CreatedAt: now, MemberCount: len(seen)}, nil
}

func insertGroupMember(ctx context.Context, tx pgx.Tx, groupID, addr, now string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO group_members (group_id, member_addr, status, joined_at)
		 VALUES ($1, $2, 'alive', $3)
		 ON CONFLICT (group_id, member_addr) DO UPDATE SET status = 'alive'`,
		groupID, addr, now)
	return err
}

// AddMember idempotently brings a member back to alive (a rejoining member
// reuses the same row).
func (s *groupStore) AddMember(ctx context.Context, groupID, addr string) (*common.GroupMemberRow, error) {
	if groupID == "" || addr == "" {
		return nil, fmt.Errorf("group id and member address are required")
	}
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO group_members (group_id, member_addr, status, joined_at)
		 VALUES ($1, $2, 'alive', $3)
		 ON CONFLICT (group_id, member_addr) DO UPDATE SET status = 'alive'`,
		groupID, addr, now)
	if err != nil {
		return nil, err
	}
	row := &common.GroupMemberRow{GroupID: groupID, MemberAddr: addr, Status: "alive", JoinedAt: now}
	enrichMember(row)
	return row, nil
}

// GetGroup reads a group by id, including its alive member count.
func (s *groupStore) GetGroup(ctx context.Context, id string) (*common.GroupRow, error) {
	var g common.GroupRow
	err := s.pool.QueryRow(ctx,
		`SELECT g.id, g.name, g.owner, g.created_at,
		        (SELECT COUNT(*) FROM group_members m WHERE m.group_id = g.id AND m.status = 'alive')
		   FROM groups g WHERE g.id = $1`, id).
		Scan(&g.ID, &g.Name, &g.Owner, &g.CreatedAt, &g.MemberCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, common.ErrGroupNotFound
		}
		return nil, err
	}
	return &g, nil
}

// ListGroupsFor returns the groups visible to addr: owned by addr or where addr
// is an alive member.
func (s *groupStore) ListGroupsFor(ctx context.Context, addr string) ([]common.GroupRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT g.id, g.name, g.owner, g.created_at,
		        (SELECT COUNT(*) FROM group_members m2 WHERE m2.group_id = g.id AND m2.status = 'alive')
		   FROM groups g
		  WHERE g.owner = $1
		     OR EXISTS (SELECT 1 FROM group_members m
		                 WHERE m.group_id = g.id AND m.member_addr = $2 AND m.status = 'alive')
		  ORDER BY g.created_at DESC, g.id`, addr, addr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]common.GroupRow, 0, 8)
	for rows.Next() {
		var g common.GroupRow
		if err := rows.Scan(&g.ID, &g.Name, &g.Owner, &g.CreatedAt, &g.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListMembers returns all members (including left ones, for showing history in
// the org tree).
func (s *groupStore) ListMembers(ctx context.Context, groupID string) ([]common.GroupMemberRow, error) {
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT group_id, member_addr, status, joined_at
		   FROM group_members WHERE group_id = $1
		  ORDER BY CASE status WHEN 'alive' THEN 0 ELSE 1 END, joined_at, member_addr`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]common.GroupMemberRow, 0, 8)
	for rows.Next() {
		var m common.GroupMemberRow
		if err := rows.Scan(&m.GroupID, &m.MemberAddr, &m.Status, &m.JoinedAt); err != nil {
			return nil, err
		}
		enrichMember(&m)
		out = append(out, m)
	}
	return out, rows.Err()
}

// IsAliveMember reports whether addr is an alive member (the owner counts as a
// member).
func (s *groupStore) IsAliveMember(ctx context.Context, groupID, addr string) (bool, error) {
	g, err := s.GetGroup(ctx, groupID)
	if err != nil {
		return false, err
	}
	if g.Owner == addr {
		return true, nil
	}
	var status string
	err = s.pool.QueryRow(ctx,
		`SELECT status FROM group_members WHERE group_id = $1 AND member_addr = $2`,
		groupID, addr).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "alive", nil
}

// enrichMember derives a readable name from `agent:<role>/<instance>`; non-agent
// addresses are returned verbatim.
func enrichMember(m *common.GroupMemberRow) {
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
