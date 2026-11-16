package serverapp

import (
	"path/filepath"
	"testing"
	"time"

	"xbot/agent"
	"xbot/channel/web"
	"xbot/config"
)

// 现场取证（2026-11-04 10:31Z，~/.xbot/xbot.db 只读拷贝）：
//   tenants.channel='agent' 的行，chat_id **就是** interactiveSubAgents 的 map key：
//     'web:web-4/proj.xbot.dev:cron-subagent-scope'          （一级子代理，裸 key）
//     'agent:web:web-4/court.shoufu:main/court.gongbu:spot6-rebuild'
//                                                            （嵌套子代理，key 自带 agent:，
//                                                             因为它的父是 agent 会话）
//   所以 DB chat_id 与内存 key 同形，running 查询本身不会 miss。
//
// 那「前端看不到在跑的子代理」坏在哪？坏在行的**父会话标注**：
//   spawn 时 ia.parentKey = sessionAddress(origin)（agent/interactive.go:1344），
//   一级子代理的父是主会话 ⇒ parentKey="web:chat-1"（不含 "/"）；
//   ListInteractiveSessions 却把任何非空 parentKey 一律当成 agent 父
//   （45b69ce5 版 agent/interactive.go:3511-3513：parentChannel="agent"；
//   现已改由 interactiveParentAddress 归一化，见 interactive.go:3530 起的 ③ 分支）。于是
//   serverapp 树里 lookupAgent("agent:web:chat-1") 必然 miss，合成父也 parse 失败
//   ⇒ 行掉进 OrphanSubAgents ⇒ 前端 session-tree 只渲染 nodes 的 children
//   ⇒ 侧边栏根本没有这条子代理 ⇒ 用户看到「没有子代理在跑」。
//
// 下面的测试用真实 key 形态复现，并与 SubAgentList 的 running 断言分离，
// 以便判定坏的是「行不可见」还是「running 为 false」。

// newSubAgentFixture 起一个真实 Agent（临时目录里的真 sqlite），并返回
// buildWebCallbacks 产出的 callbacks。
func newSubAgentFixture(t *testing.T) (*agent.Agent, web.WebCallbacks) {
	t.Helper()
	dir := t.TempDir()
	ag, err := agent.New(agent.Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	return ag, buildWebCallbacks(&config.Config{}, ag, nil)
}

func insertAgentTenant(t *testing.T, ag *agent.Agent, chatID string) {
	t.Helper()
	if _, err := ag.MultiSession().DB().Conn().Exec(
		"INSERT INTO tenants (channel, chat_id, last_active_at) VALUES ('agent', ?, ?)",
		chatID, time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert agent tenant %q: %v", chatID, err)
	}
}

func insertWebTenant(t *testing.T, ag *agent.Agent, chatID string) {
	t.Helper()
	conn := ag.MultiSession().DB().Conn()
	if _, err := conn.Exec(
		"INSERT INTO tenants (channel, chat_id, last_active_at) VALUES ('web', ?, ?)",
		chatID, time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert web tenant %q: %v", chatID, err)
	}
	// ChatList（/api/chats）走 store.Tenant().ListUserChats —— 行必须同时存在于
	// user_chats，否则主会话行读不到。
	if _, err := conn.Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES ('web', 'web-1', ?, ?)",
		chatID, chatID,
	); err != nil {
		t.Fatalf("insert user_chats %q: %v", chatID, err)
	}
}

