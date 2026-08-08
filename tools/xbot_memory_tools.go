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
	sb.WriteString(fmt.Sprintf("## Memory Search Results (%d found)\n\n", len(entries)))
	for i, e := range entries {
		sb.WriteString(fmt.Sprintf("### %d. [%s] (importance: %.1f)\n", i+1, e.Type, e.Importance))
		sb.WriteString(e.Content)
		if e.Keywords != "" {
			sb.WriteString(fmt.Sprintf("\n*Keywords: %s*\n", e.Keywords))
		}
		sb.WriteString(fmt.Sprintf("\n*Created: %s*\n\n", e.CreatedAt))
	}

	return NewResult(sb.String()), nil
}

// --- memory_add ---

// MemoryAddTool manually adds a long-term memory entry.
type MemoryAddTool struct{}

func (t *MemoryAddTool) Name() string { return "memory_add" }
func (t *MemoryAddTool) Description() string {
	return "Save a memory for future conversations. Memories persist across sessions and can be searched with memory_search. Use this for important facts, user preferences, key decisions, or anything worth remembering."
}
func (t *MemoryAddTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "content", Type: "string", Description: "The memory content (1-2 sentences, self-contained)", Required: true},
		{Name: "type", Type: "string", Description: "Memory type: fact, preference, event, decision, or skill", Required: true},
		{Name: "keywords", Type: "string", Description: "3-5 comma-separated keywords for search (auto-extracted if empty)", Required: false},
		{Name: "tags", Type: "string", Description: "1-3 comma-separated category tags", Required: false},
		{Name: "importance", Type: "number", Description: "Importance score 0.0-1.0 (default: 0.5)", Required: false},
	}
}

func (t *MemoryAddTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Content    string  `json:"content"`
		Type       string  `json:"type"`
		Keywords   string  `json:"keywords"`
		Tags       string  `json:"tags"`
		Importance float64 `json:"importance"`
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

	mem := getXbotMemory(ctx)
	if mem == nil {
		return NewResult("Memory is not available (memory provider is not xbot)."), nil
	}

	id, err := mem.AddMemory(ctx.Ctx, xbotmemory.LongTermMemory{
		Type:       params.Type,
		Content:    params.Content,
		Keywords:   params.Keywords,
		Tags:       params.Tags,
		Importance: params.Importance,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to add memory: %w", err)
	}

	log.WithFields(log.Fields{
		"id":   id,
		"type": params.Type,
	}).Info("Memory added via tool")

	return NewResult(fmt.Sprintf("Memory saved (ID: %d). Use memory_search to find it later.", id)), nil
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
		sb.WriteString(fmt.Sprintf("## All Memories (%d)\n\n", len(entries)))
		for _, e := range entries {
			sb.WriteString(fmt.Sprintf("- **#%d [%s]** (importance: %.1f) %s", e.ID, e.Type, e.Importance, e.Content))
			if e.Keywords != "" {
				sb.WriteString(fmt.Sprintf("  *(keywords: %s)*", e.Keywords))
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
// Returns nil if the memory provider is not xbot.
func getXbotMemory(ctx *ToolContext) *xbotmemory.XbotMemory {
	return ctx.XbotMemory
}
