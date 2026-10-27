package main

// 业务事件解析：把网关 op=0 的 d 报文映射成渠道内部的 Inbound。
//
// 文档依据（QQ 机器人官方文档）：
//
//	《单聊消息事件》C2C_MESSAGE_CREATE
//	  https://bot.q.qq.com/wiki/develop/api-v2/autogen/event/c2c_message_create.html
//	  - Intent: GROUP_AND_C2C_EVENT (1<<25)
//	  - 事件体：id / author / content / timestamp / message_type …
//	  - author 表（同页 User）：user_openid「用户 OpenID（单聊场景使用）」
//	    member_openid「群成员 OpenID（群聊场景使用）」；id 是「用户唯一标识（OpenID 格式）」
//
//	《群@机器人消息》GROUP_AT_MESSAGE_CREATE
//	  https://bot.q.qq.com/wiki/develop/api-v2/autogen/event/group_at_message_create.html
//	  - author「发送者（member_openid 有值）」；group_openid「群 OpenID」
//	  - content「消息文本内容（已去除@机器人的前缀）」
//
//	《群消息（全量模式）》GROUP_MESSAGE_CREATE
//	  https://bot.q.qq.com/wiki/develop/api-v2/autogen/event/group_message_create.html
//	  - 「当机器人开启了『接收所有消息』功能后，群里的每一条消息（不限于@机器人）
//	    都会推送此事件」；Intent 同为 1<<25
//
//	《消息收发概述》§收发场景 / §消息去重
//	  https://bot.q.qq.com/wiki/develop/api-v2/server-inter/message/overview.html
//
// 身份只认 openid：虽然 User.id 也标称 OpenID 格式，但事件页明确区分了
// 「单聊场景使用 user_openid」「群聊场景使用 member_openid」，本实现按场景取
// 对应字段；两个字段都没有时报错，不退回 author.id（避免拿错作用域的 ID 做白名单）。
//
// ⚠️ 文档自相矛盾（记录在案）：
//   - event-emit.html §事件订阅与通知 的示例 payload 把 s 描述为「消息唯一性」；
//   - c2c_message_create.html / group_at_message_create.html 开头说「相同 msg_id
//     可能重复推送，开发者需结合 msg_seq 做去重」；
//   - 但同页下一句又说「需结合 message_scene.ext 中的 msg_idx 做去重」；
//   - overview.html §消息去重 又写「相同 msg_id 可能多次推送，请结合 msg_seq 去重」。
//   msg_seq 是**发送**侧字段（回复去重），事件体里并没有 msg_seq —— 这里以事件页
//   「msg_idx」描述为准，**本插件不做去重**（request_id=d.id 交给 host 侧对齐），
//   重复推送的实际频率属未证实项，待联调观察。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 支持的官方事件类型。
const (
	EventC2CMessageCreate     = "C2C_MESSAGE_CREATE"
	EventGroupAtMessageCreate = "GROUP_AT_MESSAGE_CREATE"
	EventGroupMessageCreate   = "GROUP_MESSAGE_CREATE"
)

// Inbound 是一条解析完成的官方入站消息。
type Inbound struct {
	// EventType 是原始事件名（C2C_... / GROUP_AT_...）。
	EventType string
	// MessageID 是 d.id，被动回复时作为 msg_id 回传。
	MessageID string
	// Content 是消息正文（已去掉首尾空白；群 @ 消息带的前导空格会被清掉）。
	Content string
	// SenderOpenID 是发送者 openid（私聊 user_openid / 群聊 member_openid）。
	SenderOpenID string
	// SenderName 是展示名。官方 C2C/群事件不给昵称，通常为空。
	SenderName string
	// GroupOpenID 是群 openid，仅群聊非空。
	GroupOpenID string
	// Reply 非空表示这是一条**引用消息**（message_type==103），值是被引用
	// 消息的一层快照（不递归）。
	Reply *QuotedMessage
	// Attachments 是官方 attachments[]（多模态：图片/语音/视频/文件）。
	Attachments []MessageAttachment
	// MsgIdx 是 message_scene.ext 里的 msg_idx（官方的去重键）。缺失时为 0。
	MsgIdx int
	// SceneExt 是 message_scene.ext 解析后的 k=v 映射（含 msg_idx / ref_msg_idx /
	// auth_token 等）。官方格式是 []string 的 k=v 串，不是 map。
	SceneExt map[string]string
	// Timestamp 是消息时间（解析失败为零值）。
	Timestamp time.Time
	// Raw 保留原文，便于排查平台字段变化。
	Raw json.RawMessage
}

// IsGroup 报告是否群聊消息。
func (m Inbound) IsGroup() bool { return m.GroupOpenID != "" }

// ChatID 返回 host 会话标识：私聊 = 发送者 openid，群聊 = "group:<群 openid>"。
func (m Inbound) ChatID() string {
	if m.IsGroup() {
		return GroupChatID(m.GroupOpenID)
	}
	return PrivateChatID(m.SenderOpenID)
}

