package main

// Policy 是 QQ 渠道侧的访问控制：把"能不能进"（allow）与"是不是管理员"（admins）
// 分成两件独立的事。
//
//	allow   决定一条入站消息是否交给 agent 处理 —— 不影响 admin 判定；
//	admins  决定某个发送者是否拥有管理员身份 —— 不影响准入。
//
// 故意分开：一个被白名单拒绝的管理员仍然是管理员（阶段 3 的 slash 门禁需要
// 独立知道这件事，用于日志与拒绝理由），而一个被放行的普通群友不是管理员。
//
// 这是渠道侧的第一道闸门。host 侧另有唯一权威 config.AgentConfig.IsAdminSender
// （cli/web 恒 admin，其他渠道看 agent.admins）。因此 admin 名单必须在两处口径一致：
// QQ 号原样写进 config.json 的 agent.admins 与 channels.qq.admins。
type Policy struct {
	allow  Patterns
	admins Patterns
}

// Decision 是一次准入判定的完整结果。
type Decision struct {
	// Handle 为 true 表示这条消息应交给 agent。
	Handle bool
	// Admin 为 true 表示发送者是管理员。
	Admin bool
}

// NewPolicy 构造一个访问策略。
func NewPolicy(allow, admins Patterns) Policy {
	return Policy{allow: allow, admins: admins}
}

// ShouldHandle 报告 senderID 发来的消息是否应被处理。
//
//	fail-closed：allow 为空 → 一律 false
//	全通配：   allow 含 "*" → 任何非空 senderID 都 true
//	空 senderID → 一律 false（没有身份的消息不做准入）
func (p Policy) ShouldHandle(senderID string) bool {
	return p.allow.Match(senderID)
}

// IsAdmin 报告 senderID 是否在管理员名单内。
//
// admins 为空 → 无人是管理员；admins 含 "*" → 任何非空 senderID 都是管理员。
func (p Policy) IsAdmin(senderID string) bool {
	return p.admins.Match(senderID)
}

// Decide 一次算出准入与权限两个结果（调用方通常两件都要）。
func (p Policy) Decide(senderID string) Decision {
	return Decision{
		Handle: p.ShouldHandle(senderID),
		Admin:  p.IsAdmin(senderID),
	}
}

// Allow 返回白名单（只读用途：日志 / 状态展示）。
func (p Policy) Allow() Patterns { return p.allow }

// Admins 返回管理员名单（只读用途：日志 / 状态展示）。
func (p Policy) Admins() Patterns { return p.admins }
