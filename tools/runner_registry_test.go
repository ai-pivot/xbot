package tools

import (
	"path/filepath"
	"sort"
	"testing"

	"xbot/storage/sqlite"
)

// ============================================================================
// runner 注册表分类（幽灵执行目标的判定）
//
// 背景（用户实测）：注册表里 3~7 月的历史行（default/ubuntu/web1/remote-arch/
// linked）既没有 SSH 目标也没在线，却出现在「切换执行目标」列表里；选中即把会话
// 绑到不存在的机器 ⇒ SandboxRouter 对"已绑定但离线"硬失败 ⇒ 该会话每次工具调用全废。
// 分类由核心给出（唯一权威），执行目标只列 selectable 的行。
// ============================================================================

// registryFixture 建一个带 runners 表的临时库并种入给定 runner。
func registryFixture(t *testing.T, names ...string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewRunnerStore(db.Conn())
	for _, name := range names {
		if _, err := store.Create(name, "native", "", "", RunnerLLMSettings{}); err != nil {
			t.Fatalf("seed runner %q: %v", name, err)
		}
	}
	return db
}

// bindRunner 种一条会话→runner 绑定（tenants.runner_id）。
func bindRunner(t *testing.T, db *sqlite.DB, channel, chatID, runner string) {
	t.Helper()
	if _, err := db.Conn().Exec(
		`INSERT INTO tenants (channel, chat_id, runner_id) VALUES (?, ?, ?)`, channel, chatID, runner); err != nil {
		t.Fatalf("seed binding %s:%s → %s: %v", channel, chatID, runner, err)
	}
}

// withSandbox 把全局 sandbox 换成一个持有若干"已连接" runner 的 router（测试用）。
func withSandbox(t *testing.T, onlineRunners ...string) {
	t.Helper()
	router, _ := newRemoteRouter(onlineRunners...)
	SetSandbox(router)
	t.Cleanup(func() { SetSandbox(&NoneSandbox{}) })
}

// namesOf 提取注册表行的名字（排序后比较，避免依赖 created_at 同秒时的名字序）。
func namesOf(entries []RunnerEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	return names
}

