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

// ResolveSubAgentRoleSandbox 按文件头注释的规则解析角色。
//
// 返回 (role, autoMatched, err)：
//   - err == nil 且 role != nil → 命中（autoMatched=true 表示是推断出来的）
//   - err != nil → 歧义（并列最高分 / 多个名字候选）或无从推断（零分 / 无可用角色）；
//     err 文案里已列出候选，调用方直接上抛即可。
func ResolveSubAgentRoleSandbox(ctx context.Context, requested, task string, sb Sandbox, userID string, userAgentDirs ...string) (*SubAgentRole, bool, error) {
	available := ListSubAgentRolesSandbox(ctx, sb, userID, userAgentDirs...)
	if len(available) == 0 {
		return nil, false, fmt.Errorf("no SubAgent roles available — define one under agents/ or use a built-in role (see <available_agents> in system prompt)")
	}
	names := make([]string, 0, len(available))
	for i := range available {
		names = append(names, available[i].Name)
	}
	sort.Strings(names)

	// 1) 规范化精确匹配。
	normRequested := normalizeRoleName(requested)
	if normRequested != "" {
		for i := range available {
			if normalizeRoleName(available[i].Name) == normRequested {
				return &available[i], false, nil
			}
		}
		// 2) 容错名匹配（含拼写近似）：唯一候选才采用，多个即歧义。
		var fuzzy []int
		for i := range available {
			n := normalizeRoleName(available[i].Name)
			if len(normRequested) >= 3 && (strings.Contains(n, normRequested) || strings.Contains(normRequested, n)) {
				fuzzy = append(fuzzy, i)
			}
		}
		if len(fuzzy) == 1 {
			log.WithFields(log.Fields{"requested": requested, "matched": available[fuzzy[0]].Name}).
				Info("SubAgent role: fuzzy-matched an unknown/typo'd role name")
			return &available[fuzzy[0]], true, nil
		}
		if len(fuzzy) > 1 {
			return nil, false, fmt.Errorf(
				"ambiguous role %q — it matches %s; pass the exact role name (available: %s)",
				requested, roleNameList(available, fuzzy), strings.Join(names, ", "))
		}
	}

	// 3) 按 task 文本推断。
	taskNorm := normalizeRoleName(task)
	taskTerms := asciiTerms(task)
	taskBigrams := cjkBigrams(task)

	bestScore := 0
	var best []int
	for i := range available {
		s := scoreRoleForTask(available[i], taskNorm, taskTerms, taskBigrams)
		switch {
		case s > bestScore:
			bestScore, best = s, []int{i}
		case s == bestScore && s > 0:
			best = append(best, i)
		}
	}

	unknownHint := ""
	if requested != "" {
		unknownHint = fmt.Sprintf(" (unknown role %q)", requested)
	}
	if bestScore == 0 {
		return nil, false, fmt.Errorf(
			"cannot infer SubAgent role%s from the task — pass `role` explicitly (available: %s)",
			unknownHint, strings.Join(names, ", "))
	}
	if len(best) > 1 {
		return nil, false, fmt.Errorf(
			"ambiguous SubAgent role: %s all match equally%s — pass `role` explicitly (available: %s)",
			roleNameList(available, best), unknownHint, strings.Join(names, ", "))
	}

	matched := available[best[0]]
	log.WithFields(log.Fields{
		"matched": matched.Name,
		"score":   bestScore,
		"task":    truncateRoleLog(task),
	}).Info("SubAgent role: best-effort matched from task (role was omitted or unknown)")
	return &matched, true, nil
}

// ─── 匹配打分（纯函数） ─────────────────────────────────────────────────────

// scoreRoleForTask 给角色与 task 的匹配度打分。权重见文件头注释。
func scoreRoleForTask(role SubAgentRole, taskNorm string, taskTerms, taskBigrams map[string]struct{}) int {
	score := 0
	nameNorm := normalizeRoleName(role.Name)
	if nameNorm != "" && strings.Contains(taskNorm, nameNorm) {
		score += 100
	}
	text := role.Name + " " + role.Description
	for term := range asciiTerms(text) {
		if _, ok := taskTerms[term]; ok {
			score += 10
		}
	}
	overlap := 0
	for bg := range cjkBigrams(text) {
		if _, ok := taskBigrams[bg]; ok {
			overlap++
		}
	}
	if overlap > 10 {
		overlap = 10
	}
	return score + 2*overlap
}

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
func asciiTerms(s string) map[string]struct{} {
	out := make(map[string]struct{})
	var cur strings.Builder
	flush := func() {
		w := cur.String()
		cur.Reset()
		if len(w) >= 3 {
			out[w] = struct{}{}
		}
	}
	for _, r := range strings.ToLower(s) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// cjkBigrams 抽取中日韩文字的 2-gram（连续的两个汉/日/韩字）——中文没有空格，
// 用 2-gram 重合度近似"语义相关"。
func cjkBigrams(s string) map[string]struct{} {
	out := make(map[string]struct{})
	var prev rune
	havePrev := false
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) && !unicode.Is(unicode.Hiragana, r) && !unicode.Is(unicode.Katakana, r) && !unicode.Is(unicode.Hangul, r) {
			havePrev = false
			continue
		}
		if havePrev {
			out[string([]rune{prev, r})] = struct{}{}
		}
		prev, havePrev = r, true
	}
	return out
}

// roleNameList 把候选下标渲染成 `"a"/"b"` 形式（用于错误信息）。
func roleNameList(available []SubAgentRole, idx []int) string {
	parts := make([]string, 0, len(idx))
	for _, i := range idx {
		parts = append(parts, fmt.Sprintf("%q", available[i].Name))
	}
	return strings.Join(parts, " / ")
}

// truncateRoleLog 截断 task 用于日志（rune 安全）。
func truncateRoleLog(s string) string {
	return TruncateHeadPreview(s, 120)
}
