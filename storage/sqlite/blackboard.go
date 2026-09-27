package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// blackboardSchema is the shared blackboard DDL. It is executed both by the
// fresh-database path (schema.go) and by migrateV70ToV71 — one definition, no
// drift between "new installs" and "upgraded installs". Idempotent by
// construction (IF NOT EXISTS).
const blackboardSchema = `
-- v71: shared blackboard — the cross-agent workspace. The host owns the
-- coordination mechanics (revision CAS, claim lease, dependency edges) and
-- knows nothing about content: kind is named by the producing agent and body
-- is opaque. board is either a session-derived key (main agent + its
-- SubAgents share one board automatically) or a named board for independent
-- sessions; the ':' in a session key is why named boards must not contain it.
CREATE TABLE IF NOT EXISTS blackboard_entries (
    board TEXT NOT NULL,
    key TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    closed INTEGER NOT NULL DEFAULT 0,
    blocked_by TEXT NOT NULL DEFAULT '[]',
    revision INTEGER NOT NULL DEFAULT 1,
    claimed_by TEXT NOT NULL DEFAULT '',
    claim_token TEXT NOT NULL DEFAULT '',
    claim_expires_at INTEGER NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (board, key)
);
CREATE INDEX IF NOT EXISTS idx_blackboard_board_updated ON blackboard_entries(board, updated_at DESC);
`

// Blackboard is the generic, cross-agent shared workspace: a set of named
// entries scoped to a board, with three first-class coordination mechanics —
//
//   - revision  : a monotonic per-entry counter used as a compare-and-swap
//     token, so concurrent writers can never silently lose an update.
//   - claim     : a lease (holder + expiry) that makes "who is working on this"
//     atomic to acquire and self-healing on crash (no sweeper: expiry is
//     evaluated at read time).
//   - blocked_by: structural dependency edges; an entry is ready only when
//     every dependency is closed.
//
// The host is deliberately content-agnostic, exactly like shared_artifacts:
// Kind is named by the producing agent (task / finding / decision / …) and
// Body is opaque — the host never parses it, and nothing in this package
// knows what a "task" is.
const (
	// BlackboardMaxEntriesPerBoard bounds one board. Enforced explicitly (an
	// over-quota post fails loudly) rather than silently evicting entries —
	// dropping a teammate's work would be worse than an error.
	BlackboardMaxEntriesPerBoard = 500
	// BlackboardMaxBodyBytes bounds one entry body.
	BlackboardMaxBodyBytes = 64 * 1024
	// BlackboardMaxTitleRunes bounds the one-line summary.
	BlackboardMaxTitleRunes  = 200
	BlackboardMaxKindRunes   = 32
	BlackboardMaxStatusRunes = 32
	// BlackboardMaxBlockedBy bounds dependency fan-in per entry.
	BlackboardMaxBlockedBy = 32

	// BlackboardDefaultListLimit / BlackboardMaxListLimit bound list output.
	// The default is deliberately small: a list is a digest for an LLM's
	// context, not a dump.
	BlackboardDefaultListLimit = 50
	BlackboardMaxListLimit     = 200

	// BlackboardMinClaimTTL / BlackboardMaxClaimTTL / BlackboardClaimTTL are
	// lease bounds. A TTL outside the range is a loud error (never clamped):
	// a lease shorter than the minimum defeats the purpose, and one longer
	// than the maximum survives a teammate's failure too long.
	BlackboardMinClaimTTL = 30 * time.Second
	BlackboardMaxClaimTTL = 24 * time.Hour
	BlackboardClaimTTL    = 10 * time.Minute
)

// Blackboard board and key syntax. A board that contains ':' is rejected:
// ':' is reserved for session-derived boards (BoardForSession returns a
// session key like "web:chat_abc"), and letting a named board collide with
// that space would silently merge two unrelated scopes.
const (
	blackboardBoardPattern = `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`
	blackboardKeyPattern   = `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`
)

var (
	blackboardBoardRE = regexp.MustCompile(blackboardBoardPattern)
	blackboardKeyRE   = regexp.MustCompile(blackboardKeyPattern)
)

