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

	// RegisterTools registers multiple tools at once.
	// Returns the first error encountered.
	RegisterTools(tools ...PluginTool) error

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

	// TenantID returns the current tenant ID for multi-tenancy awareness.
	TenantID() int64

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

	// --- UI Contributions ---

	// ContributeUI registers a UI widget provider for a zone declared in plugin.json.
	// Requires "ui.contribute" permission. The widgetID must match a declared
	// UIContribution ID in the plugin's manifest.
	ContributeUI(widgetID, zone string, widget UIWidget, priority int) error

	// UpdateWidget triggers an asynchronous re-render of the named widget.
	// The widgetID must have been registered via ContributeUI.
	// Safe to call from hook handlers, tool executors, or any goroutine.
	UpdateWidget(widgetID string) error

	// SetWidgetRegistry sets the widget registry for this context.
	// Called internally by PluginManager before Activate.
	SetWidgetRegistry(wr *WidgetRegistry)

	// --- Context Values ---

	// SetValue stores an arbitrary value in the plugin context.
	// This enables cross-handler data sharing within a plugin.
	// No permission required — this is in-memory session-scoped state, not persisted.
	// Data is lost when the plugin deactivates.
	SetValue(key string, value any)

	// GetValue retrieves a value from the plugin context.
	// Returns (nil, false) if the key has not been set.
	// No permission required.
	GetValue(key string) (any, bool)

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

	// WithField returns a new Logger that pre-binds an additional field.
	// Pre-bound fields are merged with per-call fields on each log call.
	// Per-call fields take precedence over pre-bound fields for the same key.
	WithField(key string, value any) Logger
	// WithFields returns a new Logger that pre-binds additional fields.
	WithFields(fields ...Field) Logger
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
	tenantID   int64

	bus *PluginEventBus

	// pm holds a reference to the PluginManager for per-tenant bus lookup.
	pm *PluginManager

	configStore *PluginConfigStore

	errorCallback PluginErrorCallback

	// UI widget registry (set by PluginManager before Activate)
	widgetRegistry *WidgetRegistry

	// Context values — session-scoped in-memory key-value store
	contextValues map[string]any

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
func newPluginContext(manifest *PluginManifest, storage StorageAccessor, logger Logger, bus *PluginEventBus, configStore *PluginConfigStore, pm *PluginManager) *pluginContextImpl {
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
		pm:               pm,
		configStore:      configStore,
		contextValues:    make(map[string]any),
	}
}

// SetSessionMetadata updates session-specific metadata.
func (pc *pluginContextImpl) SetSessionMetadata(workingDir, channel, chatID string, tenantID int64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.workingDir = workingDir
	pc.channel = channel
	pc.chatID = chatID
	pc.tenantID = tenantID
	if pc.pm != nil {
		pc.bus = pc.pm.EventBusFor(tenantID)
	}
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

func (pc *pluginContextImpl) RegisterTools(tools ...PluginTool) error {
	for _, tool := range tools {
		if err := pc.RegisterTool(tool); err != nil {
			return err
		}
	}
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
		return fmt.Errorf("hook handler must not be nil")
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
		return fmt.Errorf("context enricher must not be nil")
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
func (pc *pluginContextImpl) TenantID() int64 {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.tenantID
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

// GetTools returns a snapshot of all tools registered by this plugin.

func (pc *pluginContextImpl) GetTools() []PluginTool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make([]PluginTool, len(pc.tools))
	copy(result, pc.tools)
	return result
}

// GetHooks returns a snapshot of all hooks registered by this plugin.
func (pc *pluginContextImpl) GetHooks() []hookRegistration {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make([]hookRegistration, len(pc.hooks))
	copy(result, pc.hooks)
	return result
}

// GetEnrichers returns a snapshot of all context enrichers registered by this plugin.
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

// SetValue stores an arbitrary value in the plugin context.
// This enables cross-handler data sharing within a plugin.
func (pc *pluginContextImpl) SetValue(key string, value any) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.contextValues[key] = value
}

// GetValue retrieves a value from the plugin context.
func (pc *pluginContextImpl) GetValue(key string) (any, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	v, ok := pc.contextValues[key]
	return v, ok
}

// ---------------------------------------------------------------------------
// UI Widget Methods
// ---------------------------------------------------------------------------

// SetWidgetRegistry sets the widget registry for this context.
// Called by PluginManager before Activate.
func (pc *pluginContextImpl) SetWidgetRegistry(wr *WidgetRegistry) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.widgetRegistry = wr
}

