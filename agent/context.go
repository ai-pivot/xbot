package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"text/template"
	"time"

	"xbot/llm"
	log "xbot/logger"
	"xbot/prompt"
)

// PromptData template rendering data
type PromptData struct {
	Channel        string
	WorkDir        string
	CWD            string // 当前Working directory（始终有值，默认等于 WorkDir）
	MemoryProvider string // "flat" or "letta"
	Identity       string
	Behavior       string
	Tools          string
	Memory         string
	Environment    string
	CodeRules      string
}

// PromptLoader handles loading and rendering system prompt templates
type PromptLoader struct {
	filePath string
	mu       sync.RWMutex
	tmpl     *template.Template
	lastMod  time.Time
}

// NewPromptLoader creates PromptLoader
// When filePath is empty or file doesn't exist, uses built-in default template
func NewPromptLoader(filePath string) *PromptLoader {
	pl := &PromptLoader{filePath: filePath}
	pl.load()
	return pl
}

// load loads template (from file → embedded default → minimal fallback)
func (pl *PromptLoader) load() {
	if pl.filePath != "" {
		if err := pl.loadFromFile(); err == nil {
			return
		} else {
			log.WithError(err).WithField("path", pl.filePath).Warn("Failed to load prompt file, trying embedded default")
		}
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	// Prefer embedded full default prompt
	if ep := EmbeddedPrompt(); ep != "" {
		if t, err := template.New("system").Parse(ep); err != nil {
			log.WithError(err).Error("Failed to parse embedded prompt, using minimal fallback")
		} else {
			pl.tmpl = t
			pl.lastMod = time.Time{}
			return
		}
	}
	// Final fallback
	fallback := EmbeddedFallbackPrompt()
	if fallback == "" {
		fallback = "你是 xbot。渠道：{{.Channel}} | Working directory：{{.WorkDir}} | Current directory: {{.CWD}}\n"
	}
	if t, err := template.New("system").Parse(fallback); err != nil {
		log.Fatalf("Failed to parse default system prompt template: %v", err)
	} else {
		pl.tmpl = t
	}
	pl.lastMod = time.Time{}
}

// loadFromFile loads template from file
func (pl *PromptLoader) loadFromFile() error {
	info, err := os.Stat(pl.filePath)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(pl.filePath)
	if err != nil {
		return err
	}

	tmpl, err := template.New("system").Parse(string(content))
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.tmpl = tmpl
	pl.lastMod = info.ModTime()
	log.WithField("path", pl.filePath).Info("System prompt loaded from file")
	return nil
}

// reload checks if file has been updated, reloads if so
func (pl *PromptLoader) reload() {
	if pl.filePath == "" {
		return
	}
	info, err := os.Stat(pl.filePath)
	if err != nil {
		return
	}
	pl.mu.RLock()
	needReload := info.ModTime().After(pl.lastMod)
	pl.mu.RUnlock()
	if needReload {
		if err := pl.loadFromFile(); err != nil {
			log.WithError(err).Warn("Failed to reload prompt file, keeping current template")
		} else {
			log.Info("System prompt reloaded (file changed)")
		}
	}
}

// Render renders system prompt
// Checks for file updates on each call, supports hot reloading
func (pl *PromptLoader) Render(data PromptData) string {
	pl.reload()
	data = enrichPromptData(data)

	pl.mu.RLock()
	tmpl := pl.tmpl
	pl.mu.RUnlock()

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.WithError(err).Error("Failed to render system prompt template")
		// fallback: use simple formatting
		return fmt.Sprintf("You are xbot, a helpful AI assistant.\nChannel: %s\nWorkDir: %s",
			data.Channel, data.WorkDir)
	}
	return buf.String()
}

func enrichPromptData(data PromptData) PromptData {
	data.Identity = prompt.Identity
	data.Behavior = prompt.Behavior
	data.Environment = prompt.Environment
	data.CodeRules = prompt.CodeRules

	switch data.MemoryProvider {
	case "letta":
		data.Tools = prompt.ToolsLetta
		data.Memory = prompt.MemoryLetta
	default:
		data.Tools = prompt.ToolsFlat
		data.Memory = ""
	}
	return data
}

// initPipelines initializes the Agent's message build pipeline.
// Called once during Agent creation, dynamically adjusted later via pipeline.Use/Remove.
func (a *Agent) initPipelines(memoryProvider string) {
	promptWorkDir := a.workDir
	if a.sandboxMode == "docker" {
		promptWorkDir = "/workspace"
	}

	// Main pipeline: for normal messages and card responses
	a.pipeline = NewMessagePipeline(
		NewSystemPromptMiddleware(a.promptLoader, memoryProvider),
		NewProjectContextMiddleware(),
		NewSkillsCatalogMiddleware(),
		NewAgentsCatalogMiddleware(),
		NewPermissionControlMiddleware(),
		NewMemoryMiddleware(),
		NewSenderInfoMiddleware(),
		NewLanguageMiddleware(a.settingsSvc),
		NewUserMessageMiddleware(memoryProvider),
	)

	// Cron pipeline: for scheduled tasks (minimal, no memory or skills)
	a.cronPipeline = NewMessagePipeline(
		NewCronSystemPromptMiddleware(promptWorkDir),
	)
}

// Pipeline returns the Agent's main message build pipeline, supporting runtime dynamic add/remove of middleware.
func (a *Agent) Pipeline() *MessagePipeline {
	return a.pipeline
}

// CronPipeline returns the Agent's Cron message build pipeline.
func (a *Agent) CronPipeline() *MessagePipeline {
	return a.cronPipeline
}

// NewMessageContext creates a pre-filled MessageContext for the main pipeline.
// After caller sets dynamic fields (ExtraKeySkillsCatalog, ExtraKeyAgentsCatalog, ExtraKeyMemoryProvider in Extra),
// pass to pipeline.Run(mc) for execution.
func NewMessageContext(ctx context.Context, userContent string, history []llm.ChatMessage, channel, workDir, senderName, senderID, chatID string) *MessageContext {
	return &MessageContext{
		Ctx:         ctx,
		SystemParts: make(map[string]string),
		UserContent: userContent,
		History:     history,
		Channel:     channel,
		WorkDir:     workDir,
		SenderName:  senderName,
		SenderID:    senderID,
		ChatID:      chatID,
		Extra:       make(map[string]any),
	}
}

// NewCronMessageContext creates a Cron-specific MessageContext.
func NewCronMessageContext(task string) *MessageContext {
	return &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: task,
		Extra:       make(map[string]any),
	}
}
