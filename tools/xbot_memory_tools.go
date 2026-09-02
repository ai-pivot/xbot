package tools

import (
	"fmt"
	"strings"

	"xbot/llm"
	log "xbot/logger"
	xbotmemory "xbot/memory/xbot"
)

// --- memory_search ---

// MemorySearchTool searches across all memory tiers using BM25.
type MemorySearchTool struct{}

func (t *MemorySearchTool) Name() string { return "memory_search" }
func (t *MemorySearchTool) Description() string {
	return "Search your persistent memories across all sessions. Uses BM25 keyword search to find relevant facts, preferences, events, decisions, and skills. Use this to recall information from past conversations."
}
func (t *MemorySearchTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "query", Type: "string", Description: "Search query (keywords or phrase)", Required: true},
		{Name: "type", Type: "string", Description: "Filter by memory type: fact, preference, event, decision, skill, or all (default: all)", Required: false},
		{Name: "limit", Type: "integer", Description: "Max results to return (default: 10, max: 50)", Required: false},
	}
}

func (t *MemorySearchTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Query string `json:"query"`
		Type  string `json:"type"`
		Limit int    `json:"limit"`
	}](input)
	if err != nil {
		return nil, err
	}
	if params.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if params.Type == "" {
		params.Type = "all"
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}
	if params.Limit > 50 {
		params.Limit = 50
	}

	mem := getXbotMemory(ctx)
	if mem == nil {
		return NewResult("Memory search is not available (memory provider is not xbot)."), nil
	}

	entries, err := mem.SearchMemories(ctx.Ctx, params.Query, params.Type, params.Limit)
	if err != nil {
		return nil, fmt.Errorf("memory search failed: %w", err)
	}

	if len(entries) == 0 {
		return NewResult("No memories found matching your query."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Memory Search Results (%d found)\n\n", len(entries))
	for i, e := range entries {
		fmt.Fprintf(&sb, "### %d. [%s] (importance: %.1f)\n", i+1, e.Type, e.Importance)
		sb.WriteString(e.Content)
		if e.Keywords != "" {
			fmt.Fprintf(&sb, "\n*Keywords: %s*\n", e.Keywords)
		}
		fmt.Fprintf(&sb, "\n*Created: %s*\n\n", e.CreatedAt)
	}

	return NewResult(sb.String()), nil
}

// --- memory_add ---

// MemoryAddTool manually adds a long-term memory entry.
type MemoryAddTool struct{}

func (t *MemoryAddTool) Name() string { return "memory_add" }
func (t *MemoryAddTool) Description() string {
	return "Save a memory for future conversations. Memories persist across sessions and can be searched with memory_search. Use this for important facts, user preferences, key decisions, or anything worth remembering. Default scope is 'global' (injected into every session); use scope='session' for task-local state that should NOT appear in other sessions."
}
func (t *MemoryAddTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "content", Type: "string", Description: "The memory content (1-2 sentences, self-contained)", Required: true},
		{Name: "type", Type: "string", Description: "Memory type: fact, preference, event, decision, or skill", Required: true},
		{Name: "keywords", Type: "string", Description: "3-5 comma-separated keywords for search (auto-extracted if empty)", Required: false},
		{Name: "tags", Type: "string", Description: "1-3 comma-separated category tags", Required: false},
		{Name: "importance", Type: "number", Description: "Importance score 0.0-1.0 (default: 0.5)", Required: false},
		{Name: "scope", Type: "string", Description: "'global' (default, injected into all sessions) or 'session' (this session only — task-local state)", Required: false},
	}
}

func (t *MemoryAddTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Content    string  `json:"content"`
		Type       string  `json:"type"`
		Keywords   string  `json:"keywords"`
		Tags       string  `json:"tags"`
		Importance float64 `json:"importance"`
		Scope      string  `json:"scope"`
	}](input)
	if err != nil {
		return nil, err
	}
	if params.Content == "" {
		return nil, fmt.Errorf("content is required")
	}

	validTypes := map[string]bool{
		"fact": true, "preference": true, "event": true,
		"decision": true, "skill": true,
	}
	if !validTypes[params.Type] {
		return nil, fmt.Errorf("type must be one of: fact, preference, event, decision, skill")
	}

	// Session isolation (2026-09-02 redesign): 'global' is the default — the
	// explicit add path means the model decided this is durable cross-session
	// knowledge. 'session' keeps it out of other sessions' Recall injection.
	if params.Scope == "" {
		params.Scope = "global"
	}
	if params.Scope != "global" && params.Scope != "session" {
		return nil, fmt.Errorf("scope must be 'global' or 'session'")
	}

	mem := getXbotMemory(ctx)
	if mem == nil {
		return NewResult("Memory is not available (memory provider is not xbot)."), nil
	}

	// SourceSession: scope='session' 必须带上会话标识（CR xbotgh 🔴）——
	// AddMemory 落库 source_session='' 时 Recall 的 sessionMemories 过滤
	//（scope='session' AND source_session = ?）永不命中，仅 memory_search
	// 可搜、注入永远看不到（工具描述引导的用法即静默失效）。
	var sourceSession string
	if params.Scope == "session" && ctx.ChatID != "" {
		sourceSession = ctx.ChatID
	}
	id, err := mem.AddMemory(ctx.Ctx, xbotmemory.LongTermMemory{
		Type:          params.Type,
		Content:       params.Content,
		Keywords:      params.Keywords,
		Tags:          params.Tags,
		Importance:    params.Importance,
		Scope:         params.Scope,
		SourceSession: sourceSession,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to add memory: %w", err)
	}

	log.WithFields(log.Fields{
		"id":    id,
		"type":  params.Type,
		"scope": params.Scope,
	}).Info("Memory added via tool")

	return NewResult(fmt.Sprintf("Memory saved (ID: %d, scope: %s). Use memory_search to find it later.", id, params.Scope)), nil
}

