package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

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
// ---------------------------------------------------------------------------
// Composite sub-interfaces (Interface Segregation Principle)
// ---------------------------------------------------------------------------

// ToolRegistrar provides tool and middleware registration capabilities.
type ToolRegistrar interface {
	RegisterTool(tool PluginTool) error
	RegisterTools(tools ...PluginTool) error
	UseMiddleware(middleware PluginMiddleware) error
}

// HookSubscriber provides lifecycle hook subscription capabilities.
type HookSubscriber interface {
	OnPreToolUse(matcher string, handler HookHandler) error
	OnPostToolUse(matcher string, handler HookHandler) error
	OnUserPrompt(handler HookHandler) error
	OnAgentStop(handler HookHandler) error
	OnSessionStart(handler HookHandler) error
	OnSessionEnd(handler HookHandler) error
	OnEvent(event HookEvent, matcher string, handler HookHandler) error
	OnAllToolUse(handler HookHandler) error
	OnError(handler HookHandler) error
}

// StorageProvider provides typed key-value storage for plugin state.
type StorageProvider interface {
	Storage() StorageAccessor
	StorageInt(key string) (int64, bool)
	StorageBool(key string) (bool, bool)
	StorageJSON(key string, value any) error
	StorageGetJSON(key string, target any) error
}

// SessionMetadata provides read-only information about the current session.
type SessionMetadata interface {
	PluginID() string
	WorkingDir() string
	Channel() string
	ChatID() string
	TenantID() int64
	Logger() Logger
}

// EventBusPublisher provides plugin-to-plugin event bus access.
type EventBusPublisher interface {
	Subscribe(topic string, handler PluginEventHandler) error
	Publish(topic string, data any) error
}

// UIContributor provides UI widget, theme, and overlay registration.
type UIContributor interface {
	ContributeUI(widgetID, zone string, widget UIWidget, priority int) error
	UpdateWidget(widgetID string) error
	SetWidgetRegistry(wr *WidgetRegistry)
	ContributeTheme(id string, themeData []byte) error
	RegisterOverlay(id string, provider OverlayProvider) error
	ShowOverlay(id string) error
	HideOverlay() error
}

// CronScheduler provides cron scheduling for plugins.
type CronScheduler interface {
	ScheduleCron(spec CronContribution) (string, error)
	CancelCron(jobID string) error
}

// PluginContext is the full context passed to a plugin during activation.
// It composes all sub-interfaces for backward compatibility — existing code
// that accepts PluginContext continues to work unchanged. New code can
// accept narrower sub-interfaces where only a subset of capabilities is needed.
type PluginContext interface {
	ToolRegistrar
	HookSubscriber
	StorageProvider
	SessionMetadata
	EventBusPublisher
	UIContributor
	CronScheduler

	// --- Context Enrichment ---
	EnrichContext(name string, enricher ContextEnricher) error

	// --- Plugin Error Callback ---
	OnPluginError(callback PluginErrorCallback) error

	// --- Context Values ---
	SetValue(key string, value any)
	GetValue(key string) (any, bool)

	// --- Resource Tracking ---
	ToolCallCount() int64
	HookCallCount() int64

	// --- Channel Registration ---
	RegisterChannelProvider(provider any) error

	// --- Command Registration ---
	RegisterCommand(name string, description string, handler PluginCommandHandler) error

	// --- Notifications & Sound ---
	Notify(level NotificationLevel, title, message string)
	PlaySound(sound SoundID)
}

// Logger provides structured logging for plugins.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)

	// Formatted variants.
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)

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
// Generic snapshot helpers
// ---------------------------------------------------------------------------

// snapshotSlice returns a defensive copy of a slice under a read lock.
func snapshotSlice[T any](mu *sync.RWMutex, src []T) []T {
	mu.RLock()
	defer mu.RUnlock()
	dst := make([]T, len(src))
	copy(dst, src)
	return dst
}

