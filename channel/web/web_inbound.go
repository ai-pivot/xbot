package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"xbot/bus"
	log "xbot/logger"
	"xbot/protocol"

	"github.com/google/uuid"
)

var (
	errInboundUnavailable = errors.New("message bus unavailable")
	errEmptyMessage       = errors.New("content or upload_keys is required")
)

const inboundRequestRetention = 10 * time.Minute

type inboundRequestState struct {
	done        chan struct{}
	sel         SessionSelector
	msgID       int64
	ts          time.Time
	turnID      uint64
	queued      bool
	err         error
	completedAt time.Time
}

type inboundRequestKey struct {
	senderID  string
	channel   string
	chatID    string
	requestID string
}

type inboundIdentity struct {
	SenderID           string
	SenderName         string
	WebUserID          int
	FeishuUserID       string
	CanonicalUserID    int64
	CanonicalRole      string
	IsCLI              bool
	OverrideSenderID   string
	OverrideSenderName string
}

// withPhysicalChannel injects the physical channel (the channel the user is
// actually connected through) into an InboundMessage's metadata map.
//
// When a web user browses a CLI-created session, msg.Channel is "cli" (the
// session's origin channel), but the user is on "web". Channel-scoped tools
// (like display_html) must resolve against the physical channel, not the
// session origin. The agent's buildMainRunConfig reads this metadata key to
// override the sessionKey's channel prefix for tool resolution.
//
// Call this on every metadata map before constructing an InboundMessage.
// The caller is responsible for ensuring metadata is non-nil (all existing
// call sites pass map[string]string{...}).
func withPhysicalChannel(metadata map[string]string, isCLI bool) {
	if !isCLI {
		metadata["physical_channel"] = "web"
	}
}

func (wc *WebChannel) inboundIdentityFromRequest(r *http.Request) inboundIdentity {
	identity := inboundIdentity{
		SenderID:  senderIDFromContext(r.Context()),
		WebUserID: userIDFromContext(r.Context()),
	}
	if si, ok := webSessionFromContext(r.Context()); ok {
		identity.SenderName = si.username
		identity.FeishuUserID = si.feishuUserID
	}
	if identity.SenderName == "" {
		identity.SenderName = identity.SenderID
	}
	if userID, role, ok := canonicalIdentityFromContext(r.Context()); ok {
		identity.CanonicalUserID = userID
		identity.CanonicalRole = role
	} else if wc.callbacks.IdentityResolver != nil {
		resolveChannel := "web"
		if identity.FeishuUserID != "" {
			resolveChannel = "feishu"
		}
		resolveID := identity.SenderID
		if identity.FeishuUserID != "" {
			resolveID = identity.FeishuUserID
		}
		identity.CanonicalUserID, identity.CanonicalRole, _ = wc.callbacks.IdentityResolver.Resolve(resolveChannel, resolveID)
	}
	// In single-user mode, all users share one identity and are treated as admin.
	if wc.singleUser {
		identity.CanonicalRole = "admin"
	}
	return identity
}

func (wc *WebChannel) resolveInboundSession(ctx context.Context, identity inboundIdentity, channelName, chatID string) (SessionSelector, error) {
	sel := wc.GetCurrentSession(identity.SenderID)
	if channelName != "" && chatID != "" {
		sel = SessionSelector{Channel: channelName, ChatID: chatID}
	}
	access := sessionAccessIdentity{
		senderID:        identity.SenderID,
		webUserID:       identity.WebUserID,
		canonicalUserID: identity.CanonicalUserID,
		canonicalRole:   identity.CanonicalRole,
	}
	if identity.IsCLI && sel.Channel == "cli" {
		if _, agentShaped := parseWebAgentTenantChatID(sel.ChatID); agentShaped {
			return SessionSelector{}, fmt.Errorf("access denied")
		}
	}
	allowed := wc.canAccessSessionAs(access, sel.Channel, sel.ChatID)
	if !allowed && identity.IsCLI && sel.Channel == "cli" {
		allowed = wc.claimCLIClientSession(sel.ChatID, identity.CanonicalUserID)
	}
	if !allowed {
		return SessionSelector{}, fmt.Errorf("access denied")
	}
	return sel, nil
}

