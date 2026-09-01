package feishu

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	log "github.com/sirupsen/logrus"
)

// ─────────────────────────────────────────────────────────────────────────────
// @mention 完整闭环：接收感知 + 群成员上下文 + 发送转换（飞书卡片 at 标签）
//
// 接收：@bot 剥离（原行为）、@所有人 语义保留、@用户 → "@名字"（LLM 可见）
//       + Name→OpenID 记忆（后续回复可 @ 回）。
// 上下文：群聊消息注入成员名单（LLM 知道能 @ 谁，自然输出 @名字）。
// 发送：@名字 → <at id=open_id></at>（飞书卡片 JSON 2.0 markdown 组件原生
//       at 语法 —— 被 @ 的用户收到提及通知）。
// 匹配源 = mention 记忆 ∪ 群成员表（chat_members API，member_id_type=open_id，
// TTL 缓存）；权限不足时自动降级为"仅 mention 记忆"（消息里 @ 过的人仍可 @ 回）。
// ─────────────────────────────────────────────────────────────────────────────

// mentionMember 是可被 @ 的实体（群成员或消息里被 @ 过的用户）。
type mentionMember struct {
	Name   string
	OpenID string
}

// memberSnapshot 是群成员表的 TTL 缓存（成员变动最多延迟一个 TTL 反映）。
type memberSnapshot struct {
	members   []mentionMember
	fetchedAt time.Time
}

// memberTTL 群成员表缓存时长。
const memberTTL = 5 * time.Minute

// maxContextMembers 群聊上下文注入的成员名上限（大群截断 + 溢出计数）。
const maxContextMembers = 30

// mentionRegistry 是 @ 闭环的状态核心（并发安全：消息回调与 Send 并发访问）。
//   - memberCache: chatID → TTL 成员快照（群成员 API，含 negative cache —— API
//     失败也写入空快照，TTL 内不重试，避免每条消息都打失败的 API）
//
// @ 转换策略（2026-09-01 用户决策）：**不做 @名字 自动匹配**（误判 + bug 多），
// LLM 直接输出飞书原生完整格式 `<at id=open_id>名字</at>`——open_id 由成员
// 名单（groupMemberContext 消息级注入，名字=open_id）提供，prompt 只教语法
// （脱敏：不含真实 open_id）。mentionRegistry 因此不再需要 Name→OpenID 记忆。
type mentionRegistry struct {
	mu          sync.Mutex
	memberCache map[string]*memberSnapshot
}

func newMentionRegistry() *mentionRegistry {
	return &mentionRegistry{
		memberCache: make(map[string]*memberSnapshot),
	}
}

// cacheMembers 写入群成员快照（nil = negative cache，TTL 内不重试）。
func (r *mentionRegistry) cacheMembers(chatID string, members []mentionMember) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memberCache[chatID] = &memberSnapshot{members: members, fetchedAt: time.Now()}
}

// snapshot 返回 TTL 内的成员快照（nil = 过期/不存在，需重新拉取）。
func (r *mentionRegistry) snapshot(chatID string) *memberSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.memberCache[chatID]
	if s != nil && time.Since(s.fetchedAt) < memberTTL {
		return s
	}
	return nil
}