// blackboardClaimableSQL is the claim precondition, expressed entirely in SQL so
// that "can I take this?" is decided atomically with the write itself: the entry
// must be open AND every dependency must exist and be closed. Claiming work whose
// inputs are not done yet is how parallel agents start on sand then redo it —
// `ready` is not advice, it is the gate.
const blackboardClaimableSQL = `
		   AND closed = 0
		   AND NOT EXISTS (
		       SELECT 1 FROM json_each(blackboard_entries.blocked_by) AS dep
		        WHERE NOT EXISTS (
		            SELECT 1 FROM blackboard_entries AS d
		             WHERE d.board = blackboard_entries.board AND d.key = dep.value AND d.closed = 1
		        )
		   )`

// Blackboard errors. Every failure names its cause; callers map them to tool
// output / RPC errors without string matching.
var (
	ErrBlackboardNotFound = errors.New("blackboard entry not found")
	ErrBlackboardInvalid  = errors.New("blackboard entry invalid")
)

// BlackboardConflictError reports that a write lost against the authoritative
// state, carrying that state so the caller can act on it immediately (retry
// with the fresh revision, wait for a lease to expire, pick another key, …).
// Carrying the state is the whole point: a conflict without the current value
// forces a second round-trip.
type BlackboardConflictError struct {
	Op     string           // post | update | close | reopen | claim | delete
	Reason string           // human-readable, e.g. "revision mismatch: expected 3, current 5"
	Entry  *BlackboardEntry // authoritative current state
}

func (e *BlackboardConflictError) Error() string {
	if e.Entry == nil {
		return fmt.Sprintf("blackboard %s conflict: %s", e.Op, e.Reason)
	}
	return fmt.Sprintf("blackboard %s conflict on %s/%s: %s (current revision %d)",
		e.Op, e.Entry.Board, e.Entry.Key, e.Reason, e.Entry.Revision)
}

func (e *BlackboardConflictError) Unwrap() error { return ErrBlackboardConflict }

// ErrBlackboardConflict is the sentinel every BlackboardConflictError wraps.
var ErrBlackboardConflict = errors.New("blackboard conflict")

// BlackboardEntry is one row of a board. Revision/Claim/BlockedBy are the
// coordination mechanics; Kind/Body/Status are caller-owned content.
type BlackboardEntry struct {
	Board string `json:"board"`
	Key   string `json:"key"`
	// Kind is the producer-declared content type (host-opaque).
	Kind string `json:"kind"`
	// Title is the one-line summary shown in lists and the UI.
	Title string `json:"title"`
	// Body is the opaque payload (may be markdown or JSON). It is omitted from
	// List output unless explicitly requested — lists are digests.
	Body string `json:"body,omitempty"`
	// Status is free-form and host-opaque (convention: open / working / done).
	Status string `json:"status"`
	// Closed is the structural "no longer pending" bit. Dependencies gate on it.
	Closed bool `json:"closed"`
	// BlockedBy lists dependency keys. A dependency is satisfied when it
	// exists and is closed; a missing dependency is NOT satisfied.
	BlockedBy []string `json:"blocked_by,omitempty"`
	// Revision is the compare-and-swap token; every write increments it.
	Revision int64 `json:"revision"`
	// ClaimedBy is the EFFECTIVE holder: empty when the lease has expired.
	ClaimedBy string `json:"claimed_by,omitempty"`
	// ClaimToken is the lease handle returned by a successful Claim. It is the
	// credential for extending or releasing that lease. Never serialized (it is
	// set only on the Claim result and never read back into a scanned struct):
	// a reader that did not acquire the lease has no business holding its handle.
	ClaimToken string `json:"-"`
	// ClaimExpiresAt is the raw lease expiry (unix ms, 0 = no lease).
	ClaimExpiresAt int64  `json:"claim_expires_at,omitempty"`
	CreatedBy      string `json:"created_by,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`

	// Derived (never stored): Blocked is true while any dependency is
	// unsatisfied; Ready is the actionable predicate — open, unblocked and
	// unclaimed (or lease-expired).
	Blocked bool `json:"blocked"`
	Ready   bool `json:"ready"`
}

// BlackboardBoard is a board summary for pickers and dashboards.
type BlackboardBoard struct {
	Board     string `json:"board"`
	Total     int    `json:"total"`
	Open      int    `json:"open"`
	Claimed   int    `json:"claimed"`
	Blocked   int    `json:"blocked"`
	Closed    int    `json:"closed"`
	UpdatedAt int64  `json:"updated_at"`
}

