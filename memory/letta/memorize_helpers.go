package letta

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// findSimilarMemories searches archival memory for entries similar to the given messages.
// Returns existing memories with similarity above the conflict threshold (0.5).
func (m *LettaMemory) findSimilarMemories(ctx context.Context, messages []llm.ChatMessage) []ExistingMemory {
	const dedupSearchLimit = 10
	const similarityConflictThreshold = float32(0.5)

	if m.archivalSvc == nil || len(messages) == 0 {
		return nil
	}

	// Build a query from the messages to consolidate
	const maxQueryLen = 500
	var queryBuilder strings.Builder
	for _, msg := range messages {
		if msg.Content == "" {
			continue
		}
		remaining := maxQueryLen - queryBuilder.Len()
		if remaining <= 1 {
			break
		}
		remaining-- // reserve 1 byte for trailing space
		content := msg.Content
		if len(content) > remaining {
			runes := []rune(content)
			for len(string(runes)) > remaining {
				runes = runes[:len(runes)-1]
			}
			content = string(runes)
		}
		queryBuilder.WriteString(content)
		queryBuilder.WriteString(" ")
	}
	query := queryBuilder.String()
	if query == "" {
		return nil
	}

	results, err := m.archivalSvc.Search(ctx, m.tenantID, query, dedupSearchLimit)
	if err != nil {
		log.WithError(err).Warn("Failed to search for similar memories during deduplication")
		return nil
	}

	var existing []ExistingMemory
	for _, r := range results {
		if r.Similarity > similarityConflictThreshold {
			existing = append(existing, ExistingMemory{
				ID:         r.ID,
				Content:    r.Content,
				Similarity: r.Similarity,
			})
		}
	}
	if len(existing) > 0 {
		log.WithField("tenant_id", m.tenantID).Infof("Found %d similar memories for deduplication", len(existing))
	}
	return existing
}

// formatMessagesAsLines formats conversation messages as timestamped text lines for the LLM prompt.
// Each line is truncated to 500 runes. Returns nil if no non-empty messages.
func formatMessagesAsLines(messages []llm.ChatMessage) []string {
	var lines []string
	for _, msg := range messages {
		if msg.Content == "" {
			continue
		}
		role := strings.ToUpper(msg.Role)
		toolHint := ""
		if msg.Role == "tool" && msg.ToolName != "" {
			toolHint = fmt.Sprintf(" [tool: %s]", msg.ToolName)
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			names := make([]string, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				names[i] = tc.Name
			}
			toolHint = fmt.Sprintf(" [tools: %s]", strings.Join(names, ", "))
		}
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		// Truncate by rune to avoid corrupting multi-byte characters
		content := msg.Content
		if len([]rune(content)) > 500 {
			content = string([]rune(content)[:500]) + "..."
		}
		lines = append(lines, fmt.Sprintf("[%s] %s%s: %s", ts.Format("2006-01-02 15:04"), role, toolHint, content))
	}
	return lines
}

