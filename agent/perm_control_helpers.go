package agent

import (
	"context"
	"xbot/internal/ctxkeys"
	"xbot/llm"
	"xbot/tools"
)

func withPermControlEnabled(ctx context.Context, enabled bool) context.Context {
	return ctxkeys.WithPermControlEnabled(ctx, enabled)
}

func withApprovalTarget(ctx context.Context, chatID, senderID string) context.Context {
	return ctxkeys.WithApprovalTarget(ctx, chatID, senderID)
}

// toolDefFilter wraps a tool definition and can hide selected params without
// mutating the global registry copy.
type toolDefFilter struct {
	base       llm.ToolDefinition
	hiddenArgs map[string]bool
}

func (t *toolDefFilter) Name() string        { return t.base.Name() }
func (t *toolDefFilter) Description() string { return t.base.Description() }
func (t *toolDefFilter) Parameters() []llm.ToolParam {
	params := t.base.Parameters()
	if len(t.hiddenArgs) == 0 {
		return params
	}
	out := make([]llm.ToolParam, 0, len(params))
	for _, p := range params {
		if t.hiddenArgs[p.Name] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// neverExposedToolNames 是**绝不暴露给模型的工具名**集合（历史伪工具名）。
//
// 来历：N2（2026-10-30）之前，每轮 reminder 以
// 「assistant(tool_calls[name=system_reminder]) + tool 结果」的假消息对注入对话，
// 名字的唯一定义点是 agent/reminder.go 的 systemReminderToolName。伪消息对已经删掉
// （reminder 改为尾部内容拼接，见 runState.llmMessages），但**工具定义列表**这个面
// 并没有随之关掉：
//
//   - tools/registry.go:291 AsDefinitionsForSession 只按 tenant/channel/session-MCP
//     合并工具，**没有任何名字黑名单**（注册入口 registry.go:43 Register /
//     :56 RegisterForTenant / :86 RegisterForChannel 收谁是谁）；
//   - 于是只要有外部来源（插件 / 会话 MCP server）恰好以这个名字注册了一个真实工具，
//     它就会原样流进模型上下文；
//   - 而调用这个名字会返回**整份上下文快照**（user-msg + cwd + todos + subagents +
//     guidelines）：既是提示注入面，也让模型误以为「那个注入块是我自己调工具拿到的」。
//
// 注册源不在本仓库内（in-tree 无任何以该名注册的工具），所以按硬要求在这里拦：
// 不论注册表里有什么，visibleToolDefs 一律丢弃这些名字。
var neverExposedToolNames = map[string]struct{}{
	systemReminderToolName: {},
}

// isNeverExposedToolName reports whether name 是历史伪工具名，绝不能作为工具定义
// 交给模型，也绝不主动暴露在任何提示/上下文里。
func isNeverExposedToolName(name string) bool {
	_, bad := neverExposedToolNames[name]
	return bad
}

// filterNeverExposedToolDefs 丢弃 neverExposedToolNames 里的工具定义（保持原顺序）。
// 不修改入参切片：总是返回新切片，避免把注册表的缓存视图原地裁剪。
func filterNeverExposedToolDefs(defs []llm.ToolDefinition) []llm.ToolDefinition {
	if len(defs) == 0 {
		return defs
	}
	out := make([]llm.ToolDefinition, 0, len(defs))
	for _, d := range defs {
		if isNeverExposedToolName(d.Name()) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func visibleToolDefs(defs []llm.ToolDefinition, permUsers *PermUsersConfig, channel string) []llm.ToolDefinition {
	// ⛔ 历史伪工具名（system_reminder）必须在**两条路径**上都消失。
	// perm-control 开启时是 early-return，若只在下面的 fallback 分支过滤，
	// 主路径（生产默认走的正是这条路）会漏掉 —— 所以先无条件过滤。
	defs = filterNeverExposedToolDefs(defs)
	if IsPermControlEnabled(permUsers) {
		return defs
	}
	out := make([]llm.ToolDefinition, 0, len(defs))
	for _, d := range defs {
		// Filter tools by channel: if a tool implements ChannelProvider and
		// the current channel is not in its supported list, skip it.
		// This prevents CLI from using Feishu-only tools like card_create.
		if cp, ok := d.(tools.ChannelProvider); ok {
			supported := cp.SupportedChannels()
			if len(supported) > 0 && !containsString(supported, channel) {
				continue
			}
		}
		switch d.Name() {
		case "Shell", "FileCreate", "FileReplace":
			out = append(out, &toolDefFilter{base: d, hiddenArgs: map[string]bool{"run_as": true, "reason": true}})
		default:
			out = append(out, d)
		}
	}
	return out
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
