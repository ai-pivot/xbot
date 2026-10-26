package main

// 约束 5（御前令）：入站去重。
//
// 官方原文（《单聊消息事件》/《群@消息事件》页）：
// 「相同 msg_id 可能重复推送，需结合 message_scene.ext 中的 msg_idx 做去重」。
//
// 所以去重键**首选 msg_idx**（同一条消息的唯一序号），msg_idx 缺失时退回
// d.id（msg_id）—— 两种情况都必须能挡住重复推送。键带上会话 id，
// 避免两个会话里恰好同号的 msg_idx 互相吞掉。
//
// 重复消息的处理：直接丢弃（不进命令门禁、不产生 send_inbound、不计入
// 任何"已处理"统计），只记一条 debug 日志。丢弃是幂等的，不会影响
// 第一次处理的结果。

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultDedupTTL 是去重窗口。官方没有给出重复推送的时间跨度，取 10 分钟：
// 远大于被动回复有效期（单聊 60 分钟内的重复推送仍可能发生，但 10 分钟已能
// 覆盖网关重连补发这种主要场景；超窗的重复会被当成新消息，最坏后果是多回一条）。
const defaultDedupTTL = 10 * time.Minute

// defaultDedupMax 是去重表上限，防止长跑进程内存无界增长。
const defaultDedupMax = 8192

// dedupKey 计算一条入站消息的去重键。
func dedupKey(in Inbound) string {
	chat := in.ChatID()
	if in.MsgIdx != 0 {
		return "idx|" + chat + "|" + strconv.Itoa(in.MsgIdx)
	}
	return "id|" + chat + "|" + in.MessageID
}

// inboundDeduper 记住窗口内见过的消息键。
type inboundDeduper struct {
	mu   sync.Mutex
	ttl  time.Duration
	max  int
	seen map[string]time.Time
	now  func() time.Time
}

func newInboundDeduper(ttl time.Duration, max int) *inboundDeduper {
	if ttl <= 0 {
		ttl = defaultDedupTTL
	}
	if max <= 0 {
		max = defaultDedupMax
	}
	return &inboundDeduper{ttl: ttl, max: max, seen: make(map[string]time.Time), now: time.Now}
}

// Duplicate 报告该键是否在窗口内已出现过；首次见到时记录下来并返回 false。
// 返回 true 表示"这是重复推送，应当丢弃"。
func (d *inboundDeduper) Duplicate(key string) bool {
	if d == nil || key == "" {
		return false
	}
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if ts, ok := d.seen[key]; ok && now.Sub(ts) < d.ttl {
		return true
	}
	d.seen[key] = now
	d.pruneLocked(now)
	return false
}

// pruneLocked 清理过期项；仍超上限时按最旧优先淘汰。
func (d *inboundDeduper) pruneLocked(now time.Time) {
	if len(d.seen) <= d.max {
		// 只在表接近上限时才做全表扫描，避免每条消息都 O(n)。
		if len(d.seen) < d.max {
			return
		}
	}
	for k, ts := range d.seen {
		if now.Sub(ts) >= d.ttl {
			delete(d.seen, k)
		}
	}
	for len(d.seen) > d.max {
		var oldestKey string
		var oldest time.Time
		first := true
		for k, ts := range d.seen {
			if first || ts.Before(oldest) {
				oldestKey, oldest, first = k, ts, false
			}
		}
		if oldestKey == "" {
			return
		}
		delete(d.seen, oldestKey)
	}
}

// Len 报告当前记录数（调试与单测用）。
func (d *inboundDeduper) Len() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

// parseSceneExt 把 message_scene.ext 的 "k=v" 字符串数组解析成 map。
//
// 官方格式是 **[]string 的 k=v 串**（例如 "msg_idx=xxx"、"ref_msg_idx=yyy"、
// "auth_token=zzz"），不是对象/映射。没有 '=' 的项忽略。
func parseSceneExt(ext []string) map[string]string {
	if len(ext) == 0 {
		return nil
	}
	out := make(map[string]string, len(ext))
	for _, item := range ext {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		k, v, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sceneExtInt 取 ext 里的整数字段（msg_idx 在官方文档里是序号，但沿途可能
// 被序列化成字符串；两种都接受，解析失败返回 0）。
func sceneExtInt(ext map[string]string, key string) int {
	raw, ok := ext[key]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return n
}
