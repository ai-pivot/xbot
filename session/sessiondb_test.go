package session

// sessiondb_test.go — 每会话一个 DB（one session, one DB，v71）的守护测试。
//
// 覆盖五类不变量：
//  1. 惰性迁移对账：主库 session_messages/iteration_history → 会话库（行数/内容
//     一致 + 幂等重跑 + migrated 标记）；
//  2. 并发无上限：>32 个并发会话全部可写（用户硬性要求 —— 池无 LRU 上限，
//     活跃会话的库永不关闭）；
//  3. 写隔离：两个会话写各自的库文件，互不污染；
//  4. preview 写入路径：append → 主库 tenants.preview（display-only 跳过）；
//  5. 删除回收：DestroySession 删会话库文件（空间立即回收）。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// newSessionDBTestMT 创建一个 MultiTenantSession（临时目录主库）。
func newSessionDBTestMT(t *testing.T) *MultiTenantSession {
	t.Helper()
	mt, err := NewMultiTenant(t.TempDir() + "/sessiondb-test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mt.Close() })
	return mt
}

// TestLazyMigrationCopiesRowsAndSetsFlag — 惰性迁移对账：主库预置
// session_messages/iteration_history → GetOrCreateSession 触发迁移 → 会话库行数
// 一致 + tenants.migrated=1 + **主库行已删**（v72 契约：惰性迁移成功后删主库冗余
// 行；数据唯一权威在会话库，下面对账已验证）。
func TestLazyMigrationCopiesRowsAndSetsFlag(t *testing.T) {
	mt := newSessionDBTestMT(t)

	// 在主库预置数据（模拟 v71 之前的存量会话）：直接经主库 SessionService 写
	//（m.sessionSvc 绑定主库 —— 拆库后它只服务迁移源/遗留路径）。
	tenantID, err := mt.tenantSvc.GetOrCreateTenantID("test", "migrate-me")
	if err != nil {
		t.Fatal(err)
	}
	const msgCount = 5
	for i := 0; i < msgCount; i++ {
		if _, err := mt.sessionSvc.AppendMessage(tenantID, llm.NewUserMessage(fmt.Sprintf("msg-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := mt.sessionSvc.AppendIterationHistory(tenantID, 0, 100, sqlite.IterationRecord{
		MessageID: 0, TurnID: 100, Iteration: 1, Content: "iter-1",
	}); err != nil {
		t.Fatal(err)
	}
	// 迁移前：migrated=0。
	info, err := mt.tenantSvc.GetTenantDBInfo(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Migrated {
		t.Fatal("tenant marked migrated before any session DB open")
	}

	// GetOrCreateSession 触发惰性迁移（打开会话库 + 复制 + 置 migrated=1）。
	sess, err := mt.GetOrCreateSession("test", "migrate-me")
	if err != nil {
		t.Fatal(err)
	}

	// 会话库行数对账：消息经会话库读（TenantSession.SessionService 绑定会话库）。
	msgs, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != msgCount {
		t.Fatalf("session DB messages = %d, want %d (migration copy)", len(msgs), msgCount)
	}
	for i, m := range msgs {
		if m.Content != fmt.Sprintf("msg-%d", i) {
			t.Fatalf("session DB message %d content = %q, want %q", i, m.Content, fmt.Sprintf("msg-%d", i))
		}
	}
	iters, err := sess.GetIterationHistoryByTurns([]uint64{100})
	if err != nil {
		t.Fatal(err)
	}
	if len(iters[100]) != 1 || iters[100][0].Content != "iter-1" {
		t.Fatalf("session DB iteration_history = %+v, want 1 row iter-1", iters[100])
	}

	// migrated=1（重跑迁移的闸门 —— DELETE+INSERT 幂等的前提）。
	info, err = mt.tenantSvc.GetTenantDBInfo(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Migrated {
		t.Fatal("tenant not marked migrated after session DB open")
	}
	if info.DBPath == "" {
		t.Fatal("tenant db_path not assigned")
	}

	// v72 契约：惰性迁移成功（migrated=1）后删除主库冗余行 —— 不变量
	//「主库 session_messages 只持有 migrated=0 残留者的数据，随时间归零」。
	// 数据唯一权威在会话库（上面对账已验证）；主库行是已被会话库替代的副本。
	mainCount := 0
	if err := mt.db.Conn().QueryRow(
		"SELECT COUNT(*) FROM session_messages WHERE tenant_id = ?", tenantID,
	).Scan(&mainCount); err != nil {
		t.Fatal(err)
	}
	if mainCount != 0 {
		t.Fatalf("main DB rows = %d, want 0 (deleted after successful lazy migration — v72 invariant)", mainCount)
	}
	// iteration_history 同理。
	iterCount := 0
	if err := mt.db.Conn().QueryRow(
		"SELECT COUNT(*) FROM iteration_history WHERE tenant_id = ?", tenantID,
	).Scan(&iterCount); err != nil {
		t.Fatal(err)
	}
	if iterCount != 0 {
		t.Fatalf("main DB iteration_history rows = %d, want 0 (deleted after successful lazy migration)", iterCount)
	}
}

// TestLazyMigrationFlagBeforeDelete — 顺序契约判别（防数据丢失的核心不变量）：
// 置 migrated=1 必须**先于**删除主库行。判别原理：注入触发器让置标记失败
// （模拟「置标记后崩溃」），此时打开必须失败，但**主库行必须还在** —— 顺序
// 反了的话（先删行后置标记），标记失败 ⇒ 行已删 + 标记未置 ⇒ 下次迁移从
// 空主库重拷 = 会话库数据丢失。旧实现必红（删了行），新实现绿（行还在）。
func TestLazyMigrationFlagBeforeDelete(t *testing.T) {
	mt := newSessionDBTestMT(t)
	tenantID, err := mt.tenantSvc.GetOrCreateTenantID("test", "flag-order")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mt.sessionSvc.AppendMessage(tenantID, llm.NewUserMessage("must survive")); err != nil {
		t.Fatal(err)
	}

	// 触发器：让 UPDATE tenants SET migrated 失败（WHEN 限定只在 migrated 变化时
	// 触发 —— SetTenantDBPath / TouchTenantID 等其他 UPDATE 不受影响）。
	if _, err := mt.DB().Conn().Exec(`CREATE TRIGGER fail_migrated
		BEFORE UPDATE OF migrated ON tenants
		WHEN NEW.migrated != OLD.migrated
		BEGIN SELECT RAISE(ABORT, 'injected migrated failure'); END`); err != nil {
		t.Fatal(err)
	}

	// 打开会话必须失败（SetTenantMigrated 报错 → sessionDB 返回错误）。
	if _, err := mt.GetOrCreateSession("test", "flag-order"); err == nil {
		t.Fatal("expected open failure with the migrated-flag trigger injected")
	}

	// ⛔ 判别断言：主库行必须还在 —— 删除绝不能先于置标记。
	var count int
	if err := mt.DB().Conn().QueryRow(
		"SELECT COUNT(*) FROM session_messages WHERE tenant_id = ?", tenantID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("main rows deleted BEFORE the migrated flag was set — data-loss ordering violation (count=%d, want 1)", count)
	}

	// 撤触发器 → 打开成功 → 幂等重拷（主库行还在，复制无损）→ 置标记 → 删主库行。
	if _, err := mt.DB().Conn().Exec(`DROP TRIGGER fail_migrated`); err != nil {
		t.Fatal(err)
	}
	sess, err := mt.GetOrCreateSession("test", "flag-order")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "must survive" {
		t.Fatalf("session messages after retry = %+v, want 1 row 'must survive'", msgs)
	}
	var after int
	if err := mt.DB().Conn().QueryRow(
		"SELECT COUNT(*) FROM session_messages WHERE tenant_id = ?", tenantID,
	).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("main rows = %d after successful migration, want 0 (lazy delete)", after)
	}
}

// TestLazyMigrationIsIdempotentOnReopen — 迁移幂等：migrated=1 后重开（驱逐 →
// 重开）不再复制（DELETE+INSERT 只在 migrated=0 时跑；migrated=1 直接跳过 ——
// 否则会清空迁移后写入的新数据）。
func TestLazyMigrationIsIdempotentOnReopen(t *testing.T) {
	mt := newSessionDBTestMT(t)
	tenantID, err := mt.tenantSvc.GetOrCreateTenantID("test", "reopen-me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mt.sessionSvc.AppendMessage(tenantID, llm.NewUserMessage("old")); err != nil {
		t.Fatal(err)
	}

	// 第一次打开：迁移 + 写入新数据。
	sess, err := mt.GetOrCreateSession("test", "reopen-me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.NewUserMessage("new-after-migration")); err != nil {
		t.Fatal(err)
	}

	// 模拟真实驱逐路径（cleanupInactiveResources）：缓存里的 TenantSession 移除 +
	// 会话库关闭（两者必须一起 —— 缓存里的 sessionSvc 绑定被关的库会报
	// "database connection is closed"）。
	mt.mu.Lock()
	delete(mt.tenantCache, sessKey("test", "reopen-me"))
	mt.mu.Unlock()
	mt.closeSessionDB(tenantID)

	// 重开：migrated=1 → 跳过迁移 → 迁移后写入的数据必须还在。
	sess2, err := mt.GetOrCreateSession("test", "reopen-me")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := sess2.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("reopen messages = %d, want 2 (old + new-after-migration; re-migration must not wipe post-migration writes)", len(msgs))
	}
}

// TestConcurrentSessionsBeyond32 — 并发无上限（用户硬性要求）：40 个并发会话
// 同时打开并写入，全部成功（池无 LRU 上限，活跃会话的库永不关闭）。
// 回归判据：任何形式的"最多 N 个打开"都会让第 N+1 个会话失败/阻塞。
func TestConcurrentSessionsBeyond32(t *testing.T) {
	mt := newSessionDBTestMT(t)
	const sessions = 40 // > 32：证明没有隐藏的池上限
	var wg sync.WaitGroup
	errs := make(chan error, sessions)
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chatID := fmt.Sprintf("concurrent-%d", i)
			sess, err := mt.GetOrCreateSession("test", chatID)
			if err != nil {
				errs <- fmt.Errorf("session %d open: %w", i, err)
				return
			}
			// 写入 + 读回对账（每个会话独立库文件）。
			if _, err := sess.AppendMessage(llm.NewUserMessage(fmt.Sprintf("hello-%d", i))); err != nil {
				errs <- fmt.Errorf("session %d append: %w", i, err)
				return
			}
			msgs, err := sess.GetMessages()
			if err != nil {
				errs <- fmt.Errorf("session %d read: %w", i, err)
				return
			}
			if len(msgs) != 1 || msgs[0].Content != fmt.Sprintf("hello-%d", i) {
				errs <- fmt.Errorf("session %d readback = %+v", i, msgs)
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestSessionWriteIsolation — 写隔离：两个会话写各自的库文件，互不污染
// （每库独立 writeMu + WAL —— 不同会话的写真正并行，数据物理隔离）。
func TestSessionWriteIsolation(t *testing.T) {
	mt := newSessionDBTestMT(t)
	sessA, err := mt.GetOrCreateSession("test", "iso-a")
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := mt.GetOrCreateSession("test", "iso-b")
	if err != nil {
		t.Fatal(err)
	}
	// 两个会话的库文件必须不同（独立文件 = 物理隔离）。
	infoA, err := mt.tenantSvc.GetTenantDBInfo(sessA.TenantID())
	if err != nil {
		t.Fatal(err)
	}
	infoB, err := mt.tenantSvc.GetTenantDBInfo(sessB.TenantID())
	if err != nil {
		t.Fatal(err)
	}
	if infoA.DBPath == "" || infoB.DBPath == "" || infoA.DBPath == infoB.DBPath {
		t.Fatalf("session DB paths must differ: A=%q B=%q", infoA.DBPath, infoB.DBPath)
	}

	// 并发写（交错）+ 读回对账：A 只看到 A 的消息，B 只看到 B 的。
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			if _, err := sessA.AppendMessage(llm.NewUserMessage(fmt.Sprintf("A-%d", i))); err != nil {
				t.Errorf("A append: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			if _, err := sessB.AppendMessage(llm.NewUserMessage(fmt.Sprintf("B-%d", i))); err != nil {
				t.Errorf("B append: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	msgsA, err := sessA.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgsA) != 10 {
		t.Fatalf("A messages = %d, want 10 (isolation violated)", len(msgsA))
	}
	for i, m := range msgsA {
		if m.Content != fmt.Sprintf("A-%d", i) {
			t.Fatalf("A message %d = %q (cross-session contamination)", i, m.Content)
		}
	}
	msgsB, err := sessB.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgsB) != 10 {
		t.Fatalf("B messages = %d, want 10 (isolation violated)", len(msgsB))
	}
}

// TestPreviewWritePath — preview 写入路径：eligible 消息（user/assistant 非展示）
// append 后主库 tenants.preview 更新；display-only 消息不更新 preview。
func TestPreviewWritePath(t *testing.T) {
	mt := newSessionDBTestMT(t)
	sess, err := mt.GetOrCreateSession("test", "preview-me")
	if err != nil {
		t.Fatal(err)
	}
	tenantID := sess.TenantID()

	readPreview := func() string {
		t.Helper()
		var preview string
		if err := mt.db.Conn().QueryRow(
			"SELECT COALESCE(preview, '') FROM tenants WHERE id = ?", tenantID,
		).Scan(&preview); err != nil {
			t.Fatal(err)
		}
		return preview
	}

	// user 消息 → preview 更新。
	if _, err := sess.AppendMessage(llm.NewUserMessage("first user message")); err != nil {
		t.Fatal(err)
	}
	if got := readPreview(); got != "first user message" {
		t.Fatalf("preview after user append = %q, want %q", got, "first user message")
	}

	// assistant 消息 → preview 更新（最新 eligible 覆盖）。
	if _, err := sess.AppendMessage(llm.NewAssistantMessage("assistant reply")); err != nil {
		t.Fatal(err)
	}
	if got := readPreview(); got != "assistant reply" {
		t.Fatalf("preview after assistant append = %q, want %q", got, "assistant reply")
	}

	// display-only 消息（命令行）→ preview 不更新（保持最新 eligible）。
	if _, err := sess.AppendCommandRow("user", "!pwd output"); err != nil {
		t.Fatal(err)
	}
	if got := readPreview(); got != "assistant reply" {
		t.Fatalf("preview after display-only append = %q, want %q (display-only must not update preview)", got, "assistant reply")
	}

	// DisplayOnly 判别用例（2026-09-30 CR 发现既有 display-only 断言无判别力：
	// AppendCommandRow 走 AppendCommandMessage，根本不经过钩子 —— 删掉
	// updatePreviewForMessage 的 DisplayOnly 守卫它照样绿）。本用例直走钩子
	// 路径（AppendMessage），守卫必须在：role=assistant + DisplayOnly=true 的
	// 追加不得更新 preview。
	directOnly := llm.NewAssistantMessage("secret display-only reply")
	directOnly.DisplayOnly = true
	if _, err := sess.AppendMessage(directOnly); err != nil {
		t.Fatal(err)
	}
	if got := readPreview(); got != "assistant reply" {
		t.Fatalf("preview after AppendMessage(DisplayOnly=true) = %q, want %q — the DisplayOnly guard in updatePreviewForMessage is required (discriminating case)", got, "assistant reply")
	}

	// tool 消息 → preview 不更新。
	if _, err := sess.AppendMessage(llm.NewToolMessage("Shell", "call-1", `{}`, "tool output")); err != nil {
		t.Fatal(err)
	}
	if got := readPreview(); got != "assistant reply" {
		t.Fatalf("preview after tool append = %q, want %q (tool must not update preview)", got, "assistant reply")
	}
}

// TestDestroySessionDeletesFile — 删除回收：DestroySession 后会话库文件（含
// -wal/-shm）从磁盘消失（空间立即回收 —— 单库 DELETE CASCADE + freelist 的
// 老问题根治）。
func TestDestroySessionDeletesFile(t *testing.T) {
	mt := newSessionDBTestMT(t)
	sess, err := mt.GetOrCreateSession("test", "destroy-me")
	if err != nil {
		t.Fatal(err)
	}
	tenantID := sess.TenantID()
	if _, err := sess.AppendMessage(llm.NewUserMessage("to be destroyed")); err != nil {
		t.Fatal(err)
	}
	// db_path 必须在删除 tenants 行**之前**读（注册表是路径唯一权威 —— DestroySession
	// 的调用序：读 db_path → 关库 → 删文件 → 删 tenants 行）。
	info, err := mt.tenantSvc.GetTenantDBInfo(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if info.DBPath == "" {
		t.Fatal("session DB path not assigned before destroy")
	}
	absPath := filepath.Join(mt.sessionDBDir, info.DBPath)

	if err := mt.DestroySession("test", "destroy-me"); err != nil {
		t.Fatal(err)
	}
	// 文件（含 -wal/-shm）全部消失。
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(absPath + suffix); err == nil {
			t.Fatalf("session DB file %q still exists after DestroySession (space not reclaimed)", absPath+suffix)
		}
	}
	// tenants 行也删了（注册表清理）。
	if id, err := mt.tenantSvc.GetTenantIDByChannelChatID("test", "destroy-me"); err != nil || id != 0 {
		t.Fatalf("tenant row survived destroy: id=%d err=%v", id, err)
	}
}

// TestSessionDBRelPathSanitization — 路径派生：chatID 含路径分隔符/中文/超长
// 时派生安全路径（净化 + hash 后缀保证唯一）。
func TestSessionDBRelPathSanitization(t *testing.T) {
	cases := []struct {
		channel, chatID string
	}{
		{"web", "chat_abc123"},
		{"cli", "/home/user/src/project:Agent-main"},
		{"feishu", "oc_中文会话"},
		{"agent", "web:web-1/review:1"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		p := sqlite.SessionDBRelPath(tc.channel, tc.chatID)
		if p == "" {
			t.Fatalf("empty path for %s:%s", tc.channel, tc.chatID)
		}
		// 不含路径穿越（.. 段）。
		for _, seg := range filepath.SplitList(p) {
			if seg == ".." {
				t.Fatalf("path traversal in %q for %s:%s", p, tc.channel, tc.chatID)
			}
		}
		// 唯一（不同 chatID → 不同路径）。
		if seen[p] {
			t.Fatalf("duplicate path %q for %s:%s", p, tc.channel, tc.chatID)
		}
		seen[p] = true
	}
}

// TestSweepIdleSessionDBsLockOrder — 死锁守护（2026-09-29 自查发现的 bug）：
// sweepIdleSessionDBs 绝不能在持 sessionDBMu 时获取 m.mu —— 那会与
// GetOrCreateSession 慢路径（m.mu → sessionDBMu）形成 AB-BA 死锁（sweep 持
// sessionDBMu 等 m.mu.RLock，GetOrCreateSession 持 m.mu 等 sessionDBMu —— 双方
// 永久互等，整个会话系统挂起；sweep 每 5 分钟跑一次，必然触发）。
//
// 判别原理（确定性）：测试持 m.mu.Lock（模拟 GetOrCreateSession 慢路径已过
// m.mu、正要拿 sessionDBMu 的状态），并发跑 sweep。旧实现（先拿 sessionDBMu
// 再查 tenantCache）会持 sessionDBMu 阻塞在 m.mu.RLock 上 —— 此时 sessionDBMu
// 被长期占用，测试的 sessionDBMu 获取超时红。新实现（先 RLock 快照、释放，
// 再 sessionDBMu）在 m.mu 被持时阻塞在 RLock 上（不持 sessionDBMu）——
// sessionDBMu 空闲可获取，测试绿。
func TestSweepIdleSessionDBsLockOrder(t *testing.T) {
	mt := newSessionDBTestMT(t)
	if _, err := mt.GetOrCreateSession("test", "lock-order"); err != nil {
		t.Fatal(err)
	}

	// 模拟 GetOrCreateSession 慢路径：持 m.mu（写锁）。
	mt.mu.Lock()
	defer mt.mu.Unlock()

	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		mt.sweepIdleSessionDBs() // 不得死锁：不得持 sessionDBMu 等 m.mu
	}()
	// 给 sweep 一点时间进入（旧实现会在此持 sessionDBMu 阻塞在 m.mu.RLock）。
	time.Sleep(50 * time.Millisecond)

	// sessionDBMu 必须可获取（sweep 不得在等 m.mu 时持有它）。
	acquired := make(chan struct{})
	go func() {
		mt.sessionDBMu.Lock()
		_ = len(mt.sessionDBs) // 平凡读（非空临界区，SA2001）：探测锁可用性
		mt.sessionDBMu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
		// sessionDBMu 空闲 —— sweep 没有在持它等 m.mu。
	case <-time.After(2 * time.Second):
		t.Fatal("sweepIdleSessionDBs holds sessionDBMu while waiting for m.mu — AB-BA deadlock with GetOrCreateSession (m.mu → sessionDBMu)")
	}

	// 释放 m.mu 后 sweep 必须完成。
	mt.mu.Unlock()
	select {
	case <-sweepDone:
	case <-time.After(2 * time.Second):
		t.Fatal("sweepIdleSessionDBs did not complete after m.mu was released")
	}
	// defer 已 Unlock —— 手动再 Lock 保持 defer 对称（defer 会再 Unlock 一次会
	// panic；改为显式管理）。
	mt.mu.Lock()
}

// TestSweepIdleSessionDBsClosesOrphansKeepsCached — sweep 语义：idle 且无缓存
// TenantSession 的孤儿库（SessionServiceFor 直连路径打开）被关闭；有缓存
// TenantSession 的库（即使 idle）不关（驱逐路径负责，24h 空闲）。
func TestSweepIdleSessionDBsClosesOrphansKeepsCached(t *testing.T) {
	mt := newSessionDBTestMT(t)

	// 孤儿库：SessionServiceFor 直连打开（无 TenantSession）。
	orphanTenant, err := mt.tenantSvc.GetOrCreateTenantID("test", "sweep-orphan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mt.SessionServiceFor(orphanTenant); err != nil {
		t.Fatal(err)
	}
	// 缓存库：GetOrCreateSession 打开（有 TenantSession）。
	cachedSess, err := mt.GetOrCreateSession("test", "sweep-cached")
	if err != nil {
		t.Fatal(err)
	}

	// 两个库的 lastAccess 回拨到 2h 前（超过 sessionDBIdleSweep=1h）。
	mt.sessionDBMu.Lock()
	for _, id := range []int64{orphanTenant, cachedSess.TenantID()} {
		if entry, ok := mt.sessionDBs[id]; ok {
			entry.lastAccess = time.Now().Add(-2 * sessionDBIdleSweep)
		}
	}
	mt.sessionDBMu.Unlock()

	mt.sweepIdleSessionDBs()

	// 孤儿库被关（出池）；缓存库保留（驱逐路径负责）。
	mt.sessionDBMu.Lock()
	_, orphanAlive := mt.sessionDBs[orphanTenant]
	_, cachedAlive := mt.sessionDBs[cachedSess.TenantID()]
	mt.sessionDBMu.Unlock()
	if orphanAlive {
		t.Fatal("idle orphan session DB (no cached TenantSession) was not swept")
	}
	if !cachedAlive {
		t.Fatal("cached session DB was swept by the orphan sweep (must be owned by the eviction path)")
	}
	// 缓存会话仍可写（库还开着）。
	if _, err := cachedSess.AppendMessage(llm.NewUserMessage("still alive")); err != nil {
		t.Fatalf("cached session DB broken after sweep: %v", err)
	}
}

// TestEvictSessionDBGuardSkipsRecreated — 驱逐竞态守护（2026-09-29 自查发现的
// bug）：cleanupInactiveResources 的缓存移除与关库之间，GetOrCreateSession 可能
// 重建该会话（复用池里的库）—— 无守卫地关库 = 新会话永久绑在已关的库上。
// evictSessionDB 的重建守卫（m.mu.RLock 跨重查+关库）必须跳过已重建的会话。
func TestEvictSessionDBGuardSkipsRecreated(t *testing.T) {
	mt := newSessionDBTestMT(t)
	sess, err := mt.GetOrCreateSession("test", "evict-race")
	if err != nil {
		t.Fatal(err)
	}
	tenantID := sess.TenantID()
	key := sessKey("test", "evict-race")

	// 模拟驱逐的锁内段：从缓存移除（cleanupInactiveResources 的 m.mu.Lock 段）。
	mt.mu.Lock()
	delete(mt.tenantCache, key)
	mt.mu.Unlock()

	// 竞态窗口内：GetOrCreateSession 重建该会话（复用池里的库 —— map 命中）。
	recreated, err := mt.GetOrCreateSession("test", "evict-race")
	if err != nil {
		t.Fatal(err)
	}

	// 驱逐的锁外段：evictSessionDB —— 重建守卫必须跳过关库（新会话拥有它）。
	mt.evictSessionDB(key, tenantID)

	// 重建的会话必须仍可写（库没被关）。
	if _, err := recreated.AppendMessage(llm.NewUserMessage("alive after eviction race")); err != nil {
		t.Fatalf("re-created session DB was closed by the eviction race guard failure: %v", err)
	}
	// 对照：无重建时 evictSessionDB 正常关库（驱逐路径的语义不变）。
	mt.mu.Lock()
	delete(mt.tenantCache, key)
	mt.mu.Unlock()
	mt.evictSessionDB(key, tenantID)
	mt.sessionDBMu.Lock()
	_, alive := mt.sessionDBs[tenantID]
	mt.sessionDBMu.Unlock()
	if alive {
		t.Fatal("evictSessionDB did not close the DB for a non-recreated session")
	}
}

// TestNewSessionDoesNotAttachMainDB —— 2026-09-30 生产事故根治的判别测试。
//
// 事故：新建会话因惰性迁移**无条件 ATTACH 主库**，撞上主库坏死的 WAL 状态报
// SQLITE_IOERR_SHORT_READ(522)（服务内存量连接好的、新 ATTACH 坏的），新建会话
// 与子代理 spawn 直接失败。用户的判断（「新建会话又不需要迁移」）就是修复方向。
//
// 判别手法：把主库文件**改名**（新的 ATTACH 必然找不到文件 → 必失败），服务自身
// 的池连接仍持旧 inode（Linux 语义）继续可用 —— 与事故现场完全同构。
//
//	修复后：新会话经主库现有连接探测无残留行 ⇒ 完全不 ATTACH ⇒ 创建成功。
//	修复前（无条件迁移）：ATTACH 找不到主库文件 ⇒ 打开会话报错 ⇒ 本测试必红。
func TestNewSessionDoesNotAttachMainDB(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 不允许重命名**被打开**的文件（SQLite 不启用 FILE_SHARE_DELETE），
		// 而本测试的判别手法正是「改名池持开的主库文件 ⇒ 新 ATTACH 必失败」——
		// 该场景在 Windows 上无法表达。判别力由 Linux/macOS CI + 本地承担；
		// 变异自证（探针恒 true ⇒ 必红）在 Linux 上成立。
		t.Skip("renaming an open SQLite DB file is not possible on Windows; the ATTACH-break scenario is POSIX-only")
	}
	mt := newSessionDBTestMT(t)
	// 先建一个会话，让主库池的连接都进入稳态。
	if _, err := mt.GetOrCreateSession("test", "existing-ok"); err != nil {
		t.Fatal(err)
	}

	// 主库文件改名：新 ATTACH 的路径失效（存量池连接走旧 inode，不受影响）。
	moved := mt.dbPath + ".moved-for-test"
	if err := os.Rename(mt.dbPath, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(moved, mt.dbPath) })

	// 全新会话必须仍可创建 —— 不依赖任何新 ATTACH。
	sess, err := mt.GetOrCreateSession("test", "brand-new-after-move")
	if err != nil {
		t.Fatalf("new session creation must NOT ATTACH the main DB (2026-09-30 production incident): %v", err)
	}
	info, ierr := mt.tenantSvc.GetTenantDBInfo(sess.TenantID())
	if ierr != nil || !info.Migrated {
		t.Fatalf("new session must be marked migrated without a copy: info=%+v err=%v", info, ierr)
	}
	// 且会话库自包含（可正常写入）。
	if _, err := sess.AppendMessage(llm.NewUserMessage("hello")); err != nil {
		t.Fatalf("new session DB must be writable: %v", err)
	}
}