// snapshotMap returns a defensive copy of a map under a read lock.
func snapshotMap[K comparable, V any](mu *sync.RWMutex, src map[K]V) map[K]V {
	mu.RLock()
	defer mu.RUnlock()
	dst := make(map[K]V, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
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
	channelProviders []any // channel.ChannelProvider instances (any to avoid import cycle)

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

	// Command handlers
	commands map[string]pluginCommandEntry

	// Cron tasks
	crons             []CronContribution
	cronCancellations map[string]bool

	// Themes
	themes map[string][]byte

	// Overlays
	overlays map[string]OverlayProvider
}

type hookRegistration struct {
	Event   HookEvent
	Matcher string
	Handler HookHandler
	// Global marks hooks that are session-agnostic (bypass session isolation).
	// Set by subscribeTrigger for script plugins that manage per-workDir state.
	Global bool
}

type enricherRegistration struct {
	Name     string
	Enricher ContextEnricher
}

// pluginCommandEntry stores a registered command handler.
type pluginCommandEntry struct {
	name        string
	description string
	handler     PluginCommandHandler
}

// newPluginContext creates a new PluginContext for the given plugin.
func newPluginContext(manifest *PluginManifest, storage StorageAccessor, logger Logger, bus *PluginEventBus, configStore *PluginConfigStore, pm *PluginManager) *pluginContextImpl {
	return &pluginContextImpl{
		pluginID:          manifest.ID,
		manifest:          manifest,
		perm:              NewPermissionChecker(manifest.Permissions),
		storage:           storage,
		logger:            logger,
		tools:             make([]PluginTool, 0),
		hooks:             make([]hookRegistration, 0),
		contextEnrichers:  make([]enricherRegistration, 0),
		channelProviders:  make([]any, 0),
		bus:               bus,
		pm:                pm,
		configStore:       configStore,
		contextValues:     make(map[string]any),
		commands:          make(map[string]pluginCommandEntry),
		crons:             make([]CronContribution, 0),
		cronCancellations: make(map[string]bool),
		themes:            make(map[string][]byte),
		overlays:          make(map[string]OverlayProvider),
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
	return pc.onEvent(event, matcher, handler, false)
}

// OnGlobalEvent registers a session-agnostic hook that bypasses session isolation.
// Used by script plugin triggers that manage per-workDir state internally.
func (pc *pluginContextImpl) OnGlobalEvent(event HookEvent, matcher string, handler HookHandler) error {
	return pc.onEvent(event, matcher, handler, true)
}

func (pc *pluginContextImpl) onEvent(event HookEvent, matcher string, handler HookHandler, global bool) error {
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
		Global:  global,
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
	return snapshotSlice(&pc.mu, pc.tools)
}

// GetHooks returns a snapshot of all hooks registered by this plugin.
func (pc *pluginContextImpl) GetHooks() []hookRegistration {
	return snapshotSlice(&pc.mu, pc.hooks)
}

// GetEnrichers returns a snapshot of all context enrichers registered by this plugin.
func (pc *pluginContextImpl) GetEnrichers() []enricherRegistration {
	return snapshotSlice(&pc.mu, pc.contextEnrichers)
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
	return snapshotSlice(&pc.mu, pc.toolMiddlewares)
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
// Default Logger — per-plugin log file only (keeps main log clean)
// ---------------------------------------------------------------------------

// pluginLogger writes structured logs ONLY to a per-plugin log file
// (daily-rotating, stored under ~/.xbot/plugins/<id>/logs/). This keeps the
// main xbot log clean — plugin operational logs (tool/hook registration,
// script execution, widget refresh, etc.) are isolated in the plugin's own
// directory. If the per-plugin writer could not be created (fileOut == nil),
// it falls back to the global logrus logger so that no logs are silently lost.
type pluginLogger struct {
	id      string
	fileOut io.Writer // per-plugin rotateWriter (may be nil if creation failed)
}

// newPluginLogger creates a plugin logger that writes to the per-plugin log
// file only.
func newPluginLogger(pluginID string, logMgr *pluginLogManager) *pluginLogger {
	pl := &pluginLogger{id: pluginID}
	if logMgr != nil {
		if rw, err := logMgr.GetWriter(pluginID); err == nil {
			pl.fileOut = rw
		}
	}
	return pl
}

func (l *pluginLogger) writeToFile(level, msg string, fields []Field) {
	if l.fileOut == nil {
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("%s [%s] plugin=%s", now, level, l.id)
	for _, f := range fields {
		line += fmt.Sprintf(" %s=%v", f.Key, f.Value)
	}
	line += " " + msg + "\n"
	l.fileOut.Write([]byte(line)) // ignore error — logging must not block
}

// emit writes to the per-plugin file. If no file writer is available it falls
// back to the global logrus logger so logs are never silently lost.
func (l *pluginLogger) emit(level, msg string, fields []Field) {
	if l.fileOut != nil {
		l.writeToFile(level, msg, fields)
		return
	}
	// Fallback: per-plugin writer unavailable — use global logrus.
	fieldsMap := l.buildLogrusFields(fields)
	switch level {
	case "DEBUG":
		log.Glob(log.CatPlugin).WithFields(fieldsMap).Debug(msg)
	case "INFO":
		log.Glob(log.CatPlugin).WithFields(fieldsMap).Info(msg)
	case "WARN":
		log.Glob(log.CatPlugin).WithFields(fieldsMap).Warn(msg)
	case "ERROR":
		log.Glob(log.CatPlugin).WithFields(fieldsMap).Error(msg)
	}
}

func (l *pluginLogger) Debug(msg string, fields ...Field) {
	l.emit("DEBUG", msg, fields)
}

func (l *pluginLogger) Info(msg string, fields ...Field) {
	l.emit("INFO", msg, fields)
}

func (l *pluginLogger) Warn(msg string, fields ...Field) {
	l.emit("WARN", msg, fields)
}

func (l *pluginLogger) Error(msg string, fields ...Field) {
	l.emit("ERROR", msg, fields)
}

func (l *pluginLogger) Debugf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.Debug(msg)
}

func (l *pluginLogger) Infof(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.Info(msg)
}

func (l *pluginLogger) Warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.Warn(msg)
}

func (l *pluginLogger) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.Error(msg)
}

func (l *pluginLogger) WithField(key string, value any) Logger {
	return &loggerWithFields{parent: l, fields: []Field{{Key: key, Value: value}}}
}

func (l *pluginLogger) WithFields(fields ...Field) Logger {
	return &loggerWithFields{parent: l, fields: fields}
}

// buildLogrusFields constructs logrus Fields including the plugin namespace.
func (l *pluginLogger) buildLogrusFields(fields []Field) log.Fields {
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

func (l *loggerWithFields) Debugf(format string, args ...any) {
	l.parent.Debugf(format, args...)
}
func (l *loggerWithFields) Infof(format string, args ...any) {
	l.parent.Infof(format, args...)
}
func (l *loggerWithFields) Warnf(format string, args ...any) {
	l.parent.Warnf(format, args...)
}
func (l *loggerWithFields) Errorf(format string, args ...any) {
	l.parent.Errorf(format, args...)
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

// ---------------------------------------------------------------------------
// Channel Provider Registration
// ---------------------------------------------------------------------------

// RegisterChannelProvider 注册自定义 Channel provider。
// 需要 "channels.register" 权限。provider 必须实现 channel.ChannelProvider 接口。
func (pc *pluginContextImpl) RegisterChannelProvider(provider any) error {
	if !pc.perm.Has(PermChannelsRegister) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermChannelsRegister,
			Action:     "register channel provider",
		}
	}
	if provider == nil {
		return fmt.Errorf("provider must not be nil")
	}
	// 验证 provider 实现了 channel.ChannelProvider 接口：
	// Name() string, CreateChannel(...) (..., error), ConfigSchema() [], IsEnabled(...) bool
	type nameable interface{ Name() string }
	n, ok := provider.(nameable)
	if !ok {
		return fmt.Errorf("provider must implement channel.ChannelProvider interface (missing Name() method)")
	}
	name := n.Name()
	if name == "" {
		return fmt.Errorf("provider.Name() must not be empty")
	}
	// 内置 channel 名称禁止覆盖
	switch name {
	case "feishu", "qq", "napcat", "web", "cli":
		return fmt.Errorf("cannot register provider with built-in channel name %q", name)
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.channelProviders = append(pc.channelProviders, provider)
	pc.logger.Info("Channel provider registered", Field{Key: "channel", Value: name})
	return nil
}

// ChannelProviders 返回当前插件注册的所有 ChannelProvider 实例。
// 由 serverapp 桥接层收集并转换为 channel.ChannelProvider。
func (pc *pluginContextImpl) ChannelProviders() []any {
	return snapshotSlice(&pc.mu, pc.channelProviders)
}

// ---------------------------------------------------------------------------
// Command Registration
// ---------------------------------------------------------------------------

// RegisterCommand registers a slash command handler for this plugin.
func (pc *pluginContextImpl) RegisterCommand(name string, description string, handler PluginCommandHandler) error {
	if !pc.perm.Has(PermCommandsRegister) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermCommandsRegister,
			Action:     "register command",
		}
	}
	if handler == nil {
		return fmt.Errorf("command handler must not be nil")
	}
	if name == "" {
		return fmt.Errorf("command name must not be empty")
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.commands[name] = pluginCommandEntry{
		name:        name,
		description: description,
		handler:     handler,
	}
	pc.logger.Info("Command registered", Field{Key: "command", Value: name})
	return nil
}

// GetCommands returns a snapshot of all registered command handlers.
func (pc *pluginContextImpl) GetCommands() map[string]pluginCommandEntry {
	return snapshotMap(&pc.mu, pc.commands)
}

// ---------------------------------------------------------------------------
// Cron Scheduling
// ---------------------------------------------------------------------------

// ScheduleCron creates a scheduled task. Returns a job ID.
func (pc *pluginContextImpl) ScheduleCron(spec CronContribution) (string, error) {
	if !pc.perm.Has(PermCronSchedule) {
		return "", &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermCronSchedule,
			Action:     "schedule cron",
		}
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	jobID := fmt.Sprintf("plugin:%s:%d", pc.pluginID, len(pc.crons))
	pc.crons = append(pc.crons, spec)
	pc.logger.Info("Cron scheduled", Field{Key: "job_id", Value: jobID})
	return jobID, nil
}

// CancelCron cancels a previously scheduled task.
func (pc *pluginContextImpl) CancelCron(jobID string) error {
	if !pc.perm.Has(PermCronSchedule) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermCronSchedule,
			Action:     "cancel cron",
		}
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cronCancellations[jobID] = true
	pc.logger.Info("Cron cancellation requested", Field{Key: "job_id", Value: jobID})
	return nil
}

