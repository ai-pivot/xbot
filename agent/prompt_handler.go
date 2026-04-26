package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/bus"
	log "xbot/logger"
	"xbot/memory"
	"xbot/session"
)

// handlePromptQuery builds the full prompt, writes it to a file, and sends it to the user (dryrun, no LLM call)
func (a *Agent) handlePromptQuery(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	// Extract query content after /prompt (trim then truncate, aligned with cmd parsing)
	trimmed := strings.TrimSpace(msg.Content)
	query := strings.TrimSpace(trimmed[len("/prompt"):])
	if query == "" {
		query = "(empty query)"
	}

	// Replace msg.Content with query, reuse buildPrompt
	dryMsg := msg
	dryMsg.Content = query
	messages, err := a.buildPrompt(ctx, dryMsg, tenantSession)
	if err != nil {
		return nil, err
	}

	// Get tool definitions
	sessionKey := msg.Channel + ":" + msg.ChatID
	toolDefs := visibleToolDefs(a.tools.AsDefinitionsForSession(sessionKey), a.settingsSvc, msg.Channel, msg.SenderID)

	// Format output
	var buf strings.Builder
	buf.WriteString("=== Prompt Dry Run ===\n\n")
	for i, m := range messages {
		fmt.Fprintf(&buf, "--- [%d] role: %s ---\n", i, m.Role)
		buf.WriteString(m.Content)
		buf.WriteString("\n\n")
	}

	fmt.Fprintf(&buf, "--- Tools (%d) ---\n", len(toolDefs))
	for _, td := range toolDefs {
		fmt.Fprintf(&buf, "- %s: %s\n", td.Name(), td.Description())
		for _, p := range td.Parameters() {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Fprintf(&buf, "    %s (%s)%s: %s\n", p.Name, p.Type, req, p.Description)
		}
	}

	fmt.Fprintf(&buf, "\n--- Total messages: %d ---\n", len(messages))

	// Write to file and send
	sbUID := sandboxUserID(msg)
	workspaceRoot := a.sandboxWorkspace(sbUID)
	if err := a.ensureWorkspace(ctx, workspaceRoot, sbUID); err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}
	promptFile := filepath.Join(workspaceRoot, "prompt-dryrun.md")
	if a.sandbox != nil {
		if err := a.sandbox.WriteFile(ctx, promptFile, []byte(buf.String()), 0o644, sbUID); err != nil {
			return nil, fmt.Errorf("write prompt file: %w", err)
		}
	} else {
		if err := os.WriteFile(promptFile, []byte(buf.String()), 0o644); err != nil {
			return nil, fmt.Errorf("write prompt file: %w", err)
		}
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("[prompt-dryrun.md](%s)", promptFile),
	}, nil
}

// handleNewSession handles /new command: archive memory first, then clear session
func (a *Agent) handleNewSession(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	llmClient, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	messages, err := tenantSession.GetMessages()
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取会话消息失败，请重试。",
		}, nil
	}
	lastConsolidated := tenantSession.LastConsolidated()
	mem := tenantSession.Memory()

	// Take unmerged messages for archiving
	snapshot := messages
	if lastConsolidated < len(messages) {
		snapshot = messages[lastConsolidated:]
	}

	if len(snapshot) > 0 {
		log.Ctx(ctx).WithField("tenant", tenantSession.String()).Infof("/new: archiving %d unconsolidated messages", len(snapshot))
		result, _ := mem.Memorize(ctx, memory.MemorizeInput{
			Messages:         snapshot,
			LastConsolidated: 0,
			LLMClient:        llmClient,
			Model:            model,
			ArchiveAll:       true,
		})
		if !result.OK {
			return &bus.OutboundMessage{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "Memory归档失败，会话未重置，请重试。",
			}, nil
		}
	}

	if err := tenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to clear tenant session")
	}
	if err := tenantSession.SetLastConsolidated(0); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to reset last consolidated")
	}

	// Clear memory consolidation state, cancel in-progress consolidation tasks (multi-path coordination)
	tenantKey := msg.Channel + ":" + msg.ChatID

	// Clean up offload data
	if a.offloadStore != nil {
		a.offloadStore.CleanSession(tenantKey)
	}
	// Clean up mask data
	if a.maskStore != nil {
		a.maskStore.Clear()
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "会话已重置，Memory已归档。",
	}, nil
}
