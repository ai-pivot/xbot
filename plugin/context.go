package plugin

import (
	"sync"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// PluginContext — the safe, permission-filtered API surface for plugins
// ---------------------------------------------------------------------------

// PluginContext provides plugins with controlled access to xbot capabilities.
// It is the ONLY interface plugins should use; direct access to internal
// structures (ToolContext, Registry, etc.) is prohibited by design.
type PluginContext interface {
	// --- Tool Registration ---

	// RegisterTool registers a plugin-provided tool.
	// Requires "tools.register" permission in manifest.
	RegisterTool(tool PluginTool) error

	// --- Hook Subscriptions ---

	// OnPreToolUse subscribes to PreToolUse events with an optional matcher.
	// Requires "hooks.subscribe" permission.
	OnPreToolUse(matcher string, handler HookHandler) error

	// OnPostToolUse subscribes to PostToolUse events with an optional matcher.
	// Requires "hooks.subscribe" permission.
	OnPostToolUse(matcher string, handler HookHandler) error

	// OnUserPrompt subscribes to UserPromptSubmit events.
	// Requires "hooks.subscribe" permission.
	OnUserPrompt(handler HookHandler) error

	// OnAgentStop subscribes to AgentStop events.
	// Requires "hooks.subscribe" permission.
	OnAgentStop(handler HookHandler) error

	// OnSessionStart subscribes to SessionStart events.
	// Requires "hooks.subscribe" permission.
	OnSessionStart(handler HookHandler) error

	// OnSessionEnd subscribes to SessionEnd events.
	// Requires "hooks.subscribe" permission.
	OnSessionEnd(handler HookHandler) error

	// OnEvent subscribes to any lifecycle event by name.
	// Requires "hooks.subscribe" permission.
	OnEvent(event HookEvent, matcher string, handler HookHandler) error

	// OnAllToolUse subscribes to both PreToolUse and PostToolUse events for all tools.
	// Requires "hooks.subscribe" permission.
	OnAllToolUse(handler HookHandler) error

	// OnError subscribes to PostToolUseFailure events for all tools.
	// Requires "hooks.subscribe" permission.
	OnError(handler HookHandler) error

	// --- Context Enrichment ---

	// EnrichContext registers a function that injects dynamic content
	// into the system prompt. Requires "context.enrich" permission.
	EnrichContext(name string, enricher ContextEnricher) error

	// --- Storage ---

	// Storage returns the plugin's isolated key-value store.
	// Requires "storage.private" permission.
	Storage() StorageAccessor

	// --- Metadata ---

	// PluginID returns the unique identifier of this plugin.
	PluginID() string

	// WorkingDir returns the current working directory.
	WorkingDir() string

	// Channel returns the message channel (feishu, cli, web, etc.).
	Channel() string

	// ChatID returns the current chat/session ID.
	ChatID() string

	// Logger returns a namespaced logger for this plugin.
	Logger() Logger
}

// Logger provides structured logging for plugins.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
}

// Field is a structured logging field.
type Field struct {
	Key   string
	Value any
}

// ---------------------------------------------------------------------------
// StorageAccessor — per-plugin isolated key-value storage
// ---------------------------------------------------------------------------

// StorageAccessor provides a simple key-value store scoped to a single plugin.
// Data is persisted to disk at ~/.xbot/plugins/<id>/storage.json
type StorageAccessor interface {
	// Get retrieves a value by key. Returns ("", false) if not found.
	Get(key string) (string, bool)

	// Set stores a key-value pair.
	Set(key, value string) error

	// Delete removes a key.
	Delete(key string) error

	// Keys returns all keys in the store.
	Keys() []string

	// Clear removes all entries.
	Clear() error
}

// ---------------------------------------------------------------------------
// pluginContextImpl — the concrete implementation
// ---------------------------------------------------------------------------

