package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "xbot/logger"

	"xbot/config"
	"xbot/llm"
	"xbot/memory"
	"xbot/memory/flat"
	"xbot/memory/letta"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
	"xbot/tools"
)

// MultiTenantOption is a functional option for MultiTenantSession
type MultiTenantOption func(*MultiTenantSession)

// WithMCPTimeout sets the MCP inactivity timeout
func WithMCPTimeout(timeout time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.mcpInactivityTimeout = timeout
	}
}

// WithCleanupInterval sets the cleanup scan interval
func WithCleanupInterval(interval time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.mcpCleanupInterval = interval
	}
}

// WithSessionCacheTimeout sets the session cache timeout
func WithSessionCacheTimeout(timeout time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.sessionCacheTimeout = timeout
	}
}

// WithMemoryProvider sets the memory provider ("flat" or "letta")
func WithMemoryProvider(provider string) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.memoryProvider = provider
	}
}

// WithPersonaIsolation enables per-tenant persona isolation (no fallback to tenantID=0).
func WithPersonaIsolation(enabled bool) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.coreSvc.SetPersonaIsolation(enabled)
	}
}

// WithArchivalService sets the vector archival service (used in Letta mode)
// If not set, it will be auto-created in NewMultiTenant from EmbeddingConfig
func WithArchivalService(svc *vectordb.ArchivalService) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.archivalSvc = svc
	}
}

// EmbeddingConfig holds embedding configuration (for auto-creating archival service)
type EmbeddingConfig struct {
	Provider   string // Embedding provider: "openai" (default) or "ollama"
	BaseURL    string
	APIKey     string
	Model      string
	MaxTokens  int     // Maximum tokens for embedding model (default 2048)
	LLMClient  llm.LLM // LLM client for content compression (optional)
	LLMModel   string  // Model name for LLM compression (optional)
	TokenModel string  // Model name for token counting (default "gpt-4")
}

// WithEmbeddingConfig sets embedding config; NewMultiTenant will auto-create chromem-go archival service
func WithEmbeddingConfig(cfg EmbeddingConfig) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.embeddingConfig = &cfg
	}
}

// WithToolIndexService sets the tool index service
func WithToolIndexService(svc *vectordb.ToolIndexService) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.toolIndexSvc = svc
	}
}

// Default timeout constants for session management.
const (
	defaultMCPInactivityTimeout = 30 * time.Minute
	defaultMCPCleanupInterval   = 5 * time.Minute
	defaultSessionCacheTimeout  = 24 * time.Hour
)

// MultiTenantSession manages multiple tenant sessions with SQLite backing
type MultiTenantSession struct {
	db                    *sqlite.DB
	tenantSvc             *sqlite.TenantService
	sessionSvc            *sqlite.SessionService
	memorySvc             *sqlite.MemoryService
	userProfileSvc        *sqlite.UserProfileService
	tokenUsageSvc         *sqlite.UserTokenUsageService
	coreSvc               *sqlite.CoreMemoryService
	archivalSvc           *vectordb.ArchivalService
	toolIndexSvc          *vectordb.ToolIndexService
	recallTimeRangeFn     vectordb.RecallTimeRangeFunc // time-range session history search
	embeddingConfig       *EmbeddingConfig             // for auto-creating archival service
	memoryProvider        string                       // "flat" or "letta"
	mu                    sync.RWMutex
	tenantCache           map[string]*TenantSession // key: "channel:chat_id"
	dbPath                string
	mcpConfigPath         string          // MCP config file path
	mcpInactivityTimeout  time.Duration   // MCP inactivity timeout
	mcpCleanupInterval    time.Duration   // MCP cleanup scan interval
	sessionCacheTimeout   time.Duration   // session cache timeout
	cleanupStopCh         chan struct{}   // cleanup goroutine stop signal
	cleanupWg             sync.WaitGroup  // cleanup goroutine wait group
	cleanupStopOnce       sync.Once       // ensures StopCleanupRoutine runs only once
	shutdownCtx           context.Context // cancelled on StopCleanupRoutine; used as parent for background goroutines
	shutdownCancel        context.CancelFunc
	toolIndexFingerprints map[int64]string          // per-tenant catalog fingerprint (guarded by mu)
	toolIndexPrevNames    map[int64]map[string]bool // per-tenant previous tool name set (guarded by mu)
	onSessionEvict        func(sessionKey string)   // callback when session is evicted (for Registry cleanup of sessionActivated/sessionRound)
}

