package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"xbot/storage/common"
)

// web_activity.go — 跨会话活动流（通信视图性能重构）。
//
//	GET /api/activity?limit=200&active_within=72h&kind=report,decision
//	GET /api/agents?active_within=72h&include_unloaded=false
//
// 为什么要有这两个接口：会话按 chat 分库（one session one DB）后，前端跨会话看
// 消息只能逐会话 GET /api/messages —— 40 个会话/200 条 ≈ 8000 行 + 40 次 SQLite
// open，会话数一涨就线性恶化，调参无解。这里把「最近发生了什么」压成一次主库
// 查询（activity_index / interactive_agents 都在主库），代价与会话数无关。
//
// 铁律：本文件的处理路径**不许**出现 GetOrCreateSession / OpenSessionDB /
// sessionDB() —— 一旦出现，性能就退化成按会话数线性。守护测试见
// web_activity_test.go（源码断言 + 运行期「没有新会话库文件」断言）。

// handleActivity 返回最近的结构化活动流（新→旧）。
func (wc *WebChannel) handleActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if wc.callbacks.ActivityList == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "activity feed unavailable")
		return
	}
	q := r.URL.Query()
	limit := parseActivityLimit(q.Get("limit"))
	within := parseActivityWindow(q.Get("active_within"), common.ActivityWindowDefault)
	kinds, ok := parseActivityKinds(q.Get("kind"))
	if !ok {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid kind")
		return
	}
	items, hasMore, err := wc.callbacks.ActivityList(limit, within, kinds)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "activity query failed")
		return
	}
	if items == nil {
		items = []ActivityEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":         items,
		"has_more":      hasMore,
		"limit":         limit,
		"active_within": durationString(within),
	})
}

// handleAgentActivity 返回活跃子代理（默认排除 unloaded），按 updated_at 倒序。
func (wc *WebChannel) handleAgentActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if wc.callbacks.AgentActivityList == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "agents view unavailable")
		return
	}
	q := r.URL.Query()
	within := parseActivityWindow(q.Get("active_within"), common.ActivityWindowDefault)
	includeUnloaded := parseBoolParam(q.Get("include_unloaded"))
	items, err := wc.callbacks.AgentActivityList(within, includeUnloaded)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "agents query failed")
		return
	}
	if items == nil {
		items = []AgentActivityEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":            items,
		"total":            len(items),
		"active_within":    durationString(within),
		"include_unloaded": includeUnloaded,
	})
}

// parseActivityLimit 把 ?limit= 归一：空/非法/<=0 → 默认，> 上限 → 上限。
// 默认值与上限的唯一事实源在 storage/sqlite（服务层还会再归一一次，这里只是
// 让响应里的 limit 与实际查询口径一致）。
func parseActivityLimit(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return common.ActivityLimitDefault
	}
	return common.NormalizeActivityLimit(n)
}

// parseActivityWindow 解析 ?active_within=（"72h"、"30d"、空）。
//
// 支持 d（天）后缀是因为人看「90 天没动静」比「2160h」自然；非法值回落到默认
// 窗口而不是 400 —— 时间窗是过滤条件，不是必须精确的参数，宁可给出默认视图。
func parseActivityWindow(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if strings.HasSuffix(raw, "d") {
		if days, err := strconv.ParseFloat(strings.TrimSuffix(raw, "d"), 64); err == nil && days > 0 {
			return time.Duration(days * 24 * float64(time.Hour))
		}
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return fallback
}

// parseActivityKinds 解析 ?kind=a,b：逐个校验，任一非法即 400。
// 空串 ⇒ (nil, true) —— 表示「全部结构化 kind」。
func parseActivityKinds(raw string) ([]string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	var out []string
	seen := make(map[string]bool, 4)
	for _, part := range strings.Split(raw, ",") {
		k := strings.TrimSpace(part)
		if k == "" {
			continue
		}
		if !isStructuredKind(k) {
			return nil, false
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, true
}

func parseBoolParam(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// durationString 让响应里的时间窗可读（"72h0m0s" → "72h"）。
func durationString(d time.Duration) string {
	return d.String()
}