// BlackboardListOptions selects entries. IncludeBody is explicit because a
// list is a digest by default (an agent listing a 500-entry board must not
// pull 30 MB into its context).
type BlackboardListOptions struct {
	Board         string
	Prefix        string
	IncludeClosed bool
	Limit         int
	IncludeBody   bool
}

// BlackboardService is the storage layer of the shared blackboard. It owns
// atomicity (single-statement CAS under the process-wide write gate) and
// validation; it owns no policy about what entries mean.
type BlackboardService struct{ db *DB }

// NewBlackboardService creates a BlackboardService.
func NewBlackboardService(db *DB) *BlackboardService { return &BlackboardService{db: db} }

// ValidateBlackboardID checks a board id. Two namespaces share the table and
// cannot collide:
//
//   - session-derived boards are session keys ("web:chat_abc", "cli:/repo") —
//     created automatically from the root session, so a main agent and all its
//     SubAgents land on the same board with no ceremony;
//   - named boards carry the '@' sigil ("@dev-team") — because no session key
//     contains '@', a named board can never be confused with a session's board.
func ValidateBlackboardID(board string) error {
	if board == "" {
		return fmt.Errorf("%w: board is required", ErrBlackboardInvalid)
	}
	if name, ok := strings.CutPrefix(board, "@"); ok {
		if !blackboardBoardRE.MatchString(name) {
			return fmt.Errorf("%w: board name %q must match %s", ErrBlackboardInvalid, name, blackboardBoardPattern)
		}
		return nil
	}
	if len(board) > 200 || strings.ContainsAny(board, " \t\r\n") {
		return fmt.Errorf("%w: board %q is neither a @name nor a usable session key", ErrBlackboardInvalid, board)
	}
	if !strings.Contains(board, ":") {
		return fmt.Errorf("%w: board %q must be a @name (shared board) or a session key (channel:chatID)", ErrBlackboardInvalid, board)
	}
	return nil
}

// ValidateBlackboardKey checks an entry key.
func ValidateBlackboardKey(key string) error {
	if !blackboardKeyRE.MatchString(key) {
		return fmt.Errorf("%w: key %q must match %s", ErrBlackboardInvalid, key, blackboardKeyPattern)
	}
	return nil
}

// validateEntryContent checks the caller-owned content fields.
func validateEntryContent(kind, title, body, status string, blockedBy []string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("%w: title is required (a one-line summary every reader can scan)", ErrBlackboardInvalid)
	}
	if utf8.RuneCountInString(title) > BlackboardMaxTitleRunes {
		return fmt.Errorf("%w: title exceeds %d characters", ErrBlackboardInvalid, BlackboardMaxTitleRunes)
	}
	if utf8.RuneCountInString(kind) > BlackboardMaxKindRunes {
		return fmt.Errorf("%w: kind exceeds %d characters", ErrBlackboardInvalid, BlackboardMaxKindRunes)
	}
	if utf8.RuneCountInString(status) > BlackboardMaxStatusRunes {
		return fmt.Errorf("%w: status exceeds %d characters", ErrBlackboardInvalid, BlackboardMaxStatusRunes)
	}
	if len(body) > BlackboardMaxBodyBytes {
		return fmt.Errorf("%w: body exceeds %d bytes", ErrBlackboardInvalid, BlackboardMaxBodyBytes)
	}
	if len(blockedBy) > BlackboardMaxBlockedBy {
		return fmt.Errorf("%w: blocked_by accepts at most %d keys", ErrBlackboardInvalid, BlackboardMaxBlockedBy)
	}
	for _, dep := range blockedBy {
		if err := ValidateBlackboardKey(dep); err != nil {
			return fmt.Errorf("%w (in blocked_by)", err)
		}
	}
	return nil
}

