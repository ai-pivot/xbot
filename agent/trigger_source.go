package agent

// trigger_source：**turn 级**触发来源标记。
//
// 背景（2026-11-12 用户 P0）：cron 定时任务被投进一个"活的" persistent
// interactive 子代理会话后，会在那里跑一轮 Run；这一轮结束照旧向父会话发
// Completed 通知 ⇒ 父会话（首辅/工部）被**定时任务**反复唤醒，白白空跑 LLM。
// 现场：户部每条 cron 一条 user 行；首辅在每次 cron 触发后 ~90s 收到一条
// `court.hubu completed`。
//
// 设计原则：**按「这一轮是谁触发的」判定，不按消息正文**。绝不能用
// `strings.Contains(content, "⏰ [定时任务触发]")` —— 用户/父代理完全可以发一条
// 内容包含该前缀的消息，字面量匹配会把正常 turn 误判成 cron turn。
//
// 传递链路（唯一一条）：
//
//	入站 bus.InboundMessage.Metadata[trigger_source]   ← 写入点（cron 投递处）
//	  → RunConfig.TriggerSource                        ← per-turn 变量
//	  → 投递点判定 isCronTriggeredTurn(cfg)            ← 读取点
//
// ⚠️ RunConfig.TriggerSource 是 **per-turn** 的：每次 send 都必须**无条件**从
// msg.Metadata 重算（读到空就是空），否则上一轮的 "cron" 会残留到用户 turn 上。
const (
	// triggerSourceMetadataKey 是入站消息 metadata 上的键名。
	triggerSourceMetadataKey = "trigger_source"
	// triggerSourceCron 表示这一轮 Run 由 cron 定时任务触发。
	triggerSourceCron           = "cron"
	triggerSourceBackgroundTask = "background_task"
)

// isCronTriggeredTurn 判定本轮 Run 是否由 cron 触发（nil-safe：裸 RunConfig 不是 cron turn）。
func isCronTriggeredTurn(cfg *RunConfig) bool {
	return cfg != nil && cfg.TriggerSource == triggerSourceCron
}

// shouldNotifyParent 判定本轮 Run 结束时是否向父会话投递 Progress/Completed。
//
//   - 常规 turn：投递（既有行为）。
//   - cron turn：**不投递** —— 定时任务不该把父会话唤醒一轮 LLM。
//   - 后台任务完成唤醒的 turn：**照常投递**。它可能有真实结论，父代理必须收到；
//     早期版本把它一并抑制，导致子代理 idle 前的最后输出再也没通知父级（回归）。
//   - 但 cron turn 期间若**真有** pendingUserMsg 被 drain 注入（有人主动给这个
//     会话发了消息），fail-open 照常投递：用户那次派单必须有完成回执，
//     宁可多发一条，也不能吞掉人的输入（2026-11-12 lead 口径）。
//
// 异常路径（panic）不走本函数：panic 通知一律照发，异常必须让上层知道；
// interrupt（Completed 的 "[interrupted]" 变体）按 cron 口径抑制，见各调用点注释。
func shouldNotifyParent(cfg *RunConfig, userInputJoined bool) bool {
	return userInputJoined || !isCronTriggeredTurn(cfg)
}

// bgSectionsTriggerSource 判定一条**合并后**的通知 user 消息（一次突发 = 一条
// user 消息 = 一个 turn）是否整条都来自 cron。
//
// 只有**全部节**都是 cron 才返回 triggerSourceCron；混批（含任何非 cron 节）返回
// "" —— 混批那一轮里可能夹着人要看的真实结果，按 cron 抑制会吞掉它。
func bgSectionsTriggerSource(cronSections, otherSections int) string {
	if cronSections > 0 && otherSections == 0 {
		return triggerSourceCron
	}
	return ""
}