func (wc *WebChannel) dispatchUserMessage(ctx context.Context, identity inboundIdentity, msg protocol.WSClientMessage) (SessionSelector, int64, time.Time, uint64, bool, error) {
	if strings.TrimSpace(msg.Content) == "" && len(msg.UploadKeys) == 0 {
		return SessionSelector{}, 0, time.Time{}, 0, false, errEmptyMessage
	}

	sel, err := wc.resolveInboundSession(ctx, identity, msg.Channel, msg.ChatID)
	if err != nil {
		return SessionSelector{}, 0, time.Time{}, 0, false, err
	}
	msg.ID = strings.TrimSpace(msg.ID)
	if msg.ID == "" {
		return wc.dispatchResolvedUserMessage(ctx, identity, sel, msg)
	}
	key := inboundRequestKey{
		senderID:  identity.SenderID,
		channel:   sel.Channel,
		chatID:    sel.ChatID,
		requestID: msg.ID,
	}
	sel2, msgID, ts, turnID, queued, err := wc.dispatchUserMessageOnce(ctx, key, func() (SessionSelector, int64, time.Time, uint64, bool, error) {
		return wc.dispatchResolvedUserMessage(ctx, identity, sel, msg)
	})
	return sel2, msgID, ts, turnID, queued, err
}

func (wc *WebChannel) dispatchResolvedUserMessage(ctx context.Context, identity inboundIdentity, sel SessionSelector, msg protocol.WSClientMessage) (SessionSelector, int64, time.Time, uint64, bool, error) {

	originalContent := msg.Content
	content := wc.expandUploadKeys(msg)
	metadata := map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional}
	withPhysicalChannel(metadata, identity.IsCLI)
	if identity.FeishuUserID != "" {
		metadata["feishu_user_id"] = identity.FeishuUserID
	}
	// Inject canonical user identity for agent layer.
	// Without this, ResolveUserContext re-resolves via (msg.Channel, senderID),
	// which misses when browsing a CLI session cross-channel (msg.Channel=="cli"
	// but the web user identity is registered under channel=="web") → userID=0
	// fallback → wrong LLM/subscription/settings + role downgrade.
	if identity.CanonicalUserID > 0 {
		metadata["user_id"] = strconv.FormatInt(identity.CanonicalUserID, 10)
		metadata["user_role"] = identity.CanonicalRole
	}

	msgSenderID := identity.SenderID
	msgSenderName := identity.SenderName
	msgChatType := "p2p"
	if identity.IsCLI {
		if msg.SenderID != "" {
			msgSenderID = msg.SenderID
		}
		if msg.SenderName != "" {
			msgSenderName = msg.SenderName
		}
		if msg.ChatType != "" {
			msgChatType = msg.ChatType
		}
	}

	requestID := msg.ID
	if requestID == "" {
		requestID = strings.ReplaceAll(uuid.New().String(), "-", "")
	}
	receivedAt := time.Now()

	// History persistence is Agent-owned after session operation-gate
	// admission. The transport must not pre-write the user message; the
	// agent loop persists it eagerly before running the turn. The REST
	// response therefore returns message_id=0 (the DB id is not yet known).
	var msgDBID int64

	res, err := wc.enqueueInbound(ctx, bus.InboundMessage{
		Channel:    sel.Channel,
		SenderID:   msgSenderID,
		SenderName: msgSenderName,
		ChatID:     sel.ChatID,
		ChatType:   msgChatType,
		Content:    content,
		Time:       receivedAt,
		RequestID:  requestID,
		From:       bus.NewIMAddress(sel.Channel, msgSenderID),
		Metadata:   metadata,
	})
	if err != nil {
		return sel, 0, time.Time{}, 0, false, err
	}

	// The agent persists accepted user messages before running the turn. Echo
	// expanded attachments only after queue admission so failed requests leave
	// neither replay events nor phantom history.
	if content != originalContent && len(msg.UploadKeys) > 0 {
		wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{
			Type:            protocol.MsgTypeUserEcho,
			ID:              requestID,
			Content:         content,
			OriginalContent: originalContent,
			TS:              receivedAt.Unix(),
		})
	}
	// res.TurnID is the per-session turn id allocated at queue-admission time
	// (by agent.chatWorker.admitToMsgCh); res.Queued reports whether the chat
	// was already busy processing an earlier message. Both are returned to the
	// REST layer so the API response can carry them directly.
	return sel, msgDBID, receivedAt, res.TurnID, res.Queued, nil
}