// buildConsolidationPrompt constructs the LLM prompt for memory consolidation.
func (m *LettaMemory) buildConsolidationPrompt(
	blocks map[string]string,
	existingMemories []ExistingMemory,
	lines []string,
	userID string,
) string {
	var coreDisplay strings.Builder
	for _, name := range []string{"persona", "human", "working_context"} {
		content := blocks[name]
		if content == "" {
			content = "(empty)"
		}
		fmt.Fprintf(&coreDisplay, "### %s\n%s\n\n", blockTitle(name), content)
	}

	// Build deduplication context for the prompt
	var dedupContext string
	if len(existingMemories) > 0 {
		dedupContext = "\n## Existing Similar Memories (for deduplication)\n"
		dedupContext += "The following memories are similar to what you're about to archive. Use this to:\n"
		dedupContext += "- Skip duplicate information (similarity > 0.9)\n"
		dedupContext += "- Resolve conflicts by choosing the more accurate one (similarity 0.5-0.9)\n"
		dedupContext += "- Set entries_to_delete for any memories that should be replaced/removed\n\n"
		for _, mem := range existingMemories {
			dedupContext += fmt.Sprintf("- [ID: %s, Similarity: %.2f]: %s\n", mem.ID, mem.Similarity, mem.Content)
			if len(dedupContext) > 2000 {
				dedupContext += "... (truncated)\n"
				break
			}
		}
	}

	prompt := fmt.Sprintf(`You are a memory consolidation agent for a Letta-style memory system.
Review the conversation below and call the consolidate_memory tool to update the memory system.

## Instructions

- Update core memory blocks (persona/human/working_context) if the conversation reveals new important information
- Archive detailed facts/events to archival memory that don't fit in core memory
- Write a history entry summarizing key events
- Only update blocks that need changes. Set unchanged block values to empty string "".
- Keep core memory blocks concise (bullet points, not prose)
- Deduplicate: compare new archival_entries against existing memories. If information is redundant (similarity > 0.9), merge or skip it. If there's conflict (similarity 0.5-0.9), decide which to keep and add its ID to entries_to_delete.
- Always prefer new information when in doubt

## Current Core Memory
%s
%s

## Conversation to Process
%s
`, coreDisplay.String(), dedupContext, strings.Join(lines, "\n"))

	return prompt
}

// applyCoreMemoryUpdates applies core memory block updates from the consolidation result.
func (m *LettaMemory) applyCoreMemoryUpdates(blocks map[string]string, args consolidateMemoryArgs, userID string) {
	for _, blockName := range []string{"persona", "human", "working_context"} {
		var newContent string
		switch blockName {
		case "persona":
			newContent = args.Persona
		case "human":
			newContent = args.Human
		case "working_context":
			newContent = args.WorkingContext
		}
		oldContent := blocks[blockName]
		if newContent != "" {
			log.WithFields(log.Fields{
				"tenant_id": m.tenantID,
				"block":     blockName,
				"old_len":   len(oldContent),
				"new_len":   len(newContent),
			}).Info("Updating core memory block")
			if err := m.coreSvc.SetBlock(m.tenantID, blockName, newContent, userID); err != nil {
				log.WithError(err).WithField("block", blockName).Error("Failed to update core memory block")
			}
		}
	}
}

// applyArchivalUpdates writes archival entries, deletes duplicates, and appends history.
func (m *LettaMemory) applyArchivalUpdates(ctx context.Context, args consolidateMemoryArgs, oldMessages []llm.ChatMessage) {
	// Archive to archival memory (embedding computed by chromem-go)
	// Use the midpoint of the conversation time range as the information timestamp
	archivalTS := conversationMidpoint(oldMessages)
	for _, entry := range args.ArchivalEntries {
		if entry == "" {
			continue
		}
		if m.archivalSvc != nil {
			if _, err := m.archivalSvc.Insert(ctx, m.tenantID, entry, archivalTS); err != nil {
				log.WithError(err).Error("Failed to insert archival entry during consolidation")
			}
		}
	}

	// Delete duplicate/conflicting memories as determined by LLM
	if len(args.EntriesToDelete) > 0 && m.archivalSvc != nil {
		for _, entryID := range args.EntriesToDelete {
			if err := m.archivalSvc.Delete(ctx, m.tenantID, entryID); err != nil {
				log.WithError(err).WithField("entry_id", entryID).Warn("Failed to delete duplicate memory during consolidation")
			} else {
				log.WithField("tenant_id", m.tenantID).Infof("Deleted duplicate memory: %s", entryID)
			}
		}
	}

	// Append history entry
	if args.HistoryEntry != "" {
		if err := m.memorySvc.AppendHistory(ctx, m.tenantID, args.HistoryEntry); err != nil {
			log.WithError(err).Error("Failed to append history entry")
		}
	}
}
