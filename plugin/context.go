package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// PluginContext — the safe, permission-filtered API surface for plugins
// ---------------------------------------------------------------------------

// PluginErrorCallback is called when the plugin encounters an unhandled error
// during its lifecycle (activation failure, runtime crash, etc.).
// Unlike OnError (which handles tool execution failures), this handles
// errors in the plugin's own lifecycle.
type PluginErrorCallback func(ctx context.Context, err error)

// PluginContext provides plugins with controlled access to xbot capabilities.
// It is the ONLY interface plugins should use; direct access to internal
// structures (ToolContext, Registry, etc.) is prohibited by design.
type PluginContext interface {
	// --- Tool Registration ---

	// RegisterTool registers a plugin-provided tool.
	// Requires "tools.register" permission in manifest.
	RegisterTool(tool PluginTool) error

	// UseMiddleware registers a plugin middleware.
	// Middleware is called for ALL tool executions from this plugin.
	// Requires "tools.register" permission.
	UseMiddleware(middleware PluginMiddleware) error

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

	// StorageInt retrieves a value by key and parses it as int64.
	// Returns (0, false) if the key does not exist or the value cannot be parsed.
	// Requires "storage.private" permission.
	StorageInt(key string) (int64, bool)

	// StorageBool retrieves a value by key and parses it as bool.
	// Returns (false, false) if the key does not exist or the value cannot be parsed.
	// Requires "storage.private" permission.
	StorageBool(key string) (bool, bool)

	// StorageJSON marshals the value to JSON and stores it under the given key.
	// Requires "storage.private" permission.
	StorageJSON(key string, value any) error

	// StorageGetJSON retrieves a value by key and unmarshals it into target.
	// Returns an error if the key does not exist or JSON unmarshaling fails.
	// Requires "storage.private" permission.
	StorageGetJSON(key string, target any) error

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

	// --- Plugin Event Bus ---

	// Subscribe registers a handler for plugin-to-plugin events.
	// Requires "bus.plugin" + "bus.read" permissions.
	Subscribe(topic string, handler PluginEventHandler) error

	// Publish sends an event to all subscribers of the topic.
	// Requires "bus.plugin" + "bus.write" permissions.
	Publish(topic string, data any) error

	// --- Plugin Error Callback ---

	// OnPluginError registers a callback for plugin-level errors (not tool errors).
	// Called when the plugin encounters an unhandled error during operation
	// (activation failure, runtime crash, etc.).
	// Requires "hooks.subscribe" permission.
	OnPluginError(callback PluginErrorCallback) error

	// --- Resource Tracking ---

	// ToolCallCount returns the cumulative number of tool executions for this plugin.
	ToolCallCount() int64

	// HookCallCount returns the cumulative number of hook dispatches for this plugin.
	HookCallCount() int64
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
	toolMiddlewares  []PluginMiddleware

	// Metadata from the current session
	workingDir string
	channel    string
	chatID     string

	bus *PluginEventBus

	configStore *PluginConfigStore

	errorCallback PluginErrorCallback

	// Runtime resource tracking (atomic for lock-free reads)
	toolCallCount int64
	hookCallCount int64
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
func newPluginContext(manifest *PluginManifest, storage StorageAccessor, logger Logger, bus *PluginEventBus, configStore *PluginConfigStore) *pluginContextImpl {
	return &pluginContextImpl{
		pluginID:         manifest.ID,
		manifest:         manifest,
		perm:             NewPermissionChecker(manifest.Permissions),
		storage:          storage,
		logger:           logger,
		tools:            make([]PluginTool, 0),
		hooks:            make([]hookRegistration, 0),
		contextEnrichers: make([]enricherRegistration, 0),
		bus:              bus,
		configStore:      configStore,
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

func (pc *pluginContextImpl) StorageInt(key string) (int64, bool) {
	s := pc.Storage()
	raw, ok := s.Get(key)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (pc *pluginContextImpl) StorageBool(key string) (bool, bool) {
	s := pc.Storage()
	raw, ok := s.Get(key)
	if !ok {
		return false, false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return v, true
}

func (pc *pluginContextImpl) StorageJSON(key string, value any) error {
	s := pc.Storage()
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("storage: marshal JSON for key %q: %w", key, err)
	}
	return s.Set(key, string(data))
}

func (pc *pluginContextImpl) StorageGetJSON(key string, target any) error {
	if target == nil {
		return fmt.Errorf("storage: target must not be nil")
	}
	s := pc.Storage()
	raw, ok := s.Get(key)
	if !ok {
		return fmt.Errorf("storage: key %q not found", key)
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("storage: unmarshal JSON for key %q: %w", key, err)
	}
	return nil
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

func (pc *pluginContextImpl) Config() (map[string]any, error) {
	// Start with manifest defaults
	config := GetDefaultConfig(pc.manifest)

	// Overlay user config
	if pc.configStore != nil {
		userConfig, err := pc.configStore.Load(pc.pluginID)
		if err != nil {
			return config, fmt.Errorf("load config: %w", err)
		}
		for k, v := range userConfig {
			config[k] = v
		}
	}
	return config, nil
}

func (pc *pluginContextImpl) SetConfig(key string, value any) error {
	if pc.configStore == nil {
		return fmt.Errorf("plugin config: config store not available")
	}
	return pc.configStore.Update(pc.pluginID, key, value)
}

func (pc *pluginContextImpl) Subscribe(topic string, handler PluginEventHandler) error {
	if !pc.perm.HasAll(PermBusPlugin, PermBusRead) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermBusPlugin + "+" + PermBusRead,
			Action:     "subscribe to event bus",
		}
	}
	if handler == nil {
		return nil
	}
	return pc.bus.Subscribe(topic, handler)
}

func (pc *pluginContextImpl) Publish(topic string, data any) error {
	if !pc.perm.HasAll(PermBusPlugin, PermBusWrite) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermBusPlugin + "+" + PermBusWrite,
			Action:     "publish to event bus",
		}
	}
	errs := pc.bus.Publish(context.Background(), topic, data)
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

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

// UseMiddleware registers a plugin middleware for tool execution interception.
func (pc *pluginContextImpl) UseMiddleware(middleware PluginMiddleware) error {
	if !pc.perm.Has(PermToolsRegister) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermToolsRegister,
			Action:     "register middleware",
		}
	}
	if middleware == nil {
		return nil
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.toolMiddlewares = append(pc.toolMiddlewares, middleware)
	pc.logger.Info("Middleware registered")
	return nil
}

// GetMiddlewares returns a copy of the registered middleware list.
func (pc *pluginContextImpl) GetMiddlewares() []PluginMiddleware {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make([]PluginMiddleware, len(pc.toolMiddlewares))
	copy(result, pc.toolMiddlewares)
	return result
}

func (pc *pluginContextImpl) OnPluginError(callback PluginErrorCallback) error {
	if !pc.perm.Has(PermHooksSubscribe) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermHooksSubscribe,
			Action:     "register plugin error callback",
		}
	}
	if callback == nil {
		return nil
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.errorCallback = callback
	pc.logger.Info("Plugin error callback registered")
	return nil
}

// GetErrorCallback returns the registered error callback (nil if none).
func (pc *pluginContextImpl) GetErrorCallback() PluginErrorCallback {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.errorCallback
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

// ---------------------------------------------------------------------------
// Resource Tracking — runtime call counters
// ---------------------------------------------------------------------------

// incrementToolCallCount atomically increments the tool call counter.
func (pc *pluginContextImpl) incrementToolCallCount() {
	atomic.AddInt64(&pc.toolCallCount, 1)
}

// incrementHookCallCount atomically increments the hook call counter.
func (pc *pluginContextImpl) incrementHookCallCount() {
	atomic.AddInt64(&pc.hookCallCount, 1)
}

// ToolCallCount returns the total number of tool executions for this plugin.
func (pc *pluginContextImpl) ToolCallCount() int64 {
	return atomic.LoadInt64(&pc.toolCallCount)
}

// HookCallCount returns the total number of hook dispatches for this plugin.
func (pc *pluginContextImpl) HookCallCount() int64 {
	return atomic.LoadInt64(&pc.hookCallCount)
}
