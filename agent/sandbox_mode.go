package agent

import log "xbot/logger"

// resolveSandboxMode 归一化沙箱模式（Agent 侧唯一的模式解析入口）。
//
// 语义（2026-09-16 用户要求「默认 sandbox 必须是 none」）：
//
//   - 空值（未配置 sandbox）⇒ "none"。**绝不再默认 "docker"** —— 旧代码把空值当
//     "docker"，使未配置 sandbox 的部署在新建会话设 CWD 时直接失败
//     （"CWD sync not supported in docker sandbox mode"，P0）。
//   - 历史遗留值 "docker" ⇒ "none"（本地 docker sandbox 已整体删除；沙箱统一走
//     runner/remote 接入）。配置里残留的旧值不阻断启动，只记一条 warning。
//   - "none" / "remote" 原样保留。
func resolveSandboxMode(configured string) string {
	switch configured {
	case "":
		return "none"
	case "docker":
		log.Warn("sandbox mode \"docker\" is obsolete (local docker sandbox removed) — using \"none\"; sandbox runs via runner/remote now")
		return "none"
	default:
		return configured
	}
}
