package agent

// interactive_persist.go —— interactive SubAgent 的持久化与「递归恢复」。
//
// 背景（2026-10-21）：interactiveSubAgents 是进程内 sync.Map，进程重启即清空，
// 于是重启前存在的子代理全部变成 `unknown channel`（SendMessage / SubAgent send
// 都找不到）。消息本身在各自的 TenantSession 里没丢，丢的是「可寻址的实例」。
//
// 设计：
//   - 每个子代理在 spawn / 状态变化时把「重建所需的最小事实」写进主库
//     interactive_agents 表（见 storage/sqlite/interactive_agents.go）；
//   - 主会话每次进入一个 turn（processMessage）时，按 parent 递归恢复整棵树：
//     父 → 子 → 孙，深度受限、幂等、单个失败不影响其它；
//   - 重启前处于 running 的子代理，其 goroutine 已随进程消失，不可能「恢复成
//     正在计算」；恢复时标记 interrupted，并复用主代理同一套 resume 路径
//     （InjectInboundResume → resume_turn metadata）续上同一个 turn。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xbot/bus"
	log "xbot/logger"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// maxAgentRestoreDepth 限制递归深度，防御 parent_key 意外成环。
const maxAgentRestoreDepth = 6

// interactiveAgentSvc 返回注册表的读写入口。
func (a *Agent) interactiveAgentSvc() *sqlite.InteractiveAgentService {
	return sqlite.NewInteractiveAgentService(a.multiSession.DB().Conn())
}

