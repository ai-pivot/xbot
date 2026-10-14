package internal

import (
	"fmt"
	"strings"
	"time"
)

// ParseTimestamp parses a timestamp string from SQLite.
//
// The modernc.org/sqlite driver reformats DATETIME values on read, appending a "Z"
// suffix to bare local wall-clock timestamps (e.g. "2006-01-02 15:04:05" →
// "2006-01-02T15:04:05Z"). This "Z" does NOT mean UTC — it's a local wall-clock
// value that the driver reformatted. We detect this case and parse in local location.
//
// Production code stores timestamps in RFC3339 format with explicit offset
// (e.g. "2026-03-05T14:30:00+08:00"), which is parsed correctly by time.Parse.
//
// Supported formats (in priority order):
//  1. RFC3339 with timezone offset (e.g. "2026-03-05T14:30:00+08:00")
//  2. "Z"-suffixed strings treated as local wall-clock (SQLite driver reformatting)
//  3. Legacy bare local format "2006-01-02 15:04:05"
func ParseTimestamp(s string) time.Time {
	// RFC3339 with explicit timezone offset (e.g. "2026-03-05T14:30:00+08:00")
	// These come from production code using time.Now().Format(time.RFC3339).
	if !strings.HasSuffix(s, "Z") {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Local()
		}
	}

	// "Z"-suffixed strings: treated as local wall-clock, not UTC.
	// The SQLite driver appends "Z" when reformatting DATETIME columns, even for
	// local wall-clock values. Parsing with time.Parse would interpret as UTC and
	// .Local() would shift by the timezone offset, producing wrong times.
	if t, err := time.ParseInLocation("2006-01-02T15:04:05Z", s, time.Local); err == nil {
		return t
	}

	// Legacy bare local format (no longer produced but may exist in old rows)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t
	}

	return time.Time{}
}

// ParseIterationTimestamp parses a timestamp string from **iteration_history**
// ONLY. ⛔ Must NOT be used for other tables — see the contract below.
//
// 表级时间表示契约（2026-10-14「压缩分隔条堆一摞」事故的根因）：
// iteration_history.created_at 有**两种且只有两种**合法来源，
//
//  1. Go 显式写入的 canonical 形态：RFC3339 带显式时区（"2026-10-14T17:22:20.6+08:00"，
//     AppendIterationHistory 自 2026-10-14 起显式写这一列）；
//  2. 历史 DDL 默认 `CURRENT_TIMESTAMP` 写出的 **UTC 裸串**（"2026-10-14 09:10:31"；
//     驱动字符串扫描可能改写成 "...T09:10:31Z"）—— 一律按 **UTC** 解析。
//
// ⛔ 裸串按 time.Local 猜 = 8 小时级错位（本表裸串与 session_messages 的老裸串
// 语义**相反**：表级语义由该表唯一的写方决定）：错位后压缩标记"晚于"全部 turn
// 时间线 ⇒ compactionAnchor 退化成「最后一个 turn + 其最大迭代号」⇒ 同窗全部
// 「上下文已压缩」分隔条堆叠（live API 实测 5 条全是 after_iteration=30；
// 用户 DOM 实测 turn-154 后连摞 4 条）。
//
// 解析失败（未知形态）必须报错而非猜测 —— 调用方记 ERROR、按零值处理：
// 下游 buildTurnSpans 跳过该行 / compactionIteration 回落 standalone 标记行
// （信息不丢、绝不静默错位）。
func ParseIterationTimestamp(s string) (time.Time, error) {
	if s == "" {
		// NULL/'' = 「无时间戳」（老数据合法形态，非损坏）—— 零值交给下游回落。
		return time.Time{}, nil
	}
	// ① canonical：RFC3339 带显式时区（+08:00 或 Z —— Z 形态在本表恒为 UTC：
	// 要么是 canonical 的 Z 写入、要么是驱动对 CURRENT_TIMESTAMP 的改写，两者同值）。
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// ② 历史形态：CURRENT_TIMESTAMP 的 UTC 裸串（驱动扫描可能去空格/加 'T'）。
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("iteration_history.created_at 无法识别: %q", s)
}
