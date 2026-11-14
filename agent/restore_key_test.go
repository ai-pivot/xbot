package agent

import (
	"testing"

	"xbot/storage/sqlite"
)

// TestRestoreMapKeyMatchesSpawnKey 锁住 2026-10-26 的「重启后父子关系丢失」bug。
//
// 现场：首辅（court.shoufu/main）派了工部（court.gongbu/qq-channel-plugin），
// 进程重启后工部被恢复，但首辅执行 SubAgent/子代理操作时报
//
//	no sub-agent with instance="qq-channel-plugin" in your sub-agent tree
//
// 根因：spawn 用 interactiveKey(origin, role, instance) 建内存 key，
// restore 却用 interactiveKey(parentChannel, parentChatID, role, instance)，
// 而 parentChatID 里带着 "/<父role>:<父instance>" 后缀 ⇒ 两次拼出的 key 不同。
func TestRestoreMapKeyMatchesSpawnKey(t *testing.T) {
	// 首辅在主会话 web:web-4 下 spawn 工部
	const originChannel = "web"
	const originChatID = "web-4"
	const parentRole, parentInstance = "court.shoufu", "main"

	spawnKey := interactiveKey(originChannel, originChatID, "court.gongbu", "qq-channel-plugin")

	rec := sqlite.InteractiveAgentRecord{
		AgentKey:      "agent:court.gongbu/qq-channel-plugin",
		ParentChannel: originChannel,
		// 持久化侧把父的完整路径写进 chatID
		ParentChatID: originChatID + "/" + parentRole + ":" + parentInstance,
		RoleName:     "court.gongbu",
		Instance:     "qq-channel-plugin",
	}

	gotOrigin, gotKey := restoreMapKey(rec)
	if gotKey != spawnKey {
		t.Fatalf("restore key 与 spawn key 不一致：\n spawn   = %q\n restore = %q", spawnKey, gotKey)
	}
	if gotOrigin != originChatID {
		t.Fatalf("origin chatID 解析错误: got %q want %q", gotOrigin, originChatID)
	}

	// 顶层（主会话直接 spawn）时 parentChatID 不含后缀，必须原样通过
	top := sqlite.InteractiveAgentRecord{
		ParentChannel: "web",
		ParentChatID:  "web-4",
		RoleName:      "court.shoufu",
		Instance:      "main",
	}
	if _, k := restoreMapKey(top); k != interactiveKey("web", "web-4", "court.shoufu", "main") {
		t.Fatalf("顶层恢复 key 错误: %q", k)
	}
}

// TestRestoreChildLookupKeyMatchesPersistedFormat 锁住递归恢复的查询键：
// 孙代理的 parent 记录是 (同 channel, "<本节点 chatID>/<role>:<instance>")，
// 旧实现查 ("agent", "<role>/<instance>") 与写入侧格式不符 ⇒ 孙代理永远恢复不出来。
func TestRestoreChildLookupKeyMatchesPersistedFormat(t *testing.T) {
	parent := sqlite.InteractiveAgentRecord{
		ParentChannel: "web",
		ParentChatID:  "web-4",
		RoleName:      "court.shoufu",
		Instance:      "main",
	}
	childChatID := parent.ParentChatID + "/" + parent.RoleName + ":" + parent.Instance
	want := "web-4/court.shoufu:main"
	if childChatID != want {
		t.Fatalf("子节点查询 chatID = %q, want %q", childChatID, want)
	}
}
