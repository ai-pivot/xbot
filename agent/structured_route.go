package agent

import (
	"fmt"
	"sort"
	"strings"
)

// structured_route.go —— 结构化消息的**地址解析**（逻辑地址 → 物理会话 key）。
//
// 背景（2026-10-22 实测事故）：SendMessage 的地址 `agent:tester/task-verify` 是
// **逻辑地址**（Dispatcher 用），而子代理真正的会话 key 是
// interactiveKey(父channel, 父chatID, role, instance) =
// `web:web-4/tester:task-verify`。之前直接把逻辑地址当 chatID 传给
// GetOrCreateSession，于是每次批复都会**新建一个空壳会话**把消息写进去——
// 实测：`agent:1a/tester_task-verify-*.db` 里躺着 report+decision 两行，
// 而backend真实的会话一条都收不到。批复等于凭空蒸发。
//
// 因此这里做严格解析：内存注册表 → 持久化注册表（parent_channel/parent_chat_id
// + role + instance 重建）；解析不到就返回空串，由调用方**直接报错**，
// 绝不退回逻辑地址去建野会话。
//
// D-10（2026-10-22）：解析必须带**调用方作用域**。原先无条件按 role/instance
// 全局解析 —— 任何会话都能凭 `agent:<role>/<instance>` 打到别人的子代理会话
// （web-5 可以把 report/decision 投进 web-4 名下的目标会话）。现在解析出的目标
// 必须挂在调用方名下（按 parentKey 过滤）；父不匹配、同名多命中、根本解析不出来
// 一律返回空串 + 错误：宁可失败，不许猜。

// scopeForms 把一份「会话身份」归一成可比较的 key 形态集合。
//
// 同一个会话在系统里有两种等价写法，比较时**必须双向归一**（禁止字符串 ==）：
//
//	① 顶层会话        (web, web-4)                       → "web:web-4"（qualifyChatID）
//	② interactive 子代理：物理 key 就是它的 chatID
//	                  (web, "web:web-4/team.ops:main") → "web:web-4/team.ops:main"
//
// 而 ② 作为**父**出现在子代理的 key 里时，父段被写成「agent 通道 + 自己那段 key 当
// chatID」：`agent:web:web-4/team.ops:main`。两种写法来自 bgParentKey 的两条来源
// （嵌套 spawn 直接存自己的 key；顶层 spawn 用 origin 回落，见
// SpawnInteractiveSession / parseInteractiveKeyParent / splitParentKey），
// 因此候选父键与调用方作用域都可能以任一种形态出现，求交才安全。
//
// 归一规则：先算 base —— 顶层是 "channel:chatID"；agent 通道则是 chatID 本身
// （若它自带 agent: 前缀且剥掉后仍含 "/"，说明是上面那种前缀写法，剥掉）。
// 最后只在 base 确实是 interactive 物理 key（含 "/"）时追加 "agent:"+base；
// 不给不含 "/" 的 base 加前缀，避免把 "agent:web:web-4" 这种「父会话是顶层会话」
// 的写法跟别的父会话混为一谈。
func scopeForms(channel, chatID string) map[string]struct{} {
	forms := make(map[string]struct{}, 2)
	id := strings.TrimSpace(chatID)
	if id == "" {
		return forms
	}
	var base string
	switch ch := strings.TrimSpace(channel); ch {
	case "", "agent":
		base = id
		if rest, ok := strings.CutPrefix(id, "agent:"); ok && strings.Contains(rest, "/") {
			base = rest
		}
	default:
		base = ch + ":" + id
	}
	forms[base] = struct{}{}
	if strings.Contains(base, "/") {
		forms["agent:"+base] = struct{}{}
	}
	return forms
}

// scopeIntersects 两套归一形态只要有交集，就是同一个会话作用域。
func scopeIntersects(a, b map[string]struct{}) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for f := range a {
		if _, ok := b[f]; ok {
			return true
		}
	}
	return false
}

// joinScopeForms 把形态集合拍成一行（用于错误信息，确定顺序便于排障）。
func joinScopeForms(forms map[string]struct{}) string {
	all := make([]string, 0, len(forms))
	for f := range forms {
		all = append(all, f)
	}
	sort.Strings(all)
	return strings.Join(all, " | ")
}