func (wc *WebChannel) dispatchUserMessageOnce(ctx context.Context, key inboundRequestKey, fn func() (SessionSelector, int64, time.Time, uint64, bool, error)) (SessionSelector, int64, time.Time, uint64, bool, error) {
	now := time.Now()
	wc.inboundRequestsMu.Lock()
	for existingKey, state := range wc.inboundRequests {
		if !state.completedAt.IsZero() && now.Sub(state.completedAt) > inboundRequestRetention {
			delete(wc.inboundRequests, existingKey)
		}
	}
	if state, ok := wc.inboundRequests[key]; ok {
		wc.inboundRequestsMu.Unlock()
		select {
		case <-state.done:
			return state.sel, state.msgID, state.ts, state.turnID, state.queued, state.err
		case <-ctx.Done():
			return SessionSelector{}, 0, time.Time{}, 0, false, ctx.Err()
		}
	}
	state := &inboundRequestState{done: make(chan struct{})}
	wc.inboundRequests[key] = state
	wc.inboundRequestsMu.Unlock()

	sel, msgID, ts, turnID, queued, err := fn()
	wc.inboundRequestsMu.Lock()
	state.sel = sel
	state.msgID = msgID
	state.ts = ts
	state.turnID = turnID
	state.queued = queued
	state.err = err
	state.completedAt = time.Now()
	if err != nil {
		delete(wc.inboundRequests, key)
	}
	close(state.done)
	wc.inboundRequestsMu.Unlock()
	return sel, msgID, ts, turnID, queued, err
}

func (wc *WebChannel) expandUploadKeys(msg protocol.WSClientMessage) string {
	content := msg.Content
	if len(msg.UploadKeys) == 0 || wc.ossProvider == nil {
		return content
	}
	for i, key := range msg.UploadKeys {
		displayName := key
		if i < len(msg.FileNames) && msg.FileNames[i] != "" {
			displayName = filepath.Base(msg.FileNames[i])
		}
		var fileSize int64
		if i < len(msg.FileSizes) {
			fileSize = msg.FileSizes[i]
		}
		downloadURL, err := wc.ossProvider.GetDownloadURL(key)
		if err != nil {
			log.WithError(err).WithField("key", key).Warn("Failed to get download URL for OSS file")
			content += fmt.Sprintf("\n\n📎 [用户上传文件: %s] (获取下载链接失败)", displayName)
			continue
		}
		ext := strings.ToLower(filepath.Ext(displayName))
		if isImageExt(ext) {
			content += fmt.Sprintf("\n\n<image url=\"%s\" name=\"%s\" size=\"%d\" />\n![%s](%s)", downloadURL, displayName, fileSize, displayName, downloadURL)
		} else {
			content += fmt.Sprintf("\n\n<file name=\"%s\" url=\"%s\" size=\"%d\" />", displayName, downloadURL, fileSize)
		}
	}
	return content
}