// ContributeUI registers a UI widget. The widgetID must match a declared
// UIContribution in the plugin's manifest.
func (pc *pluginContextImpl) ContributeUI(widgetID, zone string, widget UIWidget, priority int) error {
	if !pc.perm.Has(PermUIContribute) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermUIContribute,
			Action:     "contribute UI widget",
		}
	}
	if pc.widgetRegistry == nil {
		return fmt.Errorf("widget registry not available")
	}
	return pc.widgetRegistry.Register(pc.pluginID, widgetID, zone, widget, priority)
}

// UpdateWidget triggers an asynchronous re-render of the named widget.
func (pc *pluginContextImpl) UpdateWidget(widgetID string) error {
	if pc.widgetRegistry == nil {
		return fmt.Errorf("widget registry not available")
	}
	// Use default width 0 (unbounded) — TUI will refresh with real width on resize.
	return pc.widgetRegistry.RefreshWidget(pc.pluginID, widgetID, 0, nil)
}

// getWidgetRegistry returns the underlying WidgetRegistry. Used internally by
// script plugins to trigger notifications without updating the global cache.
// Thread-safe: acquires read lock to prevent race with SetWidgetRegistry.
func (pc *pluginContextImpl) getWidgetRegistry() *WidgetRegistry {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.widgetRegistry
}

// ---------------------------------------------------------------------------
// deniedStorage — no-op storage returned when permission is missing
// ---------------------------------------------------------------------------

// deniedStorage implements StorageAccessor by rejecting all write operations
// with PermissionError. Reads return zero values. Used when a plugin lacks storage permissions.
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

// Clear rejects with PermissionError — the plugin does not have storage:private permission.
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

func (l *pluginLogger) WithField(key string, value any) Logger {
	return &loggerWithFields{parent: l, fields: []Field{{Key: key, Value: value}}}
}

func (l *pluginLogger) WithFields(fields ...Field) Logger {
	return &loggerWithFields{parent: l, fields: fields}
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
// loggerWithFields — immutable wrapper that pre-binds fields to any Logger
// ---------------------------------------------------------------------------

// loggerWithFields wraps a Logger with pre-bound fields.
// Each logging call merges pre-bound fields with per-call fields.
// Per-call fields take precedence over pre-bound fields (last-write-wins).
type loggerWithFields struct {
	parent Logger
	fields []Field
}

func (l *loggerWithFields) WithField(key string, value any) Logger {
	// Must copy to avoid sharing the underlying array with the parent.
	newFields := make([]Field, len(l.fields), len(l.fields)+1)
	copy(newFields, l.fields)
	newFields = append(newFields, Field{Key: key, Value: value})
	return &loggerWithFields{parent: l.parent, fields: newFields}
}

func (l *loggerWithFields) WithFields(fields ...Field) Logger {
	merged := make([]Field, len(l.fields), len(l.fields)+len(fields))
	copy(merged, l.fields)
	merged = append(merged, fields...)
	return &loggerWithFields{parent: l.parent, fields: merged}
}

func (l *loggerWithFields) Debug(msg string, fields ...Field) {
	l.parent.Debug(msg, l.mergeFields(fields)...)
}
func (l *loggerWithFields) Info(msg string, fields ...Field) {
	l.parent.Info(msg, l.mergeFields(fields)...)
}
func (l *loggerWithFields) Warn(msg string, fields ...Field) {
	l.parent.Warn(msg, l.mergeFields(fields)...)
}
func (l *loggerWithFields) Error(msg string, fields ...Field) {
	l.parent.Error(msg, l.mergeFields(fields)...)
}

func (l *loggerWithFields) mergeFields(callFields []Field) []Field {
	if len(l.fields) == 0 {
		return callFields
	}
	if len(callFields) == 0 {
		return l.fields
	}
	merged := make([]Field, len(l.fields)+len(callFields))
	copy(merged, l.fields)
	copy(merged[len(l.fields):], callFields)
	return merged
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