// Post creates an entry. A duplicate key is a conflict that carries the
// existing entry (the caller should read it and use Update with its revision
// rather than blindly overwriting a teammate's work).
func (s *BlackboardService) Post(e *BlackboardEntry) (*BlackboardEntry, error) {
	if err := ValidateBlackboardID(e.Board); err != nil {
		return nil, err
	}
	if err := ValidateBlackboardKey(e.Key); err != nil {
		return nil, err
	}
	if err := validateEntryContent(e.Kind, e.Title, e.Body, e.Status, e.BlockedBy); err != nil {
		return nil, err
	}
	blocked, err := json.Marshal(nonNilStrings(e.BlockedBy))
	if err != nil {
		return nil, fmt.Errorf("encode blocked_by: %w", err)
	}
	now := time.Now().UnixMilli()

	// Process-wide write gate — see db.writeMu.
	s.db.writeMu.Lock()
	defer s.db.writeMu.Unlock()
	conn := s.db.Conn()

	var count int
	if err := conn.QueryRow(
		"SELECT COUNT(*) FROM blackboard_entries WHERE board = ?", e.Board,
	).Scan(&count); err != nil {
		return nil, fmt.Errorf("count blackboard entries: %w", err)
	}
	if count >= BlackboardMaxEntriesPerBoard {
		return nil, fmt.Errorf("%w: board %q already holds %d entries (max %d) — close and delete finished ones",
			ErrBlackboardInvalid, e.Board, count, BlackboardMaxEntriesPerBoard)
	}

	if _, err := conn.Exec(`
		INSERT INTO blackboard_entries
			(board, key, kind, title, body, status, closed, blocked_by, revision, claimed_by, claim_expires_at, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, 1, '', 0, ?, ?, ?)
	`, e.Board, e.Key, e.Kind, e.Title, e.Body, e.Status, string(blocked), e.CreatedBy, now, now); err != nil {
		// The INSERT is the authority on uniqueness. Re-read to report the live
		// entry instead of leaking a driver-level constraint message.
		if cur, gerr := s.getLocked(e.Board, e.Key); gerr == nil {
			return nil, &BlackboardConflictError{Op: "post", Reason: "key already exists — read it and use update with its revision", Entry: cur}
		}
		return nil, fmt.Errorf("insert blackboard entry: %w", err)
	}
	return s.getLocked(e.Board, e.Key)
}

// Get returns one entry (including body).
func (s *BlackboardService) Get(board, key string) (*BlackboardEntry, error) {
	if err := ValidateBlackboardID(board); err != nil {
		return nil, err
	}
	if err := ValidateBlackboardKey(key); err != nil {
		return nil, err
	}
	return s.getLocked(board, key)
}

// getLocked reads one entry and applies the derived flags. No lock is needed
// for reads (SQLite reads are concurrent with the in-process write gate).
func (s *BlackboardService) getLocked(board, key string) (*BlackboardEntry, error) {
	e, err := scanBlackboardEntry(s.db.Conn().QueryRow(blackboardSelect+`
		WHERE board = ? AND key = ?`, board, key))
	if err != nil {
		return nil, err
	}
	if err := s.applyDerived([]*BlackboardEntry{e}); err != nil {
		return nil, err
	}
	return e, nil
}

// List returns the board's entries, newest update first. Closed entries are
// excluded unless requested; bodies are excluded unless requested.
func (s *BlackboardService) List(opts BlackboardListOptions) ([]BlackboardEntry, error) {
	if err := ValidateBlackboardID(opts.Board); err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = BlackboardDefaultListLimit
	}
	if limit > BlackboardMaxListLimit {
		return nil, fmt.Errorf("%w: limit %d exceeds %d", ErrBlackboardInvalid, limit, BlackboardMaxListLimit)
	}

	cols := blackboardSelect
	if !opts.IncludeBody {
		cols = blackboardSelectNoBody
	}
	q := cols + " WHERE board = ?"
	args := []any{opts.Board}
	if opts.Prefix != "" {
		// Prefix match on the key; LIKE is fine because the key charset has no
		// LIKE metacharacters (see blackboardKeyPattern).
		q += " AND key LIKE ?"
		args = append(args, opts.Prefix+"%")
	}
	if !opts.IncludeClosed {
		q += " AND closed = 0"
	}
	q += " ORDER BY updated_at DESC, key ASC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Conn().Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list blackboard entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Collect by POINTER first and dereference LAST: taking the address of a
	// slice element while the slice keeps growing is a trap — append reallocates
	// and the derived flags end up written into an array nobody reads (this
	// exact shape silently returned blocked=false for every listed entry).
	ptrs := []*BlackboardEntry{}
	for rows.Next() {
		e, err := scanBlackboardEntryRows(rows, opts.IncludeBody)
		if err != nil {
			return nil, err
		}
		ptrs = append(ptrs, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blackboard entries: %w", err)
	}
	if err := s.applyDerived(ptrs); err != nil {
		return nil, err
	}
	out := make([]BlackboardEntry, 0, len(ptrs))
	for _, e := range ptrs {
		out = append(out, *e)
	}
	return out, nil
}

