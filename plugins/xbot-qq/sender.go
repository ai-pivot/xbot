package main

// 下行发送：把 host 的 channel 回复发到 QQ 官方接口。
//
// 三条官方规则必须遵守，否则消息要么发不出去、要么重复：
//
//  1. 路由看 chat_id：私聊是用户 openid，群聊是 "group:<群 openid>"，
//     分别对应 POST /v2/users/{openid}/messages 与 /v2/groups/{openid}/messages。
//  2. 被动回复要带收到消息的 msg_id（d.id），且必须在 5 分钟内
//     （官方限制）；超出窗口只能发主动消息（不带 msg_id，受配额限制）。
//  3. 同一个 msg_id 的多条回复用 msg_seq 递增去重/排序，从 1 开始。
//     主动消息也要带 msg_seq。
//
// 本文件不碰网络细节（那是 api.go），只负责路由、分片、seq 与窗口。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// maxMessageRunes 是单条消息的分片上限（官方文本上限 2000 字符左右，
// 留余量给前缀/标点；按 rune 切，不会把中文切坏）。
const maxMessageRunes = 1500

// passiveReplyWindow 是被动回复的官方时限。超过它就不能再带 msg_id。
const passiveReplyWindow = 5 * time.Minute

// MessageAPI 是发送侧依赖的最小接口（便于单测断言 URL 与 msg_seq）。
type MessageAPI interface {
	SendC2C(ctx context.Context, userOpenID, content, msgID string, msgSeq uint32) (*SentMessage, error)
	SendGroup(ctx context.Context, groupOpenID, content, msgID string, msgSeq uint32) (*SentMessage, error)
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
	api  MessageAPI
	now  func() time.Time
	mu   sync.Mutex
	seq  map[string]uint32
	last map[string]msgAnchor
}

// NewOutboundSender 构造发送器。
func NewOutboundSender(api MessageAPI) *OutboundSender {
	return &OutboundSender{
		api:  api,
		now:  time.Now,
		seq:  make(map[string]uint32),
		last: make(map[string]msgAnchor),
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

	var last *SentMessage
	for _, chunk := range chunks {
		seq := s.nextSeq(chatID, msgID)
		if groupOpenID != "" {
			last, err = s.api.SendGroup(ctx, groupOpenID, chunk, msgID, seq)
		} else {
			last, err = s.api.SendC2C(ctx, userOpenID, chunk, msgID, seq)
		}
		if err != nil {
			return last, err
		}
	}
	return last, nil
}

// passiveMsgID 返回当前可用的被动回复锚点；超出 5 分钟窗口或没有锚点时返回 ""。
func (s *OutboundSender) passiveMsgID(chatID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.last[chatID]
	if !ok || a.msgID == "" {
		return ""
	}
	if s.now().Sub(a.at) > passiveReplyWindow {
		return ""
	}
	return a.msgID
}

// nextSeq 返回这条回复的 msg_seq（从 1 开始，按 msg_id 分组递增）。
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
