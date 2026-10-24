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