type pluginContextImpl struct {
	mu       sync.RWMutex
	pluginID string
	manifest *PluginManifest
	perm     *PermissionChecker
	storage  StorageAccessor
	logger   Logger

	// Registered capabilities (collected during Activate)
	tools            []PluginTool
	hooks            []hookRegistration
	contextEnrichers []enricherRegistration

	// Metadata from the current session
	workingDir string
	channel    string
	chatID     string
}

type hookRegistration struct {
	Event   HookEvent
	Matcher string
	Handler HookHandler
}

type enricherRegistration struct {
	Name     string
	Enricher ContextEnricher
}

// newPluginContext creates a new PluginContext for the given plugin.
func newPluginContext(manifest *PluginManifest, storage StorageAccessor, logger Logger) *pluginContextImpl {
	return &pluginContextImpl{
		pluginID:         manifest.ID,
		manifest:         manifest,
		perm:             NewPermissionChecker(manifest.Permissions),
		storage:          storage,
		logger:           logger,
		tools:            make([]PluginTool, 0),
		hooks:            make([]hookRegistration, 0),
		contextEnrichers: make([]enricherRegistration, 0),
	}
}

// SetSessionMetadata updates session-specific metadata.
func (pc *pluginContextImpl) SetSessionMetadata(workingDir, channel, chatID string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.workingDir = workingDir
	pc.channel = channel
	pc.chatID = chatID
}

func (pc *pluginContextImpl) RegisterTool(tool PluginTool) error {
	if !pc.perm.Has(PermToolsRegister) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermToolsRegister,
			Action:     "register tool",
		}
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.tools = append(pc.tools, tool)
	pc.logger.Info("Tool registered", Field{Key: "tool", Value: tool.Definition().Name})
	return nil
}

func (pc *pluginContextImpl) OnPreToolUse(matcher string, handler HookHandler) error {
	return pc.OnEvent(HookPreToolUse, matcher, handler)
}

func (pc *pluginContextImpl) OnPostToolUse(matcher string, handler HookHandler) error {
	return pc.OnEvent(HookPostToolUse, matcher, handler)
}

func (pc *pluginContextImpl) OnUserPrompt(handler HookHandler) error {
	return pc.OnEvent(HookUserPromptSubmit, "", handler)
}

func (pc *pluginContextImpl) OnAgentStop(handler HookHandler) error {
	return pc.OnEvent(HookAgentStop, "", handler)
}

func (pc *pluginContextImpl) OnSessionStart(handler HookHandler) error {
	return pc.OnEvent(HookSessionStart, "", handler)
}

func (pc *pluginContextImpl) OnSessionEnd(handler HookHandler) error {
	return pc.OnEvent(HookSessionEnd, "", handler)
}

func (pc *pluginContextImpl) OnAllToolUse(handler HookHandler) error {
	if err := pc.OnPreToolUse("", handler); err != nil {
		return err
	}
	return pc.OnPostToolUse("", handler)
}

func (pc *pluginContextImpl) OnError(handler HookHandler) error {
	return pc.OnEvent(HookPostToolUseError, "", handler)
}

func (pc *pluginContextImpl) OnEvent(event HookEvent, matcher string, handler HookHandler) error {
	if !pc.perm.Has(PermHooksSubscribe) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermHooksSubscribe,
			Action:     "subscribe to " + string(event),
		}
	}
	if handler == nil {
		return nil
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.hooks = append(pc.hooks, hookRegistration{
		Event:   event,
		Matcher: matcher,
		Handler: handler,
	})
	pc.logger.Info("Hook registered", Field{Key: "event", Value: string(event)}, Field{Key: "matcher", Value: matcher})
	return nil
}

func (pc *pluginContextImpl) EnrichContext(name string, enricher ContextEnricher) error {
	if !pc.perm.Has(PermContextEnrich) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermContextEnrich,
			Action:     "register context enricher",
		}
	}
	if enricher == nil {
		return nil
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.contextEnrichers = append(pc.contextEnrichers, enricherRegistration{
		Name:     name,
		Enricher: enricher,
	})
	pc.logger.Info("Context enricher registered", Field{Key: "name", Value: name})
	return nil
}