func (wc *WebChannel) dispatchCancel(ctx context.Context, identity inboundIdentity, channelName, chatID string) (SessionSelector, error) {
	sel, err := wc.resolveInboundSession(ctx, identity, channelName, chatID)
	if err != nil {
		return SessionSelector{}, err
	}
	msgSenderID := identity.SenderID
	msgSenderName := identity.SenderName
	if identity.IsCLI {
		if identity.OverrideSenderID != "" {
			msgSenderID = identity.OverrideSenderID
		}
		if identity.OverrideSenderName != "" {
			msgSenderName = identity.OverrideSenderName
		}
	}
	cancelMeta := map[string]string{}
	withPhysicalChannel(cancelMeta, identity.IsCLI)
	_, err = wc.enqueueInbound(ctx, bus.InboundMessage{
		Channel:    sel.Channel,
		SenderID:   msgSenderID,
		SenderName: msgSenderName,
		ChatID:     sel.ChatID,
		ChatType:   "p2p",
		Content:    "/cancel",
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		From:       bus.NewIMAddress(sel.Channel, msgSenderID),
		Metadata:   cancelMeta,
	})
	return sel, err
}

func (wc *WebChannel) dispatchAskUserResponse(ctx context.Context, identity inboundIdentity, channelName, chatID string, response protocol.AskUserResponse) (SessionSelector, error) {
	sel, err := wc.resolveInboundSession(ctx, identity, channelName, chatID)
	if err != nil {
		return SessionSelector{}, err
	}
	if response.Cancelled {
		return wc.dispatchCancel(ctx, identity, sel.Channel, sel.ChatID)
	}
	if len(response.Answers) == 0 {
		return SessionSelector{}, fmt.Errorf("answer is required")
	}
	parts := make([]string, 0, len(response.Answers))
	for questionID, answer := range response.Answers {
		parts = append(parts, fmt.Sprintf("Q%s: %s", questionID, answer))
	}
	_, err = wc.enqueueInbound(ctx, bus.InboundMessage{
		Channel:    sel.Channel,
		SenderID:   identity.SenderID,
		SenderName: identity.SenderName,
		ChatID:     sel.ChatID,
		ChatType:   "p2p",
		Content:    strings.Join(parts, "\n\n"),
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		From:       bus.NewIMAddress(sel.Channel, identity.SenderID),
		Metadata: func() map[string]string {
			m := map[string]string{"ask_user_answered": "true"}
			withPhysicalChannel(m, identity.IsCLI)
			return m
		}(),
	})
	return sel, err
}

func (wc *WebChannel) enqueueInbound(ctx context.Context, message bus.InboundMessage) (bus.DeliveryResult, error) {
	if wc.msgBus == nil {
		return bus.DeliveryResult{}, errInboundUnavailable
	}
	var deliveryAck chan bus.DeliveryResult
	if wc.msgBus.DeliveryAcknowledgementEnabled() {
		deliveryAck = make(chan bus.DeliveryResult, 1)
		message.DeliveryAck = deliveryAck
	}
	select {
	case wc.msgBus.Inbound <- message:
		if deliveryAck == nil {
			return bus.DeliveryResult{}, nil
		}
	case <-ctx.Done():
		return bus.DeliveryResult{}, ctx.Err()
	case <-wc.stopCh:
		return bus.DeliveryResult{}, errInboundUnavailable
	}
	// Wait for the agent's ack. Do NOT select on ctx.Done() here: once the
	// message has been handed off to the bus (above), it is admitted to the
	// agent's per-chat queue and WILL be processed. Returning context.Canceled
	// here would tell the frontend the send failed, triggering a same-ID retry
	// that the dedup layer would swallow — silently losing the user message.
	// The transport's own request timeout / channel stop are the only exits.
	select {
	case res := <-deliveryAck:
		return res, res.Err
	case <-wc.stopCh:
		return bus.DeliveryResult{}, errInboundUnavailable
	}
}