// Identity 转成身份注入用的 Identity。
func (m Inbound) Identity() Identity {
	return Identity{
		SenderID:   m.SenderOpenID,
		SenderName: m.SenderName,
		GroupID:    m.GroupOpenID,
	}.Normalize()
}

// Valid 报告这条消息是否齐全到可以进 agent。
// Valid 报告这条入站消息是否可处理。
//
// 2026-10-26 修正：图片/语音/文件消息的 content 是**空的**（正文在图/附件里），
// 旧实现要求 content 非空，导致「发图完全没反应」（日志表现为
// inbound dropped: ... incomplete (empty content)，用户侧看起来像卡死）。
// 现在：只要正文非空 **或** 带附件，就算有效；真正的内容由 attachments 注入。
func (m Inbound) Valid() bool {
	if m.MessageID == "" || m.SenderOpenID == "" {
		return false
	}
	return strings.TrimSpace(m.Content) != "" || len(m.Attachments) > 0
}

// eventEnvelope 是业务事件的 d 结构（官方字段名，出处见文件头）。
type eventEnvelope struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	GroupOpenID string `json:"group_openid"`
	Timestamp   string `json:"timestamp"`
	// MessageType 官方 message_type：103 = 引用消息。
	MessageType int `json:"message_type"`
	// MsgElements 出处：事件页 msg_elements 数组（引用消息携带的被引用快照）。
	MsgElements []MsgElement `json:"msg_elements"`
	// Attachments 出处：事件页 attachments 数组（MessageAttachment）：
	// url / filename / width / height / size / content_type / voice_wav_url / asr_refer_text。
	Attachments []MessageAttachment `json:"attachments"`
	// MessageScene 出处：事件页 message_scene 对象；ext 是 **[]string 的 "k=v" 串**
	// （msg_idx / ref_msg_idx / auth_token …）。
	MessageScene struct {
		Ext    []string `json:"ext"`
		Source string   `json:"source"`
	} `json:"message_scene"`
	Author struct {
		ID           string `json:"id"`
		Username     string `json:"username"`
		UserOpenID   string `json:"user_openid"`
		MemberOpenID string `json:"member_openid"`
		UnionOpenID  string `json:"union_openid"`
	} `json:"author"`
	// Mentions 是被 @ 的用户列表（GROUP_MESSAGE_CREATE 才有；用于判断是否 @ 了机器人）。
	// 出处：group_message_create.html §事件体「被 @ 的成员列表见 mentions 字段」，
	// 其中 User.bot 标明该成员是否为机器人。
	Mentions []MentionUser `json:"mentions"`
}

// SupportedEvent 报告该事件是否由本渠道处理。
//
// GROUP_MESSAGE_CREATE（群全量消息）**故意不在此列**：本渠道只处理 @ 机器人的
// 群消息与单聊消息。它与 GROUP_AT_MESSAGE_CREATE 共用同一个 intent 位，
// 订阅层拆不开（见 gateway.go IntentGroupAndC2C 注释），因此分发层
// （main.go handleGatewayEvent）显式丢弃并记 debug 日志 —— 这里是第二道防线。
func SupportedEvent(eventType string) bool {
	switch eventType {
	case EventC2CMessageCreate, EventGroupAtMessageCreate, EventGroupMessageCreate:
		return true
	default:
		return false
	}
}

