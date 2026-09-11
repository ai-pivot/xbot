package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/tools"
)

// 主 agent 与 subagent 必须共享同一个 offload 存储（相互可见）：
//   - 写入侧：subagent 的 runState.offloadSessionKey = cfg.RootSessionKey
//     （engine_run.go），cfg.RootSessionKey 由 buildSubAgentRunConfig 从
//     parentCtx 继承（canonical root session）；
//   - 读取侧：offload_recall 用 ctx.RootSessionKey 定位 session 目录
//     （tools/offload_recall.go）—— 必须与写入侧同源，否则 subagent 自己
//     offload 的内容自己都召回不了、主 agent 更看不到。
func TestSubAgentOffloadSharesRootSessionStore(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{
		multiSession: mt,
		tools:        tools.NewRegistry(),
		skills:       NewSkillStore(t.TempDir(), nil, nil),
	}
	ctx := WithUserContext(context.Background(), &UserContext{})

	rootKey := "web:chat-share"
	parentCtx := &tools.ToolContext{
		AgentID:  "main",
		Channel:  "web",
		ChatID:   "chat-share",
		SenderID: "u1",
	}
	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, "task", "", nil, tools.SubAgentCapabilities{}, "reviewer", false, "inst-1", "")

	if cfg.SessionKey == rootKey {
		t.Fatalf("subagent 的 SessionKey 必须与 root 隔离（得到 %q）", cfg.SessionKey)
	}
	if cfg.RootSessionKey != rootKey {
		t.Fatalf("cfg.RootSessionKey=%q, want %q —— subagent 的 offload 会落到独立目录，主 agent 看不到",
			cfg.RootSessionKey, rootKey)
	}

	// 读取侧同源：ToolContext 必须带 RootSessionKey（offload_recall 依赖它）。
	tc := buildToolContext(ctx, &cfg)
	if tc.RootSessionKey != rootKey {
		t.Fatalf("ToolContext.RootSessionKey=%q, want %q —— subagent 内 offload_recall 查不到父 session 的 offload",
			tc.RootSessionKey, rootKey)
	}

	// 端到端（同一 store）：subagent offload → 主 agent recall，反之亦然。
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{StoreDir: dir})
	big := strings.Repeat("payload-", 4000)

	res, ok := store.MaybeOffload(ctx, cfg.RootSessionKey, cfg.SessionKey, "Shell", "cmd", big, "", "", "")
	if !ok {
		t.Fatal("subagent 侧 expect offload，未触发")
	}
	got, err := store.Recall(rootKey, res.ID) // 主 agent 用 canonical root key 召回
	if err != nil {
		t.Fatalf("主 agent 无法召回 subagent 的 offload: %v", err)
	}
	if got != big {
		t.Fatal("主 agent 召回内容不匹配")
	}

	res2, ok2 := store.MaybeOffload(ctx, rootKey, rootKey, "Read", "f.txt", big, "", "", "")
	if !ok2 {
		t.Fatal("主 agent 侧 expect offload，未触发")
	}
	got2, err2 := store.Recall(cfg.RootSessionKey, res2.ID) // subagent 召回主 agent 的 offload
	if err2 != nil {
		t.Fatalf("subagent 无法召回主 agent 的 offload: %v", err2)
	}
	if got2 != big {
		t.Fatal("subagent 召回内容不匹配")
	}
}

// 共享 store 后，压缩清理必须按归属隔离：主 agent 的 post-compress 清理只删
// 自己产生的条目，不能顺手删掉 SubAgent 仍在引用的条目（反之亦然）。否则
// SubAgent 压缩一次，主 agent 的 offload 就凭空消失（共享引入的数据丢失）。
func TestSharedOffloadStore_CleanupIsOwnershipScoped(t *testing.T) {
	ctx := context.Background()
	store := NewOffloadStore(OffloadConfig{StoreDir: t.TempDir()})

	rootKey := "web:chat-clean"
	subKey := "web:chat-clean#reviewer"

	big := strings.Repeat("payload-", 4000)
	main1, ok := store.MaybeOffload(ctx, rootKey, rootKey, "Read", "main.go", big, "", "", "")
	if !ok {
		t.Fatal("main offload not triggered")
	}
	sub1, ok := store.MaybeOffload(ctx, rootKey, subKey, "Shell", "go test ./...", big, "", "", "")
	if !ok {
		t.Fatal("subagent offload not triggered")
	}

	// SubAgent 压缩：只引用自己的条目 → 只能删自己的；主 agent 的必须留下。
	if removed := store.CleanUnreferencedEntries(rootKey, subKey, map[string]bool{sub1.ID: true}); removed != 0 {
		t.Fatalf("subagent cleanup removed %d entries, want 0（主 agent 的条目被误删）", removed)
	}
	if _, err := store.Recall(rootKey, main1.ID); err != nil {
		t.Fatalf("主 agent 的 offload 被 SubAgent 的清理删掉了: %v", err)
	}
	if _, err := store.Recall(rootKey, sub1.ID); err != nil {
		t.Fatalf("SubAgent 自己引用的条目不该被删: %v", err)
	}

	// SubAgent 不再引用自己的条目 → 自己的可删，主 agent 的仍不可动。
	if removed := store.CleanUnreferencedEntries(rootKey, subKey, map[string]bool{}); removed != 1 {
		t.Fatalf("subagent cleanup removed %d, want 1（只清自己未引用的）", removed)
	}
	if _, err := store.Recall(rootKey, main1.ID); err != nil {
		t.Fatalf("主 agent 的 offload 不该被删: %v", err)
	}

	// 主 agent 清理：只清自己的。
	main2, ok := store.MaybeOffload(ctx, rootKey, rootKey, "Grep", "pattern", big, "", "", "")
	if !ok {
		t.Fatal("main offload #2 not triggered")
	}
	if removed := store.CleanUnreferencedEntries(rootKey, rootKey, map[string]bool{main2.ID: true}); removed != 1 {
		t.Fatalf("main cleanup removed %d, want 1（只清自己未引用的 main1）", removed)
	}
	if _, err := store.Recall(rootKey, main2.ID); err != nil {
		t.Fatalf("主 agent 引用的条目不该被删: %v", err)
	}
}