func (pc *pluginContextImpl) Storage() StorageAccessor {
	if !pc.perm.Has(PermStoragePrivate) {
		pc.logger.Warn("Storage access denied", Field{Key: "permission", Value: PermStoragePrivate})
		return newDeniedStorage(pc.pluginID)
	}
	return pc.storage
}

func (pc *pluginContextImpl) PluginID() string { return pc.pluginID }
func (pc *pluginContextImpl) WorkingDir() string {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.workingDir
}
func (pc *pluginContextImpl) Channel() string {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.channel
}
func (pc *pluginContextImpl) ChatID() string {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.chatID
}
func (pc *pluginContextImpl) Logger() Logger { return pc.logger }

// --- Internal accessors for PluginManager ---

func (pc *pluginContextImpl) GetTools() []PluginTool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make([]PluginTool, len(pc.tools))
	copy(result, pc.tools)
	return result
}

func (pc *pluginContextImpl) GetHooks() []hookRegistration {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make([]hookRegistration, len(pc.hooks))
	copy(result, pc.hooks)
	return result
}

func (pc *pluginContextImpl) GetEnrichers() []enricherRegistration {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make([]enricherRegistration, len(pc.contextEnrichers))
	copy(result, pc.contextEnrichers)
	return result
}

// ---------------------------------------------------------------------------
// PermissionError
// ---------------------------------------------------------------------------

// PermissionError is returned when a plugin attempts an unauthorized action.
type PermissionError struct {
	PluginID   string
	Permission string
	Action     string
}

func (e *PermissionError) Error() string {
	return "plugin " + e.PluginID + ": permission denied for '" + e.Permission + "' (action: " + e.Action + ")"
}

// ---------------------------------------------------------------------------
// deniedStorage — no-op storage returned when permission is missing
// ---------------------------------------------------------------------------

type deniedStorage struct {
	pluginID string
}

func newDeniedStorage(pluginID string) *deniedStorage {
	return &deniedStorage{pluginID: pluginID}
}

func (d *deniedStorage) Get(key string) (string, bool) { return "", false }
func (d *deniedStorage) Set(key, value string) error {
	return &PermissionError{PluginID: d.pluginID, Permission: PermStoragePrivate, Action: "storage write"}
}
func (d *deniedStorage) Delete(key string) error {
	return &PermissionError{PluginID: d.pluginID, Permission: PermStoragePrivate, Action: "storage delete"}
}
func (d *deniedStorage) Keys() []string { return nil }
func (d *deniedStorage) Clear() error {
	return &PermissionError{PluginID: d.pluginID, Permission: PermStoragePrivate, Action: "storage clear"}
}

// ---------------------------------------------------------------------------
// Default Logger — wraps logrus
// ---------------------------------------------------------------------------

// pluginLogger wraps the global logger with a plugin namespace.
type pluginLogger struct {
	id string
}

func newPluginLogger(pluginID string) *pluginLogger {
	return &pluginLogger{id: pluginID}
}

func (l *pluginLogger) Debug(msg string, fields ...Field) {
	log.WithFields(l.buildFields(fields)).Debug(msg)
}

func (l *pluginLogger) Info(msg string, fields ...Field) {
	log.WithFields(l.buildFields(fields)).Info(msg)
}

func (l *pluginLogger) Warn(msg string, fields ...Field) {
	log.WithFields(l.buildFields(fields)).Warn(msg)
}

func (l *pluginLogger) Error(msg string, fields ...Field) {
	log.WithFields(l.buildFields(fields)).Error(msg)
}

// buildFields constructs structured log fields including the plugin namespace
// and any additional fields passed by the plugin.
func (l *pluginLogger) buildFields(fields []Field) log.Fields {
	f := log.Fields{"plugin": l.id}
	for _, field := range fields {
		f[field.Key] = field.Value
	}
	return f
}
