package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// TenantSession represents a single tenant's conversation session
type TenantSession struct {
	tenantID   int64
	channel    string
	chatID     string
	sessionSvc *sqlite.SessionService // 会话库（v71：session_messages/iteration_history —— 每会话独立 DB）
	tenantSvc  *sqlite.TenantService  // 主库（v71：tenants 注册表 —— CWD/preview 等主库表操作）
	memorySvc  *sqlite.MemoryService  // for consolidation state (LastConsolidated) —— 主库（tenant_state）
	memory     memory.MemoryProvider
	mcpManager *tools.SessionMCPManager // 会话 MCP 管理器
	lastActive time.Time                // 会话活跃时间
	mu         sync.RWMutex             // 保护 lastActive 和 cwd
	cwd        string                   // 当前工作目录（PWD 工具优化）
}

// SessionService 返回绑定该会话**会话库**的 SessionService（消息/迭代数据的
// 读写入口）。v71 每会话一个 DB：直接构造点（agent/serverapp 里只有 tenantID
// 的路径）经 MultiTenantSession.SessionServiceFor 收口；持有 TenantSession 的
// 调用方用本访问器 —— 绝不再 sqlite.NewSessionService(主库)（那会读写主库的
// 旧 session_messages，拆库后是错误目标）。
func (s *TenantSession) SessionService() *sqlite.SessionService {
	return s.sessionSvc
}

// AddMessage adds a message to this tenant's session
func (s *TenantSession) AddMessage(msg llm.ChatMessage) error {
	return s.sessionSvc.AddMessage(s.tenantID, msg)
}

// AddMessageWithID adds a message and returns the DB auto-increment id.
func (s *TenantSession) AddMessageWithID(msg llm.ChatMessage) (int64, error) {
	return s.sessionSvc.AddMessageWithID(s.tenantID, msg)
}

// AppendIterationHistory writes a structured iteration record (v54+).
// Replaces Detail JSON as the authoritative source for iteration data.
func (s *TenantSession) AppendIterationHistory(msgID int64, turnID uint64, rec sqlite.IterationRecord) error {
	return s.sessionSvc.AppendIterationHistory(s.tenantID, msgID, turnID, rec)
}

// AppendIterationTool 把一个工具快照（JSON 对象）追加进【已落盘】的
// (turnID, iteration) 迭代记录的 tools JSON 数组尾部（见
// SessionService.AppendIterationTool 的完整契约）。found=false 表示该迭代
// 记录尚未写库（迭代中途注入——工具由后续快照正常写入），调用方静默跳过。
func (s *TenantSession) AppendIterationTool(turnID uint64, iteration int, toolJSON string) (bool, error) {
	return s.sessionSvc.AppendIterationTool(s.tenantID, turnID, iteration, toolJSON)
}

// GetIterationHistoryByTurns 批量查询多个 turn 的迭代（一次 IN 查询）。
// LLM 上下文构建用：assistant 消息不写 content（msg 是 iter 组成的集合，
// content 是历史遗留字段），回复文本从迭代取 —— 迭代 content 是权威数据源，
// 没有才 fallback 到 msg.content。
func (s *TenantSession) GetIterationHistoryByTurns(turnIDs []uint64) (map[uint64][]sqlite.IterationRecord, error) {
	return s.sessionSvc.GetIterationHistoryByTurns(s.tenantID, turnIDs)
}

// GetAllIterationHistory returns ALL iteration records for this tenant, ordered
// by (turn_id, iteration). Used by session fork to copy per-iteration render
// detail (content/reasoning/tools per iteration) into the fork target session.
func (s *TenantSession) GetAllIterationHistory() ([]sqlite.IterationRecord, error) {
	return s.sessionSvc.GetAllIterationHistory(s.tenantID)
}