// applyDerived fills Blocked/Ready/effective claim for a batch of entries with
// ONE extra query (the board's key→closed map), instead of per-entry lookups.
func (s *BlackboardService) applyDerived(entries []*BlackboardEntry) error {
	boards := map[string]bool{}
	for _, e := range entries {
		boards[e.Board] = true
	}
	closed := map[string]map[string]bool{}
	for board := range boards {
		m, err := s.closedByKey(board)
		if err != nil {
			return err
		}
		closed[board] = m
	}
	for _, e := range entries {
		e.Blocked = blackboardBlocked(e.BlockedBy, closed[e.Board])
		e.ClaimedBy, e.Ready = effectiveClaim(e.ClaimedBy, e.ClaimExpiresAt), false
		e.Ready = !e.Closed && !e.Blocked && e.ClaimedBy == ""
	}
	return nil
}

// closedByKey returns the board's key → closed map.
func (s *BlackboardService) closedByKey(board string) (map[string]bool, error) {
	rows, err := s.db.Conn().Query("SELECT key, closed FROM blackboard_entries WHERE board = ?", board)
	if err != nil {
		return nil, fmt.Errorf("read blackboard closed set: %w", err)
	}
	defer func() { _ = rows.Close() }()
	m := map[string]bool{}
	for rows.Next() {
		var k string
		var c bool
		if err := rows.Scan(&k, &c); err != nil {
			return nil, fmt.Errorf("scan blackboard closed set: %w", err)
		}
		m[k] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blackboard closed set: %w", err)
	}
	return m, nil
}

// blackboardBlocked reports whether any dependency is unsatisfied. A missing
// dependency is unsatisfied: forward references are allowed (a plan can be
// posted in one shot) and simply block until the referenced entry is closed.
func blackboardBlocked(deps []string, closed map[string]bool) bool {
	for _, dep := range deps {
		if !closed[dep] {
			return true
		}
	}
	return false
}

// effectiveClaim hides an expired lease: the entry is free again the moment
// the lease lapses, with no background sweeper and no clock ordering issues
// beyond comparing against "now" at read time.
func effectiveClaim(holder string, expiresAt int64) string {
	if holder == "" || expiresAt <= time.Now().UnixMilli() {
		return ""
	}
	return holder
}

// Update rewrites an entry's content under compare-and-swap. expectedRevision
// must match the live revision — that is what makes concurrent edits safe:
// the loser gets a conflict carrying the winner's value instead of silently
// overwriting it.
func (s *BlackboardService) Update(board, key, kind, title, body, status string, blockedBy []string, expectedRevision int64) (*BlackboardEntry, error) {
	if err := ValidateBlackboardID(board); err != nil {
		return nil, err
	}
	if err := ValidateBlackboardKey(key); err != nil {
		return nil, err
	}
	if err := validateEntryContent(kind, title, body, status, blockedBy); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(nonNilStrings(blockedBy))
	if err != nil {
		return nil, fmt.Errorf("encode blocked_by: %w", err)
	}
	now := time.Now().UnixMilli()

	// Process-wide write gate — see db.writeMu.
	s.db.writeMu.Lock()
	res, err := s.db.Conn().Exec(`
		UPDATE blackboard_entries
		   SET kind = ?, title = ?, body = ?, status = ?, blocked_by = ?,
		       revision = revision + 1, updated_at = ?
		 WHERE board = ? AND key = ? AND revision = ?
	`, kind, title, body, status, string(encoded), now, board, key, expectedRevision)
	s.db.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("update blackboard entry: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("update blackboard entry: %w", err)
	} else if n == 0 {
		return nil, s.conflict("update", board, key, expectedRevision, "revision mismatch or entry missing")
	}
	return s.getLocked(board, key)
}

