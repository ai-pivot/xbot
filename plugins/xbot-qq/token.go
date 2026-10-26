package main

// 官方 QQ 机器人的鉴权模型（docs: 开放平台 → 开发 → 接口鉴权）：
//
//	POST https://bots.qq.com/app/getAppAccessToken
//	{"appId": "...", "clientSecret": "..."}
//	→ {"access_token": "...", "expires_in": 7200}
//
// 然后每个 OpenAPI 请求带 `Authorization: QQBot {access_token}`。
//
// 两个容易踩的点，本文件专门处理：
//
//  1. **必须提前续期**。token 有效期约 7200s，用到期才换会让每 2 小时出现
//     一批 401。这里在"到期前 skew"就换新（默认 60s，且不超过有效期一半），
//     所以正常路径不会打到过期 token。
//  2. **expires_in 可能是字符串**。官方返回的 expires_in 在不同版本里出现过
//     number 与 string 两种形态，这里用 json.RawMessage 两种都认。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTokenURL = "https://bots.qq.com/app/getAppAccessToken"
	defaultAPIBase  = "https://api.sgroup.qq.com"

	// tokenRefreshSkew 是提前续期的安全窗：token 剩余寿命小于它就换新。
	tokenRefreshSkew = 60 * time.Second

	// defaultTokenTTL 是官方文档的典型值，仅在响应缺 expires_in 时兜底。
	defaultTokenTTL = 7200 * time.Second
)

// TokenSource 是"取一个当前可用的 access_token"的抽象。
// APIClient 与 Gateway 都只依赖它，单测可替换成计数用的假实现。
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Invalidate 丢弃缓存。收到 401 或 op=9 时调用，下一次 Token 必然重取。
	Invalidate()
}

// TokenClient 是 TokenSource 的生产实现：带缓存与提前续期。
type TokenClient struct {
	appID     string
	appSecret string
	tokenURL  string
	http      *http.Client
	now       func() time.Time
	skew      time.Duration

	mu         sync.Mutex
	token      string
	usableTill time.Time
	fetches    int
}

// NewTokenClient 构造生产用 TokenClient。
func NewTokenClient(appID, appSecret string) *TokenClient {
	return &TokenClient{
		appID:     appID,
		appSecret: appSecret,
		tokenURL:  defaultTokenURL,
		http:      &http.Client{Timeout: 15 * time.Second},
		now:       time.Now,
		skew:      tokenRefreshSkew,
	}
}

// tokenRequest 是换 token 的请求体（字段名是官方的小驼峰）。
type tokenRequest struct {
	AppID        string `json:"appId"`
	ClientSecret string `json:"clientSecret"`
}

// tokenResponse 是换 token 的响应。ExpiresIn 保留原始 JSON 以兼容
// number / string 两种形态。
type tokenResponse struct {
	AccessToken string          `json:"access_token"`
	ExpiresIn   json.RawMessage `json:"expires_in"`
	TraceID     string          `json:"trace_id"`
}

// Token 返回当前可用的 access_token。
//
// 缓存命中条件：token 非空且 now < usableTill。否则同步换新（持锁期间换，
// 顺带避免并发请求把换 token 打成惊群）。
func (c *TokenClient) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.token != "" && now.Before(c.usableTill) {
		return c.token, nil
	}

	token, ttl, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	c.token = token
	c.usableTill = now.Add(c.usableWindow(ttl))
	c.fetches++
	return token, nil
}

// Invalidate 丢掉缓存 token（401 / op=9 后调用）。
func (c *TokenClient) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.usableTill = time.Time{}
}

// Fetches 返回真正发起换 token 请求的次数（对账 / 单测断言用）。
func (c *TokenClient) Fetches() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetches
}

// usableWindow 计算"拿到 token 后多久就得换新"。
//
// ttl - skew，但有效期本身很短（测试或平台调整）时最多用一半，
// 避免 usableWindow 变负导致每次调用都换 token。
func (c *TokenClient) usableWindow(ttl time.Duration) time.Duration {
	skew := c.skew
	if skew <= 0 {
		skew = tokenRefreshSkew
	}
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	if ttl <= 2*skew {
		return ttl / 2
	}
	return ttl - skew
}

func (c *TokenClient) fetch(ctx context.Context) (string, time.Duration, error) {
	body, err := json.Marshal(tokenRequest{AppID: c.appID, ClientSecret: c.appSecret})
	if err != nil {
		return "", 0, fmt.Errorf("marshal token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("getAppAccessToken: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("read getAppAccessToken response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("getAppAccessToken: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", 0, fmt.Errorf("decode getAppAccessToken response: %w (body=%s)", err, truncate(string(raw), 300))
	}
	if strings.TrimSpace(tr.AccessToken) == "" {
		return "", 0, fmt.Errorf("getAppAccessToken returned empty access_token (body=%s)", truncate(string(raw), 300))
	}
	return tr.AccessToken, parseExpiresIn(tr.ExpiresIn), nil
}

// parseExpiresIn 兼容 number 与 string，缺省返回 0（由 usableWindow 兜底）。
func parseExpiresIn(raw json.RawMessage) time.Duration {
	s := strings.TrimSpace(strings.Trim(string(raw), `"`))
	if s == "" || s == "null" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// truncate 截断日志里的响应体，避免把整页 HTML 打进日志。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
