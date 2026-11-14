package protocol

import "time"

// structured.go — 结构化消息（sendmessage-structured 设计 §4/§5）的协议形态。
//
// 存储层是会话库 session_messages 的一行；这里是与前端/HTTP 交互的 JSON 形态。
// message_id 用会话库行 id（int64）表示（前端按它去重；同一会话库内唯一）。

// StructuredMessageInput 是 POST /api/message 的请求体。
type StructuredMessageInput struct {
	Kind           string         `json:"kind"`
	Payload        map[string]any `json:"payload,omitempty"`
	CorrelationID  string         `json:"correlation_id,omitempty"`
	ReplyTo        string         `json:"reply_to,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	To             string         `json:"to,omitempty"`
	Text           string         `json:"text,omitempty"`
	Channel        string         `json:"channel,omitempty"`
	ChatID         string         `json:"chat_id,omitempty"`
	// CreatedAt 可选（RFC3339）：仅用于导入/回填/演示数据，缺省由服务端取 now。
	// 消息顺序与时间线展示以它为准，因此调用方要保证单调递增。
	CreatedAt string `json:"created_at,omitempty"`

	// ── 仅内部路径（SendMessage to="user"）使用：json:"-" 让 HTTP 客户端
	//    无法伪造（否则任意网页会话都能把卡片"回流"打进别人的子代理会话）。
	//
	// OriginSession 是发起方的**物理会话 key**（interactive 子代理形如
	// "web:web-4/court.shoufu:main"，主会话形如 "web:web-4"）。用户在看板上处理
	// 卡片时，回流消息投递到这个会话。
	OriginSession string `json:"-"`
	// OriginAgent 是发起方的逻辑地址（"agent:<role>/<instance>"），审计/看板展示用。
	OriginAgent string `json:"-"`
	// Silent 为 true 表示**落库但不唤醒**目标会话：卡片是给用户看的待办，
	// 不是给目标 agent 的新一轮输入（否则用户会话会被自己的卡片自激）。
	Silent bool `json:"-"`
}

// StructuredResolveInput 是 POST /api/inbox/resolve 的请求体：用户在看板上处理一张卡片。
//
// Action：approve（准）/ reject（驳）/ dismiss（忽略）。状态机是 pending → resolved，
// CAS 保证同一张卡只生效一次（重复点、刷新后重点都不重复回流）。
type StructuredResolveInput struct {
	MessageID      int64  `json:"message_id"`
	Action         string `json:"action"`
	Comment        string `json:"comment,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Channel        string `json:"channel,omitempty"`
	ChatID         string `json:"chat_id,omitempty"`
}

// StructuredResolveResult 是卡片处理结果。
type StructuredResolveResult struct {
	// MessageID 是被处理的卡片（收件箱里那一行）的 id。
	MessageID int64 `json:"message_id"`
	// ResolutionID 是本次写入的 decision/answer 行 id（重复提交时为首次那一行）。
	ResolutionID int64 `json:"resolution_id"`
	// Status：approved | rejected | dismissed。
	Status string `json:"status"`
	// Duplicate 为 true 表示这次是重复提交（状态早已 resolved，未再次回流）。
	Duplicate bool `json:"duplicate"`
	// Origin 是发起方物理会话 key（空表示这张卡没有回流目标）。
	Origin string `json:"origin,omitempty"`
}

// StructuredAnswerInput 是 POST /api/answer 的请求体。
type StructuredAnswerInput struct {
	CorrelationID  string             `json:"correlation_id"`
	Answers        []StructuredAnswer `json:"answers,omitempty"`
	Cancelled      bool               `json:"cancelled,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
	Channel        string             `json:"channel,omitempty"`
	ChatID         string             `json:"chat_id,omitempty"`
}

// StructuredAnswer 是单个问题的回答。
type StructuredAnswer struct {
	QuestionID string   `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	Text       string   `json:"text,omitempty"`
}

// StructuredMessage 是可渲染的结构化消息。
type StructuredMessage struct {
	MessageID     int64          `json:"message_id"`
	Kind          string         `json:"kind"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	ReplyTo       string         `json:"reply_to,omitempty"`
	Direction     string         `json:"direction"` // in | out
	Answered      bool           `json:"answered"`
	Cancelled     bool           `json:"cancelled"`
	Text          string         `json:"text"`
	Payload       map[string]any `json:"payload,omitempty"`
	Sender        SenderInfo     `json:"sender"`
	CreatedAt     time.Time      `json:"created_at"`
	// Envelope 是完整信封 JSON（原样回传，前端需要时可解析扩展字段）。
	Envelope map[string]any `json:"envelope,omitempty"`
}

// SenderInfo 是消息发送者身份。
type SenderInfo struct {
	Type string `json:"type"` // user | agent | system
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}