// GetUsageStats aggregates this session's usage & performance from
// iteration_history (v59: per-iteration input/cached tokens + model).
// recentLimit caps the recent-iteration detail rows (0 = default 20, negative = skip).
func (s *TenantSession) GetUsageStats(recentLimit int) (*sqlite.TenantUsageStats, error) {
	return s.sessionSvc.GetTenantUsageStats(s.tenantID, recentLimit)
}

// AppendMessage appends a message and returns its stable history ID.
// v71（每会话一个 DB）：追加落会话库；eligible 消息（user/assistant 非展示）
// 同步更新主库 tenants.preview（跨会话列表读这一列 —— 拆库后主库没有消息数据）。
func (s *TenantSession) AppendMessage(msg llm.ChatMessage) (int64, error) {
	id, err := s.sessionSvc.AppendMessage(s.tenantID, msg)
	if err == nil {
		s.updatePreviewForMessage(msg)
	}
	return id, err
}

// AppendCommandRow 落库一行命令行（`!cmd` / slash 的输入或输出）。
//
// 语义 = **只给 UI 展示**（display_only=1 + record_type='command'），永不进 LLM 上下文；
// 目的是让命令行在**页面刷新后仍在**（刷新 = 从 DB 重建渲染状态，用户报告
// 2026-09-21：「为什么 !cmd 消息的输入输出在页面刷新之后就消失了？」）。
func (s *TenantSession) AppendCommandRow(role, content string) (int64, error) {
	return s.sessionSvc.AppendCommandMessage(s.tenantID, role, content)
}

// AppendMessages atomically appends a related message batch.
// v71（每会话一个 DB）：追加落会话库；批内最后一条 eligible 消息（user/assistant
// 非展示，id 单调递增 ⇒ 它就是最新一条）同步更新主库 tenants.preview。
func (s *TenantSession) AppendMessages(messages []llm.ChatMessage) ([]int64, error) {
	ids, err := s.sessionSvc.AppendMessages(s.tenantID, messages)
	if err == nil {
		for i := len(messages) - 1; i >= 0; i-- {
			if (messages[i].Role == "user" || messages[i].Role == "assistant") && !messages[i].DisplayOnly {
				s.updatePreviewForMessage(messages[i])
				break
			}
		}
	}
	return ids, err
}

// AppendMessagesAndAskQuestion atomically appends an AskUser tool exchange and
// the control record that makes the question pending across restarts.
func (s *TenantSession) AppendMessagesAndAskQuestion(messages []llm.ChatMessage, metadata map[string]string) ([]int64, int64, error) {
	return s.sessionSvc.AppendMessagesAndAskQuestion(s.tenantID, messages, metadata)
}

func (s *TenantSession) AppendControl(recordType sqlite.HistoryRecordType, targetHistoryID int64, data any) (int64, error) {
	return s.sessionSvc.AppendControl(s.tenantID, recordType, targetHistoryID, data)
}

func (s *TenantSession) AppendContextSnapshot(recordType sqlite.HistoryRecordType, messages []llm.ChatMessage) (int64, error) {
	return s.sessionSvc.AppendContextSnapshot(s.tenantID, recordType, messages)
}

func (s *TenantSession) AppendAskQuestion(metadata map[string]string) (int64, error) {
	return s.sessionSvc.AppendAskQuestion(s.tenantID, metadata)
}

func (s *TenantSession) AppendAskAnswer(answer string) (int64, error) {
	return s.sessionSvc.AppendAskAnswer(s.tenantID, answer)
}

// AppendAskAnswerWithUserMessage atomically appends the ask_answer control record
// AND the answer user message in one transaction (crash consistency).
// v71（每会话一个 DB）：追加落会话库；answerMsg（user 消息）同步更新主库
// tenants.preview。
func (s *TenantSession) AppendAskAnswerWithUserMessage(answer string, answerMsg llm.ChatMessage) (int64, error) {
	id, err := s.sessionSvc.AppendAskAnswerWithUserMessage(s.tenantID, answer, answerMsg)
	if err == nil {
		s.updatePreviewForMessage(answerMsg)
	}
	return id, err
}