// candidateScopeForms 汇总一个**候选子代理**可能落进的作用域形态：
//   - 父作用域：物理 key 里字面写着的父段（parseInteractiveKeyParent），
//     以及 ia.parentKey（bgParentKey / spawn 回落写进去的那份）；
//   - 自身 key：候选会话自己的 base（scopeForms 传空 channel 即"就用它当 base"），
//     供子树前缀判定使用（顶层会话寻址调度方名下的子代理组时，子代理组 key 本身就在前缀里）。
//
// 几种写法都可能不同，任一命中调用方作用域即算「在作用域内」。
func candidateScopeForms(physicalKey, parentKey string) map[string]struct{} {
	forms := map[string]struct{}{}
	if key := strings.TrimSpace(physicalKey); key != "" {
		for f := range scopeForms("", key) {
			forms[f] = struct{}{}
		}
		if pch, pchat := parseInteractiveKeyParent(key); pch != "" && pchat != "" {
			for f := range scopeForms(pch, pchat) {
				forms[f] = struct{}{}
			}
		}
	}
	if pk := strings.TrimSpace(parentKey); pk != "" {
		pch, pchat := splitParentKey(pk)
		for f := range scopeForms(pch, pchat) {
			forms[f] = struct{}{}
		}
	}
	return forms
}

// subtreePrefixes 返回调用方的**整棵子树**前缀；**只在调用方是顶层会话时非空**。
//
// 为什么必须有它（2026-10-22 用户拍板，这是阻断性缺陷而不是"边界"）：
// 分层改造后由调度方 spawn 子代理组，子代理组的物理 key 是
// `agent:web:web-4/team.lead:main/team.backend:main` —— 它既不是顶层会话的直接子级，
// 也不在"顶层会话的直接上级那一层"（顶层会话没有上级，rootForms 为空）里。
// 只靠 callerScope+rootForms 的话，顶层会话给子代理组发批复/催办会被**全部挡住**，
// 批复链路直接断。
//
// 语义：调用方是顶层会话（顶层会话）时，**整棵子树都可寻址**（整棵树都属于同一个团队）；
// 仍然禁止跨顶层会话（web:web-5、别的用户 一律拒）。
// interactive 调用方（channel=="agent"）不启用前缀判定：它自己的子树已由
// callerScope + 直接上级那一层（rootForms）精确覆盖，放开前缀会把"兄弟会话名下的
// 子代理"也一并放进来，超出授权意图。
func subtreePrefixes(channel, chatID string) []string {
	ch := strings.TrimSpace(channel)
	id := strings.TrimSpace(chatID)
	if id == "" || ch == "" || ch == "agent" {
		return nil
	}
	return []string{ch + ":" + id + "/"}
}

// matchesSubtreePrefix 判断候选作用域形态是否落在调用方的子树前缀内。
// 形态集合里可能带 "agent:" 前缀写法，比较时两种都试。
func matchesSubtreePrefix(forms map[string]struct{}, prefixes []string) bool {
	if len(prefixes) == 0 || len(forms) == 0 {
		return false
	}
	for f := range forms {
		bases := []string{f}
		if rest, ok := strings.CutPrefix(f, "agent:"); ok && strings.Contains(rest, "/") {
			bases = append(bases, rest)
		}
		for _, b := range bases {
			for _, p := range prefixes {
				if strings.HasPrefix(b, p) {
					return true
				}
			}
		}
	}
	return false
}

// isPhysicalInteractiveKey 判断 chatID 是否已经是 interactive 物理会话 key
// （形如 "channel:chatID/role[:instance]"）。父 chatID 自身可能含 ":" 和 "/"，
// 所以判据不是「含冒号」，而是 parseInteractiveKeyParent 能解出非空父段。
func isPhysicalInteractiveKey(s string) bool {
	ch, chatID := parseInteractiveKeyParent(s)
	return ch != "" && chatID != ""
}