// --- memory_manage ---

// MemoryManageTool lists, deletes, or updates memories.
type MemoryManageTool struct{}

func (t *MemoryManageTool) Name() string { return "memory_manage" }
func (t *MemoryManageTool) Description() string {
	return "Manage your memories: list all, delete by ID, or update content. Use action 'list' to see all memories, 'delete' to remove one, or 'update' to modify."
}
func (t *MemoryManageTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: list, delete, or update", Required: true},
		{Name: "id", Type: "integer", Description: "Memory ID (required for delete and update)", Required: false},
		{Name: "type", Type: "string", Description: "Filter by type (for list): fact, preference, event, decision, skill, or all", Required: false},
		{Name: "content", Type: "string", Description: "New content (for update)", Required: false},
		{Name: "keywords", Type: "string", Description: "New keywords (for update, optional)", Required: false},
		{Name: "importance", Type: "number", Description: "New importance 0.0-1.0 (for update, optional)", Required: false},
	}
}

func (t *MemoryManageTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Action     string  `json:"action"`
		ID         int64   `json:"id"`
		Type       string  `json:"type"`
		Content    string  `json:"content"`
		Keywords   string  `json:"keywords"`
		Tags       string  `json:"tags"`
		Importance float64 `json:"importance"`
	}](input)
	if err != nil {
		return nil, err
	}

	mem := getXbotMemory(ctx)
	if mem == nil {
		return NewResult("Memory is not available (memory provider is not xbot)."), nil
	}

	switch params.Action {
	case "list":
		if params.Type == "" {
			params.Type = "all"
		}
		entries, err := mem.ListMemories(ctx.Ctx, params.Type, 50)
		if err != nil {
			return nil, fmt.Errorf("list memories failed: %w", err)
		}
		if len(entries) == 0 {
			return NewResult("No memories found."), nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "## All Memories (%d)\n\n", len(entries))
		for _, e := range entries {
			fmt.Fprintf(&sb, "- **#%d [%s]** (importance: %.1f", e.ID, e.Type, e.Importance)
			if e.Scope != "" {
				fmt.Fprintf(&sb, ", scope: %s", e.Scope)
			}
			fmt.Fprintf(&sb, ") %s", e.Content)
			if e.Keywords != "" {
				fmt.Fprintf(&sb, "  *(keywords: %s)*", e.Keywords)
			}
			sb.WriteString("\n")
		}
		return NewResult(sb.String()), nil

	case "delete":
		if params.ID == 0 {
			return nil, fmt.Errorf("id is required for delete")
		}
		if err := mem.DeleteMemory(ctx.Ctx, params.ID); err != nil {
			return nil, fmt.Errorf("delete memory failed: %w", err)
		}
		return NewResult(fmt.Sprintf("Memory #%d deleted.", params.ID)), nil

	case "update":
		if params.ID == 0 {
			return nil, fmt.Errorf("id is required for update")
		}
		if params.Content == "" {
			return nil, fmt.Errorf("content is required for update")
		}
		if err := mem.UpdateMemory(ctx.Ctx, params.ID, params.Content, params.Keywords, params.Tags, params.Importance); err != nil {
			return nil, fmt.Errorf("update memory failed: %w", err)
		}
		return NewResult(fmt.Sprintf("Memory #%d updated.", params.ID)), nil

	default:
		return nil, fmt.Errorf("unknown action: %s (use list, delete, or update)", params.Action)
	}
}

// XbotMemoryTools returns all xbot memory tools for registration.
func XbotMemoryTools() []Tool {
	return []Tool{
		&MemorySearchTool{},
		&MemoryAddTool{},
		&MemoryManageTool{},
	}
}

func init() {
	RegisterMemoryTools("xbot", func() []Tool { return XbotMemoryTools() })
}

// getXbotMemory extracts the XbotMemory instance from the tool context.
// Uses the generic MemoryProvider field + type assertion — no provider-specific
// field needed on ToolContext. Returns nil if the memory provider is not xbot.
func getXbotMemory(ctx *ToolContext) *xbotmemory.XbotMemory {
	if ctx.MemoryProvider == nil {
		return nil
	}
	if xm, ok := ctx.MemoryProvider.(*xbotmemory.XbotMemory); ok {
		return xm
	}
	return nil
}
