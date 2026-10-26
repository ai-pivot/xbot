package main

// 下行发送：把 host 的 channel 回复发到 QQ 官方接口。
//
// 官方规则（文档依据见下方常量块与各函数注释）：
//
//  1. 路由看 chat_id：私聊是用户 openid，群聊是 "group:<群 openid>"，
//     分别对应 POST /v2/users/{user_openid}/messages 与
//     POST /v2/groups/{group_openid}/messages。
//     （《消息收发概述》§收发场景）
//  2. 被动回复要带收到消息的 msg_id（事件最外层 d.id），有效期按场景区分：
//     单聊 60 分钟、群聊 5 分钟；超出窗口只能发主动消息（不带 msg_id，受配额限制）。
//  3. 同一个 msg_id 的多条回复用 msg_seq 递增：相同 msg_id + msg_seq 重复发送
//     会失败（《消息收发概述》§消息去重）。
//
// 本文件不碰网络细节（那是 api.go），只负责路由、分片、seq 与窗口。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// maxMessageRunes 是单条消息的分片上限。
//
// 官方发送接口页没有给出文本长度上限（未证实项），这里取保守的 1500 rune：
// 按 rune 切，不会把中文切坏；一条回复拆成多片时 msg_seq 递增。
const maxMessageRunes = 1500

// 被动消息与时效规则（文档依据）：
//
//	《消息收发概述》§频率与时效规则 → 被动消息
//	https://bot.q.qq.com/wiki/develop/api-v2/server-inter/message/overview.html
//	  场景   有效期    每条消息可回复次数
//	  单聊   60 分钟   4 次
//	  群聊   5 分钟    5 次
//
// 《发送单聊消息》/《发送群聊消息》各页开头重复了同一条约束：
//
//	https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_users_user_openid_messages.post.html
//	https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_messages.post.html
//
// 《消息收发概述》§消息去重：「相同 msg_id 可能多次推送……被动回复时，相同的
// msg_id + msg_seq 重复发送会失败，可递增 msg_seq 实现对同一消息的多次回复。」
//
// 注意：回复次数上限（单聊 4 / 群聊 5）在这里**强制自检** —— 不交给平台去报
// 40034128（被动回复时间或次数超限）。平台报错时消息已经丢了；我们能提前
// 拒发并把原因写进本地 ERROR 日志，便于排障。超出后的平台确切行为文档没写清，
// 属未证实项，留待真机联调确认。
const (
	// 被动回复有效期，按会话类型区分（见上表）。
	passiveWindowPrivate = 60 * time.Minute
	passiveWindowGroup   = 5 * time.Minute

	// 被动回复次数上限（每锚点）。出处同上表：单聊 4 次 / 群聊 5 次。
	passiveReplyLimitPrivate = 4
	passiveReplyLimitGroup   = 5
)

// passiveReplyLimit 返回该会话每个锚点允许的被动回复条数（单聊 4 / 群聊 5）。
func passiveReplyLimit(chatID string) uint32 {
	if strings.HasPrefix(strings.TrimSpace(chatID), "group:") {
		return passiveReplyLimitGroup
	}
	return passiveReplyLimitPrivate
}

// passiveReplyWindow 返回该会话的被动回复有效期（文档口径，按场景区分）。
func passiveReplyWindow(chatID string) time.Duration {
	if strings.HasPrefix(strings.TrimSpace(chatID), "group:") {
		return passiveWindowGroup
	}
	return passiveWindowPrivate
}

// MessageAPI 是发送侧依赖的最小接口（便于单测断言 URL 与 msg_seq）。
type MessageAPI interface {
	SendC2C(ctx context.Context, userOpenID, content, msgID string, msgSeq uint32) (*SentMessage, error)
	SendGroup(ctx context.Context, groupOpenID, content, msgID string, msgSeq uint32) (*SentMessage, error)
	// Markdown 变体（msg_type=2）。官方硬约束：此时 content 必须为空。
	SendC2CMarkdown(ctx context.Context, userOpenID, markdown, msgID string, msgSeq uint32) (*SentMessage, error)
	SendGroupMarkdown(ctx context.Context, groupOpenID, markdown, msgID string, msgSeq uint32) (*SentMessage, error)
	// StreamC2C 发一片私聊流式消息（仅私聊；群聊官方无流式接口）。
	StreamC2C(ctx context.Context, userOpenID string, body streamBody) (*SentMessage, error)
}

