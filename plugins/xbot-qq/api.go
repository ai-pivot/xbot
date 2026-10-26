package main

// 官方 QQ 机器人 OpenAPI 客户端（只用 stdlib）。
//
// 文档依据（QQ 机器人官方文档，API v2）：
//
//	GET  /gateway
//	     《获取通用 WSS 接入点》§接口
//	     https://bot.q.qq.com/wiki/develop/api-v2/openapi/wss/url_get.html
//	     返回 {"url": "wss://api.bot.qq.com/websocket/"}
//
//	POST /v2/users/{user_openid}/messages
//	     《发送单聊消息》§请求/§基础信息
//	     https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_users_user_openid_messages.post.html
//	     「被动消息有效时间 60 分钟，每个消息最多回复 4 次」
//
//	POST /v2/groups/{group_openid}/messages
//	     《发送群聊消息》§请求/§基础信息
//	     https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_messages.post.html
//	     「被动消息有效时间 5 分钟，每个消息最多回复 5 次」
//
// 请求体字段（msg_type / msg_id / msg_seq / content）与响应 id/timestamp 出处同上
// 两页的「请求体」「响应体」小节；msg_type=0 表示文本见《消息收发概述》§消息类型。
//
// 鉴权头 `Authorization: QQBot {ACCESS_TOKEN}` 出处：
//	《接口调用与鉴权》§鉴权方式
//	https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/api-use.html
//
// 401 的处理是实现层选择（文档只写了「开发者需要在过期后自行刷新 access_token」）：
// 收到 401 后 Invalidate + 重新取 token 再重试一次，避免把 401 直接丢给调用方。

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
//
// 出处：《消息收发概述》§消息类型（msg_type=0 → 文本，内容字段 content）。
const MsgTypeText = 0

// SentMessage 是发消息接口的响应。
//
// 出处：《发送单聊消息》/《发送群聊消息》§响应体（id 消息 ID、timestamp 发送时间）。
type SentMessage struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

// APIError 是一个非 2xx 的 OpenAPI 响应。
//
// Code / ErrCode / Message 是从官方错误体里解析出的结构化字段，用于把
// "重试也没用"的错误（IP 不在白名单、被限流、凭据错）与"可以重试"的错误
// （网络抖动、5xx）分开 —— 否则插件会在白名单没配的情况下无限重试，
// 一边烧被动消息配额，一边把自己送进平台风控。
//
// 官方错误体形如：
//
//	{"message":"接口访问源IP不在白名单","code":11298,"err_code":40023002,"trace_id":"..."}
type APIError struct {
	Op      string // "gateway" / "send_c2c" / "send_group"
	Status  int
	Body    string
	Code    int
	ErrCode int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("qq openapi %s: HTTP %d: %s", e.Op, e.Status, e.Body)
}

// IsUnauthorized 便于调用方判断是否需要换 token。
func (e *APIError) IsUnauthorized() bool { return e.Status == http.StatusUnauthorized }

