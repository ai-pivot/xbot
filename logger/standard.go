package logger

import "context"

// Category constants for use with Req / Sess / Usr / Glob.
// Each log entry MUST carry a category field identifying its business domain.
const (
	CatRequest       = "request"       // 请求生命周期：消息接收、处理开始/结束、取消、响应发送
	CatLLM           = "llm"           // LLM 调用：流式请求、首 chunk、完成、重试、token 统计
	CatTool          = "tool"          // 工具执行：调用开始/完成、错误、offload
	CatAgent         = "agent"         // Agent 循环：迭代、压缩检查/执行、context 管理
	CatSubAgent      = "subagent"      // SubAgent 生命周期：spawn、unload、interrupt、消息注入
	CatRPC           = "rpc"           // RPC 调用：分发、响应、panic 恢复
	CatChannel       = "channel"       // Channel 适配器：注册/注销、消息收发
	CatSession       = "session"       // 会话管理：创建/切换/删除/恢复/持久化
	CatSubscription  = "subscription"  // 订阅管理：增删改、切换、模型刷新
	CatCron          = "cron"          // 定时任务：触发、调度错误、过期清理
	CatHook          = "hook"          // Hook 系统：事件分发、决策结果、handler panic
	CatPlugin        = "plugin"        // 插件系统：激活/停用、工具注册、widget 渲染
	CatTransport     = "transport"     // 传输层：连接建立/断开、重连、WS 读写
	CatConfig        = "config"        // 配置管理：加载、设置变更、runner 切换
	CatDB            = "db"            // 数据存储：迁移、写入失败、查询异常
	CatAuth          = "auth"          // 认证授权：身份解析、权限校验
	CatStartup       = "startup"       // 启动关闭：组件初始化、服务启停
	CatTUI           = "tui"           // TUI 渲染：TUI 控制
)

// --- Request scope (请求级) ---

// Req returns a log entry for Request-scoped logging.
// Automatically extracts request_id from ctx and sets the given category.
// Use this for all logs within a single user message processing cycle.
//
// Required fields: request_id, category, session_key (add session_key via WithField)
//
// Example:
//
//	log.Req(ctx, log.CatLLM).WithField("model", model).Info("LLM stream started")
//	log.Req(ctx, log.CatTool).WithFields(log.Fields{
//	    "tool":       "Shell",
//	    "elapsed_ms": 234,
//	}).Info("tool completed")
func Req(ctx context.Context, category string) *Entry {
	return Ctx(ctx).WithField("category", category)
}

// --- Session scope (会话级) ---

// Sess returns a log entry for Session-scoped logging.
// Sets category, session_key, and user_id. Extracts request_id from ctx if present.
// Use this for events that span multiple requests within a single session.
//
// Required fields: category, session_key, user_id
// Optional: request_id (if event occurs during request processing)
//
// Example:
//
//	log.Sess(ctx, log.CatSession, sessionKey, userID).
//	    WithField("action", "created").Info("session created")
//	log.Sess(ctx, log.CatSubAgent, sessionKey, userID).
//	    WithFields(log.Fields{"role": "dev", "instance": "fix-1"}).
//	    Info("SubAgent spawned")
func Sess(ctx context.Context, category, sessionKey, userID string) *Entry {
	return Ctx(ctx).WithFields(Fields{
		"category":    category,
		"session_key": sessionKey,
		"user_id":     userID,
	})
}

// --- User scope (用户级) ---

// Usr returns a log entry for User-scoped logging.
// Sets category and user_id. Extracts request_id from ctx if present.
// Use this for events that are user-specific but cross-session (subscriptions, settings, auth).
//
// Required fields: category, user_id
// Optional: request_id (if triggered via RPC)
//
// Example:
//
//	log.Usr(ctx, log.CatSubscription, userID).
//	    WithFields(log.Fields{"action": "added", "sub_id": subID}).
//	    Info("subscription added")
//	log.Usr(nil, log.CatAuth, userID).
//	    WithField("role", role).Info("identity resolved")
func Usr(ctx context.Context, category, userID string) *Entry {
	return Ctx(ctx).WithFields(Fields{
		"category": category,
		"user_id":  userID,
	})
}

// --- Global scope (全局级) ---

// Glob returns a log entry for Global-scoped logging.
// Sets only the category field. No request_id, session_key, or user_id.
// Use this for process-level events (startup, channel registration, DB migration).
//
// Required fields: category
//
// Example:
//
//	log.Glob(log.CatStartup).WithField("component", "agent").Info("Agent loop started")
//	log.Glob(log.CatChannel).WithField("channel", "feishu").Info("Channel registered")
func Glob(category string) *Entry {
	return WithField("category", category)
}
