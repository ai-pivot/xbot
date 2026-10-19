package protocol

// Group 是「Agent 通信指挥室」的群组元数据（主库 groups 表的一行）。
//
// 群组只承载元数据：成员关系与展示所需的身份信息。消息仍按「群发=遍历成员
// 各发一份」的既有语义，落在每个成员自己的会话库里 —— 本类型不引入任何
// 群级消息聚合。
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	CreatedAt   string `json:"created_at"`
	MemberCount int    `json:"member_count"`
}

// GroupMember 是群组成员（主库 group_members 表的一行）。
type GroupMember struct {
	GroupID    string `json:"group_id"`
	MemberAddr string `json:"member_addr"`
	Status     string `json:"status"`
	JoinedAt   string `json:"joined_at"`
	// Name / Role / Instance 由 member_addr 派生（不落库），供前端直接渲染
	// 组织树节点，避免前端重复解析 agent:<role>/<instance> 约定。
	Name     string `json:"name,omitempty"`
	Role     string `json:"role,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// GroupMessageEnvelope 是写入 session_messages.record_data 的结构化元数据。
//
// 只放「可视化需要的信封字段」：kind 决定泳道弧线颜色与卡片形态，
// correlation_id 把提问→回答→决策→动作折叠成一条线程，to 表示扇出目标。
// 消息正文仍走 content，身份字段同时冗余在 sender_* 列上（索引/过滤用）。
type GroupMessageEnvelope struct {
	Kind          string         `json:"kind"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	To            []string       `json:"to,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

// GroupMessage 是 GET /api/groups/{id}/messages 的返回项。
type GroupMessage struct {
	ID            int64          `json:"id"`
	GroupID       string         `json:"group_id"`
	Kind          string         `json:"kind"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	SenderType    string         `json:"sender_type"`
	SenderID      string         `json:"sender_id"`
	SenderName    string         `json:"sender_name"`
	Role          string         `json:"role"`
	Content       string         `json:"content"`
	To            []string       `json:"to,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

// GroupMessageEvent 是 group.message 事件（WS/SSE 复用现有推送通道）。
type GroupMessageEvent struct {
	GroupID string        `json:"group_id"`
	Message *GroupMessage `json:"message"`
}

// GroupMessageInput 是 POST /api/groups/{id}/messages 的请求体（演示写入）。
type GroupMessageInput struct {
	Kind          string         `json:"kind"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Content       string         `json:"content"`
	SenderType    string         `json:"sender_type"`
	SenderID      string         `json:"sender_id"`
	SenderName    string         `json:"sender_name"`
	To            []string       `json:"to,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}
