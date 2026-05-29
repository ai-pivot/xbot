package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/plugin"
)

// ---------------------------------------------------------------------------
// grpcChannelBridge — wraps a gRPC plugin process as channel.Channel
//
// Phase 2: uses bidirectional multiplexer for inbound push.
// The plugin pushes "channel_inbound" messages to xbot via stdout.
// The readLoop in GrpcPluginProcess routes them to our InboundHandler.
// No polling needed.
// ---------------------------------------------------------------------------

type grpcChannelBridge struct {
	process *plugin.GrpcPluginProcess
	decl    *plugin.ChannelProviderDecl
	msgBus  *bus.MessageBus
	stopCh  chan struct{}
}

// Compile-time checks
var _ channel.ChannelProvider = (*grpcChannelBridge)(nil)
var _ channel.Channel = (*grpcChannelBridge)(nil)
var _ channel.ChannelHistoryProvider = (*grpcChannelBridge)(nil)
var _ channel.ChannelUpdateProvider = (*grpcChannelBridge)(nil)

// --- channel.ChannelProvider interface ---

func (b *grpcChannelBridge) Name() string {
	return b.decl.Name
}

func (b *grpcChannelBridge) CreateChannel(cfg map[string]string, msgBus *bus.MessageBus) (channel.Channel, error) {
	b.msgBus = msgBus
	b.stopCh = make(chan struct{})

	// Register our inbound handler — readLoop will call it for "channel_inbound" messages.
	b.process.SetInboundHandler(b.handleInbound)

	// Tell the plugin to start the channel.
	resp, err := b.process.Call(context.Background(), &plugin.PluginRequest{
		Method: "channel_start",
		Params: map[string]any{"config": cfg},
	})
	if err != nil {
		return nil, fmt.Errorf("channel_start call failed: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("channel_start error: %s", resp.Error)
	}

	return b, nil
}

func (b *grpcChannelBridge) ConfigSchema() []channel.SettingDefinition {
	schema := make([]channel.SettingDefinition, 0, len(b.decl.ConfigSchema))
	for _, s := range b.decl.ConfigSchema {
		sd := channel.SettingDefinition{
			Key:          strVal(s["key"]),
			Label:        strVal(s["label"]),
			Description:  strVal(s["description"]),
			Type:         channel.SettingType(strVal(s["type"])),
			DefaultValue: strVal(s["default_value"]),
			Category:     strVal(s["category"]),
		}
		if v, ok := s["read_only"]; ok {
			sd.ReadOnly = boolVal(v)
		}
		if opts, ok := s["options"].([]any); ok {
			for _, o := range opts {
				if m, ok := o.(map[string]any); ok {
					sd.Options = append(sd.Options, channel.SettingOption{
						Label: strVal(m["label"]),
						Value: strVal(m["value"]),
					})
				}
			}
		}
		schema = append(schema, sd)
	}
	return schema
}

func (b *grpcChannelBridge) IsEnabled(cfg map[string]string) bool {
	if cfg == nil {
		return false
	}
	return cfg["enabled"] == "true"
}

// --- channel.Channel interface ---

func (b *grpcChannelBridge) Start() error {
	return nil
}

func (b *grpcChannelBridge) Stop() {
	if b.stopCh != nil {
		close(b.stopCh)
	}
	b.process.Call(context.Background(), &plugin.PluginRequest{
		Method: "channel_stop",
	})
}

func (b *grpcChannelBridge) Send(msg channel.OutboundMsg) (string, error) {
	params := map[string]any{
		"chat_id":      msg.ChatID,
		"content":      msg.Content,
		"is_partial":   msg.IsPartial,
		"waiting_user": msg.WaitingUser,
	}
	if msg.Metadata != nil {
		params["metadata"] = msg.Metadata
	}
	if len(msg.ToolsUsed) > 0 {
		params["tools_used"] = msg.ToolsUsed
	}
	if len(msg.Media) > 0 {
		params["media"] = msg.Media
	}

	resp, err := b.process.Call(context.Background(), &plugin.PluginRequest{
		Method: "channel_send",
		Params: params,
	})
	if err != nil {
		return "", fmt.Errorf("channel_send: %w", err)
	}
	if resp.Error != "" {
		return "", fmt.Errorf("channel_send error: %s", resp.Error)
	}
	return resp.Result, nil
}

// --- channel.ChannelHistoryProvider ---

func (b *grpcChannelBridge) LoadHistory(ctx context.Context, chatID string, limit int) ([]channel.PlatformMessage, error) {
	resp, err := b.process.Call(ctx, &plugin.PluginRequest{
		Method: "channel_history",
		Params: map[string]any{"chat_id": chatID, "limit": limit},
	})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("channel_history error: %s", resp.Error)
	}
	if resp.Result == "" || resp.Result == "null" || resp.Result == "[]" {
		return nil, nil
	}

	var msgs []channel.PlatformMessage
	if err := json.Unmarshal([]byte(resp.Result), &msgs); err != nil {
		return nil, fmt.Errorf("parse history: %w", err)
	}
	return msgs, nil
}

// --- channel.ChannelUpdateProvider ---

func (b *grpcChannelBridge) UpdateMessage(ctx context.Context, chatID, messageID, newContent string) error {
	resp, err := b.process.Call(ctx, &plugin.PluginRequest{
		Method: "channel_update_message",
		Params: map[string]any{"chat_id": chatID, "message_id": messageID, "new_content": newContent},
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("channel_update_message error: %s", resp.Error)
	}
	return nil
}

func (b *grpcChannelBridge) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	resp, err := b.process.Call(ctx, &plugin.PluginRequest{
		Method: "channel_delete_message",
		Params: map[string]any{"chat_id": chatID, "message_id": messageID},
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("channel_delete_message error: %s", resp.Error)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Inbound handler — called by readLoop when plugin pushes channel_inbound
// ---------------------------------------------------------------------------

// handleInbound is the InboundHandler registered on the GrpcPluginProcess.
// It processes "channel_inbound" messages from the plugin and routes them
// to bus.Inbound.
func (b *grpcChannelBridge) handleInbound(msg *plugin.PluginInbound) {
	if msg.Method != "channel_inbound" {
		return
	}
	if b.msgBus == nil {
		return
	}

	// Parse messages array from params.
	// Single message: {"method":"channel_inbound","params":{"chat_id":"...","content":"..."}}
	// Batch: {"method":"channel_inbound","params":{"messages":[{...},{...}]}}
	if rawMessages, ok := msg.Params["messages"]; ok {
		// Batch mode
		if arr, ok := rawMessages.([]any); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					b.pushInboundMessage(m)
				}
			}
		}
	} else {
		// Single message mode — params IS the message
		b.pushInboundMessage(msg.Params)
	}
}

func (b *grpcChannelBridge) pushInboundMessage(params map[string]any) {
	chatID, _ := params["chat_id"].(string)
	senderID, _ := params["sender_id"].(string)
	senderName, _ := params["sender_name"].(string)
	content, _ := params["content"].(string)
	chatType, _ := params["chat_type"].(string)
	if chatType == "" {
		chatType = "p2p"
	}

	// Parse media if present
	var media []string
	if rawMedia, ok := params["media"].([]any); ok {
		for _, m := range rawMedia {
			if s, ok := m.(string); ok {
				media = append(media, s)
			}
		}
	}

	b.msgBus.Inbound <- bus.InboundMessage{
		Channel:    b.decl.Name,
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     chatID,
		ChatType:   chatType,
		Content:    content,
		Media:      media,
		Time:       time.Now(),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func strVal(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func boolVal(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