// NewMultiTenant creates a new multi-tenant session manager
func NewMultiTenant(dbPath string, opts ...MultiTenantOption) (*MultiTenantSession, error) {
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	m := &MultiTenantSession{
		db:                    db,
		tenantSvc:             sqlite.NewTenantService(db),
		sessionSvc:            sqlite.NewSessionService(db),
		memorySvc:             sqlite.NewMemoryService(db),
		userProfileSvc:        sqlite.NewUserProfileService(db),
		tokenUsageSvc:         sqlite.NewUserTokenUsageService(db),
		coreSvc:               sqlite.NewCoreMemoryService(db),
		memoryProvider:        "flat",
		tenantCache:           make(map[string]*TenantSession),
		toolIndexFingerprints: make(map[int64]string),
		toolIndexPrevNames:    make(map[int64]map[string]bool),
		dbPath:                dbPath,
		mcpConfigPath:         "mcp.json", // default in working directory
		mcpInactivityTimeout:  defaultMCPInactivityTimeout,
		mcpCleanupInterval:    defaultMCPCleanupInterval,
		sessionCacheTimeout:   defaultSessionCacheTimeout,
		cleanupStopCh:         make(chan struct{}),
		shutdownCtx:           shutdownCtx,
		shutdownCancel:        shutdownCancel,
	}

	// Apply configuration options
	for _, opt := range opts {
		opt(m)
	}

	// Build shared embedding limit options (used by both archival and tool index)
	var embOpts []vectordb.EmbeddingLimitOption
	if m.embeddingConfig != nil {
		if m.embeddingConfig.MaxTokens > 0 {
			embOpts = append(embOpts, vectordb.WithMaxTokens(m.embeddingConfig.MaxTokens))
		}
		if m.embeddingConfig.TokenModel != "" {
			embOpts = append(embOpts, vectordb.WithTokenModel(m.embeddingConfig.TokenModel))
		}
		if m.embeddingConfig.LLMClient != nil && m.embeddingConfig.LLMModel != "" {
			compressor := vectordb.LLMContentCompressor(m.embeddingConfig.LLMClient, m.embeddingConfig.LLMModel)
			embOpts = append(embOpts, vectordb.WithCompressor(compressor))
		}
	}

	// Letta mode: initialize archival, tool index, and time-range search services.
	if m.memoryProvider == "letta" {
		m.initLettaServices(dbPath, db, embOpts)
	}

	return m, nil
}

// initLettaServices initializes Letta-specific services: archival memory, tool index, and time-range search.
func (m *MultiTenantSession) initLettaServices(dbPath string, db *sqlite.DB, embOpts []vectordb.EmbeddingLimitOption) {
	// Create time-range search function (does not require embedding config)
	m.recallTimeRangeFn = vectordb.NewSQLiteRecallTimeRangeFunc(db.Conn())

	if m.embeddingConfig == nil {
		return
	}

	embFunc := vectordb.NewEmbeddingFunc(
		m.embeddingConfig.BaseURL, m.embeddingConfig.APIKey,
		m.embeddingConfig.Model, m.embeddingConfig.Provider,
		m.embeddingConfig.MaxTokens,
	)

	// Auto-create chromem-go archival service (if not injected via WithArchivalService)
	if m.archivalSvc == nil {
		archivalDir := filepath.Join(filepath.Dir(dbPath), "archival")
		archSvc, err := vectordb.NewArchivalService(archivalDir, embFunc, embOpts...)
		if err != nil {
			log.WithError(err).Error("Failed to initialize archival memory (chromem-go), archival tools will be unavailable")
		} else {
			m.archivalSvc = archSvc
		}
	}

	// Auto-create tool index service (if not injected via WithToolIndexService)
	if m.toolIndexSvc == nil {
		toolIndexDir := filepath.Join(filepath.Dir(dbPath), "tool_index")
		toolIdxSvc, err := vectordb.NewToolIndexService(toolIndexDir, embFunc, embOpts...)
		if err != nil {
			log.WithError(err).Warn("Tool index DB corrupted, removing and recreating")
			if removeErr := os.RemoveAll(toolIndexDir); removeErr != nil {
				log.WithError(removeErr).Error("Failed to remove corrupted tool index directory")
			} else {
				toolIdxSvc, err = vectordb.NewToolIndexService(toolIndexDir, embFunc, embOpts...)
				if err != nil {
					log.WithError(err).Error("Failed to initialize tool index service after recreation, tool search will be unavailable")
				}
			}
		}
		if toolIdxSvc != nil {
			m.toolIndexSvc = toolIdxSvc
		}
	}

}

