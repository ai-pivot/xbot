package web

// web_memory.go — 长期记忆面板的三个接口。
//
// 前端面板（web/src/memory/MemoryPanel.tsx）需要：列出、编辑、删除。
// 与 memory_* 工具走同一套底层（XbotMemory.ListMemories / UpdateMemory /
// DeleteMemory），所以写入闸门（长度 / 注入模式 / 协议控制标签 / importance 钳制
// / 内容级去重）在面板里同样生效 —— 面板不是绕过闸门的后门。
//
// 路由：
//	GET  /api/memory?type=all&limit=100   列出
//	POST /api/memory/update               编辑
//	POST /api/memory/delete               删除（单条）

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// maxMemoryListLimit 单次列出的上限（与 XbotMemory 的内部限制保持一致）。
const maxMemoryListLimit = 500

// handleMemoryList 列出当前会话所属 owner 的长期记忆。
func (wc *WebChannel) handleMemoryList(w http.ResponseWriter, r *http.Request) {
	if wc.callbacks.MemoryList == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "memory panel unavailable")
		return
	}
	q := r.URL.Query()
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, q.Get("channel"), q.Get("chat_id"))
	if !ok {
		return
	}
	memType := q.Get("type")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	if limit > maxMemoryListLimit {
		limit = maxMemoryListLimit
	}
	items, err := wc.callbacks.MemoryList(sel, memType, limit)
	if err != nil {
		writeStructuredError(w, err)
		return
	}
	if items == nil {
		items = nil // 保持 JSON 为 [] 而不是 null
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "total": len(items)})
}

// handleMemoryUpdate 编辑一条长期记忆。
func (wc *WebChannel) handleMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if wc.callbacks.MemoryUpdate == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "memory panel unavailable")
		return
	}
	var input struct {
		Channel    string  `json:"channel"`
		ChatID     string  `json:"chat_id"`
		ID         int64   `json:"id"`
		Content    string  `json:"content"`
		Keywords   string  `json:"keywords"`
		Tags       string  `json:"tags"`
		Importance float64 `json:"importance"`
	}
	if err := decodeJSONBody(r, &input, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.ID <= 0 {
		jsonErrorResponse(w, http.StatusBadRequest, "id is required")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, input.Channel, input.ChatID)
	if !ok {
		return
	}
	if err := wc.callbacks.MemoryUpdate(sel, input.ID, input.Content, input.Keywords, input.Tags, input.Importance); err != nil {
		writeStructuredError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMemoryDelete 删除一条长期记忆。
func (wc *WebChannel) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if wc.callbacks.MemoryDelete == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "memory panel unavailable")
		return
	}
	var input struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
		ID      int64  `json:"id"`
	}
	if err := decodeJSONBody(r, &input, true); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.ID <= 0 {
		jsonErrorResponse(w, http.StatusBadRequest, "id is required")
		return
	}
	senderID := senderIDFromContext(r.Context())
	sel, ok := wc.resolveAPISession(w, r, senderID, input.Channel, input.ChatID)
	if !ok {
		return
	}
	if err := wc.callbacks.MemoryDelete(sel, input.ID); err != nil {
		writeStructuredError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// 确保 encoding/json 被用到（decodeJSONBody 已处理，这里只是防止未来重构漏 import）。
var _ = json.Marshal
