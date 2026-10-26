package main

// 官方 QQ 机器人 OpenAPI 客户端（只用 stdlib）。
//
// 覆盖本插件需要的四个端点：
//
//	GET  /gateway                                   拿 WebSocket 地址（接 gateway.go）
//	POST /v2/users/{openid}/messages                私聊发消息
//	POST /v2/groups/{group_openid}/messages         群聊发消息
//
// 鉴权头固定 `Authorization: QQBot {access_token}`。
//
// 401 的处理是本文件的关键：token 可能在两次请求之间过期（缓存窗口算错、
// 平台提前失效、时钟漂移），收到 401 后**必须** Invalidate + 重新取 token
// 再重试一次，而不是把 401 直接丢给调用方导致消息永久丢失。只重试一次，
// 避免凭据本身错误时打成无限循环。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MsgTypeText 是官方文本消息的 msg_type。
const MsgTypeText = 0

// SentMessage 是发消息接口的响应。
type SentMessage struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

// APIError 是一个非 2xx 的 OpenAPI 响应。
type APIError struct {
	Op     string // "gateway" / "send_c2c" / "send_group"
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("qq openapi %s: HTTP %d: %s", e.Op, e.Status, e.Body)
}

// IsUnauthorized 便于调用方判断是否需要换 token。
func (e *APIError) IsUnauthorized() bool { return e.Status == http.StatusUnauthorized }

// APIClient 是官方 OpenAPI 的最小客户端。
type APIClient struct {
	tokens  TokenSource
	http    *http.Client
	baseURL string
}

// NewAPIClient 构造生产用客户端。
func NewAPIClient(tokens TokenSource) *APIClient {
	return &APIClient{
		tokens:  tokens,
		http:    &http.Client{Timeout: 20 * time.Second},
		baseURL: defaultAPIBase,
	}
}

// GatewayURL 调 GET /gateway 拿 wss 地址。
func (c *APIClient) GatewayURL(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.do(ctx, "gateway", http.MethodGet, "/gateway", nil, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.URL) == "" {
		return "", fmt.Errorf("qq openapi gateway: empty url in response")
	}
	return out.URL, nil
}

// SendC2C 给私聊用户发一条文本消息。
//
// msgID 非空表示被动回复（官方要求 5 分钟内、按收到的消息 id 回）；
// msgID 为空表示主动消息（受平台主动消息配额限制）。msgSeq 由 Sender 递增。
func (c *APIClient) SendC2C(ctx context.Context, userOpenID, content, msgID string, msgSeq uint32) (*SentMessage, error) {
	path := "/v2/users/" + url.PathEscape(userOpenID) + "/messages"
	return c.sendMessage(ctx, "send_c2c", path, content, msgID, msgSeq)
}

// SendGroup 给群聊发一条文本消息。
func (c *APIClient) SendGroup(ctx context.Context, groupOpenID, content, msgID string, msgSeq uint32) (*SentMessage, error) {
	path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/messages"
	return c.sendMessage(ctx, "send_group", path, content, msgID, msgSeq)
}

// messageBody 是发消息请求体。msg_seq 必须带（官方用它给同一条被动回复
// 的多个分片去重/排序）。
type messageBody struct {
	Content string `json:"content"`
	MsgType int    `json:"msg_type"`
	MsgID   string `json:"msg_id,omitempty"`
	MsgSeq  uint32 `json:"msg_seq"`
}

func (c *APIClient) sendMessage(ctx context.Context, op, path, content, msgID string, msgSeq uint32) (*SentMessage, error) {
	body := messageBody{Content: content, MsgType: MsgTypeText, MsgID: msgID, MsgSeq: msgSeq}
	var out SentMessage
	if err := c.do(ctx, op, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do 执行一次带鉴权的请求；401 时换 token 重试一次。
func (c *APIClient) do(ctx context.Context, op, method, path string, payload, out any) error {
	err := c.doOnce(ctx, op, method, path, payload, out)
	var apiErr *APIError
	if err != nil && errors.As(err, &apiErr) && apiErr.IsUnauthorized() {
		// token 过期/被平台提前失效：丢掉缓存，用新 token 重试一次。
		c.tokens.Invalidate()
		err = c.doOnce(ctx, op, method, path, payload, out)
	}
	return err
}

func (c *APIClient) doOnce(ctx context.Context, op, method, path string, payload, out any) error {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("qq openapi %s: %w", op, err)
	}

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("qq openapi %s: marshal body: %w", op, err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("qq openapi %s: build request: %w", op, err)
	}
	req.Header.Set("Authorization", "QQBot "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qq openapi %s: %w", op, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("qq openapi %s: read response: %w", op, err)
	}
	if resp.StatusCode/100 != 2 {
		return &APIError{Op: op, Status: resp.StatusCode, Body: truncate(string(raw), 500)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("qq openapi %s: decode response: %w (body=%s)", op, err, truncate(string(raw), 300))
	}
	return nil
}
