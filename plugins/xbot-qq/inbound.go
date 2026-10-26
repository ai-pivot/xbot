package main

import "fmt"

// 本文件实现**身份注入**：把一条 QQ 入站消息的发送者身份，映射成 host
// send_inbound RPC 的参数（host 侧字段见 serverapp/rpc_table.go
// registerSettingsHandlers 里 send_inbound 的匿名 struct）。
//
// 字段映射表（host 字段 ← QQ 官方事件来源）：
//
//	channel      ← 固定 "qq"（渠道标识）
//	chat_id      ← 私聊 "<user_openid>"，群聊 "group:<group_openid>"
//	sender_id    ← 发送者 openid（C2C 用 d.author.user_openid，
//	               群聊用 d.author.member_openid）；白名单与 admin 判定都用它
//	sender_name  ← 群名片 / 昵称（官方 C2C/群事件通常不给，留空；不参与权限判断）
//	chat_type    ← "p2p" / "group"
//	request_id   ← 渠道生成的请求追踪 ID（host 用它对齐乐观消息与去重）
//	content      ← 消息正文
//	metadata.is_admin ← "true"/"false"，渠道侧管理员判定结果
//	metadata.group_id ← 群 openid（群聊时有，私聊时缺省）
//
// 身份只认 openid：d.author.id 是平台内部 id，不是 openid 也不是 QQ 号，
// 不能写进 sender_id（换 app 就变，且无法与用户对应）。
//
// 关于 admin 的边界：host 侧的权限权威是 config.AgentConfig.IsAdminSender
// （agent.isAdminSender → agent.admins）。本字段是**渠道侧**判定结果，用于
// 进 agent 之前的门禁。两处名单必须同口径：同一个 openid 要同时写进
// agent.admins 与 channels.qq.admins。

// 元数据键。抽成常量，避免测试与实现各写一份字符串。
const (
	MetaKeyIsAdmin = "is_admin"
	MetaKeyGroupID = "group_id"
)

// ChatTypeP2P / ChatTypeGroup 是 host 认的会话类型字面量。
const (
	ChatTypeP2P   = "p2p"
	ChatTypeGroup = "group"
)

// Identity 是一条入站 QQ 消息的发送者身份。
type Identity struct {
	// SenderID 是发送者 openid（C2C: user_openid；群聊: member_openid）。
	SenderID string
	// SenderName 是群名片或昵称，可为空（官方事件通常不给）。
	SenderName string
	// ChatID 是会话标识；为空时可由 Normalize 从 GroupID/SenderID 推导。
	ChatID string
	// ChatType 是 "p2p" / "group"；为空时由 Normalize 推导。
	ChatType string
	// GroupID 是群 openid，仅群聊时非空。
	GroupID string
}

// PrivateChatID 返回私聊会话标识（host 侧沿用 "<openid>" 的形式）。
func PrivateChatID(senderID string) string { return senderID }

// GroupChatID 返回群聊会话标识（"group:<群 openid>"，与白名单里 group:* 的写法同构）。
func GroupChatID(groupID string) string { return "group:" + groupID }

// Normalize 补齐可推导字段，幂等。
//
// 推导规则：有 GroupID → 群聊（chat_id=group:<群 openid>）；否则 → 私聊
// （chat_id=<openid>）。已显式给出的字段一律保留，不覆盖调用方的判断。
func (i Identity) Normalize() Identity {
	out := i
	if out.ChatType == "" {
		if out.GroupID != "" {
			out.ChatType = ChatTypeGroup
		} else {
			out.ChatType = ChatTypeP2P
		}
	}
	if out.ChatID == "" {
		switch {
		case out.GroupID != "":
			out.ChatID = GroupChatID(out.GroupID)
		case out.SenderID != "":
			out.ChatID = PrivateChatID(out.SenderID)
		}
	}
	return out
}

// Valid 报告身份是否齐全到可以送进 agent。
func (i Identity) Valid() bool {
	return i.SenderID != "" && i.ChatID != ""
}

// IsGroup 报告这是不是群聊消息。
func (i Identity) IsGroup() bool { return i.ChatType == ChatTypeGroup }

// Describe 返回日志用的单行描述。
func (i Identity) Describe() string {
	who := i.SenderID
	if i.SenderName != "" {
		who = fmt.Sprintf("%s(%s)", i.SenderName, i.SenderID)
	}
	return fmt.Sprintf("%s in %s", who, i.ChatID)
}

// InboundCandidate 是一条已决定提交给 host 的入站消息。
type InboundCandidate struct {
	Identity Identity
	Content  string
	// Admin 是渠道侧管理员判定结果（Policy.IsAdmin）。
	Admin bool
	// RequestID 用于 host 侧去重/对齐乐观消息。
	RequestID string
}

// Params 映射成 host send_inbound 的参数对象。
//
// 返回 map[string]any 而不是预定义 struct：host 侧 send_inbound 的参数是
// 匿名 struct，字段名就是契约；用 map 能让测试直接序列化比对，也便于阶段 3
// 增补 metadata（媒体路径等）而不改类型。
func (c InboundCandidate) Params() map[string]any {
	id := c.Identity.Normalize()

	meta := map[string]string{
		MetaKeyIsAdmin: fmt.Sprintf("%t", c.Admin),
	}
	if id.GroupID != "" {
		meta[MetaKeyGroupID] = id.GroupID
	}

	return map[string]any{
		"channel":     ChannelName,
		"chat_id":     id.ChatID,
		"content":     c.Content,
		"sender_id":   id.SenderID,
		"sender_name": id.SenderName,
		"chat_type":   id.ChatType,
		"request_id":  c.RequestID,
		"metadata":    meta,
	}
}

// NewInboundCandidate 按渠道策略给一条消息定身份与权限。
//
// 只负责"组装"，不负责"是否发送" —— 发送由调用方（app.emitInbound）在
// Policy.ShouldHandle 之后决定，这样拒绝路径也能拿到 Admin 标记写日志。
func NewInboundCandidate(id Identity, content, requestID string, p Policy) InboundCandidate {
	id = id.Normalize()
	return InboundCandidate{
		Identity:  id,
		Content:   content,
		Admin:     p.IsAdmin(id.SenderID),
		RequestID: requestID,
	}
}
