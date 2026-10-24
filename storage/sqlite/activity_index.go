package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// activity_index.go — 跨会话「活动流」索引（通信视图性能重构）。
//
// 问题（已量化）：会话按 chat 分库（one session one DB，v71/v72）后，跨会话看
// 消息必须逐库打开 —— 40 个会话 = 40 次 open + 8000 行，会话数一涨就线性恶化，
// 调参无解。activity_index 在主库冗余一份「结构化消息的可展示摘要」，视图查询
// 退化为一次主库索引扫描，代价与会话数**无关**。
//
// 边界（铁律）：
//   - 会话库仍是消息原文的唯一权威；本表只存摘要，读不到原文这件事永远由会话库
//     负责（这里不能成为第二个消息源）。
//   - 只索引结构化 kind（report/ask_user/answer/decision/command/event）。普通
//     text 不入索引 —— 否则索引会被聊天文本撑成第二个消息库。
//   - created_at 是**固定宽度 UTC 文本**，见 activityTimeLayout。

const (
	// ActivitySummaryMaxRunes 是 summary 列的截断长度（按 rune，不按 byte ——
	// 中文摘要按 byte 截断会切出半个字）。
	ActivitySummaryMaxRunes = 200
	// ActivityLimitDefault 是 /api/activity 的默认条数。
	ActivityLimitDefault = 200
	// ActivityLimitMax 是 /api/activity 的条数上限（挡住 ?limit=999999 这种）。
	ActivityLimitMax = 500
	// ActivityWindowDefault 是默认活跃窗口（72h）：更老的记录不参与活动流。
	ActivityWindowDefault = 72 * time.Hour
)

// activityTimeLayout 是 activity_index.created_at 的唯一合法形态。
//
// 固定宽度 UTC（纳秒补齐 9 位、后缀恒为 Z）⇒ TEXT 的字符串序 == 时间序，
// ORDER BY created_at DESC 可以直走 idx_activity_created。
// ⛔ 不要改用 canonicalTimestamp（本地偏移，DST 处字符串序与时间序相反），
// 也不要用 time.RFC3339Nano（纳秒为 0 时省略小数位 ⇒ 宽度不定，"…07Z" 会排在
// "…07.5Z" 之后，时间序反了）。
const activityTimeLayout = "2006-01-02T15:04:05.000000000Z"

// FormatActivityTime 把时间规范成 activity_index.created_at 的落库形态（UTC）。
func FormatActivityTime(t time.Time) string { return t.UTC().Format(activityTimeLayout) }

// ParseActivityTime 解析 activity_index.created_at（宽容：也接受 RFC3339）。
func ParseActivityTime(s string) (time.Time, error) {
	for _, layout := range []string{activityTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid activity timestamp %q", s)
}

// ActivityIndexedKinds 是允许进入活动流索引的 kind 集合。
//
// text 被**刻意排除**：聊天正文量大且无视图价值，进索引只会把索引撑大。
var ActivityIndexedKinds = map[string]bool{
	StructuredKindReport:   true,
	StructuredKindAskUser:  true,
	StructuredKindAnswer:   true,
	StructuredKindDecision: true,
	StructuredKindCommand:  true,
	StructuredKindEvent:    true,
}

// ActivityKindIndexed 报告某个 kind 是否应该写入活动流索引。
func ActivityKindIndexed(kind string) bool { return ActivityIndexedKinds[kind] }

// TruncateActivitySummary 按 rune 截断摘要（不切断多字节字符）。
func TruncateActivitySummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= ActivitySummaryMaxRunes {
		return s
	}
	return string(runes[:ActivitySummaryMaxRunes])
}

// ActivityItem 是活动流里的一行（对外 JSON 即 /api/activity 的 items 元素）。
type ActivityItem struct {
	TenantID   int64  `json:"-"`
	ChatID     string `json:"chat_id"`
	Channel    string `json:"channel"`
	MessageID  int64  `json:"message_id"`
	Kind       string `json:"kind"`
	SenderID   string `json:"-"`
	SenderName string `json:"sender_name"`
	Summary    string `json:"summary"`
	// CreatedAt 是固定宽度 UTC 文本（activityTimeLayout）。
	CreatedAt string `json:"created_at"`
}

