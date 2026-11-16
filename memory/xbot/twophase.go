package xbot

// twophase.go — 记忆写入的二段式（proposal → confirm）+ 校验层 + 去重。
//
// 2026-11-09 从 fix/state-protocol-branch 迁移到 experiment/xbot（v80）。
// 原始设计来自 2026-11 事故：模型反复往记忆里塞垃圾（同一注入事件被记 6 次），
// 而写入路径没有任何闸门。现在：
//   - global scope 写入必须两步：memory_add → PROPOSAL（不落库）→ memory_confirm
//   - 每次写入前过 validateMemoryContent（长度/注入模式/草稿标记/乱码启发式）
//   - 内容级去重（normalize 相等 或 token Jaccard ≥ 0.8）→ DuplicateMemoryError
//
// 与 v80 的适配：v80 的 XbotMemory 没有 db 句柄（读写走 storage.AgentMemoryStore，
// 同时支持 SQLite/PG），所以 proposal 暂存在**进程内全局表**（跨 provider 实例
// 共享，TTL 10 分钟），而不是 memory_proposals 表 —— 避免为 proposal 给两个后端
// 各加一套 schema/接口。proposal 只活 10 分钟，进程重启丢失可接受。

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	agentmem "xbot/storage/agentmem"
)

const (
	// memoryContentMaxRunes 单条记忆正文上限。
	memoryContentMaxRunes = 2000
	// duplicateJaccardThreshold 近似重复的 token 集合重叠阈值。
	duplicateJaccardThreshold = 0.8
	// proposalTTL 未确认 proposal 的存活时间。
	proposalTTL = 10 * time.Minute
)

// injectionPatterns 注入载荷特征串（小写匹配）。记忆正文命中即拒。
var injectionPatterns = []string{
	"ignore all previous instructions",
	"mark task as complete",
	"rm -rf /",
	"select * from",
	"drop table",
	"<script",
}

// draftMarkers 未完成草稿的行首标记（LLM 内心独白被误存）。
var draftMarkers = []string{"wait.", "so i should", "let me count"}

// controlTags 模型输出 / 协议控制标签。
//
// 2026-11-11 实证：#112343 的正文里混进了 "</think><tool_call>"、"importance"、
// 以及 "Smg 曎 max gap" / "mington" 这类被截断的碎片 —— 流式输出被切断后，
// 半截协议标记和乱码一起当成了记忆正文。这类内容一旦落库没有任何价值，还会
// 污染检索（BM25 命中一堆碎片）与注入（占掉注入预算）。
var controlTags = []string{
	"</think",
	"<tool_call",
	"</tool_call",
	"<|im_start|",
	"<|im_end|",
	"<|endoftext|>",
	"<system>",
	"</system>",
	"[inst]",
	"[/inst]",
}

// clampImportance 把 importance 归一到 [0,1]。
//
// 2026-11-11 实证：#112343 的 importance 落库为 24440.0。ListMemories 用
// OrderImportance 排序，于是这一行永久霸占 memory_manage list 的第一位，
// 正常记忆被挤出前 50 条 —— 用户看到的现象就是"记忆坏了/更新不进去"。
// 写入侧必须钳制；非有限值（NaN/Inf）按默认 0.5 处理。
func clampImportance(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0.5
	}
	if v <= 0 {
		// 与既有 "importance == 0 → 0.5" 的默认语义保持一致。
		return 0.5
	}
	if v > 1 {
		return 1
	}
	return v
}

// DuplicateMemoryError 表示候选内容与既有 ACTIVE 记忆重复。
type DuplicateMemoryError struct {
	ExistingID int64
	Reason     string
}

func (e *DuplicateMemoryError) Error() string {
	return fmt.Sprintf("duplicate memory: %s (existing memory ID: %d)", e.Reason, e.ExistingID)
}

// validateMemoryContent 写入闸门（所有路径共用）。
func validateMemoryContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("memory content is empty")
	}
	if n := len([]rune(content)); n > memoryContentMaxRunes {
		return fmt.Errorf("memory content too long: %d runes (max %d)", n, memoryContentMaxRunes)
	}
	lower := strings.ToLower(content)
	for _, pat := range injectionPatterns {
		if strings.Contains(lower, pat) {
			return fmt.Errorf("memory content rejected: contains injection pattern %q", pat)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		tl := strings.ToLower(strings.TrimSpace(line))
		for _, marker := range draftMarkers {
			if strings.HasPrefix(tl, marker) {
				return fmt.Errorf("memory content rejected: line starts with unfinished-draft marker %q", marker)
			}
		}
	}
	for _, r := range content {
		if isCyrillicRune(r) {
			return fmt.Errorf("memory content rejected: contains Cyrillic characters (mojibake heuristic)")
		}
		if isKanaRune(r) {
			return fmt.Errorf("memory content rejected: contains Japanese kana (mojibake heuristic)")
		}
	}
	for _, tag := range controlTags {
		if strings.Contains(lower, tag) {
			return fmt.Errorf("memory content rejected: contains protocol control tag %q (truncated stream)", tag)
		}
	}
	return nil
}

func isCyrillicRune(r rune) bool { return r >= 0x0400 && r <= 0x04FF }

func isKanaRune(r rune) bool {
	return (r >= 0x3040 && r <= 0x309F) || (r >= 0x30A0 && r <= 0x30FF)
}

