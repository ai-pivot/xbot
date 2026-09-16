package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"

	log "xbot/logger"
)

// feishu_cot.go — 飞书「原生 CoT（思考过程）」传输层。
//
// 渲染契约**照抄 dsh-lark**（github.com/omdsh-dev/dsh-lark：src/runtime.ts +
// src/cot.ts），因为用户要求「飞书的渲染功能和 dshlark 对齐」：
//
//	① 创建思考过程（每 turn 一次）：
//	   POST /open-apis/im/v1/message_cot?receive_id_type=chat_id
//	     {receive_id, origin_message_id?, cot_hidden, enable_badge:false, update_feed_rank:false}
//	     → {cot_id, message_id}
//	② 写事件（批量）：
//	   PUT  /open-apis/im/v1/message_cot
//	     {events:[{event_type, content(JSON string), timestamp}], message_id, cot_id}
//
// 两条硬约束（dsh-lark 实测得到的平台行为）：
//   - 单次 ≤50 事件（MAX_EVENTS_PER_WRITE）；
//   - 事件 content ≤4096 字符，超出按 rune 安全截断并打 {"truncated":true}；
//   - timestamp 必须**严格递增**（客户端按它排序，重复会乱序）。
//
// 思考过程是「呈现」，不是答案：最终答复仍走普通消息（见 channel 的最终回复
// 路径）。因此这里任何失败都只降级、绝不影响作答。
const (
	// feishuCotAPI 是飞书原生思考过程的 API 路径（dsh-lark 的 COT_API）。
	feishuCotAPI = "/open-apis/im/v1/message_cot"

	// cotMaxEventsPerWrite 是单次 PUT 的事件上限（dsh-lark: MAX_EVENTS_PER_WRITE）。
	cotMaxEventsPerWrite = 50

	// cotMaxEventContentChars 是单个事件 content 的上限（dsh-lark: 4096）。
	cotMaxEventContentChars = 4096

	// cotMaxToolResultRunes 是工具结果 code block 的截断长度（dsh-lark: boundResult 1500）。
	cotMaxToolResultRunes = 1500
)

