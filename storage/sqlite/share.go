package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// ErrSharedArtifactNotFound is returned when a share token does not resolve to a
// live artifact — unknown token, revoked, or expired. Callers must NOT
// distinguish these cases to the client: doing so would leak whether a token
// ever existed.
var ErrSharedArtifactNotFound = errors.New("shared artifact not found")

// SharedArtifact is one immutable, shareable snapshot of plugin-provided
// content.
//
// The host is deliberately content-agnostic: ContentType is named by the
// producing plugin and Payload is opaque to the host — only that plugin's own
// share renderer can interpret it. Token is the credential (a high-entropy
// random string) that grants read access to this one artifact without any
// session.
type SharedArtifact struct {
	Token string `json:"token"`
	// PluginID is the plugin that produced this artifact — the public share page
	// loads exactly that plugin to obtain the renderer for it.
	PluginID    string `json:"plugin_id"`
	ContentType string `json:"content_type"`
	Payload     string `json:"payload"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
	CreatedBy   int64  `json:"created_by"`
	// ExpiresAt is RFC3339, or "" for no expiry.
	ExpiresAt string `json:"expires_at"`
	// RevokedAt is RFC3339 once revoked, otherwise "".
	RevokedAt string `json:"revoked_at"`
}

// newSharedArtifactToken returns a 256-bit URL-safe random credential.
func newSharedArtifactToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate share token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ShareService stores shared artifacts (generic host capability — it holds no
// knowledge of any particular content type).
type ShareService struct{ db *DB }

// NewShareService creates a ShareService.
func NewShareService(db *DB) *ShareService { return &ShareService{db: db} }

// Create inserts a new shared artifact, minting its token.
//
// The token is the credential, so it is generated here — 256 bits of crypto/rand,
// never derived from content — and callers cannot supply their own.
func (s *ShareService) Create(a *SharedArtifact) error {
	if a.ContentType == "" || a.PluginID == "" {
		return fmt.Errorf("create shared artifact: plugin_id and content_type are required")
	}
	if a.Token == "" {
		token, err := newSharedArtifactToken()
		if err != nil {
			return err
		}
		a.Token = token
	}
	if a.CreatedAt == "" {
		a.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := s.db.Conn().Exec(`
		INSERT INTO shared_artifacts
			(token, plugin_id, content_type, payload, title, created_at, created_by, expires_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, a.Token, a.PluginID, a.ContentType, a.Payload, a.Title, a.CreatedAt, a.CreatedBy, a.ExpiresAt, a.RevokedAt)
	if err != nil {
		return fmt.Errorf("create shared artifact: %w", err)
	}
	return nil
}

// Get returns the live artifact for a token. Revoked and expired artifacts
// report ErrSharedArtifactNotFound, identical to an unknown token.
func (s *ShareService) Get(token string) (*SharedArtifact, error) {
	if token == "" {
		return nil, ErrSharedArtifactNotFound
	}
	row := s.db.Conn().QueryRow(`
		SELECT token, plugin_id, content_type, payload, title, created_at, created_by, expires_at, revoked_at
		FROM shared_artifacts WHERE token = ?
	`, token)
	var a SharedArtifact
	if err := row.Scan(&a.Token, &a.PluginID, &a.ContentType, &a.Payload, &a.Title,
		&a.CreatedAt, &a.CreatedBy, &a.ExpiresAt, &a.RevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSharedArtifactNotFound
		}
		return nil, fmt.Errorf("get shared artifact: %w", err)
	}
	if a.RevokedAt != "" {
		return nil, ErrSharedArtifactNotFound
	}
	if a.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, a.ExpiresAt); err == nil && time.Now().After(exp) {
			return nil, ErrSharedArtifactNotFound
		}
	}
	return &a, nil
}

// Revoke marks an artifact revoked. Scoped to the creator so one user cannot
// revoke another's share; revoking an already-revoked/unknown token is a no-op
// from the caller's perspective (returns ErrSharedArtifactNotFound).
func (s *ShareService) Revoke(token string, createdBy int64) error {
	res, err := s.db.Conn().Exec(`
		UPDATE shared_artifacts SET revoked_at = ?
		WHERE token = ? AND created_by = ? AND revoked_at = ''
	`, time.Now().UTC().Format(time.RFC3339), token, createdBy)
	if err != nil {
		return fmt.Errorf("revoke shared artifact: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke shared artifact: %w", err)
	}
	if n == 0 {
		return ErrSharedArtifactNotFound
	}
	return nil
}

// ListByCreator returns the caller's artifacts, newest first (revoked and
// expired ones included so the UI can show them as inactive).
func (s *ShareService) ListByCreator(createdBy int64) ([]SharedArtifact, error) {
	rows, err := s.db.Conn().Query(`
		SELECT token, plugin_id, content_type, payload, title, created_at, created_by, expires_at, revoked_at
		FROM shared_artifacts WHERE created_by = ? ORDER BY created_at DESC
	`, createdBy)
	if err != nil {
		return nil, fmt.Errorf("list shared artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []SharedArtifact{}
	for rows.Next() {
		var a SharedArtifact
		if err := rows.Scan(&a.Token, &a.PluginID, &a.ContentType, &a.Payload, &a.Title,
			&a.CreatedAt, &a.CreatedBy, &a.ExpiresAt, &a.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan shared artifact: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate shared artifacts: %w", err)
	}
	return out, nil
}