func (s *TenantSession) AppendMasks(mutations []sqlite.MaskMutation) error {
	return s.sessionSvc.AppendMasks(s.tenantID, mutations)
}

func (s *TenantSession) Replay() (*sqlite.ReplayResult, error) {
	return s.sessionSvc.Replay(s.tenantID)
}

func (s *TenantSession) GetFullHistory() ([]sqlite.HistoryRecord, error) {
	return s.sessionSvc.GetFullHistory(s.tenantID)
}

// RewindToHistoryID 截断会话历史到指定用户消息节点。
// v71（每会话一个 DB）：截断（session_messages/iteration_history）在会话库；
// tenant_state（主库）的 token 水位恢复由本方法完成（memorySvc 绑定主库）——
// 跨库无法原子，水位是派生缓存（下一条用户消息的 SaveContextTokens 自愈），
// 失败只 Warn 不阻塞 rewind。preview（主库 tenants.preview）同步重算（截断后
// 最新一条 user/assistant 消息变化）。
func (s *TenantSession) RewindToHistoryID(historyID int64) (llm.ChatMessage, int, error) {
	target, turnIdx, promptTokens, err := s.sessionSvc.RewindToHistoryID(s.tenantID, historyID)
	if err != nil {
		return llm.ChatMessage{}, 0, err
	}
	// tenant_state 恢复（主库，尽力而为）：截断后剩余历史的最后一条用户消息的
	// context_tokens。失败只 Warn —— 水位是派生缓存，不阻塞 rewind。
	if err := s.memorySvc.SetTokenState(context.Background(), s.tenantID, promptTokens, 0); err != nil {
		log.WithError(err).WithField("tenant_id", s.tenantID).
			Warn("rewind: restore tenant_state token watermark failed (derived cache; self-heals on next message)")
	}
	// preview 重算（主库 tenants.preview）：截断后最新一条 user/assistant 消息变化。
	s.recomputePreview()
	return target, turnIdx, nil
}

// recomputePreview 从会话库重算 preview（最新一条 user/assistant 非展示消息，
// substr 256 —— 与 ListUserChats 旧子查询同语义）写主库 tenants.preview。
// 用于 rewind（截断后最新消息变化）与 clear（清空）。append 路径不走这里 ——
// 追加的消息本身就是最新 eligible（id 单调递增），直接用消息内容更新（见
// updatePreviewForMessage）。
func (s *TenantSession) recomputePreview() {
	if s.tenantSvc == nil {
		return
	}
	preview, err := s.sessionSvc.LatestPreview(s.tenantID)
	if err != nil {
		log.WithError(err).WithField("tenant_id", s.tenantID).Warn("recompute preview: query latest message failed")
		return
	}
	if err := s.tenantSvc.SetTenantPreview(s.tenantID, preview); err != nil {
		log.WithError(err).WithField("tenant_id", s.tenantID).Warn("recompute preview: update tenants.preview failed")
	}
}

// updatePreviewForMessage 在 eligible 消息追加后更新主库 tenants.preview。
// eligible = role IN (user, assistant) 且非 display_only —— 与 ListUserChats 旧
// 子查询的过滤条件完全一致（追加的 eligible 消息就是最新一条：id 单调递增）。
// substr(?, 1, 256) 在 SQL 里截断（与旧子查询的 substr(sm.content, 1, 256) 同界）。
// 失败只 Warn：preview 是展示性冗余（跨会话列表读主库这一列），不阻塞消息追加。
func (s *TenantSession) updatePreviewForMessage(msg llm.ChatMessage) {
	if s.tenantSvc == nil {
		return
	}
	if msg.Role != "user" && msg.Role != "assistant" {
		return
	}
	if msg.DisplayOnly {
		return
	}
	if err := s.tenantSvc.SetTenantPreview(s.tenantID, msg.Content); err != nil {
		log.WithError(err).WithField("tenant_id", s.tenantID).Warn("update tenants.preview failed (display-only redundancy)")
	}
}

