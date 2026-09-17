package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	log "xbot/logger"
)

// ─── best-effort SubAgent role resolution ─────────────────────────────────
//
// 用户要求（2026-09-16）：`role` 缺省时**尽量匹配**，只有"有歧义"才报错
// （旧行为是缺省/不认就直接报 `role is required, see <available_agents>…`，
// 模型漏传一个字段就完全跑不起来）。
//
// 规则（显式、确定性、可测）：
//  1. requested 非空 → 规范化精确匹配（大小写 / `-` / `_` / 空格 无关）。
//  2. 容错名匹配：规范化后互为子串（拼写近似 / 少个字母 / 简写）→ 唯一候选采用；
//     多个候选 → 歧义报错。
//  3. requested 为空，或名字都没命中 → 按 task 文本给每个角色打分：
//       角色名出现在 task 中            → +100
//       角色名/描述里的英文词（≥3 字母）出现 → 每个 +10
//       角色名/描述里的中文 2-gram 与 task 重合 → 每个 +2（最多计 10 个）
//     最高分唯一 → 采用；并列最高 → 歧义报错；全 0 → 报错（无从猜，列出候选）。
//
// 绝不"随便挑一个"：歧义与零分都必须报错并列出候选名，避免把任务派给错误的角色。
// 自动匹配会在工具结果里显式标注（调用方可感知），并写一条 info 日志。

// RoleResolution 一次 best-effort 角色匹配的结果。
type RoleResolution struct {
	// Role 命中的角色（nil = 未命中，仅在与 error 一起用于诊断时存在）。
	Role *SubAgentRole
	// AutoMatched 为 true 表示 role 参数为空或未知，由本函数推断得出。
	AutoMatched bool
	// Candidates 是歧义/未命中时的候选角色名（用于错误信息）。
	Candidates []string
}

// ListSubAgentRolesSandbox 枚举本次调用可见的全部角色：用户目录 → 全局目录 →
// 内嵌定义，按名字去重（先出现的优先，与 GetSubAgentRoleSandbox 的查找顺序一致）。
func ListSubAgentRolesSandbox(ctx context.Context, sb Sandbox, userID string, userAgentDirs ...string) []SubAgentRole {
	seen := make(map[string]bool)
	var out []SubAgentRole
	add := func(roles []SubAgentRole) {
		for _, r := range roles {
			if r.Name == "" || seen[r.Name] {
				continue
			}
			seen[r.Name] = true
			out = append(out, r)
		}
	}

	for _, dir := range userAgentDirs {
		if dir == "" {
			continue
		}
		var roles []SubAgentRole
		var err error
		if sb != nil {
			roles, err = LoadAgentRolesSandbox(ctx, dir, sb, userID)
		} else {
			roles, err = LoadAgentRoles(dir)
		}
		if err != nil {
			log.WithField("dir", dir).WithError(err).Warn("Failed to load user agent roles, skipping directory")
			continue
		}
		add(roles)
	}

	if agentsDir != "" {
		if roles, err := LoadAgentRoles(agentsDir); err == nil {
			add(roles)
		} else {
			log.WithError(err).Warn("Failed to load agent roles")
		}
	}

	for _, name := range ListEmbeddedAgents() {
		if seen[name] {
			continue
		}
		if role, ok := getEmbeddedAgentRole(name); ok {
			add([]SubAgentRole{*role})
		}
	}
	return out
}

// roleLookupContext 收集角色解析所需的 sandbox / 用户 agent 目录上下文
// （与 SubAgent / SendMessage 两处的路径规则一致：remote sandbox 用 runner
// 工作区的 agents/，本地/docker 用 server 侧路径）。
func roleLookupContext(ctx *ToolContext) (Sandbox, string, []string) {
	if ctx == nil {
		return nil, "", nil
	}
	EnsureSynced(ctx)
	originUserID := ctx.OriginUserID
	if originUserID == "" {
		originUserID = ctx.SenderID
	}
	if shouldUseSandbox(ctx) {
		roleUserID := originUserID
		var dirs []string
		if sbDir := sandboxBaseDir(ctx); sbDir != "" {
			dirs = append(dirs, filepath.Join(sbDir, "agents"))
		}
		return ctx.Sandbox, roleUserID, dirs
	}
	var dirs []string
	if originUserID != "" && ctx.WorkingDir != "" {
		dirs = append(dirs, UserAgentsRoot(ctx.WorkingDir, originUserID))
	}
	if ctx.WorkspaceRoot != "" {
		dirs = append(dirs, filepath.Join(ctx.WorkspaceRoot, ".agents"))
	}
	return nil, originUserID, dirs
}

// ResolveSubAgentRoleSandbox 解析 SubAgent role —— **只做规范化精确匹配**。
//
// ⛔ 用户 2026-09-17（三次强调）：「任何地方都不能推断」「send 要 best-effort 但严禁推断」。
// 这里**不再有任何推断/近似/按 task 打分**：role 必须显式给出且唯一对应到可用角色，
// 否则报错并列出可用角色（由调用方纠正）。**绝不猜。**
//
// 返回 (role, autoMatched, err)：autoMatched 恒为 false（签名保持不变以兼容调用方）。
func ResolveSubAgentRoleSandbox(ctx context.Context, requested, _ string, sb Sandbox, userID string, userAgentDirs ...string) (*SubAgentRole, bool, error) {
	available := ListSubAgentRolesSandbox(ctx, sb, userID, userAgentDirs...)
	if len(available) == 0 {
		return nil, false, fmt.Errorf("no SubAgent roles available — define one under agents/ or use a built-in role (see <available_agents> in system prompt)")
	}
	names := make([]string, 0, len(available))
	for i := range available {
		names = append(names, available[i].Name)
	}
	sort.Strings(names)

	normRequested := normalizeRoleName(requested)
	if normRequested == "" {
		return nil, false, fmt.Errorf("role is required — pass one of: %s", strings.Join(names, ", "))
	}
	for i := range available {
		if normalizeRoleName(available[i].Name) == normRequested {
			return &available[i], false, nil
		}
	}
	return nil, false, fmt.Errorf("unknown SubAgent role %q — pass the exact name (available: %s)", requested, strings.Join(names, ", "))
}

// ─── 匹配打分（纯函数） ─────────────────────────────────────────────────────

// scoreRoleForTask 给角色与 task 的匹配度打分。权重见文件头注释。

// normalizeRoleName 归一化角色名/文本用于比较：小写 + 去掉非字母数字（`-`/`_`/
// 空格/点号等一律忽略，所以 "code-reviewer" == "code_reviewer" == "CodeReviewer"）。
func normalizeRoleName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// asciiTerms 抽取文本里的英文/数字词（≥3 字符，小写）——用于关键词重合打分。

// cjkBigrams 抽取中日韩文字的 2-gram（连续的两个汉/日/韩字）——中文没有空格，
// 用 2-gram 重合度近似"语义相关"。

// roleNameList 把候选下标渲染成 `"a"/"b"` 形式（用于错误信息）。

// truncateRoleLog 截断 task 用于日志（rune 安全）。
