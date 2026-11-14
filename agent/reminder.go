package agent

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
	"xbot/protocol"
)

// wrapCDATA 100% 防 XML 注入：内容里的 ]]> 拆分为两个 CDATA 区段。
// 用于包裹用户消息、goal summary 等可能含 XML 特殊字符的内容。
func wrapCDATA(content string) string {
	safe := strings.ReplaceAll(content, "]]>", "]]]]><![CDATA[>")
	return "<![CDATA[" + safe + "]]>"
}
func resolveAbsolutePath(path string) string {
	if path == "" {
		return ""
	}
	// Expand ~/...
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	} else if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		}
	}
	// Resolve . and .. to absolute path
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path
}

// systemReminderToolName 是**历史形态**的伪工具名（2026-10-30 N2 之前的
// 「assistant tool_call + tool 结果」假消息对）。
//
// 现在 reminder 走尾部内容拼接（见 runState.llmMessages / appendReminderToTail），
// 这个名字**不得再出现在任何发给上游的请求里** —— 保留常量只为让测试能机械扫描
// 「0 处 system_reminder 伪工具调用」，以及让历史注释/日志可读。
const systemReminderToolName = "system_reminder"

// BuildSystemReminder builds the per-turn system reminder block
// (`<system-reminder role="reminder">…</system-reminder>`), which the engine
// concatenates onto the TAIL of the request (never a fake tool pair, never
// persisted — see runState.llmMessages / appendReminderToTail).
// agentID "main" = main Agent, otherwise SubAgent.
// sessionName is the current session display name (used to detect auto-generated names needing rename).
//
// v6 结构（N2a-B）：<note> + <goal> + <cwd> + 结构化 <todos> + <subagents> + 4 条 guidelines。
//
// ⛔ 不再回吐用户原话（N2a-B 删掉了原「用户消息 CDATA」整段）：模型本来就看得到
// 用户消息，重复一遍纯属浪费 prompt，且会诱发「我在读一条被重新表述的指令」的自我指涉。
//
// todosKnown 是**显式的可知性入参**（不从 todoItems 反推）：
//   - true + 有 items ⇒ 逐条 `<todo status=… id=…>`；
//   - true + 空     ⇒ `<todos status="empty">…` 引导段；
//   - false         ⇒ **整段不输出**。调用方没接 TodoManager / key 为空时，
//     「现在有几条 todo」是不可知的 —— 此时输出 status="empty" 是在**编造状态**，
//     比不输出更坏（模型会据此重复新建清单，或误判任务已经完成）。
func BuildSystemReminder(
	messages []llm.ChatMessage,
	todoItems []TodoProgressItem,
	todosKnown bool,
	goalInfo *protocol.GoalInfo,
	agentID string,
	cwd string,
	sessionName string,
	activeSubAgents []SubAgentStatus,
) string {
	if len(messages) == 0 {
		return ""
	}

	isSubAgent := agentID != "main"

	var sb strings.Builder
	sb.WriteString("<system-reminder role=\"reminder\">")
	// note 只保留「这是自动注入」这一个事实标识（用户 2026-10-20：不要搞那么大一段）。
	// 历史：2026-09-17 曾因「原文太长且可能被误读」缩短过一次；2026-10-14 又因模型把注入
	// 误当成自己调用过的工具（当时 `system_reminder` 是一个假 tool pair 的名字，就在历史里）
	// 而写成 60+ 词的自解释。2026-10-30 起改为**尾部内容拼接**（不再伪造 tool 调用对，
	// 上游永远看不到那个工具名），模型不再有「我是不是调错了工具」的困惑来源 ——
	// 长文案反而每轮占 prompt，故保持这一句。
	sb.WriteString("<note>AUTO-INJECTED</note>")

	// 当前 goal（如有 active goal）
	if goalInfo != nil && goalInfo.Status == "active" {
		summary := goalInfo.Summary
		if summary == "" {
			summary = goalInfo.Objective
		}
		if summary != "" {
			fmt.Fprintf(&sb, "<goal status=%q>%s</goal>", goalInfo.Status, wrapCDATA(summary))
		}
	}

	if cwd != "" {
		cwd = resolveAbsolutePath(cwd)
		fmt.Fprintf(&sb, "<cwd>%s</cwd>", html.EscapeString(cwd))
	}

	// 结构化 todos（具体项 + status——LLM 一眼看清进度）
	switch {
	case !todosKnown:
		// 不可知：整段不输出（连「尚无计划」都不许出现 —— 那是编造状态，
		// 见函数头 todosKnown 的说明）。只有「无 todo 且确实知道无 todo」才给引导语。
	case len(todoItems) > 0:
		sb.WriteString("<todos>")
		for i, t := range todoItems {
			// id is positional — the list order IS the plan order, there is no stored key.
			fmt.Fprintf(&sb, `<todo status=%q id="%d">%s</todo>`, t.Status, i+1, html.EscapeString(t.Text))
		}
		sb.WriteString("</todos>")
	default:
		// 可知的空 todo：不能整段静默消失。旧实现在没有计划时什么都不写，模型看不到
		// 「该建计划」的信号 —— 首轮 reminder 因此退化成一条纯状态播报。
		// 显式 status="empty" 保留了「当前无计划」这个事实，同时给出下一步动作。
		sb.WriteString(`<todos status="empty">尚无计划：多步任务应先用 TodoWrite 建立待办清单，再逐项推进；单项琐碎任务可跳过</todos>`)
	}

	// Active SubAgents（当前执行中 vs 空闲）
	if !isSubAgent && len(activeSubAgents) > 0 {
		sb.WriteString("<subagents>")
		for _, sa := range activeSubAgents {
			status := "idle"
			if sa.Running {
				status = "running"
			}
			label := sa.Role
			if sa.Instance != "" {
				label += "/" + sa.Instance
			}
			fmt.Fprintf(&sb, "<subagent role=%q status=%q>%s</subagent>", sa.Role, status, html.EscapeString(label))
		}
		sb.WriteString("</subagents>")
	}

	// 行为准则（4 条——v5.2 加"主动维护 TODO 进度"）
	sb.WriteString("<guidelines>")
	sb.WriteString("<guideline>修改后运行测试验证</guideline>")
	sb.WriteString("<guideline>错误时先分析根因再修改</guideline>")
	sb.WriteString("<guideline>主动维护知识文档和代码质量</guideline>")
	sb.WriteString("<guideline>每完成一个 TODO 立即用 TodoWrite 标记 done；已完成的过时 TODO（不再相关的条目）直接删除，不要保留在列表里</guideline>")
	sb.WriteString("</guidelines>")

	sb.WriteString("</system-reminder>")
	return sb.String()
}