// ReplaceToolMessage updates the most recent matching tool-role message.
// Empty toolName/toolCallID act as wildcards (match any).
func (s *TenantSession) ReplaceToolMessage(toolName, toolCallID, content string) error {
	return s.sessionSvc.ReplaceToolMessage(s.tenantID, toolName, toolCallID, content)
}

// GetHistory retrieves recent messages for LLM context window
func (s *TenantSession) GetHistory(maxMessages int) ([]llm.ChatMessage, error) {
	return s.sessionSvc.GetHistory(s.tenantID, maxMessages)
}

// GetHistoryBefore returns up to maxMessages raw history messages before
// beforeID.
func (s *TenantSession) GetHistoryBefore(beforeID int64, maxMessages int) ([]llm.ChatMessage, error) {
	return s.sessionSvc.GetHistoryBefore(s.tenantID, beforeID, maxMessages)
}

// GetHistoryBeforeForDisplay returns up to maxMessages messages (including
// pre-compression) before beforeID, plus the total display message count.
// Used by the web frontend's history display — shows ALL messages from the
// append-only session_messages table, not just the Replay() summary.
func (s *TenantSession) GetHistoryBeforeForDisplay(beforeID int64, maxMessages int) ([]llm.ChatMessage, int, error) {
	return s.sessionSvc.GetHistoryBeforeForDisplay(s.tenantID, beforeID, maxMessages)
}

// GetMessages retrieves all messages for this tenant
func (s *TenantSession) GetMessages() ([]llm.ChatMessage, error) {
	return s.sessionSvc.GetAllMessages(s.tenantID)
}

// Len returns the number of messages in this tenant's session
func (s *TenantSession) Len() (int, error) {
	return s.sessionSvc.GetMessagesCount(s.tenantID)
}

// UserMessageCount returns the number of user-role messages (conversation turns).
func (s *TenantSession) UserMessageCount() (int, error) {
	return s.sessionSvc.GetUserMessageCount(s.tenantID)
}

// LastConsolidated returns the last consolidated message index
func (s *TenantSession) LastConsolidated() int {
	lastConsolidated, err := s.memorySvc.GetState(context.Background(), s.tenantID)
	if err != nil {
		// If error, return 0 as safe default
		return 0
	}
	return lastConsolidated
}

// SetLastConsolidated updates the last consolidated message index
func (s *TenantSession) SetLastConsolidated(n int) error {
	return s.memorySvc.SetState(context.Background(), s.tenantID, n)
}

// Clear removes all messages from this tenant's session
// v71（每会话一个 DB）：清空落会话库；preview（主库 tenants.preview）同步清空
// （跨会话列表读这一列 —— 拆库后主库没有消息数据）。
func (s *TenantSession) Clear() error {
	if err := s.sessionSvc.Clear(s.tenantID); err != nil {
		return err
	}
	s.recomputePreview()
	return nil
}

// UpdateMessageContent updates the content of the Nth message (0-indexed) in this tenant's session.
// Used by observation masking to persist masked content back to session.
func (s *TenantSession) UpdateMessageContent(messageIndex int, content string) error {
	return s.sessionSvc.UpdateMessageContent(s.tenantID, messageIndex, content)
}

// UpdateMessageContentNonDisplayOnly updates the content of the Nth non-display-only
// message (0-indexed). Aligns with GetAllMessages() ordering (both filter display_only).
func (s *TenantSession) UpdateMessageContentNonDisplayOnly(messageIndex int, content string) error {
	return s.sessionSvc.UpdateMessageContentNonDisplayOnly(s.tenantID, messageIndex, content)
}

// Memory returns the memory provider for this tenant
func (s *TenantSession) Memory() memory.MemoryProvider {
	return s.memory
}