// SetClosed closes or reopens an entry under compare-and-swap. Closed is the
// structural bit dependencies gate on — it is deliberately separate from the
// host-opaque Status label.
func (s *BlackboardService) SetClosed(board, key string, closed bool, expectedRevision int64) (*BlackboardEntry, error) {
	if err := ValidateBlackboardID(board); err != nil {
		return nil, err
	}
	if err := ValidateBlackboardKey(key); err != nil {
		return nil, err
	}
	op := "close"
	if !closed {
		op = "reopen"
	}
	now := time.Now().UnixMilli()

	// Process-wide write gate — see db.writeMu.
	s.db.writeMu.Lock()
	var res interface{ RowsAffected() (int64, error) }
	var err error
	if closed {
		// Closing releases the lease: finished work must never keep a lock.
		res, err = s.db.Conn().Exec(`
			UPDATE blackboard_entries
			   SET closed = 1, claimed_by = '', claim_expires_at = 0, revision = revision + 1, updated_at = ?
			 WHERE board = ? AND key = ? AND revision = ? AND closed = 0
		`, now, board, key, expectedRevision)
	} else {
		res, err = s.db.Conn().Exec(`
			UPDATE blackboard_entries
			   SET closed = 0, revision = revision + 1, updated_at = ?
			 WHERE board = ? AND key = ? AND revision = ? AND closed = 1
		`, now, board, key, expectedRevision)
	}
	s.db.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("%s blackboard entry: %w", op, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("%s blackboard entry: %w", op, err)
	} else if n == 0 {
		return nil, s.conflict(op, board, key, expectedRevision, "revision mismatch, entry missing, or already in that state")
	}
	return s.getLocked(board, key)
}

// Claim acquires or extends the lease atomically.
//
// Without a token it ACQUIRES: it succeeds when the entry is free or its lease
// has expired, and mints a fresh claim token (the lease handle) that the caller
// passes back to extend or release. With a token it EXTENDS, succeeding only for
// the holder of that exact token.
//
// Exclusivity is carried by the token, not by the caller's identity: identity
// strings are not unique (two SubAgent instances of the same role share one
// session key — the instance name never reaches the tool context), so an
// identity-based lease could be extended by a different worker holding the same
// role label. A token cannot.
func (s *BlackboardService) Claim(board, key, holder, token string, ttl time.Duration) (*BlackboardEntry, error) {
	if err := ValidateBlackboardID(board); err != nil {
		return nil, err
	}
	if err := ValidateBlackboardKey(key); err != nil {
		return nil, err
	}
	if strings.TrimSpace(holder) == "" {
		return nil, fmt.Errorf("%w: claim requires a holder identity", ErrBlackboardInvalid)
	}
	if ttl < BlackboardMinClaimTTL || ttl > BlackboardMaxClaimTTL {
		return nil, fmt.Errorf("%w: ttl must be between %s and %s (got %s)",
			ErrBlackboardInvalid, BlackboardMinClaimTTL, BlackboardMaxClaimTTL, ttl)
	}
	now := time.Now().UnixMilli()
	expires := now + ttl.Milliseconds()

	// Process-wide write gate — see db.writeMu.
	s.db.writeMu.Lock()
	if token == "" {
		minted, err := newBlackboardClaimToken()
		if err != nil {
			s.db.writeMu.Unlock()
			return nil, err
		}
		res, err := s.db.Conn().Exec(`
			UPDATE blackboard_entries
			   SET claimed_by = ?, claim_token = ?, claim_expires_at = ?, revision = revision + 1, updated_at = ?
			 WHERE board = ? AND key = ?`+blackboardClaimableSQL+`
			   AND (claimed_by = '' OR claim_expires_at <= ?)
		`, holder, minted, expires, now, board, key, now)
		s.db.writeMu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("claim blackboard entry: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, fmt.Errorf("claim blackboard entry: %w", err)
		} else if n == 0 {
			return nil, s.conflict("claim", board, key, 0, "entry is closed, missing, or held by a live lease")
		}
		e, err := s.getLocked(board, key)
		if err != nil {
			return nil, err
		}
		e.ClaimToken = minted
		return e, nil
	}

	res, err := s.db.Conn().Exec(`
		UPDATE blackboard_entries
		   SET claimed_by = ?, claim_expires_at = ?, revision = revision + 1, updated_at = ?
		 WHERE board = ? AND key = ?`+blackboardClaimableSQL+`
		   AND claim_token = ?
	`, holder, expires, now, board, key, token)
	s.db.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("extend blackboard claim: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("extend blackboard claim: %w", err)
	} else if n == 0 {
		return nil, s.conflict("claim", board, key, 0, "claim_token no longer holds this lease — re-claim without a token to acquire it")
	}
	e, err := s.getLocked(board, key)
	if err != nil {
		return nil, err
	}
	e.ClaimToken = token
	return e, nil
}