// normalizeForDedup 只保留字母/数字/CJK —— 大小写、空白、标点全部忽略。
func normalizeForDedup(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if isASCIIWordRune(r) || isCJKRune(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// tokenSetForDedup 切成小写词集合（CJK 连续串保持整块）。
func tokenSetForDedup(s string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, tok := range strings.Fields(strings.ToLower(s)) {
		tok = strings.TrimFunc(tok, func(r rune) bool {
			return !isASCIIWordRune(r) && !isCJKRune(r)
		})
		if tok != "" {
			set[tok] = struct{}{}
		}
	}
	return set
}

func jaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}

// findDuplicateMemory 在用户的 ACTIVE 记忆里找内容级重复。
// 用 ListAgentLongTermMemories（store 接口，SQLite/PG 通用）而不是 BM25：
// BM25 分不清「近乎重复」和「矛盾更新」（"cluster at X" → "moved to Y"），
// 后者必须继续走 supersede 链而不是被拒。
func (m *XbotMemory) findDuplicateMemory(entry LongTermMemory) int64 {
	// IncludeSuperseded: 去重要看**全部**行（含被 supersede 的）——否则一条刚被
	// 更新的旧行查不到，重复内容会再插一遍。Scope 由后端忽略（v80 实现里
	// ListAgentLongTermMemories 只按 user_id 过滤），这里保留字段仅为可读性。
	q := agentmem.Query{UserID: m.scopeArg(), Limit: 500, Scope: "global", IncludeSuperseded: true}
	rows, err := m.store.ListAgentLongTermMemories(context.Background(), q)
	if err != nil {
		return 0
	}
	norm := normalizeForDedup(entry.Content)
	tokens := tokenSetForDedup(entry.Content)
	// 同 source_session 的 session-scope 行也参与（与 BM25 dedup 的可见性一致）
	if entry.SourceSession != "" {
		sq := agentmem.Query{UserID: m.scopeArg(), Limit: 200, Scope: "session", IncludeSuperseded: true}
		if extra, err := m.store.ListAgentLongTermMemories(context.Background(), sq); err == nil {
			rows = append(rows, extra...)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, r := range rows {
		if norm != "" && norm == normalizeForDedup(r.Content) {
			return r.ID
		}
		if jaccardSimilarity(tokens, tokenSetForDedup(r.Content)) >= duplicateJaccardThreshold {
			return r.ID
		}
	}
	return 0
}

// ── proposal 存储（进程内全局，跨 provider 实例共享）──

type memoryProposal struct {
	ID        int64
	Entry     LongTermMemory
	CreatedAt time.Time
}

type proposalTable struct {
	sync.Mutex
	nextID int64
	m      map[int64]memoryProposal
}

var proposalStore = proposalTable{m: make(map[int64]memoryProposal)}

func (s *proposalTable) dropExpired() {
	now := time.Now()
	for id, p := range s.m {
		if now.Sub(p.CreatedAt) > proposalTTL {
			delete(s.m, id)
		}
	}
}

// ProposeMemory 是二段式写入的第一步。
//
// 返回：
//   - proposalID > 0, memoryID == 0：global scope 已暂存，等 memory_confirm
//   - proposalID == 0, memoryID > 0：session scope 直写完成（已过校验+去重）
//   - err（可能是 *DuplicateMemoryError）：被拒
func (m *XbotMemory) ProposeMemory(ctx context.Context, entry LongTermMemory) (proposalID, memoryID int64, err error) {
	if err := validateMemoryContent(entry.Content); err != nil {
		return 0, 0, err
	}
	if dup := m.findDuplicateMemory(entry); dup > 0 {
		return 0, 0, &DuplicateMemoryError{ExistingID: dup, Reason: "identical or near-identical content already stored"}
	}

	scope := entry.Scope
	if scope == "" {
		scope = "global"
	}
	// importance 钳制（第 1 项修复，与二段式无关）：越界值必须在暂存前就归一，
	// 否则 pending proposal 里也带着 24440 这种值。
	entry.Importance = clampImportance(entry.Importance)
	if scope == "session" {
		id, aerr := m.AddMemory(ctx, entry)
		if aerr != nil {
			return 0, 0, aerr
		}
		return 0, id, nil
	}

	// global scope：**必须**两步。第一步只暂存 proposal（TTL 10 分钟），
	// 过期或未确认一律不落库 —— 这是既定设计，不允许为"用起来顺手"改成直写。
	proposalStore.Lock()
	defer proposalStore.Unlock()
	proposalStore.dropExpired()
	proposalStore.nextID++
	id := proposalStore.nextID
	entry.Scope = "global"
	proposalStore.m[id] = memoryProposal{ID: id, Entry: entry, CreatedAt: time.Now()}
	return id, 0, nil
}

// ConfirmProposal 是二段式写入的第二步：取出 proposal → 再查重 → 落库。
func (m *XbotMemory) ConfirmProposal(ctx context.Context, proposalID int64) (int64, error) {
	proposalStore.Lock()
	p, ok := proposalStore.m[proposalID]
	if ok {
		delete(proposalStore.m, proposalID) // 一次性：无论成功失败都不再重复
	}
	proposalStore.dropExpired()
	proposalStore.Unlock()

	if !ok {
		return 0, fmt.Errorf("proposal %d not found or expired (TTL %s)", proposalID, proposalTTL)
	}
	// confirm 阶段重查（proposal 与 confirm 之间可能已有同内容落库）
	if dup := m.findDuplicateMemory(p.Entry); dup > 0 {
		return 0, &DuplicateMemoryError{ExistingID: dup, Reason: "the same fact was persisted between proposal and confirm"}
	}
	if err := validateMemoryContent(p.Entry.Content); err != nil {
		return 0, err
	}
	return m.AddMemory(ctx, p.Entry)
}
