package main

// 见过的发送者台账 —— 用户拿到自己 openid 的唯一可行路径。
//
// 背景（用户原话：「你他妈让我填写 openid，我他妈怎么知道什么是 open id」）：
// QQ 官方的 openid 是平台生成的、**用户自己在客户端里看不到**。如果插件只在
// 配置面板里要求填 openid，第一次使用的用户就没有任何办法知道该填什么，
// 白名单就成了一堵没有门的墙。
//
// 对策：每条入站消息（**包括被白名单拒绝的**）都记一条台账，并打一行固定格式
// 日志 `QQ inbound: sender=<openid> name=<昵称> chat=<c2c|group:<group_openid>> handled=<true|false>`
// 用户随后发 `!whoami` 就能拿到自己的 openid。
//
// 存储：$XBOT_HOME/plugins/xbot.qq/data/seen_senders.jsonl（JSONL，一行一个对象）
//   - 按 sender_openid 去重，只留最新一条；
//   - 最多 200 条（超出丢最旧的）；
//   - 先 append 落盘（崩了也不丢最新一条），再做压缩重写保证"去重 + 上限"不变式。
//
// 落盘失败绝不影响消息处理：只记日志，消息照常走。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// seenSendersMax 是台账保留的发送者条数上限（按 openid 去重后）。
	seenSendersMax = 200
	// seenSendersFileName 是台账文件名。
	seenSendersFileName = "seen_senders.jsonl"
	// unknownSenderName 是昵称缺失时的占位（官方 C2C/群事件经常不给昵称）。
	unknownSenderName = "<unknown>"
)

// SeenSender 是一条"见过的发送者"记录。字段名是落盘契约，不要随手改。
type SeenSender struct {
	TS           string `json:"ts"`
	ChatType     string `json:"chat_type"`              // c2c | group
	GroupOpenID  string `json:"group_openid,omitempty"` // 仅群聊
	SenderOpenID string `json:"sender_openid"`
	SenderName   string `json:"sender_name"`
	// Handled 表示这条消息是否被本插件受理（!whoami 的自动应答也算受理；
	// 被白名单静默拒绝的为 false）。它**不表示**消息进了 agent。
	Handled bool `json:"handled"`
}

// SeenSenderStore 是台账的内存索引 + 落盘器（并发安全）。
type SeenSenderStore struct {
	mu      sync.Mutex
	path    string
	now     func() time.Time
	order   []string // 去重后的插入顺序（尾部最新）
	entries map[string]SeenSender
	logf    func(string, ...any)
}

// DefaultSeenSendersPath 返回默认台账路径（$XBOT_HOME 优先，退回 ~/.xbot）。
func DefaultSeenSendersPath() string {
	home := os.Getenv("XBOT_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".xbot")
		}
	}
	return filepath.Join(home, "plugins", "xbot.qq", "data", seenSendersFileName)
}

// NewSeenSenderStore 构造台账并载入已有文件（文件不存在 / 坏行都不报错）。
//
// path 为空时用 DefaultSeenSendersPath()。now 为 nil 时用 time.Now。
func NewSeenSenderStore(path string, logf func(string, ...any)) *SeenSenderStore {
	if path == "" {
		path = DefaultSeenSendersPath()
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &SeenSenderStore{
		path:    path,
		now:     time.Now,
		entries: make(map[string]SeenSender),
		logf:    logf,
	}
	s.load()
	return s
}

// Path 返回台账文件路径（日志与证据用）。
func (s *SeenSenderStore) Path() string { return s.path }

// Len 返回当前去重后的条数。
func (s *SeenSenderStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}

// Lookup 查某个 openid 最近一次的记录。
func (s *SeenSenderStore) Lookup(senderOpenID string) (SeenSender, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[senderOpenID]
	return e, ok
}

// All 返回当前台账快照（旧的在前，新的在后）。
func (s *SeenSenderStore) All() []SeenSender {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SeenSender, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.entries[id])
	}
	return out
}

// Record 记录一条发送者。同一 openid 只保留最新一条；总条数不超过 seenSendersMax。
//
// 顺序：先补默认值 → 更新内存 → append 落盘（崩溃安全）→ 需要时压缩重写。
func (s *SeenSenderStore) Record(e SeenSender) error {
	if strings.TrimSpace(e.SenderOpenID) == "" {
		return fmt.Errorf("seen_senders: empty sender_openid")
	}
	if e.TS == "" {
		e.TS = s.now().UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(e.SenderName) == "" {
		e.SenderName = unknownSenderName
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, existed := s.entries[e.SenderOpenID]
	evicted := s.upsertLocked(e)

	// 1) 追加（append-only 的最低保证：最新一条一定已落盘）
	if err := s.appendLocked(e); err != nil {
		s.logf("seen_senders: append %s failed: %v", s.path, err)
		return err
	}
	// 2) 去重 / 有淘汰时压缩重写，保证文件里也是"按 openid 最新 + ≤200"。
	//    注意：不能用 len(s.order) > seenSendersMax 判断——upsertLocked 已经
	//    在内存里把超出上限的旧条目丢掉了，那时 len 永远 ≤ 上限，文件会无限长。
	if existed || evicted {
		if err := s.rewriteLocked(); err != nil {
			s.logf("seen_senders: compact %s failed: %v", s.path, err)
			return err
		}
	}
	return nil
}

// upsertLocked 更新内存索引（调用方持锁）。
// 返回是否因超出上限淘汰了旧条目（调用方据此决定要不要压缩重写文件）。
func (s *SeenSenderStore) upsertLocked(e SeenSender) (evicted bool) {
	if _, ok := s.entries[e.SenderOpenID]; ok {
		for i, id := range s.order {
			if id == e.SenderOpenID {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
	}
	s.entries[e.SenderOpenID] = e
	s.order = append(s.order, e.SenderOpenID)
	// 超出上限：从最旧的开始丢
	for len(s.order) > seenSendersMax {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
		evicted = true
	}
	return evicted
}

// appendLocked 追加一行 JSON（调用方持锁）。
func (s *SeenSenderStore) appendLocked(e SeenSender) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// rewriteLocked 用内存索引整体重写文件（临时文件 + rename，原子替换）。
func (s *SeenSenderStore) rewriteLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".seen_senders-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后这里是 no-op

	w := bufio.NewWriter(tmp)
	for _, id := range s.order {
		b, err := json.Marshal(s.entries[id])
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// load 从已有文件恢复索引（坏行跳过；文件不存在不算错）。
func (s *SeenSenderStore) load() {
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e SeenSender
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			s.logf("seen_senders: skip malformed line in %s: %v", s.path, err)
			continue
		}
		if e.SenderOpenID == "" {
			continue
		}
		s.upsertLocked(e)
	}
	if err := sc.Err(); err != nil {
		s.logf("seen_senders: read %s failed: %v", s.path, err)
	}
}