// TestSubAgentList_RunningVisible：running=true 的 interactive session 在
// SubAgentList 里必须恰好一行且 Running=true；置 false 后同一行 Running=false。
func TestSubAgentList_RunningVisible(t *testing.T) {
	ag, callbacks := newSubAgentFixture(t)
	const key = "web:chat-1/court.gongbu:inst-1"
	insertAgentTenant(t, ag, key)

	cleanup := ag.RegisterInteractiveSessionForTest(agent.InteractiveSessionSeed{
		Key:       key,
		ParentKey: "web:chat-1",
		Role:      "court.gongbu",
		Instance:  "inst-1",
		Running:   true,
	})
	defer cleanup()

	rows, err := callbacks.SubAgentList("web-1", true)
	if err != nil {
		t.Fatalf("SubAgentList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 sub-agent row, got %d: %#v", len(rows), rows)
	}
	if !rows[0].Running {
		t.Fatalf("running sub-agent row must report Running=true, got %#v", rows[0])
	}
	if rows[0].Historical {
		t.Fatalf("running sub-agent row must not be historical: %#v", rows[0])
	}

	// running=false 后同一行仍只有一行，且 Running=false。
	cleanup()
	ag.RegisterInteractiveSessionForTest(agent.InteractiveSessionSeed{
		Key:       key,
		ParentKey: "web:chat-1",
		Role:      "court.gongbu",
		Instance:  "inst-1",
		Running:   false,
	})
	rows, err = callbacks.SubAgentList("web-1", true)
	if err != nil {
		t.Fatalf("SubAgentList (idle): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 sub-agent row after idle, got %d: %#v", len(rows), rows)
	}
	if rows[0].Running {
		t.Fatalf("idle sub-agent row must report Running=false, got %#v", rows[0])
	}
}

// TestSessionTree_SubAgentAttachedToParent：运行中的一级子代理必须挂在
// 主会话 web:chat-1 的 children 下（不是 orphan），否则前端侧边栏不渲染它。
func TestSessionTree_SubAgentAttachedToParent(t *testing.T) {
	ag, callbacks := newSubAgentFixture(t)
	const parentChatID = "chat-1"
	const key = "web:chat-1/court.gongbu:inst-1"
	insertWebTenant(t, ag, parentChatID)
	insertAgentTenant(t, ag, key)

	cleanup := ag.RegisterInteractiveSessionForTest(agent.InteractiveSessionSeed{
		Key:       key,
		ParentKey: "web:chat-1",
		Role:      "court.gongbu",
		Instance:  "inst-1",
		Running:   true,
	})
	defer cleanup()

	res, err := callbacks.SessionTree("web-1", web.SessionSelector{Channel: "web", ChatID: parentChatID}, true, 0, 100)
	if err != nil {
		t.Fatalf("SessionTree: %v", err)
	}
	if len(res.OrphanSubAgents) != 0 {
		t.Fatalf("sub-agent must not be orphaned, got %#v", res.OrphanSubAgents)
	}
	var found []web.UserChatWithPreview
	var visit func(nodes []web.UserChatWithPreview)
	visit = func(nodes []web.UserChatWithPreview) {
		for _, node := range nodes {
			found = append(found, node)
			visit(node.Children)
		}
	}
	for _, node := range res.Sessions {
		found = append(found, node.UserChatWithPreview)
		visit(node.Children)
	}
	var match *web.UserChatWithPreview
	for i := range found {
		if found[i].FullKey == key {
			match = &found[i]
			break
		}
	}
	if match == nil {
		t.Fatalf("sub-agent %q not attached anywhere in the session tree: %#v", key, found)
	}
	if !match.Running {
		t.Fatalf("attached sub-agent row must be Running=true, got %#v", *match)
	}
}

// TestSessionTree_NestedSubAgentAttached：嵌套子代理（key 自带 agent: 前缀，
// 父是另一个子代理）也必须挂到父 agent 节点下，而不是 orphan。
func TestSessionTree_NestedSubAgentAttached(t *testing.T) {
	ag, callbacks := newSubAgentFixture(t)
	insertWebTenant(t, ag, "chat-1")

	const parentKey = "web:chat-1/court.shoufu:main"
	const nestedKey = "agent:web:chat-1/court.shoufu:main/court.gongbu:inst-2"
	insertAgentTenant(t, ag, parentKey)
	insertAgentTenant(t, ag, nestedKey)

	defer ag.RegisterInteractiveSessionForTest(agent.InteractiveSessionSeed{
		Key: parentKey, ParentKey: "web:chat-1", Role: "court.shoufu", Instance: "main",
	})()
	defer ag.RegisterInteractiveSessionForTest(agent.InteractiveSessionSeed{
		Key: nestedKey, ParentKey: "agent:" + parentKey, Role: "court.gongbu", Instance: "inst-2",
		Running: true,
	})()

	res, err := callbacks.SessionTree("web-1", web.SessionSelector{Channel: "web", ChatID: "chat-1"}, true, 0, 100)
	if err != nil {
		t.Fatalf("SessionTree: %v", err)
	}
	if len(res.OrphanSubAgents) != 0 {
		t.Fatalf("nested sub-agent must not be orphaned, got %#v", res.OrphanSubAgents)
	}

	var found []web.UserChatWithPreview
	var visit func(nodes []web.UserChatWithPreview)
	visit = func(nodes []web.UserChatWithPreview) {
		for _, node := range nodes {
			found = append(found, node)
			visit(node.Children)
		}
	}
	for _, node := range res.Sessions {
		found = append(found, node.UserChatWithPreview)
		visit(node.Children)
	}
	var match *web.UserChatWithPreview
	for i := range found {
		if found[i].FullKey == nestedKey {
			match = &found[i]
			break
		}
	}
	if match == nil {
		t.Fatalf("nested sub-agent %q not attached in tree: %#v", nestedKey, found)
	}
	if !match.Running {
		t.Fatalf("nested sub-agent row must be Running=true, got %#v", *match)
	}
}