// ParseInbound 解析一条事件报文。
//
// 返回 ok=false 表示"不是本渠道关心的事件"（心跳之外的其它 dispatch，
// 例如 GUILD_CREATE），调用方应静默跳过；返回 error 表示事件类型对但报文
// 缺关键字段 —— 这种必须记日志，不能静默丢（否则线上表现为"机器人不理人"）。
func ParseInbound(eventType string, data json.RawMessage) (Inbound, bool, error) {
	if !SupportedEvent(eventType) {
		return Inbound{}, false, nil
	}
	var d eventEnvelope
	if err := json.Unmarshal(data, &d); err != nil {
		return Inbound{}, true, fmt.Errorf("parse %s payload: %w", eventType, err)
	}

	// 群全量（GROUP_MESSAGE_CREATE）：群里每条消息都会推，必须自己判断有没有 @ 机器人。
	// 判据：mentions 里存在 bot=true 的成员（官方文档：被 @ 的成员见 mentions 字段）。
	// 没 @ 机器人就 ok=false 静默跳过 —— 不进消息循环、不调 LLM、不出站。
	if eventType == EventGroupMessageCreate {
		mentionedBot := false
		for i := range d.Mentions {
			if d.Mentions[i].Bot {
				mentionedBot = true
				break
			}
		}
		if !mentionedBot {
			return Inbound{}, false, nil
		}
	}

	// 群全量模式下 content 会把 @ 成员替换成「<@对方openid>」（官方 group_message_create.html
	// §事件体：消息中 @ 某成员时替换为 <@ + member_openid + >，@全体成员为 <@all>）。
	// 若不清理，agent 收到的是 "<@5268…> 1" 而不是 "1"。
	// 处理：把 @ **本机器人** 的标记整段去掉；@ 其他成员保留（agent 需要知道 @ 了谁），
	// <@all> 也保留。
	content := strings.TrimSpace(d.Content)
	for i := range d.Mentions {
		m := &d.Mentions[i]
		if !m.Bot {
			continue
		}
		id := m.MemberOpenID
		if id == "" {
			id = m.ID
		}
		if id != "" {
			content = strings.ReplaceAll(content, "<@"+id+">", "")
		}
	}
	content = strings.TrimSpace(content)

	sceneExt := parseSceneExt(d.MessageScene.Ext)
	in := Inbound{
		EventType:   eventType,
		MessageID:   d.ID,
		Attachments: d.Attachments,
		SceneExt:    sceneExt,
		MsgIdx:      sceneExtInt(sceneExt, "msg_idx"),
		Content:     content,
		SenderName:  d.Author.Username, // 出处：事件页 User.username（可能为空，仅供展示）
		GroupOpenID: d.GroupOpenID,
		Raw:         data,
	}
	if ts, err := time.Parse(time.RFC3339, d.Timestamp); err == nil {
		in.Timestamp = ts
	}

	// ── 身份：只认对应场景的 openid ──
	//
	// 出处：事件页 User 表——user_openid「单聊场景使用」、member_openid「群聊场景
	// 使用」。author.id 是另一个字段（文档称「用户唯一标识（OpenID 格式）」），
	// 但不是事件页指定的场景字段，不拿它兜底。
	switch eventType {
	case EventC2CMessageCreate:
		in.SenderOpenID = d.Author.UserOpenID
	case EventGroupAtMessageCreate, EventGroupMessageCreate:
		in.SenderOpenID = d.Author.MemberOpenID
	}
	if in.SenderOpenID == "" {
		return in, true, fmt.Errorf("%s payload has no sender openid "+
			"(user_openid/member_openid); refusing to fall back to author.id=%q", eventType, d.Author.ID)
	}
	// 群事件必须带 group_openid；私聊事件不该带。
	isGroupEvent := eventType != EventC2CMessageCreate
	if isGroupEvent && in.GroupOpenID == "" {
		return in, true, fmt.Errorf("%s payload has no group_openid", eventType)
	}
	if !isGroupEvent && in.GroupOpenID != "" {
		return in, true, fmt.Errorf("%s payload unexpectedly carries group_openid=%q", eventType, in.GroupOpenID)
	}
	// 群事件的会话 id 必须是 "group:<group_openid>"，且**必须与发送者不同**：
	// 两者相同意味着回复会被当私聊发给那个人（群里什么都看不到）。这条不变量
	// 是"群里收不到"的直接对照实验判据，放解析层挡住，不给下游猜的机会。
	if isGroupEvent {
		if in.GroupOpenID == in.SenderOpenID {
			return in, true, fmt.Errorf("%s payload group_openid == member_openid (%q); "+
				"身份字段取错，拒绝把它当群会话", eventType, in.GroupOpenID)
		}
		if !strings.HasPrefix(in.ChatID(), "group:") {
			return in, true, fmt.Errorf("%s payload chat id %q lacks group: prefix", eventType, in.ChatID())
		}
	}
	// 引用消息：只取一层快照（msg_elements），不递归。
	if isQuoteMessage(d.MessageType, d.MsgElements) {
		if q, ok := pickQuoted(d.MsgElements, sceneExt); ok {
			in.Reply = &q
		}
	}

	// 引用里的附件也要下载。
	//
	// 2026-10-27 真机：用户「引用一条带图的消息 + @机器人」时，**当前消息**
	// 的 attachments 是空的，图片在被引用那条（msg_elements[].attachments）里。
	// 只取 d.Attachments ⇒ agent 只看到一行占位、拿不到图，表现为
	// 「引用消息里的图片没过来」。
	// 合并策略：先用当前消息的附件；没有则取引用元素的附件；两者都有时去重合并。
	if in.Reply != nil && len(in.Reply.Attachments) > 0 {
		seen := make(map[string]bool, len(in.Attachments))
		for _, at := range in.Attachments {
			seen[at.DownloadURL()] = true
		}
		for _, at := range in.Reply.Attachments {
			u := at.DownloadURL()
			if u != "" && !seen[u] {
				in.Attachments = append(in.Attachments, at)
				seen[u] = true
			}
		}
	}
	if in.MessageID == "" {
		return in, true, fmt.Errorf("%s payload has no message id (d.id required for passive reply)", eventType)
	}
	return in, true, nil
}

// MentionUser 是 mentions 数组里的一个被 @ 成员。
//
// 出处：group_message_create.html §事件体 → mentions []User，
// User.bot = 是否为机器人；群内 @ 机器人时该机器人会出现在这里。
type MentionUser struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Bot          bool   `json:"bot"`
	MemberOpenID string `json:"member_openid"`
	UnionOpenID  string `json:"union_openid"`
}
