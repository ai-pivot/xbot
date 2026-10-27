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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MsgTypeText 是官方文本消息的 msg_type。
//
// 出处：《消息收发概述》§消息类型（msg_type=0 → 文本，内容字段 content）。
const MsgTypeText = 0

// MsgTypeMarkdown 是官方 Markdown 消息的 msg_type。
//
// 出处：《发送群聊消息》/《发送单聊消息》§请求体
//
//	msg_type: 0=纯文本(content) 2=Markdown(markdown) 7=富媒体(media)
//	注意：传了 markdown 后 content 必须为空
const MsgTypeMarkdown = 2

// MessageMarkdown 是 msg_type=2 时的 markdown 内容体。
type MessageMarkdown struct {
	Content string `json:"content,omitempty"`
}

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
	// errCodePassiveQuota 被动回复次数/时间超限（HTTP 400，msg_id 5 分钟或 4 次用尽）。
	errCodePassiveQuota = 40034128
	// errCodeActiveMsgNoPerm 主动消息无权限（被动回复窗口已过又没有主动消息权限）。
	errCodeActiveMsgNoPerm = 40034105
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

// IsPassiveQuotaExceeded 报告是否"被动回复次数/时间超限"。
//
// 触发条件：同一 msg_id 超过 4 次（单聊）/ 5 次（群聊），或超过 5 分钟有效期。
// 与 IsRateLimited 一样属于"继续发只会更糟"的错误，应立刻停手而非重试。
func (e *APIError) IsPassiveQuotaExceeded() bool {
	if e == nil {
		return false
	}
	if e.ErrCode == errCodePassiveQuota || e.Code == errCodePassiveQuota {
		return true
	}
	return strings.Contains(e.Message, "被动回复")
}

// IsActiveMsgNoPerm 报告是否"主动消息无权限"（窗口过后又无权限）。
func (e *APIError) IsActiveMsgNoPerm() bool {
	if e == nil {
		return false
	}
	if e.ErrCode == errCodeActiveMsgNoPerm || e.Code == errCodeActiveMsgNoPerm {
		return true
	}
	return strings.Contains(e.Message, "主动消息")
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
	// logf 可选日志钩子（由 runtime 注入，便于把上传失败写进插件日志）。
	logf func(format string, args ...any)
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
	// Markdown 仅在 msg_type=2 时填写；此时 Content 必须为空（官方硬约束）。
	Markdown *MessageMarkdown `json:"markdown,omitempty"`
	// Media 仅在 msg_type=7 时填写（富媒体）。
	Media  *MessageMedia `json:"media,omitempty"`
	MsgID  string        `json:"msg_id,omitempty"`
	MsgSeq uint32        `json:"msg_seq"`
}

// MessageMedia 是 msg_type=7（富媒体）的内容体；file_info 来自 UploadImage。
type MessageMedia struct {
	FileInfo string `json:"file_info"`
}