func entryOf(t *testing.T, registry RunnerRegistry, name string) RunnerEntry {
	t.Helper()
	for _, e := range registry.Runners {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("runner %q missing from registry %v", name, namesOf(registry.Runners))
	return RunnerEntry{}
}

// ② 不可选：既不受管（没有 SSH 目标）也不在线的行 = 遗留登记 ⇒ selectable=false
//
//	且被列进 orphans（管理面板的清理入口）；受管/在线的行可选中。
func TestBuildRunnerRegistry_OrphansAreNotSelectable(t *testing.T) {
	db := registryFixture(t, "default", "ubuntu", "web1", "remote-arch", "linked", "b300-4")
	bindRunner(t, db, "web", "chat_1", "b300-4")
	withSandbox(t, "b300-4") // 只有 b300-4 真的连着

	registry, err := BuildRunnerRegistry(db.Conn(), []string{"b300-4"})
	if err != nil {
		t.Fatalf("BuildRunnerRegistry: %v", err)
	}

	// 用户现场：5 条幽灵 + 1 台真机器
	wantOrphans := []string{"default", "linked", "remote-arch", "ubuntu", "web1"}
	if got := append([]string(nil), registry.Orphans...); !equalStringSets(got, wantOrphans) {
		t.Fatalf("orphans = %v, want %v", got, wantOrphans)
	}

	live := entryOf(t, registry, "b300-4")
	if live.State != RunnerStateManaged || !live.Selectable || !live.Managed {
		t.Fatalf("managed+live entry = %+v, want state=managed selectable", live)
	}
	if live.BoundCount != 1 {
		t.Fatalf("b300-4 bound_count = %d, want 1", live.BoundCount)
	}

	for _, name := range wantOrphans {
		entry := entryOf(t, registry, name)
		if entry.State != RunnerStateOrphan {
			t.Errorf("%s state = %q, want orphan", name, entry.State)
		}
		if entry.Selectable {
			t.Errorf("%s must NOT be selectable (it cannot exist as a machine)", name)
		}
	}
}

// 受管但离线的机器仍然可选中（产品语义：绑定 ≠ 连接）——前提是目标还在，
// 选择器负责在**选择时**给出离线原因（前端契约）。
func TestBuildRunnerRegistry_ManagedOfflineStaysSelectable(t *testing.T) {
	db := registryFixture(t, "gpu-01")
	withSandbox(t) // 没有任何在线 runner

	registry, err := BuildRunnerRegistry(db.Conn(), []string{"gpu-01"})
	if err != nil {
		t.Fatalf("BuildRunnerRegistry: %v", err)
	}
	entry := entryOf(t, registry, "gpu-01")
	if entry.State != RunnerStateManaged || !entry.Selectable {
		t.Fatalf("managed offline entry = %+v, want managed+selectable", entry)
	}
	if len(registry.Orphans) != 0 {
		t.Fatalf("orphans = %v, want none (the machine is managed)", registry.Orphans)
	}
}

// 在线但未纳管的机器是真机器（live）：可选中，且绝不算遗留。
func TestBuildRunnerRegistry_LiveUnmanagedIsKept(t *testing.T) {
	db := registryFixture(t, "manual-box")
	withSandbox(t, "manual-box")

	registry, err := BuildRunnerRegistry(db.Conn(), nil) // 管理面板里没有它
	if err != nil {
		t.Fatalf("BuildRunnerRegistry: %v", err)
	}
	entry := entryOf(t, registry, "manual-box")
	if entry.State != RunnerStateLive || !entry.Selectable || entry.Managed {
		t.Fatalf("live unmanaged entry = %+v, want state=live selectable", entry)
	}
	if len(registry.Orphans) != 0 {
		t.Fatalf("orphans = %v, want none (a connected machine is real)", registry.Orphans)
	}
}

// 声明集合是「集合」：重复/空白名字不得产生重复分类或幻影行。
func TestBuildRunnerRegistry_DedupsManagedSet(t *testing.T) {
	db := registryFixture(t, "m1")
	withSandbox(t)

	registry, err := BuildRunnerRegistry(db.Conn(), []string{"m1", " m1 ", "", "   ", "m1"})
	if err != nil {
		t.Fatalf("BuildRunnerRegistry: %v", err)
	}
	if len(registry.Runners) != 1 || registry.Runners[0].Name != "m1" {
		t.Fatalf("runners = %v, want exactly [m1]", namesOf(registry.Runners))
	}
	if !registry.Runners[0].Managed {
		t.Fatal("m1 must be classified managed")
	}
}

// ④ 写入侧：中途放弃的纳管（create → 回滚 delete）不留孤儿；删除幂等
//
//	（重复删除同一行仍然成功，注册表保持"该行不存在"）。
func TestRunnerRegistry_AbortedEnrollmentLeavesNoTraceAndDeleteIsIdempotent(t *testing.T) {
	db := registryFixture(t)
	withSandbox(t)
	store := NewRunnerStore(db.Conn())

	// 纳管流程第一步：runner_create（必须在连接前铸 token ⇒ 行先于机器存在）
	if _, err := store.Create("ghost-1", "native", "", "", RunnerLLMSettings{}); err != nil {
		t.Fatalf("runner_create: %v", err)
	}
	registry, err := BuildRunnerRegistry(db.Conn(), nil)
	if err != nil {
		t.Fatalf("BuildRunnerRegistry: %v", err)
	}
	if got := registry.Orphans; len(got) != 1 || got[0] != "ghost-1" {
		t.Fatalf("orphans = %v, want [ghost-1] (an unrolled-back enrollment is a ghost)", got)
	}

	// 流程失败/被取消 ⇒ 回滚（前端 discardEnrollment 调的就是 runner_delete）
	if err := store.Delete("ghost-1"); err != nil {
		t.Fatalf("rollback delete: %v", err)
	}
	// 幂等：再删一次仍然成功
	if err := store.Delete("ghost-1"); err != nil {
		t.Fatalf("second delete must be a no-op success, got %v", err)
	}
	registry, err = BuildRunnerRegistry(db.Conn(), nil)
	if err != nil {
		t.Fatalf("BuildRunnerRegistry after rollback: %v", err)
	}
	if len(registry.Runners) != 0 || len(registry.Orphans) != 0 {
		t.Fatalf("registry after rollback = %v / orphans %v, want empty",
			namesOf(registry.Runners), registry.Orphans)
	}
}

// 列表是只读的：分类绝不改动注册表（无删除、无 token 轮换）。
func TestBuildRunnerRegistry_IsReadOnly(t *testing.T) {
	db := registryFixture(t, "live-1", "orphan-1")
	withSandbox(t, "live-1")

	before, err := NewRunnerStore(db.Conn()).Token("orphan-1")
	if err != nil {
		t.Fatalf("read token before: %v", err)
	}
	if _, err := BuildRunnerRegistry(db.Conn(), []string{"live-1"}); err != nil {
		t.Fatalf("BuildRunnerRegistry: %v", err)
	}
	after, err := NewRunnerStore(db.Conn()).Token("orphan-1")
	if err != nil {
		t.Fatalf("read token after: %v (the orphan row must survive listing)", err)
	}
	if before != after {
		t.Fatal("listing must not touch credentials")
	}
	if _, err := NewRunnerStore(db.Conn()).Token("live-1"); err != nil {
		t.Fatalf("managed row missing after listing: %v", err)
	}
	// 清理必须由调用方显式执行（管理面板的删除入口）。
	if err := NewRunnerStore(db.Conn()).Delete("orphan-1"); err != nil {
		t.Fatalf("explicit cleanup delete: %v", err)
	}
	registry, err := BuildRunnerRegistry(db.Conn(), []string{"live-1"})
	if err != nil {
		t.Fatalf("BuildRunnerRegistry after cleanup: %v", err)
	}
	if len(registry.Orphans) != 0 {
		t.Fatalf("orphans after explicit cleanup = %v, want none", registry.Orphans)
	}
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