// ActivityIndex 是 activity_index 的唯一读写入口（只绑主库连接）。
//
// nil 安全：PG 后端没有这张表，调用方直接持有 nil 时 Insert 静默跳过、
// List 返回空 —— 视图退化为「没有活动流」，而不是报 500。
type ActivityIndex struct {
	db *sql.DB
}

// NewActivityIndex 绑定主库连接（须是主库 *sqlite.DB 的 Conn()，绝不是会话库）。
func NewActivityIndex(db *sql.DB) *ActivityIndex {
	if db == nil {
		return nil
	}
	return &ActivityIndex{db: db}
}

// Insert 幂等写入一行索引（UNIQUE(tenant_id, chat_id, message_id) ⇒ INSERT OR IGNORE）。
//
// 调用时序铁律：必须在**原文落库成功之后**调用（索引可以有短暂延迟，但绝不允许
// 「索引里有、原文没有」—— 前端点进去会 404）。因此调用方出错时**不回滚原消息**，
// 只记日志：索引是派生物，丢一行只是少一条活动，丢原文是不可恢复的数据损失。
func (a *ActivityIndex) Insert(rec ActivityItem) error {
	if a == nil || a.db == nil {
		return nil
	}
	if !ActivityKindIndexed(rec.Kind) {
		return nil
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = FormatActivityTime(time.Now())
	}
	_, err := a.db.Exec(`
		INSERT OR IGNORE INTO activity_index
		(tenant_id, chat_id, channel, message_id, kind, sender_id, sender_name, summary, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.TenantID, rec.ChatID, rec.Channel, rec.MessageID, rec.Kind,
		rec.SenderID, rec.SenderName, TruncateActivitySummary(rec.Summary), rec.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert activity index %s/%d: %w", rec.ChatID, rec.MessageID, err)
	}
	return nil
}

// NormalizeActivityLimit 归一 limit：<=0 → 默认 200，>上限 → 上限 500。
func NormalizeActivityLimit(limit int) int {
	if limit <= 0 {
		return ActivityLimitDefault
	}
	if limit > ActivityLimitMax {
		return ActivityLimitMax
	}
	return limit
}

// NormalizeActivityWindow 归一活跃窗口：<=0 → 默认 72h。
func NormalizeActivityWindow(d time.Duration) time.Duration {
	if d <= 0 {
		return ActivityWindowDefault
	}
	return d
}

// List 返回最近的活动流（新→旧），并在**主库**上完成时间窗 + kind 过滤。
//
// 只打主库：查询路径不涉及任何会话分库（这是本重构的验收点之一）。返回的
// has_more 通过多取一行的方式判定（不做 COUNT，避免全表扫）。
func (a *ActivityIndex) List(limit int, within time.Duration, kinds []string) ([]ActivityItem, bool, error) {
	if a == nil || a.db == nil {
		return nil, false, nil
	}
	limit = NormalizeActivityLimit(limit)
	within = NormalizeActivityWindow(within)
	cutoff := FormatActivityTime(time.Now().Add(-within))

	var sb strings.Builder
	sb.WriteString(`
		SELECT tenant_id, chat_id, channel, message_id, kind, sender_id, sender_name, summary, created_at
		  FROM activity_index
		 WHERE created_at >= ?`)
	args := []any{cutoff}
	if len(kinds) > 0 {
		sb.WriteString(" AND kind IN (?")
		sb.WriteString(strings.Repeat(",?", len(kinds)-1))
		sb.WriteString(")")
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	// id DESC 只做同刻并列时的确定性 tie-break，不改变「按时间倒序」的语义。
	sb.WriteString(" ORDER BY created_at DESC, id DESC LIMIT ?")
	args = append(args, limit+1)

	rows, err := a.db.Query(sb.String(), args...)
	if err != nil {
		return nil, false, fmt.Errorf("list activity index: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ActivityItem, 0, limit)
	for rows.Next() {
		var it ActivityItem
		if err := rows.Scan(&it.TenantID, &it.ChatID, &it.Channel, &it.MessageID, &it.Kind,
			&it.SenderID, &it.SenderName, &it.Summary, &it.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("scan activity row: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate activity rows: %w", err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// Count 返回索引总行数（运维/测试用，不参与视图查询）。
func (a *ActivityIndex) Count() (int64, error) {
	if a == nil || a.db == nil {
		return 0, nil
	}
	var n int64
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM activity_index`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count activity index: %w", err)
	}
	return n, nil
}
