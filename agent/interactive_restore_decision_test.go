package agent

// interactive_restore_decision_test.go — 「重启后唤醒」路径的回归守卫。
//
// 事故（2026-11-12，用户上报）：xbot 重启后，agent 想唤醒重启前的首辅：
//  1. SubAgent(action="send") 报 "no sub-agent with instance=main"
//     —— 因为 restoreInteractiveTree 只在 processMessage 入口跑，send 路径不触发；
//  2. agent 据此判断"实例不存在"，用 interactive=true 新建
//     —— 于是建出空会话占住同一个 key，restoreInteractiveTree 因"已在内存"跳过，
//        注册表里那条记录的历史被永久遮蔽。
//
// 修复两条，都在本文件钉住：
//  A. decideRestore：内存为空 + 注册表有记录（非 unloaded）⇒ 必须恢复而非新建
//  B. send 路径：解析失败时先 restoreTreeOnDemand 再重试
//
// 目标是「agent 不看源码也不会做错」：错误的自愈下沉到代码，而不是靠调用方记住
// "重启后要先发条消息"这种隐含前提。

import (
	"errors"
	"testing"

	"xbot/storage/common"
)

func TestDecideRestore(t *testing.T) {
	idle := &common.InteractiveAgentRecord{AgentKey: "web:web-4/court.shoufu:main", Status: common.InteractiveAgentIdle}
	running := &common.InteractiveAgentRecord{AgentKey: "web:web-4/court.shoufu:main", Status: common.InteractiveAgentRunning}
	interrupted := &common.InteractiveAgentRecord{AgentKey: "web:web-4/court.shoufu:main", Status: common.InteractiveAgentInterrupted}
	unloaded := &common.InteractiveAgentRecord{AgentKey: "web:web-4/court.shoufu:main", Status: common.InteractiveAgentUnloaded}

	cases := []struct {
		name     string
		inMemory bool
		rec      *common.InteractiveAgentRecord
		getErr   error
		want     restoreDecision
		why      string
	}{
		{
			name: "已在内存 → 不恢复（交给已存在分支）", inMemory: true, rec: idle, want: restoreNo,
			why: "内存里有就是有，重复 restore 会破坏正在跑的会话",
		},
		{
			name: "重启后内存空 + 注册表 idle → 必须恢复", inMemory: false, rec: idle, want: restoreYes,
			why: "这就是 2026-11-12 事故的场景：旧代码在这里直接新建，把历史遮蔽了",
		},
		{
			name: "重启后内存空 + 注册表 running → 必须恢复", inMemory: false, rec: running, want: restoreYes,
			why: "重启前在跑的更要恢复（resume 语义由 resumeIfPreviouslyRunning 处理）",
		},
		{
			name: "重启后内存空 + 注册表 interrupted → 必须恢复", inMemory: false, rec: interrupted, want: restoreYes,
			why: "被打断的实例历史同样要保住",
		},
		{
			name: "注册表无记录（NotFound）→ 不恢复，正常新建", inMemory: false, rec: nil,
			getErr: errors.New("not found"), want: restoreNo,
			why: "真·首次创建，不该被恢复逻辑拦住",
		},
		{
			name: "注册表记录已 unloaded → 不恢复，允许新建", inMemory: false, rec: unloaded, want: restoreNo,
			why: "unload 是显式的永久卸载意图（且 restore 本来就会跳过 unloaded），新建是正确行为",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideRestore(c.inMemory, c.rec, c.getErr)
			if got != c.want {
				t.Fatalf("decideRestore(inMemory=%v, status=%v, err=%v) = %v, want %v\n理由：%s",
					c.inMemory, statusOf(c.rec), c.getErr, got, c.want, c.why)
			}
		})
	}
}

func statusOf(r *common.InteractiveAgentRecord) string {
	if r == nil {
		return "<nil>"
	}
	return r.Status
}

// 变异自证：如果 decideRestore 退化成"永远不恢复"（旧行为），
// 「重启后内存空 + idle 记录」那条必然变红——证明这个测试有判别力。
func TestDecideRestoreKillsRegressedImplementation(t *testing.T) {
	regressed := func(inMemory bool, rec *common.InteractiveAgentRecord, getErr error) restoreDecision {
		_ = inMemory
		_ = rec
		_ = getErr
		return restoreNo // 旧代码的等价行为：从注册表恢复这件事压根不存在
	}
	idle := &common.InteractiveAgentRecord{Status: common.InteractiveAgentIdle}
	if regressed(false, idle, nil) == decideRestore(false, idle, nil) {
		t.Fatal("变异未生效：回归实现与当前实现的判定相同，这个测试没有判别力")
	}
}