// splitParentKey 把 "channel:chatID" 拆成两段。
// 对 agent 通道，chatID 形如 "role/instance"，内部仍可能含 ":"（instance 带冒号），
// 所以只在第一个 ":" 处切分。
func splitParentKey(key string) (channel, chatID string) {
	if i := strings.Index(key, ":"); i > 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// persistInteractiveAgent 把会话的可恢复事实写入注册表（幂等 upsert）。
// status 传当前状态（running / idle / unloaded）。
func (a *Agent) persistInteractiveAgent(ia *interactiveAgent, status string) {
	if ia == nil || a.multiSession == nil {
		return
	}
	parentChannel, parentChatID := splitParentKey(ia.parentKey)

	originUserID := ""
	if ia.userCtx != nil {
		originUserID = ia.userCtx.SenderID
	}
	workspaceRoot := ""
	parentAgentID := ""
	if ia.cfg != nil {
		workspaceRoot = ia.cfg.WorkspaceRoot
		parentAgentID = ia.cfg.AgentID
	}

	rec := sqlite.InteractiveAgentRecord{
		AgentKey:      "agent:" + ia.roleName + "/" + ia.instance,
		ParentChannel: parentChannel,
		ParentChatID:  parentChatID,
		RoleName:      ia.roleName,
		Instance:      ia.instance,
		GroupID:       ia.groupID,
		Task:          ia.task,
		Background:    ia.background,
		Status:        status,
		ModelName:     ia.modelName(),
		SubID:         ia.subID,
		OriginUserID:  originUserID,
		WorkspaceRoot: workspaceRoot,
		ParentAgentID: parentAgentID,
		LastError:     ia.lastError,
		LastReply:     ia.lastReply,
	}
	if err := a.interactiveAgentSvc().Upsert(rec); err != nil {
		log.WithError(err).WithField("agent_key", rec.AgentKey).
			Warn("Failed to persist interactive agent (restart recovery will miss it)")
	}
}

// markPersistedAgentStatus 只更新状态与 turn_id。
func (a *Agent) markPersistedAgentStatus(ia *interactiveAgent, status string, turnID uint64) {
	if ia == nil || a.multiSession == nil {
		return
	}
	key := "agent:" + ia.roleName + "/" + ia.instance
	if err := a.interactiveAgentSvc().MarkStatus(key, status, turnID); err != nil {
		log.WithError(err).WithField("agent_key", key).Warn("Failed to mark interactive agent status")
	}
}

// restoreInteractiveTree 递归恢复 parent 名下的整棵子代理树。
//
// 幂等：已在内存中的实例直接跳过（LoadOrStore 语义）；单个恢复失败只记日志，
// 不影响同级的其它子代理，也不影响调用方（主会话照常处理当前消息）。
func (a *Agent) restoreInteractiveTree(ctx context.Context, parentChannel, parentChatID string, depth int) {
	if a.multiSession == nil || parentChannel == "" || parentChatID == "" {
		return
	}
	if depth > maxAgentRestoreDepth {
		log.WithFields(log.Fields{"parent": parentChannel + ":" + parentChatID, "depth": depth}).
			Warn("Sub-agent restore depth exceeded — stopping recursion (possible parent cycle)")
		return
	}

	rows, err := a.interactiveAgentSvc().ListChildAgents(parentChannel, parentChatID)
	if err != nil {
		log.WithError(err).Warn("Failed to list child agents for restore")
		return
	}
	if len(rows) == 0 {
		return
	}

	restored := 0
	for _, rec := range rows {
		if rec.Status == sqlite.InteractiveAgentUnloaded {
			continue
		}
		if err := a.restoreOneAgent(ctx, rec); err != nil {
			log.WithError(err).WithField("agent_key", rec.AgentKey).
				Warn("Failed to restore interactive agent (skipping subtree of this node)")
			continue
		}
		restored++
		// 递归恢复它的孩子：孙代理的 parent 是 (agent, "<role>/<instance>")。
		a.restoreInteractiveTree(ctx, "agent", rec.RoleName+"/"+rec.Instance, depth+1)
	}
	if restored > 0 {
		log.WithFields(log.Fields{
			"parent":   parentChannel + ":" + parentChatID,
			"restored": restored,
			"total":    len(rows),
			"depth":    depth,
		}).Info("Restored interactive sub-agents from the registry")
	}
}

// restoreOneAgent 重建单个子代理并注册 AgentChannel（幂等）。
func (a *Agent) restoreOneAgent(ctx context.Context, rec sqlite.InteractiveAgentRecord) error {
	mapKey := interactiveKey(rec.ParentChannel, rec.ParentChatID, rec.RoleName, rec.Instance)
	if existing, ok := a.interactiveSubAgents.Load(mapKey); ok {
		// 正在销毁的那一代不挡恢复（墓碑会话没有可用资源）：摘掉后重建。
		if ia, ok := existing.(*interactiveAgent); ok && ia != nil && ia.isDestroyed() {
			a.interactiveSubAgents.CompareAndDelete(mapKey, existing)
		} else {
			// 已在内存：如果记录说它 running，但内存里其实是 idle（上一个进程遗留），
			// 改标记并 resume。
			if ia, ok := existing.(*interactiveAgent); ok && ia != nil {
				a.resumeIfPreviouslyRunning(rec, ia)
			}
			return nil
		}
	}

	// 1) 重新解析 role 定义（以磁盘上的定义为准；文件被删则明确失败）。
	role, err := a.lookupRoleDefinition(rec.RoleName)
	if err != nil {
		return err
	}

	// 2) 重建 RunConfig：复用 spawn 的构造路径，保证工具集/提示词/模型与首次一致。
	//
	// 提示词必须带 role rules（如 court/CHARTER.md）：这里只传 role.SystemPrompt 的话，
	// 重启后恢复出来的部门会静默丢掉自己声明的规则（2026-10-22 review 发现的绕过路径）。
	parentMsg := bus.InboundMessage{
		Channel:  rec.ParentChannel,
		ChatID:   rec.ParentChatID,
		SenderID: rec.OriginUserID,
		Content:  rec.Task,
	}
	parentCtx := a.buildParentToolContext(ctx, rec.ParentChannel, rec.ParentChatID, rec.OriginUserID, parentMsg)
	cfg, err := a.buildSubAgentRunConfig(
		ctx, parentCtx, rec.Task, tools.RoleSystemPrompt(role), role.AllowedTools, role.Capabilities,
		role.Name, true, rec.Instance, rec.ModelName)
	if err != nil {
		return err
	}

	// systemPrompt 必须从**重建的 cfg** 里取回。
	//
	// ⛔ 2026-10-22 P0（"Error: assert: LLM messages must have exactly one system
	// message; got 0"）：旧实现漏了这一行 —— 恢复出来的 interactiveAgent
	// systemPrompt 是零值（Role=""），而 SendToInteractiveSession 的拼装是
	//   newMessages = [ia.systemPrompt] + ia.messages + [user]
	// ⇒ 零值消息被当成普通消息插在最前面，整条链**一条 system 都没有** ⇒
	// engine 的断言直接判 session 不可用（只能 unload 重建）。
	// 恢复路径必须与 spawn 路径同构：systemPrompt = cfg.Messages[0]。
	if len(cfg.Messages) == 0 || cfg.Messages[0].Role != "system" {
		return fmt.Errorf("restore sub-agent %q: rebuilt config carries no system prompt (messages=%d) — refusing to resurrect a promptless session",
			rec.AgentKey, len(cfg.Messages))
	}

	ia := &interactiveAgent{
		roleName:     rec.RoleName,
		instance:     rec.Instance,
		groupID:      rec.GroupID,
		task:         rec.Task,
		background:   rec.Background,
		cfg:          &cfg,
		systemPrompt: cfg.Messages[0], // 见上：拼装链的第一条，缺了就 got 0
		userCtx:      UserContextFromContext(ctx),
		parentKey:    rec.ParentChannel + ":" + rec.ParentChatID,
		lastUsed:     time.Now(),
		lastError:    rec.LastError,
		lastReply:    rec.LastReply,
	}

	// 3) 恢复历史消息（TenantSession，channel="agent"）。
	//
	// 会话 key 必须与 spawn 时的完全一致（interactiveKey → mapKey，形如
	// "web:chat/role:instance"）。旧实现传 ("agent", "<role>/<instance>") ——
	// 那是**另一个库**（恒为空），恢复出来的子代理看起来"历史全丢"，随后第一条
	// send 只剩 [system, user]，把用户的连续对话拆成孤立轮次。
	if ts, tsErr := a.multiSession.GetOrCreateSession("agent", mapKey); tsErr == nil {
		if msgs, mErr := ts.GetMessages(); mErr == nil {
			ia.messages = msgs
		} else {
			log.WithError(mErr).WithField("agent_key", rec.AgentKey).
				Warn("Restore: failed to load sub-agent history (session kept, history empty)")
		}
	} else {
		log.WithError(tsErr).WithField("agent_key", rec.AgentKey).
			Warn("Restore: failed to open sub-agent tenant session (history will look empty)")
	}

	// 4) 原子占位（并发恢复只留一个实例）。
	if _, loaded := a.interactiveSubAgents.LoadOrStore(mapKey, ia); loaded {
		return nil
	}

	// 5) 注册 AgentChannel（SendMessage 的寻址依赖它）。
	if a.registerAgentChannel != nil {
		sendFn := func(sendCtx context.Context, task string) (string, error) {
			// AgentChannel 的 sendFn 是「投递」语义：把消息交给这个子代理的
			// 会话继续处理即可，不在这里等它的完整回复（否则 SendMessage 会被
			// 长任务卡住，见 tools/send_message.go 的「立刻成功」约束）。
			if err := a.ContinueInteractiveSession(sendCtx, mapKey, task, rec.OriginUserID); err != nil {
				return "", err
			}
			return "delivered to " + rec.AgentKey, nil
		}
		if regErr := a.registerAgentChannel(rec.AgentKey, sendFn); regErr != nil {
			log.WithError(regErr).WithField("agent_key", rec.AgentKey).
				Warn("Restore: AgentChannel registration failed (SendMessage routing unavailable for this agent)")
		}
	}

	// 6) 「忙碌」恢复：上一个进程的 goroutine 已消失，标记 interrupted 后走 resume。
	a.resumeIfPreviouslyRunning(rec, ia)
	return nil
}

// resumeIfPreviouslyRunning 对「重启前仍在跑」的子代理触发 resume turn。
//
// 不是恢复成「正在计算」（物理上不可能），而是用与主代理相同的 resume 语义
// 续上同一个 turn：resume_turn metadata → 同一个 turnID → 内容拼接。
func (a *Agent) resumeIfPreviouslyRunning(rec sqlite.InteractiveAgentRecord, ia *interactiveAgent) {
	if rec.Status != sqlite.InteractiveAgentRunning && rec.Status != sqlite.InteractiveAgentInterrupted {
		return
	}
	ia.mu.Lock()
	alreadyRunning := ia.running
	ia.mu.Unlock()
	if alreadyRunning {
		return // 本进程里它已经真的在跑，不要再插一条 resume
	}
	key := "agent:" + rec.RoleName + "/" + rec.Instance
	if err := a.interactiveAgentSvc().MarkStatus(key, sqlite.InteractiveAgentInterrupted, rec.TurnID); err != nil {
		log.WithError(err).WithField("agent_key", key).Warn("Restore: failed to mark interrupted")
	}
	log.WithFields(log.Fields{
		"agent_key": key,
		"turn_id":   rec.TurnID,
	}).Info("Restore: resuming previously running sub-agent")
	a.InjectInboundResume("agent", rec.RoleName+"/"+rec.Instance, rec.OriginUserID)
}

// lookupRoleDefinition 从 agents 目录重新解析 role（恢复时以磁盘定义为准）。
func (a *Agent) lookupRoleDefinition(roleName string) (*tools.SubAgentRole, error) {
	dir := a.agentsDir
	roles, err := tools.LoadAgentRoles(dir)
	if err != nil {
		return nil, err
	}
	for i := range roles {
		if roles[i].Name == roleName {
			return &roles[i], nil
		}
	}
	return nil, &restoreRoleMissingError{role: roleName, dir: dir}
}

// restoreRoleMissingError 表示 role 定义文件已不存在 —— 恢复必须显式失败，
// 不能用一个空 prompt 的「假角色」顶上。
type restoreRoleMissingError struct {
	role string
	dir  string
}

func (e *restoreRoleMissingError) Error() string {
	return "cannot restore sub-agent: role definition " + e.role + " not found in " + e.dir +
		" (the role file was deleted or renamed after spawn)"
}