// msgAnchor 记录某个会话最后一条入站消息，用作被动回复锚点。
type msgAnchor struct {
	msgID string
	at    time.Time
}

// OutboundSender 负责下行消息的路由与 msg_seq 管理。
//
// 并发安全：host 可能同时推多条回复，seq 表与锚点表都在锁内更新。
type OutboundSender struct {
	// Markdown 为 true 时用 msg_type=2 发送（客户端渲染 Markdown）。
	Markdown bool
	// Stream 为 true 时，**私聊**回复用 stream_messages 分片发送（边生成边显示）。
	// 群聊没有流式接口，仍走普通 messages。
	Stream bool
	api    MessageAPI
	now    func() time.Time
	mu     sync.Mutex
	seq    map[string]uint32
	last   map[string]msgAnchor
	// replies 记录每个锚点已用掉的被动回复次数（配额自检用）。
	replies map[string]uint32
	// logErr 输出 ERROR 级诊断（配额耗尽等必须留痕）。
	logErr func(format string, args ...any)
}

// NewOutboundSender 构造发送器。
func NewOutboundSender(api MessageAPI) *OutboundSender {
	return &OutboundSender{
		api:     api,
		now:     time.Now,
		seq:     make(map[string]uint32),
		last:    make(map[string]msgAnchor),
		replies: make(map[string]uint32),
		logErr:  func(string, ...any) {},
	}
}

// RememberInbound 记录会话最后一条入站消息 id，供随后的被动回复使用。
func (s *OutboundSender) RememberInbound(chatID, msgID string) {
	if chatID == "" || msgID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[chatID] = msgAnchor{msgID: msgID, at: s.now()}
}

// Send 把 content 发到 chatID。
//
// 长文本按 maxMessageRunes 分片，每片一次 API 调用、msg_seq 递增。
// 返回最后一片的响应（前面分片的 id 只用于日志）。
func (s *OutboundSender) Send(ctx context.Context, chatID, content string) (*SentMessage, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("qq send: empty content for chat %q", chatID)
	}

	groupOpenID, userOpenID, err := parseChatID(chatID)
	if err != nil {
		return nil, err
	}

	msgID := s.passiveMsgID(chatID)
	chunks := splitRunes(content, maxMessageRunes)

	// ── 配额自检（约束 1）──
	//
	// 每个锚点（msg_id）的被动回复条数有硬上限：单聊 4、群聊 5。
	// 超了平台会回 40034128（被动回复时间或次数超限），消息直接丢 ——
	// 所以我们提前拒发并记 ERROR，绝不把错误留给平台去发现。
	if msgID != "" {
		limit := passiveReplyLimit(chatID)
		used := s.repliesUsed(chatID, msgID)
		if used >= limit {
			s.logErr("ERROR: passive reply quota exhausted: chat=%s msg_id=%s used=%d limit=%d — refusing to send %d chars",
				chatID, msgID, used, limit, len([]rune(content)))
			return nil, fmt.Errorf("qq send: passive reply quota exhausted for msg_id=%s (%d/%d), refused",
				msgID, used, limit)
		}
		if avail := int(limit - used); len(chunks) > avail {
			s.logErr("ERROR: passive reply quota would be exceeded by chunking: chat=%s msg_id=%s used=%d limit=%d chunks=%d — sending only %d chunk(s)",
				chatID, msgID, used, limit, len(chunks), avail)
			chunks = chunks[:avail]
		}
	}

	var last *SentMessage

	// ── 私聊流式：把整段回复按片推给 stream_messages ──
	//
	// 文档：《发送单聊流式消息》POST /v2/users/{openid}/stream_messages
	//   input_state 1=生成中，10=结束；index 从 0 递增；
	//   stream_msg_id 首片由服务端返回，后续分片必须携带；msg_seq 去重。
	// 群聊没有流式接口，仍然逐条 messages 发送。
	if s.Stream && userOpenID != "" && groupOpenID == "" {
		var streamID string
		for i, chunk := range chunks {
			body := streamBody{
				InputMode:   "replace",
				InputStream: 1,
				Index:       i,
				ContentType: streamContentType(s.Markdown),
				ContentRaw:  chunk,
				MsgID:       msgID,
				MsgSeq:      s.nextSeq(chatID, msgID),
				StreamMsgID: streamID,
			}
			if i == len(chunks)-1 {
				body.InputStream = 10 // 生成结束
			}
			sent, err := s.api.StreamC2C(ctx, userOpenID, body)
			if err != nil {
				return last, err
			}
			last = sent
			if streamID == "" && sent != nil {
				streamID = sent.ID
			}
		}
		if msgID != "" {
			s.countReply(chatID, msgID)
		}
		return last, nil
	}

	for _, chunk := range chunks {
		seq := s.nextSeq(chatID, msgID)
		// Markdown 渲染（默认开启）：agent 的输出基本都是 Markdown，
		// 用 msg_type=2 让 QQ 客户端按 Markdown 渲染（标题/列表/代码块/表格）。
		// 官方硬约束：传 markdown 时 content 必须为空 —— sendMarkdown 已保证。
		if s.Markdown {
			if groupOpenID != "" {
				last, err = s.api.SendGroupMarkdown(ctx, groupOpenID, chunk, msgID, seq)
			} else {
				last, err = s.api.SendC2CMarkdown(ctx, userOpenID, chunk, msgID, seq)
			}
		} else if groupOpenID != "" {
			last, err = s.api.SendGroup(ctx, groupOpenID, chunk, msgID, seq)
		} else {
			last, err = s.api.SendC2C(ctx, userOpenID, chunk, msgID, seq)
		}
		if err != nil {
			return last, err
		}
		if msgID != "" {
			s.countReply(chatID, msgID)
		}
	}
	return last, nil
}

