package web

// web_health.go — GET /api/health：进程自检端点（2026-11-03 P0-1）。
//
// 背景（daidai 事故，可复算）：11-02 那次部署把新二进制带进了一个**自己的库已被
// 迁到 schema v80** 的实例，旧二进制从此只会打一行 WARN，然后继续服务；而
// `validateSession` 是纯内存 map（web_auth.go:324-350），进程里没有这批 token
// ⇒ 前端所有请求 401，业务逻辑根本没进，**没有一条业务日志**。运维当时唯一的
// 可用信号是「翻到那台机器的日志文件里那一行」。
//
// 所以这个端点有两条硬性设计要求：
//
//  1. **不需要登录**：401 风暴正是它要报告的场景，任何鉴权中间件都会把它
//     一起打成 401（那样它就没用了）。它只回传进程自身的指纹与版本号，
//     不含任何用户数据、不含任何文件系统路径。
//  2. **扁平且稳定的 JSON schema**：外部探针/部署脚本直接读字段名
//     （status / binary_changed / schema_newer / binary_sha256 …），不套
//     内部的 {ok,data} 信封 —— 这是对外的契约，字段名不可改名。
//
// 状态码恒为 200：degraded 是身体气味，不是请求失败。探针读字段，不读状态码。

import (
	"encoding/json"
	"net/http"

	"xbot/internal/runtimeinfo"
)

// handleHealth serves GET /api/health.
func (wc *WebChannel) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := json.Marshal(runtimeinfo.Default().Health())
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "health snapshot unavailable")
		return
	}

	// no-store：探针要的是当前值，缓存/CDN 一律不许替它回答。
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