// TestMainSessionRow_RunningFromChatCancelCh：主会话（web）的 running 仍由
// chatCancelCh 决定，不受子代理路径影响。
func TestMainSessionRow_RunningFromChatCancelCh(t *testing.T) {
	ag, callbacks := newSubAgentFixture(t)
	insertWebTenant(t, ag, "chat-1")

	ag.RegisterChatCancelForTest("web", "chat-1")
	defer ag.UnregisterChatCancelForTest("web", "chat-1")

	rows, err := callbacks.ChatList("web-1", "chat-1", "web")
	if err != nil {
		t.Fatalf("ChatList: %v", err)
	}
	// 默认会话（web-1）也会出现，只断言我们关心的那一行。
	var row *web.UserChatWithPreview
	for i := range rows {
		if rows[i].ChatID == "chat-1" {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("chat-1 row missing: %#v", rows)
	}
	if !row.Running || row.Status != "running" {
		t.Fatalf("main session with registered cancel channel must be Running=true: %#v", *row)
	}

	// 主会话的 running 完全由 chatCancelCh 决定：注销后必须立刻 idle。
	ag.UnregisterChatCancelForTest("web", "chat-1")
	rows, err = callbacks.ChatList("web-1", "chat-1", "web")
	if err != nil {
		t.Fatalf("ChatList (after unregister): %v", err)
	}
	for i := range rows {
		if rows[i].ChatID == "chat-1" {
			if rows[i].Running {
				t.Fatalf("main session must be idle after cancel channel removal: %#v", rows[i])
			}
			return
		}
	}
	t.Fatalf("chat-1 row missing after unregister: %#v", rows)
}

// findTreeRow 在 session tree 里找 key 对应的行（返回行本身、父 chat_id、是否
// 落进 orphan_subagents）。
func findTreeRow(res web.SessionTreeResult, key string) (row web.UserChatWithPreview, parentChatID string, found, orphaned bool) {
	for _, o := range res.OrphanSubAgents {
		if o.FullKey == key || o.ChatID == key {
			return o, "", true, true
		}
	}
	var walk func(nodes []web.UserChatWithPreview, parent string) (web.UserChatWithPreview, string, bool)
	walk = func(nodes []web.UserChatWithPreview, parent string) (web.UserChatWithPreview, string, bool) {
		for _, n := range nodes {
			if n.FullKey == key || n.ChatID == key {
				return n, parent, true
			}
			if r, p, ok := walk(n.Children, n.ChatID); ok {
				return r, p, true
			}
		}
		return web.UserChatWithPreview{}, "", false
	}
	tops := make([]web.UserChatWithPreview, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		tops = append(tops, s.UserChatWithPreview)
	}
	r, p, ok := walk(tops, "")
	return r, p, ok, false
}

// TestSessionTree_StoredMirrorParentSubAgentAttached 是 2026-11-04 打回项的
// **端到端复现**，用**同源配对**（门下省 P2）：
//
//	origin = (channel="agent", chatID="web:web-4")            ← df68f64a 之前 spawn 的上下文
//	parentKey = sessionAddress(origin)      = "agent:web:web-4"  （旧代码产物 = 镜像）
//	key       = interactiveKey(origin,...)  = "agent:web:web-4/<role>:<inst>"
//
// 注意 key 与 parentKey 同源：parentKey 是 agent:web:web-4 ⇒ key 必是
// agent:web:web-4/<role>:<inst>。「parentKey=agent:web-4 配 key=web:web-4/…」
// 这种配对在代码里产生不出来，用它做 e2e 是锁错对象（③ 的判别力会被
// key-parse 兜底吃掉，删 ③ 仍绿）。
//
// 45b69ce5 行为：③ 把 parentKey 拆成 ("agent","web:web-4")，而 key-parse 也得到
// 同一个值 ⇒ serverapp 里 lookupAgent("agent:web:web-4") miss、
// parseAgentTenantChatID("web:web-4") 失败 ⇒ 落 OrphanSubAgents ⇒ 侧边栏看不到。
//
// 修复后：父标注 = (web, web-4)，行挂在主会话 web:web-4 的 children 下。
// 变异自证（删掉 interactiveParentAddress 的 ③ 镜像分支）⇒ 本用例红。
func TestSessionTree_StoredMirrorParentSubAgentAttached(t *testing.T) {
	ag, callbacks := newSubAgentFixture(t)
	const parentChatID = "web-4"
	const key = "agent:web:web-4/court.gongbu:inst-1"
	insertWebTenant(t, ag, parentChatID)
	insertAgentTenant(t, ag, key)

	cleanup := ag.RegisterInteractiveSessionForTest(agent.InteractiveSessionSeed{
		Key: key, ParentKey: "agent:web:web-4", Role: "court.gongbu", Instance: "inst-1", Running: true,
	})
	defer cleanup()

	res, err := callbacks.SessionTree("web-1", web.SessionSelector{Channel: "web", ChatID: parentChatID}, true, 0, 100)
	if err != nil {
		t.Fatalf("SessionTree: %v", err)
	}
	if len(res.OrphanSubAgents) != 0 {
		t.Fatalf("存量镜像 parentKey 的子代理不得 orphan，got %#v", res.OrphanSubAgents)
	}
	row, attachedParent, found, orphaned := findTreeRow(res, key)
	if orphaned {
		t.Fatalf("sub-agent %q fell into orphan_subagents: %#v", key, row)
	}
	if !found {
		t.Fatalf("sub-agent %q not attached anywhere in the tree", key)
	}
	if attachedParent != parentChatID {
		t.Fatalf("sub-agent %q attached under %q, want %q", key, attachedParent, parentChatID)
	}
	if !row.Running {
		t.Fatalf("attached sub-agent row must be Running=true: %#v", row)
	}
}

// TestSessionTree_HistoricalSubAgentRowAttached 锁住「老数据不需要迁移脚本」这一半：
// 历史行（不再在内存里跑、只有 tenants 表里的行）的父标注由 **key 本身**解析
// （serverapp/callbacks.go:2324 normalizeSubAgentRow → parseAgentTenantChatID），
// 所以 pre-fix 的 parent_channel/parent_chat_id 不会影响它们的挂载。
func TestSessionTree_HistoricalSubAgentRowAttached(t *testing.T) {
	ag, callbacks := newSubAgentFixture(t)
	const key = "web:chat-1/court.gongbu:old-1"
	insertWebTenant(t, ag, "chat-1")
	insertAgentTenant(t, ag, key) // 只有 DB 行，没有内存会话（= 历史行）

	res, err := callbacks.SessionTree("web-1", web.SessionSelector{Channel: "web", ChatID: "chat-1"}, true, 0, 100)
	if err != nil {
		t.Fatalf("SessionTree: %v", err)
	}
	if len(res.OrphanSubAgents) != 0 {
		t.Fatalf("historical sub-agent row must not be orphaned, got %#v", res.OrphanSubAgents)
	}
	row, parentChatID, found, _ := findTreeRow(res, key)
	if !found {
		t.Fatalf("historical sub-agent %q not attached in tree", key)
	}
	if parentChatID != "chat-1" {
		t.Fatalf("historical sub-agent %q attached under %q, want chat-1", key, parentChatID)
	}
	if row.Running {
		t.Fatalf("historical row must not be running: %#v", row)
	}
}

func insertCLITenant(t *testing.T, ag *agent.Agent, chatID string) {
	t.Helper()
	if _, err := ag.MultiSession().DB().Conn().Exec(
		"INSERT INTO tenants (channel, chat_id, last_active_at) VALUES ('cli', ?, ?)",
		chatID, time.Now().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert cli tenant %q: %v", chatID, err)
	}
}

// TestSessionTree_CLIParentSubAgentAttachedToRealParent 是 2026-11-04 御前裁定的
// **cli 父活洞**端到端回归（假祖先，不是 orphan）。
//
// 缺陷：旧 sessionAddress 判据是「chatID 不含 / 才剥」，cli 父（chatID="cli:/home/smith/repo"
// 含 /）不剥 ⇒ spawn 写出 parentKey="agent:cli:/home/smith/repo" ⇒ 建树时当成 agent 父，
// key 解析出 ("agent","cli:/home/smith/repo") 再合成出 "cli:/home/smith" 父行 ——
// 子代理被挂成与真主会话同 chatID 的 agent 行，CLI 面板按 ParentChatID==chatID 过滤看不到它。
//
// 本用例的 parentKey **由生产函数产**（agent.SessionAddressForTest），不是手写常量：
// 回滚 sessionAddress ⇒ 本用例红（假祖先重新出现）。
func TestSessionTree_CLIParentSubAgentAttachedToRealParent(t *testing.T) {
	ag, callbacks := newSubAgentFixture(t)
	const parentChatID = "/home/smith/repo"
	const key = "agent:cli:/home/smith/repo/court.gongbu:inst-1"
	insertCLITenant(t, ag, parentChatID)
	insertAgentTenant(t, ag, key)

	parentKey, ok := agent.SessionAddressForTest("agent", "cli:"+parentChatID)
	if !ok {
		t.Fatal("SessionAddressForTest rejected a valid cli origin")
	}
	// 生产者侧的判据由 agent 包锁定（TestSessionAddress_CLIParentMirrorCollapses）；
	// 这里只消费它的产物，让「假祖先」在**树层面**自己暴露（变异时本用例会挂在下面的断言上）。

	cleanup := ag.RegisterInteractiveSessionForTest(agent.InteractiveSessionSeed{
		Key: key, ParentKey: parentKey, Role: "court.gongbu", Instance: "inst-1", Running: true,
	})
	defer cleanup()

	res, err := callbacks.SessionTree("web-1", web.SessionSelector{Channel: "cli", ChatID: parentChatID}, true, 0, 100)
	if err != nil {
		t.Fatalf("SessionTree: %v", err)
	}
	if len(res.OrphanSubAgents) != 0 {
		t.Fatalf("cli 父的子代理不得 orphan，got %#v", res.OrphanSubAgents)
	}
	row, attachedParent, found, _ := findTreeRow(res, key)
	if !found {
		t.Fatalf("sub-agent %q not attached in tree", key)
	}
	if attachedParent != parentChatID {
		t.Fatalf("sub-agent %q attached under %q, want the real cli parent %q", key, attachedParent, parentChatID)
	}
	if !row.Running {
		t.Fatalf("attached row must be Running=true: %#v", row)
	}

	// 假祖先的具体形状必须消失：不存在「与真主会话同 chatID 的 agent 行」，
	// 也不存在合成出来的 "cli:/home/smith" 父行。
	var all []web.UserChatWithPreview
	var walk func(nodes []web.UserChatWithPreview)
	walk = func(nodes []web.UserChatWithPreview) {
		for _, n := range nodes {
			all = append(all, n)
			walk(n.Children)
		}
	}
	for _, s := range res.Sessions {
		all = append(all, s.UserChatWithPreview)
		walk(s.Children)
	}
	for _, r := range all {
		if r.Type == "agent" && (r.ChatID == "cli:"+parentChatID || r.FullKey == "cli:"+parentChatID) {
			t.Fatalf("假祖先行又出现了（agent 行冒用真主会话 chatID）：%#v", r)
		}
		if r.ChatID == "/home/smith" {
			t.Fatalf("合成了错误的 cli 父行 /home/smith：%#v", r)
		}
	}
}