// NewMultiTenantWithOptions creates a session manager with options (backward compatible alias)
func NewMultiTenantWithOptions(dbPath string, opts ...MultiTenantOption) (*MultiTenantSession, error) {
	return NewMultiTenant(dbPath, opts...)
}

// SetMCPConfigPath sets the MCP config file path
func (m *MultiTenantSession) SetMCPConfigPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mcpConfigPath = path
}

// RecordUserTokenUsage records token usage for a user (upsert).
func (m *MultiTenantSession) RecordUserTokenUsage(senderID, model string, inputTokens, outputTokens, cachedTokens int, conversationCount, llmCallCount int) error {
	db := m.db.Conn()
	if db == nil {
		return fmt.Errorf("database connection is nil (agent may be shutting down)")
	}
	return m.tokenUsageSvc.RecordUsage(db, senderID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount)
}

// GetUserTokenUsage retrieves cumulative token usage for a user.
func (m *MultiTenantSession) GetUserTokenUsage(senderID string) (*sqlite.UserTokenUsage, error) {
	return m.tokenUsageSvc.GetUsage(senderID)
}

// GetDailyTokenUsage retrieves daily token usage for a user.
func (m *MultiTenantSession) GetDailyTokenUsage(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	return m.tokenUsageSvc.GetDailyUsage(senderID, days)
}

// GetDailyTokenUsageSummary retrieves aggregated per-day usage for a user.
func (m *MultiTenantSession) GetDailyTokenUsageSummary(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	return m.tokenUsageSvc.GetDailyUsageSummary(senderID, days)
}

// GetAllUserTokenUsage retrieves token usage for all users, sorted by total desc.
func (m *MultiTenantSession) GetAllUserTokenUsage() ([]sqlite.UserTokenUsage, error) {
	return m.tokenUsageSvc.GetAllUsage()
}

// GetOrCreateSession retrieves or creates a tenant session for the given channel and chatID.
// senderID is passed via context (letta.WithUserID) at call time, not here.
func (m *MultiTenantSession) GetOrCreateSession(channel, chatID string) (*TenantSession, error) {
	// Cache key: channel:chat_id (NOT senderID)
	// Per-user human block is handled dynamically via Recall/Memorize with senderID parameter
	key := channel + ":" + chatID

	// Fast path: check cache with read lock
	m.mu.RLock()
	sess, ok := m.tenantCache[key]
	m.mu.RUnlock()

	if ok {
		// Mark session as active
		sess.MarkActive()
		return sess, nil
	}

	// Slow path: acquire write lock and create session
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if sess, ok := m.tenantCache[key]; ok {
		sess.MarkActive()
		return sess, nil
	}

	// Get or create tenant ID
	tenantID, err := m.tenantSvc.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return nil, fmt.Errorf("get/create tenant: %w", err)
	}

	// Create session MCP manager (user scope injected by ConfigureSessionMCP at message time)
	sessionKey := channel + ":" + chatID
	mcpManager := tools.NewSessionMCPManager(sessionKey, "", m.mcpConfigPath, "", "", m.mcpInactivityTimeout)

	// Letta mode: create LettaMemory (userID passed via context, not stored in struct)
	// Select memory provider based on configuration
	var memProvider memory.MemoryProvider
	switch m.memoryProvider {
	case "letta":
		memProvider = letta.New(tenantID, m.coreSvc, m.archivalSvc, m.memorySvc, m.toolIndexSvc)
		// Forward compat: one-time migration from user_profiles → core memory blocks
		m.migrateProfileToCoreMemory(tenantID)
	default:
		// Flat memory: file-based storage under ~/.xbot/memory/{tenantID}/
		// Use tenantID (numeric) as directory name for filesystem safety
		flatMemDir := filepath.Join(config.XbotHome(), "memory", fmt.Sprintf("%d", tenantID))
		memProvider = flat.New(tenantID, flatMemDir)
	}
	// Create tenant session
	sess = &TenantSession{
		tenantID:   tenantID,
		channel:    channel,
		chatID:     chatID,
		sessionSvc: m.sessionSvc,
		memorySvc:  m.memorySvc,
		memory:     memProvider,
		mcpManager: mcpManager,
		lastActive: time.Now(),
	}

	m.tenantCache[key] = sess
	return sess, nil
}

