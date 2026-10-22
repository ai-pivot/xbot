package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xbot/protocol"
	"xbot/storage/sqlite"
)

// web_group.go — 「Agent 通信指挥室」最小后端。
//
// 只做可视化需要的最小集：
//   GET  /api/groups                       我可见的群列表
//   GET  /api/groups/{id}/members          成员（组织树）
//   GET  /api/groups/{id}/messages         当前会话视角的群消息（keyset 游标）
//   POST /api/groups                       创建群（演示用）
//   POST /api/groups/{id}/members          加成员（演示用）
//   POST /api/groups/{id}/messages         追加一条群消息（演示 + group.message 事件验证）
//
// 权限：群列表按 owner/alive 成员过滤；消息读取要求调用者是 owner 或 alive 成员，
// 并且只能读「自己可访问的会话库」（显式 channel/chat_id 时走 resolveAPISession
// 的同一套 canAccessSession 校验）。绝不跨库聚合。

// groupMemberAddr 把 web 登录身份映射为群成员地址。群成员既可以是 agent
// （agent:<role>/<instance>），也可以是用户（web-<id>）。
func groupMemberAddr(senderID string) string { return senderID }

func (wc *WebChannel) handleGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	groups, err := wc.callbacks.GroupList(groupMemberAddr(senderID))
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": groups})
}

func (wc *WebChannel) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	var body struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if err := decodeJSONBody(r, &body, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = "未命名群组"
	}
	group, err := wc.callbacks.GroupCreate(groupMemberAddr(senderID), body.Name, body.Members)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sqlite.ErrGroupExists) {
			status = http.StatusConflict
		}
		jsonErrorResponse(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": group})
}

