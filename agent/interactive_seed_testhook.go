package agent

import "time"

// InteractiveSessionSeed describes an in-memory interactive sub-agent session
// snapshot for external test packages.
//
// 为什么需要它：子代理的 running 状态只存在于 *Agent 的私有 map
// interactiveSubAgents 里（key 是 interactiveKey，value 是私有的
// *interactiveAgent）。外部包（serverapp 的 SubAgentList / SessionTree 测试）
// 无法直接构造这个状态，而走真实 SpawnInteractiveSession 需要 LLM。
type InteractiveSessionSeed struct {
	// Key 是 interactiveSubAgents 的 map key（会话地址），形如
	// "web:chat-1/role:inst" 或嵌套形态 "agent:web:chat-1/role:inst/role2:inst2"。
	Key string
	// ParentKey 是 spawn 时记录的父会话地址（sessionAddress 的结果），
	// 一级子代理为 "web:chat-1"，嵌套子代理为 "agent:web:chat-1/role:inst"。
	ParentKey  string
	Role       string
	Instance   string
	Task       string
	Running    bool
	Background bool
}

// RegisterInteractiveSessionForTest stores one interactive session directly in
// the in-memory registry and returns a cleanup function.
//
// 仅用于测试。生产代码不得调用：它跳过 spawn / Run 的全部生命周期（租户行、
// 进度快照、取消通道都不存在），只提供一个「内存里确实在跑」的快照。
func (a *Agent) RegisterInteractiveSessionForTest(seed InteractiveSessionSeed) (cleanup func()) {
	ia := &interactiveAgent{
		roleName:   seed.Role,
		instance:   seed.Instance,
		parentKey:  seed.ParentKey,
		running:    seed.Running,
		background: seed.Background,
		task:       seed.Task,
		lastUsed:   time.Now(),
	}
	a.interactiveSubAgents.Store(seed.Key, ia)
	return func() { a.interactiveSubAgents.Delete(seed.Key) }
}

// RegisterChatCancelForTest marks a MAIN session (web/cli/...) as busy by
// parking a placeholder cancel channel under the same key the real run path
// uses ("channel:chatID"). IsProcessingByChannel's main-session branch only
// checks the key's presence, so no run is needed.
//
// 仅用于测试：真实运行链路由 registerActiveCancelState 注册。
func (a *Agent) RegisterChatCancelForTest(channelName, chatID string) {
	a.chatCancelCh.Store(channelName+":"+chatID, make(chan struct{}))
}

// UnregisterChatCancelForTest removes a placeholder installed by
// RegisterChatCancelForTest.
func (a *Agent) UnregisterChatCancelForTest(channelName, chatID string) {
	a.chatCancelCh.Delete(channelName + ":" + chatID)
}

// SessionAddressForTest exposes the **production** sessionAddress so cross-package
// tests (serverapp 建树 e2e) can seed the parentKey that spawn would really write,
// instead of hardcoding a value that may drift from the producer.
//
// 仅测试用：生产代码不得调用。
func SessionAddressForTest(channel, chatID string) (string, bool) {
	return sessionAddress(channel, chatID)
}