// ConfigureSessionMCP updates the session MCP scope for the current user.
// Returns newly registered personal MCP tool names (for immediate activation), or nil if catalog unchanged.
func (m *MultiTenantSession) ConfigureSessionMCP(channel, chatID, senderID, workDir string) ([]string, error) {
	sess, err := m.GetOrCreateSession(channel, chatID)
	if err != nil {
		return nil, err
	}

	mgr := sess.GetMCPManager()
	if mgr == nil {
		return nil, nil
	}

	userConfigPath := tools.UserMCPConfigPath(workDir, senderID)
	workspaceRoot := tools.UserWorkspaceRoot(workDir, senderID)
	mgr.UpdateScope(senderID, userConfigPath, workspaceRoot)

	newTools := m.indexPersonalMCPTools(sess.TenantID(), mgr)
	return newTools, nil
}

// catalogFingerprint computes a stable hash of the MCP catalog tool names.
func catalogFingerprint(catalog []tools.MCPServerCatalogEntry) string {
	var keys []string
	for _, entry := range catalog {
		for _, toolName := range entry.ToolNames {
			keys = append(keys, entry.Name+":"+toolName)
		}
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// indexPersonalMCPTools indexes personal MCP tools for a tenant.
// Returns only truly NEW tool names (added since last catalog snapshot) for immediate activation.
// On first load or when catalog is unchanged, returns nil.
func (m *MultiTenantSession) indexPersonalMCPTools(tenantID int64, mgr *tools.SessionMCPManager) []string {
	if mgr == nil {
		return nil
	}

	catalog := mgr.GetCatalog()
	if len(catalog) == 0 {
		return nil
	}

	// Build tool entries and names
	var entries []memory.ToolIndexEntry
	var toolNames []string
	for _, entry := range catalog {
		for _, toolName := range entry.ToolNames {
			fullName := fmt.Sprintf("mcp_%s_%s", entry.Name, toolName)
			desc := fmt.Sprintf("MCP server: %s. Tool: %s", entry.Name, toolName)
			if entry.Instructions != "" {
				desc = fmt.Sprintf("%s. %s", desc, entry.Instructions)
			}
			entries = append(entries, memory.ToolIndexEntry{
				Name:        fullName,
				ServerName:  entry.Name,
				Source:      "personal",
				Description: desc,
			})
			toolNames = append(toolNames, fullName)
		}
	}

	// Fingerprint check: skip if catalog unchanged
	// Check in-memory first, fall back to disk (survives restart)
	fp := catalogFingerprint(catalog)
	m.mu.RLock()
	prev := m.toolIndexFingerprints[tenantID]
	prevNames := m.toolIndexPrevNames[tenantID]
	m.mu.RUnlock()
	if prev == "" && m.toolIndexSvc != nil {
		prev = m.toolIndexSvc.GetFingerprint(tenantID)
		if prev != "" {
			m.mu.Lock()
			m.toolIndexFingerprints[tenantID] = prev
			m.mu.Unlock()
		}
	}
	if fp == prev {
		return nil
	}

	// Catalog changed — kick off async indexing (if tool index is available)
	if m.toolIndexSvc != nil && len(entries) > 0 {
		entriesCopy := make([]memory.ToolIndexEntry, len(entries))
		copy(entriesCopy, entries)
		fpCopy := fp
		go func() {
			// toolIndexTimeout is the timeout for tool indexing operations.
			const toolIndexTimeout = 10 * time.Minute
			ctx, cancel := context.WithTimeout(m.shutdownCtx, toolIndexTimeout)
			defer cancel()
			if err := m.IndexToolsForTenant(ctx, tenantID, entriesCopy); err != nil {
				if m.shutdownCtx.Err() != nil {
					log.Debugf("Index personal MCP tools for tenant %d cancelled by shutdown", tenantID)
				} else {
					log.WithError(err).Warnf("Failed to index personal MCP tools for tenant %d", tenantID)
				}
				return
			}
			log.Infof("Indexed %d personal MCP tools for tenant %d", len(entriesCopy), tenantID)
			m.toolIndexSvc.SetFingerprint(tenantID, fpCopy)
			m.mu.Lock()
			m.toolIndexFingerprints[tenantID] = fpCopy
			m.mu.Unlock()
		}()
	}

	// Update tool name snapshot (fingerprint updated in goroutine after success)
	currentNames := make(map[string]bool, len(toolNames))
	for _, name := range toolNames {
		currentNames[name] = true
	}
	m.mu.Lock()
	m.toolIndexPrevNames[tenantID] = currentNames
	m.mu.Unlock()

	// First load: no previous snapshot → all tools are pre-existing, not "new"
	if prevNames == nil {
		return nil
	}

	// Subsequent change: only return tools not in previous catalog
	var newTools []string
	for _, name := range toolNames {
		if !prevNames[name] {
			newTools = append(newTools, name)
		}
	}
	return newTools
}

// migrateProfileToCoreMemory performs a one-time forward-compatible migration
// of legacy user_profiles data into Letta core memory blocks.
// - __me__ profile → persona block (bot identity, global per-tenant)
// Only writes if the target block is currently empty to avoid overwriting user edits.
func (m *MultiTenantSession) migrateProfileToCoreMemory(tenantID int64) {
	// Check if persona block is already populated (persona is global, use "" for userID)
	persona, _, err := m.coreSvc.GetBlock(tenantID, "persona", "")
	if err != nil {
		log.WithError(err).Warn("Profile migration: failed to read persona block")
		return
	}
	if persona != "" {
		return // Already has content, skip migration
	}

	// Read self profile (__me__)
	_, selfProfile, err := m.userProfileSvc.GetProfile("__me__")
	if err != nil {
		log.WithError(err).Warn("Profile migration: failed to read __me__ profile")
		return
	}
	if selfProfile == "" {
		return // No profile to migrate
	}

	// Write to persona block (global, use "" for userID)
	if err := m.coreSvc.SetBlock(tenantID, "persona", selfProfile, ""); err != nil {
		log.WithError(err).Warn("Profile migration: failed to write persona block")
		return
	}
	log.WithField("tenant_id", tenantID).Info("Migrated __me__ profile to persona core memory block")
}

// RecallTimeRangeFunc returns the time-range recall search function (nil if not in Letta mode).
func (m *MultiTenantSession) RecallTimeRangeFunc() vectordb.RecallTimeRangeFunc {
	return m.recallTimeRangeFn
}

// IndexToolsForTenant indexes MCP tools for a specific tenant.
func (m *MultiTenantSession) IndexToolsForTenant(ctx context.Context, tenantID int64, tools []memory.ToolIndexEntry) error {
	if m.toolIndexSvc == nil {
		return nil // Tool index not available (flat mode or no embedding config)
	}
	// Convert memory.ToolIndexEntry to vectordb.ToolIndexEntry
	entries := make([]vectordb.ToolIndexEntry, len(tools))
	for i, t := range tools {
		entries[i] = vectordb.ToolIndexEntry{
			Name:        t.Name,
			ServerName:  t.ServerName,
			Source:      t.Source,
			Description: t.Description,
		}
	}
	return m.toolIndexSvc.IndexTools(ctx, tenantID, entries)
}

// Close closes the database connection
func (m *MultiTenantSession) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

// DBPath returns the database path (useful for migration checks)
func (m *MultiTenantSession) DBPath() string {
	return m.dbPath
}

// DB returns the underlying SQLite database connection
func (m *MultiTenantSession) DB() *sqlite.DB {
	return m.db
}

// CoreMemoryService returns the shared core memory service.
func (m *MultiTenantSession) CoreMemoryService() *sqlite.CoreMemoryService {
	return m.coreSvc
}

// ArchivalService returns the shared archival memory service.
func (m *MultiTenantSession) ArchivalService() *vectordb.ArchivalService {
	return m.archivalSvc
}

// MemoryService returns the shared memory (recall) service.
func (m *MultiTenantSession) MemoryService() *sqlite.MemoryService {
	return m.memorySvc
}

// ToolIndexService returns the shared tool index service.
func (m *MultiTenantSession) ToolIndexService() *vectordb.ToolIndexService {
	return m.toolIndexSvc
}

// RecallTimeRange returns the recall time-range search function.
func (m *MultiTenantSession) RecallTimeRange() vectordb.RecallTimeRangeFunc {
	return m.recallTimeRangeFn
}

// NewLettaMemory creates a new LettaMemory instance with an independent tenantID.
// The service instances (coreSvc, archivalSvc, etc.) are shared — data isolation
// is provided by the tenantID parameter.
func (m *MultiTenantSession) NewLettaMemory(tenantID int64) *letta.LettaMemory {
	return letta.New(tenantID, m.coreSvc, m.archivalSvc, m.memorySvc, m.toolIndexSvc)
}

// GetSessionMCPManager implements SessionMCPManagerProvider interface
func (m *MultiTenantSession) GetSessionMCPManager(sessionKey string) *tools.SessionMCPManager {
	m.mu.RLock()
	sess, ok := m.tenantCache[sessionKey]
	m.mu.RUnlock()

	if ok {
		return sess.GetMCPManager()
	}
	return nil
}

// StartCleanupRoutine starts the background cleanup goroutine
func (m *MultiTenantSession) StartCleanupRoutine() {
	m.cleanupWg.Add(1)
	go func() {
		defer m.cleanupWg.Done()
		ticker := time.NewTicker(m.mcpCleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				m.cleanupInactiveResources()
			case <-m.cleanupStopCh:
				return
			}
		}
	}()
	log.WithFields(log.Fields{
		"mcpCleanupInterval":  m.mcpCleanupInterval,
		"sessionCacheTimeout": m.sessionCacheTimeout,
	}).Info("MCP cleanup routine started")
}

// StopCleanupRoutine stops the cleanup goroutine and cancels background tasks (safe to call multiple times)
func (m *MultiTenantSession) StopCleanupRoutine() {
	m.cleanupStopOnce.Do(func() {
		m.shutdownCancel()
		close(m.cleanupStopCh)
		m.cleanupWg.Wait()
	})
}

// SetOnSessionEvict sets the callback for when a session is evicted (for Registry cleanup of sessionActivated/sessionRound)
func (m *MultiTenantSession) SetOnSessionEvict(cb func(sessionKey string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSessionEvict = cb
}

// cleanupInactiveResources cleans up inactive resources (MCP connections and session cache)
func (m *MultiTenantSession) cleanupInactiveResources() {
	m.mu.Lock()

	now := time.Now()
	var sessionsToDelete []string

	// Collect sessions to clean (only lightweight ops under lock, no I/O)
	for key, sess := range m.tenantCache {
		// Use lastActive field for timeout check (avoids calling CleanupInactiveMCPs I/O under lock)
		lastActive := sess.LastActive()
		if now.Sub(lastActive) > m.sessionCacheTimeout {
			sessionsToDelete = append(sessionsToDelete, key)
		}
	}

	// Collect sessions to close and evict from cache (only lightweight ops under lock)
	type sessionToClose struct {
		key  string
		sess *TenantSession
	}
	var toClose []sessionToClose
	for _, key := range sessionsToDelete {
		if sess, ok := m.tenantCache[key]; ok {
			toClose = append(toClose, sessionToClose{key: key, sess: sess})
			delete(m.tenantCache, key)
		}
	}

	// Get callback and release lock
	onEvict := m.onSessionEvict
	m.mu.Unlock()

	// Execute all I/O operations outside lock (close MCP connections) and callbacks
	for _, item := range toClose {
		// CleanupInactiveMCPs and Close both involve I/O, executed outside lock
		item.sess.CleanupInactiveMCPs()
		item.sess.Close()
		log.WithField("session", item.key).Info("Removed session from cache due to inactivity")
		// Notify Registry to clean up this session's activation state
		if onEvict != nil {
			onEvict(item.key)
		}
	}
}

// DestroySession completely removes a tenant session: cache eviction, DB deletion
// (with CASCADE to messages), and MCP cleanup. Used when SubAgent sessions end
// their lifecycle to prevent stale data leaking into future sessions with the
// same role/instance key.
func (m *MultiTenantSession) DestroySession(channel, chatID string) error {
	key := channel + ":" + chatID

	m.mu.Lock()
	sess, ok := m.tenantCache[key]
	if ok {
		delete(m.tenantCache, key)
	}
	onEvict := m.onSessionEvict
	m.mu.Unlock()

	if !ok {
		// Not in cache — still try to delete from DB in case it exists there.
		tenantID, err := m.tenantSvc.GetTenantIDByChannelChatID(channel, chatID)
		if err != nil || tenantID == 0 {
			return nil // doesn't exist, nothing to do
		}
		_ = m.tenantSvc.DeleteTenant(tenantID)
		return nil
	}

	// Close MCP connections outside lock
	sess.Close()

	// Delete from DB (CASCADE removes all messages)
	_ = m.tenantSvc.DeleteTenant(sess.TenantID())

	// Notify Registry to clean up session-scoped activations
	if onEvict != nil {
		onEvict(key)
	}

	log.WithFields(log.Fields{
		"session":   key,
		"tenant_id": sess.TenantID(),
	}).Info("Tenant session destroyed")

	return nil
}

// InvalidateAll invalidates all cached sessions' MCP connections, forcing reload on next use
func (m *MultiTenantSession) InvalidateAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, sess := range m.tenantCache {
		sess.InvalidateMCP()
		log.WithField("session", key).Debug("Invalidated session MCP")
	}

	log.Info("All session MCP connections invalidated, will reload on next use")
}

// InvalidateSessionMCP invalidates a specific session's MCP connections
// Used for token refresh scenarios that require re-establishing specific MCP server connections
func (m *MultiTenantSession) InvalidateSessionMCP(sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.tenantCache[sessionKey]; ok {
		sess.InvalidateMCP()
		log.WithField("session", sessionKey).Info("Session MCP invalidated")
	}
}

// ClearMemory clears the specified memory type(s) for a tenant identified by (channel, chatID).
// targetType is one of: "session", "core_persona", "core_human", "core_working",
// "core_all", "long_term", "event_history", "archival", "reset_all".
func (m *MultiTenantSession) ClearMemory(ctx context.Context, channel, chatID, targetType, userID string) error {
	tenantID, err := m.tenantSvc.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}

	var errs []string
	appendErr := func(name string, err error) {
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}

	switch targetType {
	case "session":
		appendErr("session", m.sessionSvc.Clear(tenantID))
		// Evict cached session so next request loads fresh state
		sessionKey := channel + ":" + chatID
		m.mu.Lock()
		if sess, ok := m.tenantCache[sessionKey]; ok {
			sess.Close()
			delete(m.tenantCache, sessionKey)
		}
		m.mu.Unlock()
	case "core_persona":
		appendErr("persona", m.coreSvc.ClearBlock(tenantID, "persona", ""))
	case "core_human":
		appendErr("human", m.coreSvc.ClearBlock(tenantID, "human", userID))
	case "core_working":
		appendErr("working_context", m.coreSvc.ClearBlock(tenantID, "working_context", ""))
	case "core_all":
		appendErr("core_all", m.coreSvc.ClearAllBlocks(tenantID, userID))
		// Evict cached session to reset in-memory core memory references
		sessionKey := channel + ":" + chatID
		m.mu.Lock()
		if sess, ok := m.tenantCache[sessionKey]; ok {
			sess.Close()
			delete(m.tenantCache, sessionKey)
		}
		m.mu.Unlock()
	case "long_term":
		appendErr("long_term", m.memorySvc.ClearLongTerm(ctx, tenantID))
	case "event_history":
		appendErr("event_history", m.memorySvc.ClearHistory(ctx, tenantID))
	case "archival":
		if m.archivalSvc != nil {
			appendErr("archival", m.archivalSvc.ClearAll(ctx, tenantID))
		}
	case "reset_all":
		appendErr("session", m.sessionSvc.Clear(tenantID))
		appendErr("core_all", m.coreSvc.ClearAllBlocks(tenantID, userID))
		appendErr("long_term", m.memorySvc.ClearLongTerm(ctx, tenantID))
		appendErr("event_history", m.memorySvc.ClearHistory(ctx, tenantID))
		appendErr("state", m.memorySvc.ClearState(ctx, tenantID))
		if m.archivalSvc != nil {
			appendErr("archival", m.archivalSvc.ClearAll(ctx, tenantID))
		}
		// Evict session from cache to reset in-memory state
		sessionKey := channel + ":" + chatID
		m.mu.Lock()
		if sess, ok := m.tenantCache[sessionKey]; ok {
			sess.Close()
			delete(m.tenantCache, sessionKey)
		}
		m.mu.Unlock()
	default:
		return fmt.Errorf("unknown target type: %s", targetType)
	}

	if len(errs) > 0 {
		return fmt.Errorf("partial clear failed: %s", strings.Join(errs, "; "))
	}

	log.WithFields(log.Fields{
		"tenant_id":   tenantID,
		"target_type": targetType,
	}).Info("Memory cleared")
	return nil
}

