package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/crypto"
	log "xbot/logger"
)

// pgOAuthTokenStore implements OAuthTokenStore on PostgreSQL.
//
// Credential handling matches the SQLite implementation exactly: values are
// written through crypto.Encrypt when a key is configured and decrypted on read
// (a decrypt failure means the value was stored in plaintext, which is the
// supported fallback). scopes/raw are JSON text in both backends.
type pgOAuthTokenStore struct {
	pool *pgxpool.Pool
}

func newPGOAuthTokenStore(pool *pgxpool.Pool) *pgOAuthTokenStore {
	return &pgOAuthTokenStore{pool: pool}
}

func (s *pgOAuthTokenStore) GetOAuthToken(provider, channel, chatID string) (*OAuthToken, error) {
	ctx := context.Background()
	var accessToken, refreshToken, scopesJSON, rawJSON string
	var expiresAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT access_token, COALESCE(refresh_token, ''), expires_at,
		       COALESCE(scopes, ''), COALESCE(raw, '')
		FROM oauth_tokens WHERE provider = $1 AND channel = $2 AND chat_id = $3`,
		provider, channel, chatID).
		Scan(&accessToken, &refreshToken, &expiresAt, &scopesJSON, &rawJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query token: %w", err)
	}

	if accessToken != "" {
		if decrypted, err := crypto.Decrypt(accessToken); err == nil {
			accessToken = decrypted
		}
	}
	if refreshToken != "" {
		if decrypted, err := crypto.Decrypt(refreshToken); err == nil {
			refreshToken = decrypted
		}
	}

	var scopes []string
	if scopesJSON != "" {
		if err := json.Unmarshal([]byte(scopesJSON), &scopes); err != nil {
			log.WithError(err).Warn("Failed to parse scopes JSON")
		}
	}
	var raw map[string]any
	if rawJSON != "" {
		if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
			log.WithError(err).Warn("Failed to parse raw JSON")
		}
	}

	return &OAuthToken{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		Scopes:       scopes,
		Raw:          raw,
	}, nil
}

func (s *pgOAuthTokenStore) SetOAuthToken(provider, channel, chatID string, token *OAuthToken) error {
	ctx := context.Background()
	scopesJSON, _ := json.Marshal(token.Scopes)
	rawJSON, _ := json.Marshal(token.Raw)

	accessToken := token.AccessToken
	refreshToken := token.RefreshToken
	if accessToken != "" {
		if encrypted, err := crypto.Encrypt(accessToken); err == nil {
			accessToken = encrypted
		}
	}
	if refreshToken != "" {
		if encrypted, err := crypto.Encrypt(refreshToken); err == nil {
			refreshToken = encrypted
		}
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_tokens (provider, channel, chat_id, access_token, refresh_token,
			expires_at, scopes, raw, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now(), now())
		ON CONFLICT (provider, channel, chat_id) DO UPDATE SET
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			expires_at = excluded.expires_at,
			scopes = excluded.scopes,
			raw = excluded.raw,
			updated_at = now()`,
		provider, channel, chatID, accessToken, refreshToken,
		token.ExpiresAt, string(scopesJSON), string(rawJSON))
	if err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	log.WithFields(log.Fields{"provider": provider, "channel": channel, "chat_id": chatID}).
		Debug("OAuth token stored")
	return nil
}

func (s *pgOAuthTokenStore) DeleteOAuthToken(provider, channel, chatID string) error {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM oauth_tokens WHERE provider = $1 AND channel = $2 AND chat_id = $3`,
		provider, channel, chatID); err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	return nil
}

func (s *pgOAuthTokenStore) CleanupExpiredOAuthTokens(cutoff time.Time) error {
	ctx := context.Background()
	tag, err := s.pool.Exec(ctx, `DELETE FROM oauth_tokens WHERE expires_at < $1`, cutoff)
	if err != nil {
		return fmt.Errorf("cleanup expired tokens: %w", err)
	}
	if n := tag.RowsAffected(); n > 0 {
		log.WithField("count", n).Info("Cleaned up expired OAuth tokens")
	}
	return nil
}