// NewAPIError 解析官方错误体后构造 APIError（解析失败时只带 Status/Body）。
func NewAPIError(op string, status int, body string) *APIError {
	e := &APIError{Op: op, Status: status, Body: body}
	var parsed struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
		ErrCode int    `json:"err_code"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err == nil {
		e.Message = parsed.Message
		e.Code = parsed.Code
		e.ErrCode = parsed.ErrCode
	}
	return e
}

// 官方错误码（出处：平台返回体；本仓库只记实际观测到的两种）。
const (
	// errCodeIPNotAllowed 接口访问源 IP 不在白名单（HTTP 401）。
	errCodeIPNotAllowed = 40023002
	// codeIPNotAllowed 是同一错误的 code 字段。
	codeIPNotAllowed = 11298
	// errCodeRateLimited 接口调用超过频率限制（HTTP 400）。
	errCodeRateLimited = 40023001
	// codeRateLimited 是同一错误的 code 字段。
	codeRateLimited = 100017
)

// IsIPNotAllowed 报告是否"接口访问源 IP 不在白名单"。
//
// 这是**人工才能解**的错误：出口 IP 必须由用户在 QQ 开放平台加白名单。
// 重试一万次也不会变绿，只会把风控烧起来。
func (e *APIError) IsIPNotAllowed() bool {
	if e == nil {
		return false
	}
	if e.ErrCode == errCodeIPNotAllowed || e.Code == codeIPNotAllowed {
		return true
	}
	return strings.Contains(e.Message, "白名单")
}

// IsRateLimited 报告是否"接口调用超过频率限制"。
//
// 触发后继续按秒级重试会加重风控（官方口径：持续超限会延长封禁）。
func (e *APIError) IsRateLimited() bool {
	if e == nil {
		return false
	}
	if e.ErrCode == errCodeRateLimited || e.Code == codeRateLimited {
		return true
	}
	return strings.Contains(e.Message, "频率限制") || strings.Contains(e.Message, "频率")
}

// APIClient 是官方 OpenAPI 的最小客户端。
type APIClient struct {
	tokens  TokenSource
	http    *http.Client
	baseURL string
	// sandbox 记录当前环境，只用于日志与错误提示（请求走 baseURL）。
	sandbox bool
}

// NewAPIClient 构造客户端（默认沙箱环境）。
//
// 单测会直接改 baseURL 指向 httptest，所以这里不做任何网络动作。
func NewAPIClient(tokens TokenSource) *APIClient {
	return NewAPIClientEnv(tokens, true)
}

// NewAPIClientEnv 按环境构造客户端：sandbox=true → sandbox.api.sgroup.qq.com。
func NewAPIClientEnv(tokens TokenSource, sandbox bool) *APIClient {
	return &APIClient{
		tokens:  tokens,
		http:    &http.Client{Timeout: 20 * time.Second},
		baseURL: APIBase(sandbox),
		sandbox: sandbox,
	}
}

// Env 返回环境名（sandbox / prod）。
func (c *APIClient) Env() string {
	if c == nil {
		return ""
	}
	return EnvName(c.sandbox)
}

// GatewayURL 调 GET /gateway 拿 wss 地址。
//
// 出处：《获取通用 WSS 接入点》§接口/§示例（url_get.html）。
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
// 出处：《发送单聊消息》（v2_users_user_openid_messages.post.html）
// msgID 非空表示被动回复（事件 d.id）；msgID 为空表示主动消息。
// msgSeq 由 Sender 递增（相同 msg_id + msg_seq 重复发送会失败）。
func (c *APIClient) SendC2C(ctx context.Context, userOpenID, content, msgID string, msgSeq uint32) (*SentMessage, error) {
	path := "/v2/users/" + url.PathEscape(userOpenID) + "/messages"
	return c.sendMessage(ctx, "send_c2c", path, content, msgID, msgSeq)
}

// SendGroup 给群聊发一条文本消息。
//
// 出处：《发送群聊消息》（v2_groups_group_openid_messages.post.html）
func (c *APIClient) SendGroup(ctx context.Context, groupOpenID, content, msgID string, msgSeq uint32) (*SentMessage, error) {
	path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/messages"
	return c.sendMessage(ctx, "send_group", path, content, msgID, msgSeq)
}

// messageBody 是发消息请求体。
//
// 字段与语义出处：《发送单聊消息》/《发送群聊消息》§请求体
//
//	msg_type  消息类型（0=文本）
//	msg_id    被动回复的事件 ID（事件最外层 id）；与 event_id 二选一
//	msg_seq   回复消息的序号，与 msg_id 联合使用，避免相同消息 id 重复发送；
//	          不填默认 1；相同的 msg_id + msg_seq 重复发送会失败
//	content   文本内容（msg_type=0）
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
		return NewAPIError(op, resp.StatusCode, truncate(string(raw), 500))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("qq openapi %s: decode response: %w (body=%s)", op, err, truncate(string(raw), 300))
	}
	return nil
}