// Release clears the lease. With a token it releases only that lease (a
// mismatch is a conflict, so a stale worker cannot free the entry another
// worker just took). Without a token it force-releases whatever lease is
// present — the cooperative escape hatch for a wedged holder. Freeing an
// already-free entry is a no-op: no revision bump, so it cannot wake watchers
// (or start turns) for nothing.
func (s *BlackboardService) Release(board, key, token string) (*BlackboardEntry, bool, error) {
	if err := ValidateBlackboardID(board); err != nil {
		return nil, false, err
	}
	if err := ValidateBlackboardKey(key); err != nil {
		return nil, false, err
	}
	now := time.Now().UnixMilli()

	// Process-wide write gate — see db.writeMu.
	s.db.writeMu.Lock()
	var res interface{ RowsAffected() (int64, error) }
	var err error
	if token != "" {
		res, err = s.db.Conn().Exec(`
			UPDATE blackboard_entries
			   SET claimed_by = '', claim_token = '', claim_expires_at = 0, revision = revision + 1, updated_at = ?
			 WHERE board = ? AND key = ? AND claim_token = ?
		`, now, board, key, token)
	} else {
		res, err = s.db.Conn().Exec(`
			UPDATE blackboard_entries
			   SET claimed_by = '', claim_token = '', claim_expires_at = 0, revision = revision + 1, updated_at = ?
			 WHERE board = ? AND key = ? AND claimed_by != ''
		`, now, board, key)
	}
	s.db.writeMu.Unlock()
	if err != nil {
		return nil, false, fmt.Errorf("release blackboard entry: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("release blackboard entry: %w", err)
	}
	if token != "" && changed == 0 {
		// Nothing matched. Report the truth instead of assuming success: either
		// another lease took over (stale token ⇒ conflict) or the entry was
		// already free (idempotent no-op).
		cur, gerr := s.getLocked(board, key)
		if gerr != nil {
			return nil, false, gerr
		}
		if cur.ClaimedBy != "" {
			return nil, false, &BlackboardConflictError{
				Op: "release", Reason: "the lease is held by another worker — your claim token is stale", Entry: cur}
		}
		return cur, false, nil
	}
	e, err := s.getLocked(board, key)
	if err != nil {
		return nil, false, err
	}
	return e, changed > 0, nil
}

// newBlackboardClaimToken mints the lease handle: 128 bits of crypto/rand,
// never derived from the key or identity. Like the share token, it is the
// credential — callers cannot supply their own.
func newBlackboardClaimToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint claim token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Delete removes an entry under compare-and-swap, returning the deleted entry
// so callers can report (and broadcast) what disappeared.
func (s *BlackboardService) Delete(board, key string, expectedRevision int64) (*BlackboardEntry, error) {
	if err := ValidateBlackboardID(board); err != nil {
		return nil, err
	}
	if err := ValidateBlackboardKey(key); err != nil {
		return nil, err
	}
	// Process-wide write gate — see db.writeMu.
	s.db.writeMu.Lock()
	defer s.db.writeMu.Unlock()
	cur, err := s.getLocked(board, key)
	if err != nil {
		return nil, err
	}
	if expectedRevision > 0 && cur.Revision != expectedRevision {
		return nil, &BlackboardConflictError{
			Op: "delete", Reason: fmt.Sprintf("revision mismatch: expected %d, current %d", expectedRevision, cur.Revision), Entry: cur,
		}
	}
	if _, err := s.db.Conn().Exec("DELETE FROM blackboard_entries WHERE board = ? AND key = ?", board, key); err != nil {
		return nil, fmt.Errorf("delete blackboard entry: %w", err)
	}
	return cur, nil
}

// Boards summarises every board (counts + activity), showing where work is
// happening without pulling bodies.
func (s *BlackboardService) Boards() ([]BlackboardBoard, error) {
	rows, err := s.db.Conn().Query(`
		SELECT board, key, closed, blocked_by, claimed_by, claim_expires_at, updated_at
		FROM blackboard_entries`)
	if err != nil {
		return nil, fmt.Errorf("list blackboards: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type raw struct {
		key       string
		closed    bool
		blockedBy []string
		holder    string
		expiresAt int64
		updatedAt int64
	}
	byBoard := map[string][]raw{}
	order := []string{}
	for rows.Next() {
		var board string
		var r raw
		var blockedJSON string
		if err := rows.Scan(&board, &r.key, &r.closed, &blockedJSON, &r.holder, &r.expiresAt, &r.updatedAt); err != nil {
			return nil, fmt.Errorf("scan blackboard board row: %w", err)
		}
		r.blockedBy = decodeBlockedBy(blockedJSON)
		if _, ok := byBoard[board]; !ok {
			order = append(order, board)
		}
		byBoard[board] = append(byBoard[board], r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blackboard board rows: %w", err)
	}

	out := make([]BlackboardBoard, 0, len(order))
	for _, board := range order {
		entries := byBoard[board]
		closedSet := map[string]bool{}
		for _, r := range entries {
			closedSet[r.key] = r.closed
		}
		sum := BlackboardBoard{Board: board, Total: len(entries)}
		for _, r := range entries {
			if r.updatedAt > sum.UpdatedAt {
				sum.UpdatedAt = r.updatedAt
			}
			if r.closed {
				sum.Closed++
				continue
			}
			sum.Open++
			if effectiveClaim(r.holder, r.expiresAt) != "" {
				sum.Claimed++
			}
			if blackboardBlocked(r.blockedBy, closedSet) {
				sum.Blocked++
			}
		}
		out = append(out, sum)
	}
	return out, nil
}

// conflict reloads the authoritative entry and wraps it in a conflict error.
func (s *BlackboardService) conflict(op, board, key string, expected int64, fallback string) error {
	cur, err := s.getLocked(board, key)
	if err != nil {
		if errors.Is(err, ErrBlackboardNotFound) {
			return fmt.Errorf("%w: %s/%s does not exist", ErrBlackboardNotFound, board, key)
		}
		return err
	}
	reason := fallback
	switch {
	case expected > 0 && cur.Revision != expected:
		reason = fmt.Sprintf("revision mismatch: expected %d, current %d", expected, cur.Revision)
	case op == "claim" && cur.Closed:
		reason = "entry is closed — reopen it before claiming"
	case op == "claim" && cur.Blocked:
		reason = fmt.Sprintf("entry is blocked by %s — close those first", strings.Join(cur.BlockedBy, ", "))
	case op == "claim":
		reason = fmt.Sprintf("held by %s for another %s", cur.ClaimedBy, time.Until(time.UnixMilli(cur.ClaimExpiresAt)).Round(time.Second))
	}
	return &BlackboardConflictError{Op: op, Reason: reason, Entry: cur}
}

const blackboardSelect = `SELECT board, key, kind, title, body, status, closed, blocked_by,
	revision, claimed_by, claim_expires_at, created_by, created_at, updated_at FROM blackboard_entries`

const blackboardSelectNoBody = `SELECT board, key, kind, title, '' AS body, status, closed, blocked_by,
	revision, claimed_by, claim_expires_at, created_by, created_at, updated_at FROM blackboard_entries`

// blackboardScanner is satisfied by both *sql.Row and *sql.Rows.
type blackboardScanner interface {
	Scan(dest ...any) error
}

func scanBlackboardEntry(row blackboardScanner) (*BlackboardEntry, error) {
	return scanBlackboardEntryRows(row, true)
}

func scanBlackboardEntryRows(row blackboardScanner, _ bool) (*BlackboardEntry, error) {
	var e BlackboardEntry
	var blockedJSON string
	if err := row.Scan(&e.Board, &e.Key, &e.Kind, &e.Title, &e.Body, &e.Status, &e.Closed, &blockedJSON,
		&e.Revision, &e.ClaimedBy, &e.ClaimExpiresAt, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s/%s", ErrBlackboardNotFound, "", "")
		}
		return nil, fmt.Errorf("scan blackboard entry: %w", err)
	}
	e.BlockedBy = decodeBlockedBy(blockedJSON)
	if e.ClaimedBy == "" {
		e.ClaimExpiresAt = 0
	}
	return &e, nil
}

func decodeBlockedBy(raw string) []string {
	if raw == "" || raw == "[]" {
		return nil
	}
	var deps []string
	if err := json.Unmarshal([]byte(raw), &deps); err != nil {
		return nil
	}
	return deps
}

func nonNilStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