// sendMarkdown 以 msg_type=2 发送 Markdown 消息（content 留空，官方硬约束）。
func (c *APIClient) sendMarkdown(ctx context.Context, op, path, markdown, msgID string, msgSeq uint32) (*SentMessage, error) {
	body := messageBody{MsgType: MsgTypeMarkdown, Markdown: &MessageMarkdown{Content: markdown}, MsgID: msgID, MsgSeq: msgSeq}
	var out SentMessage
	if err := c.do(ctx, op, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
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

// SendGroupMarkdown 以 Markdown（msg_type=2）发群消息。
func (c *APIClient) SendGroupMarkdown(ctx context.Context, groupOpenID, markdown, msgID string, msgSeq uint32) (*SentMessage, error) {
	return c.sendMarkdown(ctx, http.MethodPost, "/v2/groups/"+groupOpenID+"/messages", markdown, msgID, msgSeq)
}

// SendC2CMarkdown 以 Markdown（msg_type=2）发单聊消息。
func (c *APIClient) SendC2CMarkdown(ctx context.Context, userOpenID, markdown, msgID string, msgSeq uint32) (*SentMessage, error) {
	return c.sendMarkdown(ctx, http.MethodPost, "/v2/users/"+userOpenID+"/messages", markdown, msgID, msgSeq)
}

// ── 私聊流式消息（stream_messages） ──
//
// 出处：《发送单聊流式消息》POST /v2/users/{user_openid}/stream_messages
//
//	input_mode    append（默认，拼接到 Pending）/ replace（ContentRaw 为全量正文）
//	input_state   1=生成中，10=生成结束
//	index         分片序号，从 0 递增
//	content_type  text / markdown
//	content_raw   本片内容
//	msg_id        被动回复消息 ID（与 event_id 二选一）
//	stream_msg_id 流式消息 ID：**首片由服务端生成并返回**，后续分片必须携带
//	msg_seq       消息序号，用于去重
//
// 说明：群聊没有对应的流式接口（官方只提供 /v2/users/...），因此流式仅用于私聊。
type streamBody struct {
	InputMode   string `json:"input_mode,omitempty"`
	InputStream int    `json:"input_state"`
	Index       int    `json:"index"`
	ContentType string `json:"content_type"`
	ContentRaw  string `json:"content_raw"`
	MsgID       string `json:"msg_id,omitempty"`
	MsgSeq      uint32 `json:"msg_seq,omitempty"`
	StreamMsgID string `json:"stream_msg_id,omitempty"`
}

// StreamC2C 发送一片私聊流式消息；返回该片（首片即整条流式消息）的 id。
func (c *APIClient) StreamC2C(ctx context.Context, userOpenID string, body streamBody) (*SentMessage, error) {
	var out SentMessage
	path := "/v2/users/" + userOpenID + "/stream_messages"
	if err := c.do(ctx, "stream_c2c", http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── 富媒体（图片）发送 ──
//
// 出处：《上传富媒体素材》POST /v2/{groups|users}/{openid}/files
//
//	file_type   1=图片 2=视频 3=语音 4=文件
//	url         媒体 URL（http 开头），**平台会自行下载并转存** —— 不用传二进制
//	srv_send_msg false = 仅返回 file_info，用于随后在消息里引用（不占主动消息频次）
//	             true  = 直接发送，但**会占用主动消息频次**（多数应用无此权限，40034105）
//
// 因此正确姿势：先 srv_send_msg=false 拿 file_info，再用 msg_type=7 的 media
// 字段做**被动回复**。下面两个方法即为此设计。
type uploadResp struct {
	FileUUID string `json:"file_uuid"`
	FileInfo string `json:"file_info"`
	TTL      int    `json:"ttl"`
	ID       string `json:"id"`
}

// uploadBinaryLimit 是 file_data 直传的大小上限（官方：5M 以内）。
const uploadBinaryLimit = 5 << 20

// UploadImage 上传一张图片，返回 file_info（供 SendMedia 使用）。
//
// 优先**直传二进制**（file_data，base64）：因为图片来源常常是签名 URL / 内网地址，
// 平台侧没有我们的凭据，`url` 方式会下载失败（表现为 400 或 file_info 为空）。
// 超过 5MB 时才退回 url 方式（让平台去下）。
func (c *APIClient) UploadImage(ctx context.Context, chatID, imageURL string) (string, error) {
	groupOpenID, userOpenID, err := parseChatID(chatID)
	if err != nil {
		return "", err
	}
	path := "/v2/users/" + userOpenID + "/files"
	if groupOpenID != "" {
		path = "/v2/groups/" + groupOpenID + "/files"
	}
	body := map[string]any{
		"file_type":    1, // 图片
		"srv_send_msg": false,
	}
	// 先尝试本地读取（http(s) 抓取或本地文件），拿得到就走二进制直传。
	if data, name, ferr := fetchImageBytes(ctx, imageURL, 64<<20); ferr == nil && len(data) > 0 {
		// 超过 5M 直传上限时先压缩（用户要求：「超 5M 就压缩」）。
		// 压不动（格式不支持等）才退回 url 方式。
		if len(data) > uploadBinaryLimit {
			if small, cname, cerr := compressToLimit(data, uploadBinaryLimit); cerr == nil {
				if c.cfgLog() != nil {
					c.cfgLog()("upload_media: compressed %d -> %d bytes (%s)", len(data), len(small), cname)
				}
				data = small
				if cname != "" {
					name = cname
				}
			} else if c.cfgLog() != nil {
				c.cfgLog()("upload_media: compress failed (%v), falling back to url", cerr)
			}
		}
		body["file_data"] = base64.StdEncoding.EncodeToString(data)
		body["file_name"] = name
	} else {
		// 读不到或超过上限：退回让平台自己下载（仅当 URL 公网可访问时才有效）。
		if ferr != nil && c.logf != nil {
			c.logf("upload_media: local read failed (%v), falling back to url", ferr)
		}
		body["url"] = imageURL
		body["file_name"] = fileNameFromURL(imageURL)
	}
	var out uploadResp
	if err := c.do(ctx, "upload_media", http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	if out.FileInfo == "" {
		return "", fmt.Errorf("upload_media: empty file_info in response")
	}
	return out.FileInfo, nil
}

// SendMedia 以 msg_type=7（富媒体）发送一条图片消息（被动回复）。
func (c *APIClient) SendMedia(ctx context.Context, chatID, fileInfo, msgID string, msgSeq uint32) (*SentMessage, error) {
	groupOpenID, userOpenID, err := parseChatID(chatID)
	if err != nil {
		return nil, err
	}
	path := "/v2/users/" + userOpenID + "/messages"
	if groupOpenID != "" {
		path = "/v2/groups/" + groupOpenID + "/messages"
	}
	body := messageBody{
		MsgType: 7, // 富媒体
		Media:   &MessageMedia{FileInfo: fileInfo},
		MsgID:   msgID,
		MsgSeq:  msgSeq,
	}
	var out SentMessage
	if err := c.do(ctx, "send_media", http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// fetchImageBytes 读取图片字节：支持 http(s) URL 与本地绝对路径。
// 返回数据、文件名；超过 limit 直接报错（由调用方退回 url 方式）。
func fetchImageBytes(ctx context.Context, src string, limit int) ([]byte, string, error) {
	if strings.HasPrefix(src, "/") {
		fi, err := os.Stat(src)
		if err != nil {
			return nil, "", err
		}
		if fi.Size() > int64(limit) {
			return nil, "", fmt.Errorf("local file %d bytes exceeds %d", fi.Size(), limit)
		}
		b, err := os.ReadFile(src)
		return b, filepath.Base(src), err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("GET %s: HTTP %d", src, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > limit {
		return nil, "", fmt.Errorf("remote image exceeds %d bytes", limit)
	}
	return b, fileNameFromURL(src), nil
}

// fileNameFromURL 从 URL 里取文件名，取不到就用固定名（file_name 为必填）。
func fileNameFromURL(u string) string {
	if i := strings.LastIndex(u, "/"); i >= 0 && i+1 < len(u) {
		name := u[i+1:]
		if j := strings.IndexAny(name, "?#"); j > 0 {
			name = name[:j]
		}
		if name != "" {
			return name
		}
	}
	return "image.png"
}

// cfgLog 返回日志钩子（可能为 nil）。
func (c *APIClient) cfgLog() func(string, ...any) { return c.logf }

// UploadFile 上传一个**文件**（file_type=4），返回 file_info（供 SendMedia 使用）。
//
// 出处：《上传富媒体素材》POST /v2/{groups|users}/{openid}/files
//
//	file_type 4 = 文件；file_name 必填；file_data 直传上限 5M。
//
// 文件的处理与图片不同：**不做压缩**（压缩会破坏内容）。所以：
//   - ≤5M：本地读二进制直传
//   - >5M ：退回 url 方式（由平台去下；仅在公网可达时有效）
//   - 都不可行：返回明确错误（调用方会把失败原因告诉用户，不静默丢）
func (c *APIClient) UploadFile(ctx context.Context, chatID, fileSrc, fileName string) (string, error) {
	groupOpenID, userOpenID, err := parseChatID(chatID)
	if err != nil {
		return "", err
	}
	path := "/v2/users/" + userOpenID + "/files"
	if groupOpenID != "" {
		path = "/v2/groups/" + groupOpenID + "/files"
	}
	if fileName == "" {
		fileName = fileNameFromURL(fileSrc)
	}
	body := map[string]any{
		"file_type":    4, // 文件
		"srv_send_msg": false,
		"file_name":    fileName,
	}
	if data, _, ferr := fetchImageBytes(ctx, fileSrc, uploadBinaryLimit); ferr == nil && len(data) > 0 {
		body["file_data"] = base64.StdEncoding.EncodeToString(data)
	} else {
		if ferr != nil && c.cfgLog() != nil {
			c.cfgLog()("upload_file: local read failed (%v) for %q, falling back to url", ferr, fileSrc)
		}
		body["url"] = fileSrc
	}
	var out uploadResp
	if err := c.do(ctx, "upload_media", http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	if out.FileInfo == "" {
		return "", fmt.Errorf("upload_file: empty file_info in response")
	}
	return out.FileInfo, nil
}