// resolveGroupMembers 拉取群成员表（member_id_type=open_id）并写入 TTL 缓存。
// bot 自身被过滤（机器人不需要被 @）。权限不足（未开通群成员读取 scope）时
// 写入 negative cache 并返回 nil —— @ 功能降级为"仅 mention 记忆"。
func (f *FeishuChannel) resolveGroupMembers(chatID string) []mentionMember {
	if f.client == nil || chatID == "" {
		return nil
	}
	if snap := f.mentions.snapshot(chatID); snap != nil {
		return snap.members
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	it, err := f.client.Im.ChatMembers.GetByIterator(ctx, larkim.NewGetChatMembersReqBuilder().
		ChatId(chatID).
		MemberIdType("open_id").
		PageSize(100).
		Build())
	if err != nil {
		log.WithError(err).WithField("chat_id", chatID).Debug("Feishu: chat members iterator failed (@ degrades to mention-memory)")
		f.mentions.cacheMembers(chatID, nil)
		return nil
	}
	f.mu.Lock()
	selfOpenID := f.botOpenID
	f.mu.Unlock()
	var members []mentionMember
	for {
		has, m, err := it.Next()
		if err != nil {
			log.WithError(err).WithField("chat_id", chatID).Debug("Feishu: fetch chat members failed (@ degrades to mention-memory)")
			f.mentions.cacheMembers(chatID, nil)
			return nil
		}
		if !has || m == nil {
			break
		}
		if m.MemberId == nil || m.Name == nil || *m.MemberId == "" || *m.Name == "" {
			continue
		}
		if *m.MemberId == selfOpenID {
			continue // 跳过 bot 自己
		}
		members = append(members, mentionMember{Name: *m.Name, OpenID: *m.MemberId})
	}
	f.mentions.cacheMembers(chatID, members)
	if len(members) > 0 {
		log.WithFields(log.Fields{"chat_id": chatID, "members": len(members)}).Debug("Feishu: group members cached for @ mentions")
	}
	return members
}

// replaceMentionPlaceholders 处理接收消息里的 @mention 占位符（@_user_N）：
//   - @bot       → 剥离（原行为，避免占位符污染 LLM 输入）
//   - @所有人    → 语义保留（"@所有人"文本）
//   - @用户      → "@名字"（LLM 可见谁被提及）+ Name→OpenID 记忆（回复可 @ 回）
//
// 占位符（@_user_N）替换为可读名字后，LLM 在多轮对话中能理解提及语义
// （回复 @ 回时用成员名单里的 open_id 输出完整 at 标签，见 groupMemberContext）。
func (f *FeishuChannel) replaceMentionPlaceholders(msg *larkim.EventMessage, content, chatID, botOpenID string) string {
	if msg == nil || len(msg.Mentions) == 0 {
		return content
	}
	for _, m := range msg.Mentions {
		if m == nil || m.Key == nil || *m.Key == "" {
			continue
		}
		placeholder := *m.Key
		switch {
		case isBotMention(m, botOpenID):
			// 剥离时优先吃掉占位符后的相邻空格（"@bot @张三 你好" →
			// "@张三 你好" 而非 " @张三 你好"），兜底再剥裸占位符。
			content = strings.ReplaceAll(strings.ReplaceAll(content, placeholder+" ", ""), placeholder, "")
		case isAtAllMention(m):
			content = strings.ReplaceAll(content, placeholder, "@所有人")
		default:
			name := ""
			if m.Name != nil {
				name = *m.Name
			}
			if name != "" {
				content = strings.ReplaceAll(content, placeholder, "@"+name)
			} else {
				// 无名 mention（数据异常）→ 剥离占位符保内容干净
				content = strings.ReplaceAll(strings.ReplaceAll(content, placeholder+" ", ""), placeholder, "")
			}
		}
	}
	return content
}

// groupMemberContext 构造群成员上下文（注入群聊消息）——LLM 据此直接输出
// 完整 at 标签（<at id=ou_xxx>名字</at>，飞书卡片 JSON 2.0 markdown 原生语法，
// 被 @ 用户收到提及通知）。
// 成员名单带 open_id（动态注入不脱敏——真实 ID 是 LLM 输出 at 标签的必需
// 数据；git 管理的静态文件不含真实 ID）。TTL miss 时同步拉取（首条群消息
// +~200ms，TTL 内零成本）。空群/拉取失败返回 ""。
// ⚠️ @ 转换策略（2026-09-01 用户决策）：不做 @名字 自动匹配（误判 + bug 多），
// LLM 直接输出完整格式——名单注入 + prompt 教学（prompt/channels/feishu.md）。
func (f *FeishuChannel) groupMemberContext(chatID string) string {
	if f.mentions.snapshot(chatID) == nil {
		f.resolveGroupMembers(chatID)
	}
	s := f.mentions.snapshot(chatID)
	if s == nil || len(s.members) == 0 {
		return ""
	}
	total := len(s.members)
	entries := make([]string, 0, min(maxContextMembers, total))
	for i, m := range s.members {
		if i >= maxContextMembers {
			break
		}
		entries = append(entries, m.Name+"="+m.OpenID)
	}
	suffix := ""
	if total > len(entries) {
		suffix = fmt.Sprintf("（等 %d 人）", total)
	}
	return fmt.Sprintf("群成员（名字=open_id，提及成员输出 <at id=成员open_id>名字</at>）：%s%s",
		strings.Join(entries, "、"), suffix)
}
