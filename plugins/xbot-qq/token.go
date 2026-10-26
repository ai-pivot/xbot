package main

// 官方 QQ 机器人的鉴权模型。
//
// 文档依据：QQ 机器人官方文档《接口调用与鉴权》
//
//	https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/api-use.html
//	§获取调用凭证 / §鉴权方式
//
//	POST {token 端点}
//	{"appId": "...", "clientSecret": "..."}
//	→ {"access_token": "...", "expires_in": "7200"}
//
// 然后每个 OpenAPI 请求带 `Authorization: QQBot {ACCESS_TOKEN}`（同页 §鉴权方式）。
//
// 两个容易踩的点，本文件专门处理：
//
//  1. **必须提前续期**。同页 §其他说明原文：「目前 access_token 生命周期默认
//     7200 秒……在上一个 access_token 接近过期时间 60 秒内，获取 access_token
//     时，会获得一个新的 access_token，老的 access_token 在这个 60 秒内仍然
//     有效。」所以这里在"到期前 skew"就换新（默认 60s，且不超过有效期一半）。
//  2. **expires_in 可能是字符串**。同页 §请求参数把它写成 number，但 §返回示例
//     给的是 `"expires_in": "7200"`（字符串）。这里用 json.RawMessage 两种都认。
//
// ⚠️ 端点口径不一致（记录在案，未实测）：
//   - 首辅派发口径与既往实现：`https://bots.qq.com/app/getAppAccessToken`
//   - 现行文档（api-use.html §获取调用凭证）：`https://api.bot.qq.com/app/getAppAccessToken`
//
// 没有真实凭据无法判定哪个是当前生效域名，因此按"transport 失败才换下一个"
// 的顺序尝试两者；收到 HTTP 4xx 不切换（那是凭据/参数错，换域名没用）。
// 联调时以实际返回为准，并回来收敛成单一端点。

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
	// defaultAPIBase 是 OpenAPI 基地址。
	// 出处：event-emit.html §WebSocket方式（示例 wss://api.bot.qq.com/websocket/）
	// 与 url_get.html §示例（同域名）。历史上是 api.sgroup.qq.com，两者并存，
	// 联调时以实际返回为准。
	defaultAPIBase = "https://api.sgroup.qq.com"

	// tokenRefreshSkew 是提前续期的安全窗：token 剩余寿命小于它就换新。
	// 出处：api-use.html §其他说明（"接近过期时间 60 秒内"）。
	tokenRefreshSkew = 60 * time.Second

	// defaultTokenTTL 是文档给出的典型有效期，仅在响应缺 expires_in 时兜底。
	// 出处：api-use.html §请求参数（"目前是7200秒之内的值"）。
	defaultTokenTTL = 7200 * time.Second
)

// defaultTokenURLs 是换 token 的端点候选，按顺序尝试。
//
// 第 1 项来自既往文档/派发口径，第 2 项来自现行 api-use.html；两处不一致，
// 详见文件头注释。只在 transport 层失败时降级。
var defaultTokenURLs = []string{
	"https://bots.qq.com/app/getAppAccessToken",
	"https://api.bot.qq.com/app/getAppAccessToken",
}

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
	tokenURLs []string
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
		tokenURLs: defaultTokenURLs,
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

// fetch 按 tokenURLs 顺序尝试，transport 失败才降级到下一个端点。
//
// 不做 4xx/5xx 降级：那通常是凭据或参数错误，换域名只会掩盖真实原因。
func (c *TokenClient) fetch(ctx context.Context) (string, time.Duration, error) {
	urls := c.tokenURLs
	if len(urls) == 0 {
		urls = defaultTokenURLs
	}
	var lastErr error
	for _, u := range urls {
		tok, ttl, retryable, err := c.fetchFrom(ctx, u)
		if err == nil {
			return tok, ttl, nil
		}
		lastErr = err
		if !retryable {
			return "", 0, err
		}
	}
	return "", 0, lastErr
}

// fetchFrom 向单一端点换 token。retryable 表示"换个端点可能成功"
// （只有连接层失败算这一类）。
func (c *TokenClient) fetchFrom(ctx context.Context, url string) (token string, ttl time.Duration, retryable bool, err error) {
	body, err := json.Marshal(tokenRequest{AppID: c.appID, ClientSecret: c.appSecret})
	if err != nil {
		return "", 0, false, fmt.Errorf("marshal token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, false, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// 连接失败（DNS / TCP / TLS）：可能只是这个域名不通，换下一个。
		return "", 0, true, fmt.Errorf("getAppAccessToken(%s): %w", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, true, fmt.Errorf("read getAppAccessToken response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, false, fmt.Errorf("getAppAccessToken(%s): HTTP %d: %s", url, resp.StatusCode, truncate(string(raw), 300))
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", 0, false, fmt.Errorf("decode getAppAccessToken response: %w (body=%s)", err, truncate(string(raw), 300))
	}
	if strings.TrimSpace(tr.AccessToken) == "" {
		return "", 0, false, fmt.Errorf("getAppAccessToken returned empty access_token (body=%s)", truncate(string(raw), 300))
	}
	return tr.AccessToken, parseExpiresIn(tr.ExpiresIn), false, nil
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