// GetMemoryStats returns statistics for all memory types of a tenant.
func (m *MultiTenantSession) GetMemoryStats(ctx context.Context, channel, chatID, userID string) map[string]string {
	stats := map[string]string{}

	tenantID, err := m.tenantSvc.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return stats
	}

	// Session message count
	if count, err := m.sessionSvc.GetMessagesCount(tenantID); err == nil {
		stats["session"] = fmt.Sprintf("%d messages", count)
	}

	// Core memory blocks
	if blocks, err := m.coreSvc.GetAllBlocks(tenantID, userID); err == nil {
		for _, name := range []string{"persona", "human", "working_context"} {
			if content, ok := blocks[name]; ok && content != "" {
				stats[name] = fmt.Sprintf("%d chars", len(content))
			}
		}
	}

	// Archival memory count
	if m.archivalSvc != nil {
		if count, err := m.archivalSvc.Count(tenantID); err == nil && count > 0 {
			stats["archival"] = fmt.Sprintf("%d entries", count)
		}
	}

	// Long-term memory
	if content, err := m.memorySvc.ReadLongTerm(ctx, tenantID); err == nil && content != "" {
		stats["long_term"] = "has content"
	}

	// Event history count
	if count, err := m.memorySvc.GetHistoryCount(ctx, tenantID); err == nil && count > 0 {
		stats["event_history"] = fmt.Sprintf("%d entries", count)
	}

	return stats
}

// TrimHistory deletes messages newer than or equal to the given cutoff timestamp
// for the tenant identified by channel and chatID, and clears the cached token
// state so maybeCompress doesn't use stale values from before the rewind.
func (m *MultiTenantSession) TrimHistory(channel, chatID string, cutoff time.Time) error {
	if cutoff.IsZero() {
		return nil
	}
	tenantID, err := m.tenantSvc.GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return fmt.Errorf("get tenant: %w", err)
	}
	_, err = m.sessionSvc.PurgeNewerThanOrEqual(tenantID, cutoff)
	if err != nil {
		return err
	}
	// Clear token state so maybeCompress doesn't use stale values from before
	// the rewind (would otherwise trigger incorrect compression on next Run).
	if err := m.memorySvc.SetTokenState(context.Background(), tenantID, 0, 0); err != nil {
		log.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to clear token state after trim")
	}
	return nil
}
