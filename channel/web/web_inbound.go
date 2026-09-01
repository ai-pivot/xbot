package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
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

// isInterjected reports whether a ⚡ interject with this requestID has already
// been injected into the active turn (CR#6: REST timeout retry with the same
// msg.ID must not re-inject — each attempt would deliver a fresh user_interrupt
// synthetic tool). TTL mirrors inboundRequestRetention.
func (wc *WebChannel) isInterjected(msgID string) bool {
	if msgID == "" {
		return false
	}
	now := time.Now()
	wc.interjectedRequestsMu.Lock()
	defer wc.interjectedRequestsMu.Unlock()
	for id, marked := range wc.interjectedRequests {
		if now.Sub(marked) > inboundRequestRetention {
			delete(wc.interjectedRequests, id)
		}
	}
	_, ok := wc.interjectedRequests[msgID]
	return ok
}

// markInterjected records a successful interject injection (dedup marker).
func (wc *WebChannel) markInterjected(msgID string) {
	if msgID == "" {
		return
	}
	wc.interjectedRequestsMu.Lock()
	defer wc.interjectedRequestsMu.Unlock()
	wc.interjectedRequests[msgID] = time.Now()
}

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
	}
	if identity.SenderName == "" {
		identity.SenderName = identity.SenderID
	}
	return identity
}

func (wc *WebChannel) resolveInboundSession(ctx context.Context, identity inboundIdentity, channelName, chatID string) (SessionSelector, error) {
	sel := wc.GetCurrentSession(identity.SenderID)
	if channelName != "" && chatID != "" {
		sel = SessionSelector{Channel: channelName, ChatID: chatID}
	}
	access := sessionAccessIdentity{
		senderID:  identity.SenderID,
		webUserID: identity.WebUserID,
	}
	if identity.IsCLI && sel.Channel == "cli" {
		if _, agentShaped := parseWebAgentTenantChatID(sel.ChatID); agentShaped {
			return SessionSelector{}, fmt.Errorf("access denied")
		}
	}
	allowed := wc.canAccessSessionAs(access, sel.Channel, sel.ChatID)
	if !allowed && identity.IsCLI && sel.Channel == "cli" {
		// CLI sessions are created by the local operator — no owner claim
		// needed (multi-user removal: one operator owns every session).
		allowed = true
	}
	if !allowed {
		return SessionSelector{}, fmt.Errorf("access denied")
	}
	return sel, nil
}

func (wc *WebChannel) dispatchUserMessage(ctx context.Context, identity inboundIdentity, msg protocol.WSClientMessage) (SessionSelector, int64, time.Time, uint64, bool, bool, error) {
	if strings.TrimSpace(msg.Content) == "" && len(msg.UploadKeys) == 0 {
		return SessionSelector{}, 0, time.Time{}, 0, false, false, errEmptyMessage
	}

	sel, err := wc.resolveInboundSession(ctx, identity, msg.Channel, msg.ChatID)
	if err != nil {
		return SessionSelector{}, 0, time.Time{}, 0, false, false, err
	}

	// ⚡ Interject: deliver into the ACTIVE turn instead of queueing. Bypasses
	// the session queue entirely — no turn_id, no user_echo, no user row: the
	// agent injects the message as a synthetic user_interrupt tool result at
	// the next tool boundary. Returns interrupted=true so the transport can
	// report it (the frontend renders it inside the live turn, not as a new
	// message). Idle sessions fall through to the normal queue path.
	if msg.Interrupt && wc.callbacks.InjectInterrupt != nil {
		content := wc.expandUploadKeys(msg)
		// CR#6/复审#2: REST timeout retry with the same msg.ID must not
		// re-inject — each attempt would deliver a fresh user_interrupt synthetic
		// tool into the active turn. Only the FIRST attempt injects; retries
		// return the cached interrupted=true outcome (idempotent).
		// Dedup key is session-scoped (channel|chatID|msgID) — an API client
		// reusing the same request id across DIFFERENT sessions must not have
		// its interject swallowed (mirrors inboundRequestKey's composition).
		interruptKey := sel.Channel + "|" + sel.ChatID + "|" + msg.ID
		if msg.ID != "" && wc.isInterjected(interruptKey) {
			return sel, 0, time.Now(), 0, false, true, nil
		}
		if wc.callbacks.InjectInterrupt(sel.Channel, sel.ChatID, identity.SenderID, content) {
			if msg.ID != "" {
				wc.markInterjected(interruptKey)
			}
			return sel, 0, time.Now(), 0, false, true, nil
		}
		// Session idle (or callback refused) — degrade to a normal send below.
		// NOT marked: the fall-through dispatch handles retries via its own
		// dispatchUserMessageOnce gate.
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
		// The interject path returns BEFORE the Once dedup (it never enqueues),
		// so the 7th return value (interrupted) is always false here — discard it.
		sel, msgID, ts, turnID, queued, _, err := wc.dispatchResolvedUserMessage(ctx, identity, sel, msg)
		return sel, msgID, ts, turnID, queued, err
	})
	return sel2, msgID, ts, turnID, queued, false, err
}

func (wc *WebChannel) dispatchResolvedUserMessage(ctx context.Context, identity inboundIdentity, sel SessionSelector, msg protocol.WSClientMessage) (SessionSelector, int64, time.Time, uint64, bool, bool, error) {

	originalContent := msg.Content
	content := wc.expandUploadKeys(msg)
	metadata := map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional}
	withPhysicalChannel(metadata, identity.IsCLI)

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
		return sel, 0, time.Time{}, 0, false, false, err
	}

	// The agent persists accepted user messages before running the turn. Echo
	// EVERY accepted user message back to the sender WITH its turn_id — the
	// frontend renders user messages deterministically from this echo (NO
	// optimistic rendering; turn_id is authoritative). Expanded attachments
	// keep the original content for display.
	//
	// QUEUED messages are NOT echoed: the v3 staging-tray design renders a
	// queued message ONLY in the tray (not in the message flow) — it enters
	// the flow when its turn starts, materialized from turn_started.content.
	// An echo here would re-create the message-flow row the tray design
	// deliberately removed. The queue_state SSE snapshot carries the tray
	// data instead.
	if res.TurnID > 0 && !res.Queued {
		wc.hub.sendToSession(sel.Channel, sel.ChatID, protocol.WSMessage{
			Type:            protocol.MsgTypeUserEcho,
			ID:              requestID,
			Content:         content,
			OriginalContent: originalContent,
			TS:              receivedAt.Unix(),
			TurnID:          res.TurnID,
		})
	} else if content != originalContent && len(msg.UploadKeys) > 0 {
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
	return sel, msgDBID, receivedAt, res.TurnID, res.Queued, false, nil
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