// TenantID returns the tenant ID
func (s *TenantSession) TenantID() int64 {
	return s.tenantID
}

// SaveContextTokens records the exact API prompt_tokens on the most recent
// user message in this tenant's session.
func (s *TenantSession) SaveContextTokens(promptTokens int64) error {
	return s.sessionSvc.UpdateUserMessageContextTokens(s.tenantID, promptTokens)
}

// GetLastContextTokens returns the context_tokens of the most recent user message.
// Used by rewind to restore accurate token state.
// GetLastContextTokens returns the context_tokens from the most recent
// non-display-only user message, used to restore the token tracker.
func (s *TenantSession) GetLastContextTokens() (int64, error) {
	return s.sessionSvc.GetLastUserMessageContextTokens(s.tenantID)
}

// GetMaxTurnID returns the highest turn_id for this tenant's messages.
// Used to restore the per-session turn ID counter after a server restart.
func (s *TenantSession) GetMaxTurnID() (uint64, error) {
	return s.sessionSvc.GetMaxTurnID(s.tenantID)
}

// GetLastUserMessageContent returns the content of the most recent user
// message in this tenant's session DB (v71+ — messages live per-session, so
// resume flows must read them here, not from the main DB). Returns "" when
// there is no resumable user message.
func (s *TenantSession) GetLastUserMessageContent() (string, error) {
	return s.sessionSvc.GetLastUserMessageContent(s.tenantID)
}

// HasAssistantReplyAfterLastUser reports whether the last user message
// already has a final assistant reply after it — i.e. the turn completed
// and there is nothing to resume.
func (s *TenantSession) HasAssistantReplyAfterLastUser() (bool, error) {
	return s.sessionSvc.HasAssistantReplyAfterLastUser(s.tenantID)
}

// GetLastUserTurnID returns the turn_id of the last non-display-only user
// message. A restart-resumed Run (InjectInboundResume) reuses this turn id so
// the interrupted work and the resumed work belong to ONE turn — the frontend
// renders a single assistant block instead of one per restart.
// Returns 0 when there is no resolvable user turn (no user message / legacy
// rows without turn_id) — the caller falls back to a fresh turn id.
func (s *TenantSession) GetLastUserTurnID() (uint64, error) {
	return s.sessionSvc.GetLastUserTurnID(s.tenantID)
}

// GetMaxIterationForTurn returns the highest iteration number recorded for a
// turn in iteration_history. A restart-resumed Run uses it to CONTINUE the
// interrupted turn's iteration numbering (IterationStart offset) — iteration
// numbers are turn-scoped, so restarting at 1 would collide with the
// interrupted Run's records. Returns 0 when the turn has no records.
func (s *TenantSession) GetMaxIterationForTurn(turnID uint64) (int, error) {
	return s.sessionSvc.GetMaxIterationForTurn(s.tenantID, turnID)
}

// MemoryService returns the underlying SQLite memory service for this tenant.
// Used for tenant-level state operations (token state, consolidation state, etc.)
// that are independent of the memory provider implementation.
func (s *TenantSession) MemoryService() *sqlite.MemoryService {
	return s.memorySvc
}

// Channel returns the channel name
func (s *TenantSession) Channel() string {
	return s.channel
}

// ChatID returns the chat ID
func (s *TenantSession) ChatID() string {
	return s.chatID
}

// String returns a string representation of the tenant
func (s *TenantSession) String() string {
	return fmt.Sprintf("%s:%s (tenant_id=%d)", s.channel, s.chatID, s.tenantID)
}

// GetSessionKey 返回会话唯一标识
func (s *TenantSession) GetSessionKey() string {
	return sessKey(s.channel, s.chatID)
}

// MarkActive 更新会话活跃时间
func (s *TenantSession) MarkActive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActive = time.Now()
}