// repliesUsed 返回该锚点已用掉的被动回复次数。
func (s *OutboundSender) repliesUsed(chatID, msgID string) uint32 {
	key := chatID + "|" + msgID
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replies[key]
}

// countReply 记一次成功的被动回复（配额自检用）。
func (s *OutboundSender) countReply(chatID, msgID string) {
	key := chatID + "|" + msgID
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[key]++
}

// passiveMsgID 返回当前可用的被动回复锚点；超出该会话的有效期或没有锚点时返回 ""。
//
// 有效期按场景区分（单聊 60 分钟 / 群聊 5 分钟），见文件头文档依据。
func (s *OutboundSender) passiveMsgID(chatID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.last[chatID]
	if !ok || a.msgID == "" {
		return ""
	}
	if s.now().Sub(a.at) > passiveReplyWindow(chatID) {
		return ""
	}
	return a.msgID
}

// nextSeq 返回这条回复的 msg_seq（从 1 开始，按 msg_id 分组递增）。
//
// 文档依据：《消息收发概述》§消息去重；《发送单聊消息》msg_seq 字段说明
// ——「与 msg_id 联合使用，避免相同消息 id 回复重复发送，不填默认是 1。
// 相同的 msg_id + msg_seq 重复发送会失败。」
//
// 分片之间、多轮回复之间都共用同一个计数：官方按 (msg_id, msg_seq) 去重，
// 序号必须严格递增，不能重复。
func (s *OutboundSender) nextSeq(chatID, msgID string) uint32 {
	key := chatID + "|" + msgID
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq[key]++
	return s.seq[key]
}

// parseChatID 把 host 的会话标识拆成 (group_openid, user_openid)。
//
// 群聊用 "group:<群 openid>"（与 inbound 的 GroupChatID 同构）；
// 其余一律当私聊 openid。空值或残缺值直接报错 —— 宁可发送失败留日志，
// 也不能把消息发到一个猜测的地址上。
func parseChatID(chatID string) (groupOpenID, userOpenID string, err error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "", "", fmt.Errorf("qq send: empty chat_id")
	}
	if strings.HasPrefix(chatID, "group:") {
		g := strings.TrimPrefix(chatID, "group:")
		if g == "" {
			return "", "", fmt.Errorf("qq send: malformed group chat_id %q", chatID)
		}
		return g, "", nil
	}
	return "", chatID, nil
}

// splitRunes 按 rune 上限切分文本（保留原有换行，不插入额外分隔符）。
func splitRunes(s string, limit int) []string {
	if limit <= 0 {
		return []string{s}
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return []string{s}
	}
	var out []string
	for len(runes) > 0 {
		n := limit
		if n > len(runes) {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

// streamContentType 映射 content_type（text / markdown）。
func streamContentType(markdown bool) string {
	if markdown {
		return "markdown"
	}
	return "text"
}