func (wc *WebChannel) handleGroupMembers(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	senderID := senderIDFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		members, err := wc.callbacks.GroupMembers(groupMemberAddr(senderID), groupID)
		if err != nil {
			jsonErrorResponse(w, groupErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "members": members})
	case http.MethodPost:
		var body struct {
			MemberAddr string `json:"member_addr"`
		}
		if err := decodeJSONBody(r, &body, false); err != nil || strings.TrimSpace(body.MemberAddr) == "" {
			jsonErrorResponse(w, http.StatusBadRequest, "member_addr is required")
			return
		}
		member, err := wc.callbacks.GroupAddMember(groupMemberAddr(senderID), groupID, body.MemberAddr)
		if err != nil {
			jsonErrorResponse(w, groupErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "member": member})
	default:
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (wc *WebChannel) handleGroupMessages(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	senderID := senderIDFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 50
		}
		channelName := r.URL.Query().Get("channel")
		chatID := r.URL.Query().Get("chat_id")
		if channelName == "" && chatID == "" {
			// 默认「调用者当前会话视角」。
			sel := wc.GetCurrentSession(senderID)
			channelName, chatID = sel.Channel, sel.ChatID
		} else {
			// 显式会话视角：复用统一的会话可访问性校验（admin 豁免/属主校验）。
			sel, ok := wc.resolveAPISession(w, r, senderID, channelName, chatID)
			if !ok {
				return
			}
			channelName, chatID = sel.Channel, sel.ChatID
		}
		items, next, hasMore, err := wc.callbacks.GroupMessages(
			groupMemberAddr(senderID), groupID, channelName, chatID, cursor, limit)
		if err != nil {
			jsonErrorResponse(w, groupErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"items":       items,
			"next_cursor": next,
			"has_more":    hasMore,
			"channel":     channelName,
			"chat_id":     chatID,
		})
	case http.MethodPost:
		// 演示写入：真实群发路径会遍历成员各写一份；这里写当前会话一份并推送事件。
		var body struct {
			Kind          string         `json:"kind"`
			CorrelationID string         `json:"correlation_id"`
			Content       string         `json:"content"`
			SenderType    string         `json:"sender_type"`
			SenderID      string         `json:"sender_id"`
			SenderName    string         `json:"sender_name"`
			To            []string       `json:"to"`
			Payload       map[string]any `json:"payload"`
			Channel       string         `json:"channel"`
			ChatID        string         `json:"chat_id"`
		}
		if err := decodeJSONBody(r, &body, false); err != nil || strings.TrimSpace(body.Content) == "" {
			jsonErrorResponse(w, http.StatusBadRequest, "content is required")
			return
		}
		if body.Kind == "" {
			body.Kind = "text"
		}
		if body.SenderType == "" {
			body.SenderType = "user"
		}
		if body.SenderID == "" {
			body.SenderID = senderID
		}
		if body.SenderName == "" {
			body.SenderName = body.SenderID
		}
		if body.Channel != "" || body.ChatID != "" {
			sel, ok := wc.resolveAPISession(w, r, senderID, body.Channel, body.ChatID)
			if !ok {
				return
			}
			body.Channel, body.ChatID = sel.Channel, sel.ChatID
		}
		msg, err := wc.callbacks.GroupAppendMessage(groupMemberAddr(senderID), groupID,
			body.Channel, body.ChatID, protocol.GroupMessageInput{
				Kind:          body.Kind,
				CorrelationID: body.CorrelationID,
				Content:       body.Content,
				SenderType:    body.SenderType,
				SenderID:      body.SenderID,
				SenderName:    body.SenderName,
				To:            body.To,
				Payload:       body.Payload,
			})
		if err != nil {
			jsonErrorResponse(w, groupErrorStatus(err), err.Error())
			return
		}
		// 事件推送（复用现有 WS/SSE 通道；不落盘、不做 replay）。
		wc.PublishGroupMessage(groupID, msg)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": msg})
	default:
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// PublishGroupMessage 向所有 Web 客户端广播 group.message（可视化面板订阅）。
//
// 群消息本身已写进会话库；事件只负责「实时追加」——掉线后客户端用
// GET /api/groups/{id}/messages 补齐，不做事件重放。
func (wc *WebChannel) PublishGroupMessage(groupID string, msg *protocol.GroupMessage) {
	if wc == nil || wc.hub == nil || msg == nil {
		return
	}
	payload, err := json.Marshal(protocol.GroupMessageEvent{GroupID: groupID, Message: msg})
	if err != nil {
		return
	}
	wc.hub.BroadcastToWeb(protocol.WSMessage{
		Type:   protocol.MsgTypeGroupMessage,
		TS:     time.Now().UnixMilli(),
		Result: json.RawMessage(payload),
	})
}

// registerGroupRoutes 注册通信视图的全部 REST 路由。
//
// 约定（docs/design/group-visualization.md §3.4 与本文件顶部注释）：
//
//	GET  /api/groups                  → 群列表
//	POST /api/groups                  → 建群
//	GET  /api/groups/{id}/members     → 成员
//	GET  /api/groups/{id}/messages    → 群消息（当前会话视角）
//
// 2026-10-22 修复：此前只注册了 `/api/groups/create`，按文档/前端契约调用
// `POST /api/groups` 会掉进只允许 GET 的 handleGroups → 405，面板与脚本都建不了群。
// Go 1.22+ method pattern 让 POST 落到建群处理器，同时保留 authenticatedPOST 的
// postOnly + 鉴权包装（不绕过方法校验与鉴权）。
func (wc *WebChannel) registerGroupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/groups", wc.authMiddleware(wc.handleGroups))
	mux.HandleFunc("POST /api/groups", wc.authenticatedPOST(wc.handleGroupCreate))
	mux.HandleFunc("/api/groups/create", wc.authenticatedPOST(wc.handleGroupCreate))
	mux.HandleFunc("/api/groups/{id}/members", wc.authMiddleware(wc.handleGroupMembers))
	mux.HandleFunc("/api/groups/{id}/messages", wc.authMiddleware(wc.handleGroupMessages))
}

func groupErrorStatus(err error) int {
	switch {
	case errors.Is(err, sqlite.ErrGroupNotFound):
		return http.StatusNotFound
	case errors.Is(err, sqlite.ErrNotGroupMember):
		return http.StatusForbidden
	case errors.Is(err, sqlite.ErrGroupExists):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