// SetMCPManager 设置会话 MCP 管理器
func (s *TenantSession) SetMCPManager(mgr *tools.SessionMCPManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mcpManager = mgr
}

// GetMCPManager 获取 MCP 管理器
func (s *TenantSession) GetMCPManager() *tools.SessionMCPManager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mcpManager
}

// LastActive 返回会话最后活跃时间
func (s *TenantSession) LastActive() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastActive
}

// CleanupInactiveMCPs 清理不活跃的 MCP 连接
// 返回会话最后活跃时间（用于判断会话是否需要从缓存中移除）
func (s *TenantSession) CleanupInactiveMCPs() time.Time {
	s.mu.RLock()
	mgr := s.mcpManager
	s.mu.RUnlock()

	if mgr != nil {
		return mgr.UnloadInactiveServers()
	}
	return s.LastActive()
}

// Close 关闭会话资源
func (s *TenantSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mcpManager != nil {
		s.mcpManager.Close()
		s.mcpManager = nil
	}
}

// InvalidateMCP 使会话的 MCP 连接失效，强制下次使用时重新加载
func (s *TenantSession) InvalidateMCP() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mcpManager != nil {
		s.mcpManager.Invalidate()
	}
}

// GetCurrentDir 获取当前工作目录（PWD 工具优化）
func (s *TenantSession) GetCurrentDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cwd
}

// SetCurrentDir 设置当前工作目录（PWD 工具优化），持久化到数据库。
// The tenants.cwd column is the single authoritative store (file-based
// session_cwd is retired — it was unreliable across restarts).
// v71（每会话一个 DB）：tenants 是主库表 —— CWD 经 tenantSvc（主库）写，
// 不再经 sessionSvc（现在绑定会话库，没有 tenants 表）。
func (s *TenantSession) SetCurrentDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cwd = dir
	if s.tenantSvc != nil {
		_ = s.tenantSvc.SetTenantCWD(s.tenantID, dir)
	}
}

// sessionCwdFileName returns a safe filename for the given session.
// Uses SHA256 hash of "channel:chatID" to avoid filesystem-unsafe characters.
// Retained only for legacy file-based CWD (TUI local mode may still use it).
func sessionCwdFileName(channel, chatID string) string {
	h := sha256.Sum256([]byte(sessKey(channel, chatID)))
	return fmt.Sprintf("%x.txt", h[:16])
}

// loadPersistedCWD reads the session's CWD from the database. Legacy file
// based session_cwd entries (pre-v53) are ignored — the DB is authoritative.
// v71（每会话一个 DB）：tenants 是主库表 —— 经 TenantService（主库）读，
// 不再经 SessionService（现在绑定会话库，没有 tenants 表）。
func loadPersistedCWD(tenantSvc *sqlite.TenantService, tenantID int64) string {
	cwd, err := tenantSvc.GetTenantCWD(tenantID)
	if err != nil || cwd == "" {
		return ""
	}
	return cwd
}

// LoadPersistedCWD returns the persisted CWD for a session without creating a
// TenantSession. Used by TUI local mode (file-based) and API surfaces that
// need to inspect idle sessions. Server-side CWD is DB-authoritative; the
// file is a TUI-only legacy view.
func LoadPersistedCWD(channel, chatID string) string {
	cwdFile := filepath.Join(config.XbotHome(), "session_cwd", sessionCwdFileName(channel, chatID))
	data, err := os.ReadFile(cwdFile)
	if err != nil {
		return ""
	}
	return string(data)
}

// DeletePersistedCWD removes the persisted CWD file for a session.
// Must be called when a session is deleted so that a future session with
// the same chatID (e.g. the default workDir-based session) does not inherit
// a stale working directory.
func DeletePersistedCWD(channel, chatID string) {
	cwdFile := filepath.Join(config.XbotHome(), "session_cwd", sessionCwdFileName(channel, chatID))
	_ = os.Remove(cwdFile)
}
