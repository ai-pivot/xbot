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
	if wc.callbacks.IdentityResolver != nil {
		resolveChannel := "web"
		if identity.FeishuUserID != "" {
			resolveChannel = "feishu"
		}
		identity.CanonicalUserID, identity.CanonicalRole, _ = wc.callbacks.IdentityResolver.Resolve(resolveChannel, identity.SenderID)
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
	if !identity.IsCLI && !wc.canAccessSession(ctx, identity.WebUserID, identity.SenderID, sel.Channel, sel.ChatID) {
		return SessionSelector{}, fmt.Errorf("access denied")
	}
	return sel, nil
}

func (wc *WebChannel) dispatchUserMessage(ctx context.Context, identity inboundIdentity, msg protocol.WSClientMessage) (SessionSelector, error) {
	if strings.TrimSpace(msg.Content) == "" && len(msg.UploadKeys) == 0 {
		return SessionSelector{}, errEmptyMessage
	}

	sel, err := wc.resolveInboundSession(ctx, identity, msg.Channel, msg.ChatID)
	if err != nil {
		return SessionSelector{}, err
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
	return wc.dispatchUserMessageOnce(ctx, key, func() (SessionSelector, error) {
		return wc.dispatchResolvedUserMessage(ctx, identity, sel, msg)
	})
}

func (wc *WebChannel) dispatchResolvedUserMessage(ctx context.Context, identity inboundIdentity, sel SessionSelector, msg protocol.WSClientMessage) (SessionSelector, error) {

	originalContent := msg.Content
	content := wc.expandUploadKeys(msg)
	metadata := map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional}
	if identity.FeishuUserID != "" {
		metadata["feishu_user_id"] = identity.FeishuUserID
	}
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
	err := wc.enqueueInbound(ctx, bus.InboundMessage{
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
		return sel, err
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
	return sel, nil
}

func (wc *WebChannel) dispatchUserMessageOnce(ctx context.Context, key inboundRequestKey, fn func() (SessionSelector, error)) (SessionSelector, error) {
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
			return state.sel, state.err
		case <-ctx.Done():
			return SessionSelector{}, ctx.Err()
		}
	}
	state := &inboundRequestState{done: make(chan struct{})}
	wc.inboundRequests[key] = state
	wc.inboundRequestsMu.Unlock()

	sel, err := fn()
	wc.inboundRequestsMu.Lock()
	state.sel = sel
	state.err = err
	state.completedAt = time.Now()
	if err != nil {
		delete(wc.inboundRequests, key)
	}
	close(state.done)
	wc.inboundRequestsMu.Unlock()
	return sel, err
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
	return sel, wc.enqueueInbound(ctx, bus.InboundMessage{
		Channel:    sel.Channel,
		SenderID:   msgSenderID,
		SenderName: msgSenderName,
		ChatID:     sel.ChatID,
		ChatType:   "p2p",
		Content:    "/cancel",
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		From:       bus.NewIMAddress(sel.Channel, msgSenderID),
	})
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
	return sel, wc.enqueueInbound(ctx, bus.InboundMessage{
		Channel:    sel.Channel,
		SenderID:   identity.SenderID,
		SenderName: identity.SenderName,
		ChatID:     sel.ChatID,
		ChatType:   "p2p",
		Content:    strings.Join(parts, "\n\n"),
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		From:       bus.NewIMAddress(sel.Channel, identity.SenderID),
		Metadata:   map[string]string{"ask_user_answered": "true"},
	})
}

func (wc *WebChannel) enqueueInbound(ctx context.Context, message bus.InboundMessage) error {
	if wc.msgBus == nil {
		return errInboundUnavailable
	}
	var deliveryAck chan error
	if wc.msgBus.DeliveryAcknowledgementEnabled() {
		deliveryAck = make(chan error, 1)
		message.DeliveryAck = deliveryAck
	}
	select {
	case wc.msgBus.Inbound <- message:
		if deliveryAck == nil {
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-wc.stopCh:
		return errInboundUnavailable
	}
	select {
	case err := <-deliveryAck:
		return err
	case <-wc.stopCh:
		return errInboundUnavailable
	}
}
