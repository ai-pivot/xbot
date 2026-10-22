package push

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/config"
	"xbot/storage"
)

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// serviceAccount is the subset of a Firebase service-account JSON we need.
// The private key is a SECRET: never log this struct.
type serviceAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// FCMProvider sends Android push messages through FCM HTTP v1.
type FCMProvider struct {
	projectID   string
	mode        string // data | notification
	clientEmail string
	privateKey  *rsa.PrivateKey
	tokenURL    string
	sendURL     string
	http        *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewFCM loads the service account and builds the provider.
func NewFCM(cfg config.FCMConfig) (*FCMProvider, error) {
	if cfg.CredentialsFile == "" {
		return nil, fmt.Errorf("fcm: credentials_file is required")
	}
	raw, err := os.ReadFile(cfg.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("fcm: read credentials: %w", err)
	}
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("fcm: parse credentials: %w", err)
	}
	if sa.ClientEmail == "" {
		return nil, fmt.Errorf("fcm: credentials missing client_email")
	}
	key, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("fcm: parse private_key: %w", err)
	}
	projectID := cfg.ProjectID
	if projectID == "" {
		projectID = sa.ProjectID
	}
	if projectID == "" {
		return nil, fmt.Errorf("fcm: project_id is required")
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "data"
	}
	if mode != "data" && mode != "notification" {
		return nil, fmt.Errorf("fcm: unknown mode %q", mode)
	}
	tokenURL := sa.TokenURI
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}
	return &FCMProvider{
		projectID:   projectID,
		mode:        mode,
		clientEmail: sa.ClientEmail,
		privateKey:  key,
		tokenURL:    tokenURL,
		sendURL:     fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", projectID),
		http:        &http.Client{Timeout: 5 * time.Second},
	}, nil
}

func (p *FCMProvider) Name() string     { return "fcm" }
func (p *FCMProvider) Platform() string { return "android" }

func (p *FCMProvider) Validate(t storage.PushTarget) error {
	if t.Address == "" {
		return fmt.Errorf("fcm: empty registration token")
	}
	if t.Platform != "" && t.Platform != "android" {
		return fmt.Errorf("fcm: platform %q is not android", t.Platform)
	}
	return nil
}

// Send delivers one event. On an expired OAuth token it refreshes once.
func (p *FCMProvider) Send(ctx context.Context, t storage.PushTarget, ev Event) error {
	token, err := p.accessToken(ctx)
	if err != nil {
		return err
	}
	err = p.sendOnce(ctx, t, ev, token)
	if err != nil && ErrorKindOf(err) == Auth {
		p.invalidateToken()
		token, terr := p.accessToken(ctx)
		if terr != nil {
			return terr
		}
		err = p.sendOnce(ctx, t, ev, token)
	}
	return err
}

func (p *FCMProvider) sendOnce(ctx context.Context, t storage.PushTarget, ev Event, token string) error {
	data := map[string]string{
		"kind":    string(ev.Kind),
		"chat_id": ev.ChatID,
		"title":   ev.Title,
		"body":    ev.Body,
		"ts":      strconv.FormatInt(ev.TS, 10),
	}
	if ev.RequestID != "" {
		data["request_id"] = ev.RequestID
	}

	android := map[string]any{
		"priority": "NORMAL",
		"ttl":      "3600s",
	}
	if ev.HighPriority {
		android["priority"] = "HIGH"
	}
	if ev.CollapseKey != "" {
		android["collapse_key"] = ev.CollapseKey
	}
	msg := map[string]any{
		"token":   t.Address,
		"data":    data,
		"android": android,
	}
	if p.mode == "notification" {
		msg["notification"] = map[string]string{"title": ev.Title, "body": ev.Body}
		android["notification"] = map[string]string{"channel_id": "xbot_messages"}
	}

	body, err := json.Marshal(map[string]any{"message": msg})
	if err != nil {
		return PermanentError(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.sendURL, bytes.NewReader(body))
	if err != nil {
		return PermanentError(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return TransientError(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return classifyFCM(resp.StatusCode, respBody)
}

func classifyFCM(status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if len(detail) > 300 {
		detail = detail[:300]
	}
	if status == http.StatusOK {
		return nil
	}
	err := fmt.Errorf("fcm http %d: %s", status, detail)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return AuthError(err)
	case status == http.StatusTooManyRequests || status >= 500:
		return TransientError(err)
	case status == http.StatusNotFound:
		return PermanentError(err)
	case status == http.StatusBadRequest:
		// UNREGISTERED / INVALID_ARGUMENT mean the token is dead.
		return PermanentError(err)
	default:
		return PermanentError(err)
	}
}

func (p *FCMProvider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.token != "" && now.Before(p.tokenExp) {
		return p.token, nil
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]any{
		"iss":   p.clientEmail,
		"scope": fcmScope,
		"aud":   p.tokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", PermanentError(err)
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", PermanentError(err)
	}
	assertion := unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", PermanentError(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.http.Do(req)
	if err != nil {
		return "", TransientError(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("fcm token endpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized ||
			resp.StatusCode == http.StatusForbidden {
			return "", AuthError(err)
		}
		return "", TransientError(err)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", TransientError(err)
	}
	if tr.AccessToken == "" {
		return "", AuthError(fmt.Errorf("fcm token endpoint returned an empty token"))
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl < 5*time.Minute {
		ttl = 5 * time.Minute
	}
	p.token = tr.AccessToken
	p.tokenExp = now.Add(ttl - 1*time.Minute)
	return p.token, nil
}

func (p *FCMProvider) invalidateToken() {
	p.mu.Lock()
	p.token = ""
	p.tokenExp = time.Time{}
	p.mu.Unlock()
}

func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("not an RSA key")
		}
		return key, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}