// cotEvent 是 AG-UI 风格的事件：类型 + JSON 载荷 + 严格递增毫秒时间戳。
type cotEvent struct {
	EventType string `json:"event_type"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// feishuCoT 承载一个 turn 的思考过程：懒创建 + 异步批量写。
//
// 事件先入队，由单个后台 drainer 依序 flush —— SendProgress 在 agent loop 的
// 进度路径上被调用，**绝不能**在这里同步发 HTTP。
type feishuCoT struct {
	client  *lark.Client
	chatID  string
	replyTo string
	hidden  bool

	// request 是可替换的传输（测试注入 fake；生产走 lark SDK 裸请求）。
	request func(ctx context.Context, method, path string, body any) (*larkcore.ApiResp, error)

	mu        sync.Mutex
	cotID     string
	messageID string
	lastTS    int64
	pending   []cotEvent
	draining  bool
	// broken 标记创建/写入失败过一次：之后只降级（调用方回落卡片渲染）。
	broken bool
}

func newFeishuCoT(client *lark.Client, chatID, replyTo string, hidden bool) *feishuCoT {
	c := &feishuCoT{client: client, chatID: chatID, replyTo: replyTo, hidden: hidden}
	c.request = c.sdkRequest
	return c
}

// sdkRequest 生产实现：lark SDK 的裸请求（租户 token）。
func (c *feishuCoT) sdkRequest(ctx context.Context, method, path string, body any) (*larkcore.ApiResp, error) {
	switch method {
	case "POST":
		return c.client.Post(ctx, path, body, larkcore.AccessTokenTypeTenant)
	case "PUT":
		return c.client.Put(ctx, path, body, larkcore.AccessTokenTypeTenant)
	default:
		return nil, errCotUnsupportedMethod(method)
	}
}

type errCotUnsupportedMethod string

func (e errCotUnsupportedMethod) Error() string {
	return "feishu cot: unsupported method " + string(e)
}

// emit 追加一个事件（异步；调用方永不阻塞）。payload 会被编成 JSON 字符串。
func (c *feishuCoT) emit(eventType string, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		log.WithError(err).Warn("feishu cot: marshal event payload failed")
		return
	}
	content := string(raw)
	if runes := []rune(content); len(runes) > cotMaxEventContentChars {
		// rune 安全截断 + 显式标记（绝不静默丢内容）。
		// ⚠️ JSON 转义会增加字符（引号/反斜杠翻倍），所以不能"截一次就完"——
		// 逐次收缩直到**编出来的 JSON 本身**也落在上限内；下界是纯标记形式。
		trimmed := `{"truncated":true}`
		for keep := len(runes); keep > 1; keep = keep * 3 / 4 {
			candidate, err := json.Marshal(map[string]any{"truncated": true, "raw": string(runes[:keep])})
			if err == nil && len([]rune(string(candidate))) <= cotMaxEventContentChars {
				trimmed = string(candidate)
				break
			}
		}
		content = trimmed
	}

	c.mu.Lock()
	if c.broken {
		c.mu.Unlock()
		return
	}
	c.lastTS++ // 严格递增：即使同一毫秒内连续 emit 也不会重复
	if ts := time.Now().UnixMilli(); ts > c.lastTS {
		c.lastTS = ts
	}
	c.pending = append(c.pending, cotEvent{
		EventType: eventType,
		Content:   content,
		Timestamp: int64ToString(c.lastTS),
	})
	shouldStart := !c.draining && len(c.pending) > 0
	if shouldStart {
		c.draining = true
	}
	c.mu.Unlock()

	if shouldStart {
		go c.drain()
	}
}

// drain 是唯一的写线程：依序取队首 ≤50 事件 flush，直到队列空。
func (c *feishuCoT) drain() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("feishu cot: drain panic: %v", r)
		}
		c.mu.Lock()
		c.draining = false
		hasMore := len(c.pending) > 0 && !c.broken
		if hasMore {
			c.draining = true
		}
		c.mu.Unlock()
		if hasMore {
			go c.drain()
		}
	}()

	for {
		c.mu.Lock()
		if c.broken || len(c.pending) == 0 {
			c.mu.Unlock()
			return
		}
		batch := make([]cotEvent, 0, cotMaxEventsPerWrite)
		n := len(c.pending)
		if n > cotMaxEventsPerWrite {
			n = cotMaxEventsPerWrite
		}
		batch = append(batch, c.pending[:n]...)
		c.pending = c.pending[n:]
		c.mu.Unlock()

		if err := c.write(batch); err != nil {
			log.WithError(err).Warn("feishu cot: write events failed — 思考过程降级（答案不受影响）")
			c.mu.Lock()
			c.broken = true
			c.pending = nil
			c.mu.Unlock()
			return
		}
	}
}

// write 创建（一次）+ 写入一批事件。
func (c *feishuCoT) write(events []cotEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c.mu.Lock()
	cotID, messageID := c.cotID, c.messageID
	c.mu.Unlock()

	if cotID == "" || messageID == "" {
		body := map[string]any{
			"receive_id": c.chatID,
			"cot_hidden": c.hidden,
			// 思考过程不是「新消息」：不弹未读、不把会话顶到列表最前（dsh-lark 同）。
			"enable_badge":     false,
			"update_feed_rank": false,
		}
		if c.replyTo != "" {
			body["origin_message_id"] = c.replyTo
		}
		resp, err := c.request(ctx, "POST", feishuCotAPI+"?receive_id_type=chat_id", body)
		if err != nil {
			return err
		}
		var created struct {
			Data struct {
				CotID     string `json:"cot_id"`
				MessageID string `json:"message_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.RawBody, &created); err != nil {
			return err
		}
		if created.Data.CotID == "" || created.Data.MessageID == "" {
			return errCotNoHandle
		}
		c.mu.Lock()
		c.cotID, c.messageID = created.Data.CotID, created.Data.MessageID
		cotID, messageID = c.cotID, c.messageID
		c.mu.Unlock()
	}

	_, err := c.request(ctx, "PUT", feishuCotAPI, map[string]any{
		"events":     events,
		"message_id": messageID,
		"cot_id":     cotID,
	})
	return err
}

// brokenNow 报告该 turn 的思考过程是否已不可用（调用方据此回落卡片渲染）。
func (c *feishuCoT) brokenNow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.broken
}

// flushNow 同步写完队列（收尾与测试用；异步 drainer 语义不变）。
func (c *feishuCoT) flushNow() error {
	for {
		c.mu.Lock()
		if c.broken || len(c.pending) == 0 {
			c.mu.Unlock()
			return nil
		}
		n := len(c.pending)
		if n > cotMaxEventsPerWrite {
			n = cotMaxEventsPerWrite
		}
		batch := append([]cotEvent(nil), c.pending[:n]...)
		c.pending = c.pending[n:]
		c.mu.Unlock()

		if err := c.write(batch); err != nil {
			c.mu.Lock()
			c.broken = true
			c.pending = nil
			c.mu.Unlock()
			return err
		}
	}
}

type cotError string

func (e cotError) Error() string { return string(e) }

const errCotNoHandle cotError = "feishu cot: platform returned no cot_id/message_id"

func int64ToString(v int64) string {
	// 小工具：避免再引 strconv 到本文件的热路径之外（语义与 strconv 相同）。
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
		if v == 0 {
			break
		}
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// cotToolKind 把工具名归到 dsh-lark 的图标词表（read/write/search/bash）。
func cotToolKind(name string) string {
	switch strings.ToLower(name) {
	case "shell", "bash", "execute", "command":
		return "bash"
	case "read", "file_read", "readfile":
		return "read"
	case "edit", "filereplace", "filecreate", "write", "file_write", "multiedit":
		return "write"
	case "grep", "glob", "search", "websearch", "fetch", "webfetch":
		return "search"
	default:
		return ""
	}
}

// cotBoundResult 截断工具结果（rune 安全；dsh-lark 用 1500 字符上限）。
func cotBoundResult(s string) string {
	r := []rune(s)
	if len(r) <= cotMaxToolResultRunes {
		return s
	}
	return string(r[:cotMaxToolResultRunes]) + "…"
}