// ResolveStructuredTarget 解析结构化消息的目标物理会话 chatID。
//
//	channel != "agent"                            → 原样返回 chatID（普通渠道会话）
//	channel == "agent"，chatID 是 "role/instance"  → 解析成物理 key，且必须在调用方作用域内
//	channel == "agent"，chatID 已是物理 key        → 校验父会话在调用方作用域内后原样返回
//
// callerChannel/callerChatID 是**调用方自己的会话身份**：顶层会话是 (web, web-4)，
// interactive 子代理是 (agent, 它自己的物理 key)。任何「解析不出来 / 超出作用域 /
// 同名多命中」的情形都返回 ("", err)，绝不回落到逻辑地址去建野会话。
//
// 作用域口径（2026-10-22 用户拍板）：同一顶层会话（如 web:web-4）的子树内可互发；
// 跨顶层会话（web-5、别的用户）一律拒。具体三种情形：
//   - 顶层调用方（顶层会话 web:web-4）：**整棵子树**都可寻址（subtreePrefixes）——
//     分层改造后子代理组挂在调度方下面，不是直接子级，否则批复链路会断；
//   - interactive 调用方（子代理）：自己 + 直接上级那一层（callerScope + rootForms）；
//   - 一律不许跨顶层会话。
//
// **注意：地址里没有组织关系，这里不校验层级** ——「只向上报、不许平级串」靠
// CHARTCHECK 纪律 + 审计抽查，不靠这段代码。
func (a *Agent) ResolveStructuredTarget(callerChannel, callerChatID, channel, chatID string) (string, error) {
	if channel != "agent" {
		return chatID, nil
	}
	callerScope := scopeForms(callerChannel, callerChatID)
	if len(callerScope) == 0 {
		return "", fmt.Errorf("refusing to resolve agent address %q: caller session scope is unknown (callerChannel=%q callerChatID=%q) — structured messages must be sent from a concrete session", chatID, callerChannel, callerChatID)
	}

	// 同一顶层会话的子树内可互发（D-10 放宽，2026-10-22 用户拍板）：
	// 分层汇报要求子代理把结论上报给上层会话，而结构化消息是唯一可批复的通道 ——
	// 严格「只认直接父会话」会让分层治理和批复互斥，没有第三条路。
	// 因此作用域 = 调用方自身 + **它的直接上级所在的那一层**（rootForms）。
	// 语义定死：同一顶层会话（如 web:web-4）的子树内可互发；跨顶层会话
	// （web-5、别的用户）一律拒。地址里没有组织关系，所以这里**不校验层级**：
	// 「只向上报、不许平级串」靠 CHARTCHECK 纪律 + 审计抽查，不靠地址校验。
	// 顶层调用方（顶层会话 web:web-4）没有上级 → rootForms 为空，等价于只到直接子级。
	rootForms := map[string]struct{}{}
	if pch, pchat := parseInteractiveKeyParent(callerChatID); pch != "" && pchat != "" {
		rootForms = scopeForms(pch, pchat)
	}
	// 顶层调用方（顶层会话）额外覆盖**整棵子树**：分层改造后子代理组挂在调度方下面，
	// 不是顶层会话的直接子级，只靠 rootForms 会让批复/催办全被挡（见 subtreePrefixes）。
	subtree := subtreePrefixes(callerChannel, callerChatID)
	inScope := func(forms map[string]struct{}) bool {
		return scopeIntersects(forms, callerScope) ||
			scopeIntersects(forms, rootForms) ||
			matchesSubtreePrefix(forms, subtree)
	}

	// 已经是物理 key：仍要校验作用域，否则凭一段物理 key 就能越权寻址别人的子代理。
	if isPhysicalInteractiveKey(chatID) {
		if !inScope(candidateScopeForms(chatID, "")) {
			pch, pchat := parseInteractiveKeyParent(chatID)
			return "", fmt.Errorf("refusing to address sub-agent session %q: its parent session is %q, which is outside the caller's session tree (%s + root %s) — cross-top-level-session addressing is not allowed", chatID, qualifyChatID(pch, pchat), joinScopeForms(callerScope), joinScopeForms(rootForms))
		}
		return chatID, nil
	}

	// 逻辑地址必须是 "role/instance"。裸 role（"tester"）不再放行（D-11）：
	// 它没有父会话信息，旧实现直接把它当 chatID 用 → 又回到「建野会话」的老坑。
	role, instance, ok := strings.Cut(chatID, "/")
	if !ok || role == "" {
		return "", fmt.Errorf("invalid agent address %q: want \"role/instance\" or a full physical session key (like \"web:web-4/role:instance\"); a bare role name carries no parent session, so it cannot be resolved without creating a stray session — use session:<channel>:<chatID> to address a raw session", chatID)
	}

	var hits []string
	var foreign []string // 同名 role/instance 但挂在别的父会话下：只用于把错误说清楚

	// ① 内存注册表最权威（活着的实例）。
	a.interactiveSubAgents.Range(func(k, v any) bool {
		key, _ := k.(string)
		ia, isIA := v.(*interactiveAgent)
		if !isIA || ia == nil || key == "" {
			return true
		}
		ia.mu.Lock()
		roleName, inst, parentKey := ia.roleName, ia.instance, ia.parentKey
		ia.mu.Unlock()
		if roleName != role || inst != instance {
			return true
		}
		if inScope(candidateScopeForms(key, parentKey)) {
			hits = append(hits, key)
		} else {
			foreign = append(foreign, key)
		}
		return true
	})
	hits = dedupeStrings(hits)
	if len(hits) >= 2 {
		// 歧义即拒：同一调用方作用域下有多个物理会话自称是同一个
		// role/instance（典型成因：同一个子代理在不同父写法下被 spawn 了两次，
		// 于是内存里同时躺着 "web:web-4/tester:main" 和
		// "agent:web:web-4/tester:main" 两个 key，对应两个真实会话）。
		// 猜错就是把 report/decision 投进一个没人读的空壳 → 必须报错。
		return "", fmt.Errorf("ambiguous agent address %q: %d sub-agent sessions match under the caller's scope (%s) — refusing to guess; address one of them by its physical key (session:<key>) or unload the stale duplicate", chatID, len(hits), strings.Join(hits, ", "))
	}
	if len(hits) == 1 {
		return hits[0], nil
	}

	// ② 进程重启后内存空了：用持久化注册表重建物理 key。agent_key 全局唯一
	// （"agent:role/instance"），所以 DB 侧最多一条；父不匹配就只能报错。
	if a.multiSession != nil {
		if svc := a.interactiveAgentSvc(); svc != nil {
			if rec, err := svc.Get("agent:" + role + "/" + instance); err == nil && rec != nil &&
				rec.RoleName == role && rec.Instance == instance {
				switch {
				case rec.ParentChannel == "" || rec.ParentChatID == "":
					// 老版本写进来的空父键记录（2026-10-21 之前），无法判定归属。
					foreign = append(foreign, fmt.Sprintf("%s (registry row has no parent session — written before 2026-10-21)", rec.AgentKey))
				case inScope(candidateScopeForms("", qualifyChatID(rec.ParentChannel, rec.ParentChatID))):
					rebuilt := interactiveKey(rec.ParentChannel, rec.ParentChatID, rec.RoleName, rec.Instance)
					if rebuilt != "" {
						return rebuilt, nil
					}
					return "", fmt.Errorf("sub-agent %q is registered but its session key cannot be rebuilt (parent=%s:%s)", chatID, rec.ParentChannel, rec.ParentChatID)
				default:
					foreign = append(foreign, fmt.Sprintf("%s (parent session %s:%s)", rec.AgentKey, rec.ParentChannel, rec.ParentChatID))
				}
			}
		}
	}

	if len(foreign) > 0 {
		sort.Strings(foreign)
		return "", fmt.Errorf("sub-agent %q belongs to other parent session(s): %s — it is outside the caller's session tree (caller %s, root %s); structured messages may only cross sessions under the same top-level session", chatID, strings.Join(foreign, ", "), joinScopeForms(callerScope), joinScopeForms(rootForms))
	}
	return "", fmt.Errorf("sub-agent %q is not registered (no in-memory instance and no registry row with parent) — refusing to create a stray session; spawn it first or address session:<channel>:<chatID>", chatID)
}

// dedupeStrings 去重并保持输入顺序（注册表重建值与内存 key 可能同形）。
func dedupeStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
