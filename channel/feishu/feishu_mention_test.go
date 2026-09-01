package feishu

import (
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// ─── mentionRegistry（TTL 快照 + negative cache） ─────────────────────────────

func TestMentionRegistry_SnapshotTTLExpiry(t *testing.T) {
	r := newMentionRegistry()
	r.cacheMembers("chat-1", []mentionMember{{Name: "张三", OpenID: "ou"}})
	if s := r.snapshot("chat-1"); s == nil {
		t.Fatal("fresh snapshot must hit TTL cache")
	}
	// 直接改 fetchedAt 模拟过期
	r.mu.Lock()
	r.memberCache["chat-1"].fetchedAt = r.memberCache["chat-1"].fetchedAt.Add(-memberTTL - 1)
	r.mu.Unlock()
	if s := r.snapshot("chat-1"); s != nil {
		t.Fatal("expired snapshot must return nil (re-fetch required)")
	}
}

func TestMentionRegistry_NegativeCacheTTL(t *testing.T) {
	r := newMentionRegistry()
	r.cacheMembers("chat-1", nil) // API 失败的 negative cache（空成员）
	if s := r.snapshot("chat-1"); s == nil {
		t.Fatal("negative cache (nil members) must still hit TTL — 防止每条消息重打失败 API")
	} else if len(s.members) != 0 {
		t.Fatalf("negative cache members must be empty, got %d", len(s.members))
	}
}

// ─── replaceMentionPlaceholders（接收方向：占位符 → 可读语义） ────────────────

func mention(key, name, openID string) *larkim.MentionEvent {
	m := &larkim.MentionEvent{Key: &key}
	if name != "" {
		m.Name = &name
	}
	if openID != "" {
		m.Id = &larkim.UserId{OpenId: &openID}
	}
	return m
}

func botMention(key, openID string) *larkim.MentionEvent {
	m := &larkim.MentionEvent{Key: &key, Name: strPtr("Bot")}
	if openID != "" {
		m.Id = &larkim.UserId{OpenId: &openID}
	}
	return m
}

func atAllMention(key string) *larkim.MentionEvent {
	// 真实飞书 @所有人 的 Key 是 "@_all"（isAtAllMention 匹配 @all/@_all 或
	// Name "所有人"/"everyone" 等）——测试入参必须用真实形态。
	return &larkim.MentionEvent{Key: &key, Name: strPtr("所有人")}
}

func strPtr(s string) *string { return &s }

func newTestChannel() *FeishuChannel {
	f := NewFeishuChannel(FeishuConfig{}, nil)
	return f
}

func TestReplaceMentionPlaceholders_UserMentionBecomesReadable(t *testing.T) {
	f := newTestChannel()
	msg := &larkim.EventMessage{
		Mentions: []*larkim.MentionEvent{
			botMention("@_user_1", "ou_bot"),
			mention("@_user_2", "张三", "ou_zhang"),
		},
	}
	// 输入：@bot 占位符 + @张三 占位符 + 正文（飞书推送的原文含 @_user_N 占位）
	content := "@_user_1 @_user_2 帮我看看这个报告"
	got := f.replaceMentionPlaceholders(msg, content, "chat-1", "ou_bot")
	want := "@张三 帮我看看这个报告"
	if got != want {
		t.Fatalf("bot stripped (with adjacent space) + user readable:\n got %q\nwant %q", got, want)
	}
}

func TestReplaceMentionPlaceholders_AtAllPreserved(t *testing.T) {
	f := newTestChannel()
	msg := &larkim.EventMessage{
		Mentions: []*larkim.MentionEvent{atAllMention("@_user_1")},
	}
	got := f.replaceMentionPlaceholders(msg, "@_user_1 大家好", "chat-1", "ou_bot")
	if got != "@所有人 大家好" {
		t.Fatalf("at-all must keep semantics, got %q", got)
	}
}

func TestReplaceMentionPlaceholders_NamelessMentionStripped(t *testing.T) {
	f := newTestChannel()
	msg := &larkim.EventMessage{
		Mentions: []*larkim.MentionEvent{mention("@_user_9", "", "")},
	}
	got := f.replaceMentionPlaceholders(msg, "@_user_9 正文", "chat-1", "ou_bot")
	if got != "正文" {
		t.Fatalf("nameless mention must be stripped (with adjacent space), got %q", got)
	}
}

// ─── groupMemberContext（动态注入：名字=open_id 名单 + at 标签用法提示） ─────

func TestGroupMemberContext_InjectsWithOpenIDs(t *testing.T) {
	f := newTestChannel()
	f.mentions.cacheMembers("chat-1", []mentionMember{
		{Name: "张三", OpenID: "ou_zhang"},
		{Name: "李四", OpenID: "ou_li"},
	})
	ctx := f.groupMemberContext("chat-1")
	// 名单带真实 open_id（动态注入不脱敏——LLM 直接复制到 <at id=...>）
	if !strings.Contains(ctx, "张三=ou_zhang") || !strings.Contains(ctx, "李四=ou_li") {
		t.Fatalf("context must carry name=open_id entries: %q", ctx)
	}
	// 用法提示教 at 标签格式
	if !strings.Contains(ctx, "<at id=成员open_id>名字</at>") {
		t.Fatalf("context must teach the full at-tag format: %q", ctx)
	}
}

func TestGroupMemberContext_EmptyGroupReturnsEmpty(t *testing.T) {
	f := newTestChannel()
	// negative cache（空快照，TTL 内）—— groupMemberContext 不再拉 API 直接空串
	f.mentions.cacheMembers("chat-1", nil)
	if ctx := f.groupMemberContext("chat-1"); ctx != "" {
		t.Fatalf("empty group must return empty context, got %q", ctx)
	}
}

func TestGroupMemberContext_TruncatesLargeGroups(t *testing.T) {
	f := newTestChannel()
	members := make([]mentionMember, maxContextMembers+10)
	for i := range members {
		members[i] = mentionMember{Name: "成员" + string(rune('A'+i%26)) + string(rune('a'+i/26)), OpenID: "ou"}
	}
	f.mentions.cacheMembers("chat-1", members)
	ctx := f.groupMemberContext("chat-1")
	if !strings.Contains(ctx, "（等 40 人）") {
		t.Fatalf("large group must carry overflow count, got: %q", ctx)
	}
	if strings.Count(ctx, "=ou") != maxContextMembers {
		t.Fatalf("must inject exactly %d entries, got %d", maxContextMembers, strings.Count(ctx, "=ou"))
	}
}
