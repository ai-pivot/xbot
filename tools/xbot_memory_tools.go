package tools

import (
	"errors"
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
	return "Stage a memory for future conversations (two-phase write). First call: scope='global' (default) creates a PROPOSAL — nothing is persisted until you confirm with memory_confirm(proposal_id); scope='session' writes directly (session-local only). Duplicates, injection patterns, oversized or garbage content are rejected. Use this for important facts, user preferences, key decisions, or anything worth remembering."
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
	// 落库 source_session='' 时 Recall 的 sessionMemories 过滤
	//（scope='session' AND source_session = ?）永不命中，仅 memory_search
	// 可搜、注入永远看不到（工具描述引导的用法即静默失效）。
	var sourceSession string
	if params.Scope == "session" && ctx.ChatID != "" {
		sourceSession = ctx.ChatID
	}

	// Two-phase write (2026-11-07 redesign): the first call NEVER persists
	// global-scope memories — it stages a proposal the caller must confirm
	// via memory_confirm (TTL 10 minutes). Session-scope writes go straight
	// through the validation + duplicate gates. The unconfirmed gate was the
	// 2026-11 incident's amplifier: the model re-added the same garbage 6
	// times because nothing pushed back.
	proposalID, memoryID, err := mem.ProposeMemory(ctx.Ctx, xbotmemory.LongTermMemory{
		Type:          params.Type,
		Content:       params.Content,
		Keywords:      params.Keywords,
		Tags:          params.Tags,
		Importance:    params.Importance,
		Scope:         params.Scope,
		SourceSession: sourceSession,
	})
	if err != nil {
		var dup *xbotmemory.DuplicateMemoryError
		if errors.As(err, &dup) {
			return NewErrorResult(fmt.Sprintf(
				"NOT saved — duplicate of existing memory #%d. If you meant to UPDATE that fact, change the content so it states the new fact distinctly; if it is already correct, do nothing.",
				dup.ExistingID)), nil
		}
		return nil, fmt.Errorf("memory rejected: %w", err)
	}

	if memoryID > 0 {
		// session scope: direct write (already passed validation + dedup gates).
		log.WithFields(log.Fields{
			"id":    memoryID,
			"type":  params.Type,
			"scope": params.Scope,
		}).Info("Memory added via tool (session scope, direct write)")

		return NewResult(fmt.Sprintf("Memory saved directly (ID: %d, scope: session — only this session's Recall injects it). Use memory_search to find it later.", memoryID)), nil
	}

	// global scope: staged as proposal — nothing persisted yet.
	log.WithFields(log.Fields{
		"proposal_id": proposalID,
		"type":        params.Type,
		"scope":       params.Scope,
	}).Info("Memory staged as proposal via tool (awaiting memory_confirm)")

	var sb strings.Builder
	fmt.Fprintf(&sb, "PROPOSAL %d（未写入）\n\n", proposalID)
	fmt.Fprintf(&sb, "Type: %s\n", params.Type)
	fmt.Fprintf(&sb, "Scope: %s\n", params.Scope)
	fmt.Fprintf(&sb, "Content: %s\n\n", params.Content)
	sb.WriteString("⚠️ 确认后该记忆将持久化，并在未来任何时候被注入（即使当时的情况已改变）。请确认内容正确、无重复、无过时信息。\n\n")
	fmt.Fprintf(&sb, "确认方式：memory_confirm(proposal_id=%d)\n", proposalID)
	sb.WriteString("如内容有误或不再需要，忽略该 proposal 即可（10 分钟后自动过期）。\n")
	return NewResult(sb.String()), nil
}

// --- memory_confirm ---

// MemoryConfirmTool persists a staged memory proposal (the second phase of
// memory_add's two-phase write).
type MemoryConfirmTool struct{}

func (t *MemoryConfirmTool) Name() string { return "memory_confirm" }
func (t *MemoryConfirmTool) Description() string {
	return "Confirm and persist a memory proposal created by memory_add (two-phase write). Only call this after verifying the proposal's content is correct, non-duplicate, and not outdated — confirmed memories are injected into future sessions even when circumstances have changed. Expired/unknown proposal IDs error out."
}
func (t *MemoryConfirmTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "proposal_id", Type: "integer", Description: "The proposal ID returned by memory_add (valid for 10 minutes)", Required: true},
	}
}

func (t *MemoryConfirmTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		ProposalID int64 `json:"proposal_id"`
	}](input)
	if err != nil {
		return nil, err
	}
	if params.ProposalID == 0 {
		return nil, fmt.Errorf("proposal_id is required")
	}

	mem := getXbotMemory(ctx)
	if mem == nil {
		return NewResult("Memory is not available (memory provider is not xbot)."), nil
	}

	id, err := mem.ConfirmProposal(ctx.Ctx, params.ProposalID)
	if err != nil {
		var dup *xbotmemory.DuplicateMemoryError
		if errors.As(err, &dup) {
			return NewErrorResult(fmt.Sprintf(
				"NOT saved — duplicate of existing memory #%d (the same fact was persisted between proposal and confirm). Nothing further needed.",
				dup.ExistingID)), nil
		}
		return nil, fmt.Errorf("confirm proposal failed: %w", err)
	}

	log.WithFields(log.Fields{
		"proposal_id": params.ProposalID,
		"id":          id,
	}).Info("Memory persisted via memory_confirm")

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
		&MemoryConfirmTool{},
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
