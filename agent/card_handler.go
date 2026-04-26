package agent

import (
	"context"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
)

// handleCardResponse Handle card responses (button clicks, form submissions)
func (a *Agent) handleCardResponse(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	cardID := msg.Metadata["card_id"]
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
		"card_id": cardID,
	}).Info("Processing card response")

	// Inject card context so LLM understands what the user is responding to
	summary := msg.Content
	if desc := a.cardBuilder.GetDescription(cardID); desc != "" {
		summary = desc + "\nUser interaction:\n" + summary
	}

	// Cleanup card metadata after callback is processed
	defer a.cardBuilder.CleanupCard(cardID)

	// Reuse buildPrompt, replace Content with card summary
	cardMsg := msg
	cardMsg.Content = summary
	messages, err := a.buildPrompt(ctx, cardMsg, tenantSession)
	if err != nil {
		return nil, err
	}

	cardCfg := a.buildMainRunConfig(ctx, msg, messages, tenantSession, true)
	cardOut := Run(ctx, cardCfg)
	if cardOut.Error != nil {
		return nil, cardOut.Error
	}
	finalContent := cardOut.Content
	waitingUser := cardOut.WaitingUser

	if waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, skipping reply")
		return nil, nil
	}

	cardUserMsg := llm.NewUserMessage(summary)
	if !msg.Time.IsZero() {
		cardUserMsg.Timestamp = msg.Time
	}
	if err := tenantSession.AddMessage(cardUserMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
	}
	assistantMsg := llm.NewAssistantMessage(finalContent)
	assistantMsg.ReasoningContent = cardOut.ReasoningContent
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save assistant message")
	}

	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent); err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to send card response via sendMessage")
		return &bus.OutboundMessage{
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Content:  finalContent,
			Metadata: msg.Metadata,
		}, nil
	}
	return nil, nil
}