// GetCrons returns a snapshot of all scheduled crons.
func (pc *pluginContextImpl) GetCrons() []CronContribution {
	return snapshotSlice(&pc.mu, pc.crons)
}

// GetCronCancellations returns a snapshot of all cancelled cron job IDs.
func (pc *pluginContextImpl) GetCronCancellations() map[string]bool {
	return snapshotMap(&pc.mu, pc.cronCancellations)
}

// ---------------------------------------------------------------------------
// Theme Contribution
// ---------------------------------------------------------------------------

// ContributeTheme registers a theme from the plugin.
func (pc *pluginContextImpl) ContributeTheme(id string, themeData []byte) error {
	if !pc.perm.Has(PermUIThemes) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermUIThemes,
			Action:     "contribute theme",
		}
	}
	if id == "" {
		return fmt.Errorf("theme id must not be empty")
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.themes[id] = themeData
	pc.logger.Info("Theme contributed", Field{Key: "theme_id", Value: id})
	return nil
}

// GetThemes returns a snapshot of all contributed themes.
func (pc *pluginContextImpl) GetThemes() map[string][]byte {
	return snapshotMap(&pc.mu, pc.themes)
}

// ---------------------------------------------------------------------------
// Overlay
// ---------------------------------------------------------------------------

// RegisterOverlay registers a full-screen overlay provider.
func (pc *pluginContextImpl) RegisterOverlay(id string, provider OverlayProvider) error {
	if !pc.perm.Has(PermUIOverlay) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermUIOverlay,
			Action:     "register overlay",
		}
	}
	if id == "" {
		return fmt.Errorf("overlay id must not be empty")
	}
	if provider == nil {
		return fmt.Errorf("overlay provider must not be nil")
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.overlays[id] = provider
	pc.logger.Info("Overlay registered", Field{Key: "overlay_id", Value: id})
	return nil
}

// ShowOverlay triggers the display of a registered overlay.
func (pc *pluginContextImpl) ShowOverlay(id string) error {
	if !pc.perm.Has(PermUIOverlay) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermUIOverlay,
			Action:     "show overlay",
		}
	}
	pc.mu.RLock()
	_, ok := pc.overlays[id]
	pc.mu.RUnlock()
	if !ok {
		return fmt.Errorf("overlay %q not registered", id)
	}
	if pc.bus != nil {
		pc.bus.Publish(context.Background(), "plugin:overlay:show", map[string]string{
			"plugin_id":  pc.pluginID,
			"overlay_id": id,
		})
	}
	pc.logger.Info("Overlay shown", Field{Key: "overlay_id", Value: id})
	return nil
}

// HideOverlay hides the currently displayed overlay.
func (pc *pluginContextImpl) HideOverlay() error {
	if !pc.perm.Has(PermUIOverlay) {
		return &PermissionError{
			PluginID:   pc.pluginID,
			Permission: PermUIOverlay,
			Action:     "hide overlay",
		}
	}
	if pc.bus != nil {
		pc.bus.Publish(context.Background(), "plugin:overlay:hide", map[string]string{
			"plugin_id": pc.pluginID,
		})
	}
	pc.logger.Info("Overlay hidden")
	return nil
}

// GetOverlays returns a snapshot of all registered overlay providers.
func (pc *pluginContextImpl) GetOverlays() map[string]OverlayProvider {
	return snapshotMap(&pc.mu, pc.overlays)
}

// ---------------------------------------------------------------------------
// Notifications & Sound
// ---------------------------------------------------------------------------

// Notify sends a notification to the user.
func (pc *pluginContextImpl) Notify(level NotificationLevel, title, message string) {
	if !pc.perm.Has(PermNotificationsSend) {
		pc.logger.Warn("Notification denied", Field{Key: "permission", Value: PermNotificationsSend})
		return
	}
	if pc.bus != nil {
		pc.bus.Publish(context.Background(), "plugin:notify", map[string]string{
			"plugin_id": pc.pluginID,
			"level":     string(level),
			"title":     title,
			"message":   message,
		})
	}
	pc.logger.Info("Notification sent", Field{Key: "level", Value: string(level)}, Field{Key: "title", Value: title})
}

// PlaySound plays a sound effect.
func (pc *pluginContextImpl) PlaySound(sound SoundID) {
	if !pc.perm.Has(PermNotificationsSend) {
		pc.logger.Warn("Sound denied", Field{Key: "permission", Value: PermNotificationsSend})
		return
	}
	if pc.bus != nil {
		pc.bus.Publish(context.Background(), "plugin:sound:play", map[string]string{
			"plugin_id": pc.pluginID,
			"sound":     string(sound),
		})
	}
	pc.logger.Info("Sound played", Field{Key: "sound", Value: string(sound)})
}
