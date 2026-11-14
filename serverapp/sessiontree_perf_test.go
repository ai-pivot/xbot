package serverapp

// sessiontree_perf_test.go — /api/session-tree 端到端性能量化（2026-11-09）。
//
// 目的：把「1.6-3.5s」拆成可归因的分段耗时，而不是靠猜。
// 完全隔离：用 t.TempDir() 里的临时 SQLite 库 + 合成数据，绝不触碰生产库。
//
// 规模按生产快照设置：tenants≈1382（web≈900 / cli≈400 / agent≈82 行，
// 另有 471 条 interactive_agents 会话在内存注册表里）、user_chats≈424。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"xbot/channel/web"
	"xbot/storage/sqlite"
)

func seedPerfDB(t *testing.T, webN, cliN, agentN, userChatsN int) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "perf.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn := db.Conn()

	now := time.Now().UTC()
	tx, err := conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	insTenant, err := tx.Prepare(`INSERT INTO tenants
		(channel, chat_id, created_at, last_active_at, preview)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare tenants: %v", err)
	}
	seed := func(channel string, n int) {
		for i := 0; i < n; i++ {
			ts := now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
			if _, err := insTenant.Exec(channel, fmt.Sprintf("chat_%s_%03d", channel, i),
				ts, ts,
				fmt.Sprintf("preview text for %s %d with a bit of content", channel, i)); err != nil {
				t.Fatalf("insert tenant: %v", err)
			}
		}
	}
	seed("web", webN)
	seed("cli", cliN)
	seed("agent", agentN)

	insChat, err := tx.Prepare(`INSERT INTO user_chats
		(channel, chat_id, sender_id, label, sort_order) VALUES (?, ?, 'adm', ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare user_chats: %v", err)
	}
	for i := 0; i < userChatsN; i++ {
		ch := "web"
		if i%3 == 0 {
			ch = "cli"
		}
		if _, err := insChat.Exec(ch, fmt.Sprintf("chat_%s_%03d", ch, i%max(1, webN)),
			fmt.Sprintf("label %d", i), i%7); err != nil {
			t.Fatalf("insert user_chat: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return conn
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestSessionTreePerfBreakdown 打印各段耗时（-v 可见），用于定位 1-3s 的来源。
func TestSessionTreePerfBreakdown(t *testing.T) {
	conn := seedPerfDB(t, 900, 400, 82, 424)

	timings := map[string]time.Duration{}
	timeIt := func(name string, fn func()) {
		start := time.Now()
		fn()
		timings[name] = time.Since(start)
	}

	var channels []string
	timeIt("listDistinctChannels", func() { channels, _ = listDistinctChannels(conn) })

	var tenantRows []web.UserChatWithPreview
	timeIt("listTenantsByChannels", func() {
		var err error
		tenantRows, err = listTenantsByChannels(conn, []string{"web", "feishu"})
		if err != nil {
			t.Fatalf("listTenantsByChannels: %v", err)
		}
	})

	var agentRows []web.UserChatWithPreview
	timeIt("listTenantsByChannel(agent)", func() {
		var err error
		agentRows, err = listTenantsByChannel(conn, "agent", "")
		if err != nil {
			t.Fatalf("listTenantsByChannel(agent): %v", err)
		}
	})

	var cliRows []web.UserChatWithPreview
	timeIt("listTenantsByChannel(cli)", func() {
		var err error
		cliRows, err = listTenantsByChannel(conn, "cli", "")
		if err != nil {
			t.Fatalf("listTenantsByChannel(cli): %v", err)
		}
	})

	var total time.Duration
	for _, d := range timings {
		total += d
	}
	t.Logf("channels=%d tenantRows=%d agentRows=%d cliRows=%d", len(channels), len(tenantRows), len(agentRows), len(cliRows))
	for name, d := range timings {
		t.Logf("  %-28s %8.2f ms", name, float64(d.Microseconds())/1000)
	}
	t.Logf("  %-28s %8.2f ms", "TOTAL (queries only)", float64(total.Microseconds())/1000)

	// 阈值：仅查询合计超过 300ms 即说明 SQL 层是瓶颈（后续优化据此定向）
	if total > 300*time.Millisecond {
		t.Logf("NOTE: query layer alone is %.0fms — SQL/索引/CTE is the primary bottleneck",
			float64(total.Microseconds())/1000)
	}
}

// TestBuildSessionTreeAndMarshalPerf 量化内存侧：1382 mains + 471 subagents 的
// 构树 + JSON 序列化耗时（SQL 层已证明只有 13.8ms，这里验证其余部分）。
func TestBuildSessionTreeAndMarshalPerf(t *testing.T) {
	mains := make([]web.UserChatWithPreview, 0, 1382)
	now := time.Now()
	for i := 0; i < 1382; i++ {
		mains = append(mains, web.UserChatWithPreview{
			ChatID:     fmt.Sprintf("chat_web_%04d", i),
			Channel:    "web",
			Label:      fmt.Sprintf("session %d", i),
			LastActive: now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
			CreatedAt:  now.Add(-time.Duration(i) * time.Hour).Format(time.RFC3339),
			Preview:    fmt.Sprintf("preview text for session %d", i),
		})
	}
	subagents := make([]web.UserChatWithPreview, 0, 471)
	for i := 0; i < 471; i++ {
		subagents = append(subagents, web.UserChatWithPreview{
			ChatID:     fmt.Sprintf("web:chat_web_%04d/explore:inst%d", i%1382, i),
			Channel:    "agent",
			Type:       "agent",
			FullKey:    fmt.Sprintf("web:chat_web_%04d/explore:inst%d", i%1382, i),
			Label:      fmt.Sprintf("subagent %d", i),
			LastActive: now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
	}

	start := time.Now()
	tree := buildSessionTree(mains, subagents)
	buildDur := time.Since(start)

	start = time.Now()
	payload, err := json.Marshal(tree)
	marshalDur := time.Since(start)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	t.Logf("buildSessionTree: %.2f ms (nodes=%d orphans=%d)", float64(buildDur.Microseconds())/1000, len(tree.Sessions), len(tree.OrphanSubAgents))
	t.Logf("json.Marshal:     %.2f ms (%d bytes)", float64(marshalDur.Microseconds())/1000, len(payload))
}

// TestReadLatencyUnderWriteLoad 验证「连接池饥饿」假设：生产 471 个子代理并发写，
// 而主库 MaxOpenConns 曾是 4 ⇒ session-tree 的读查询排队等连接（端到端 1.6-3.5s），
// 而单线程只有 13.8ms。这里在并发写压力下对比 4 与 64 两种上限。
func TestReadLatencyUnderWriteLoad(t *testing.T) {
	conn := seedPerfDB(t, 900, 400, 82, 424)
	const writers = 8
	measure := func(maxOpen int) time.Duration {
		conn.SetMaxOpenConns(maxOpen)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					_, _ = conn.Exec(`INSERT OR REPLACE INTO tenants
						(channel, chat_id, created_at, last_active_at, preview)
						VALUES ('web', ?, datetime('now'), datetime('now'), 'load')`,
						fmt.Sprintf("load_%d", n))
				}
			}(i)
		}
		time.Sleep(50 * time.Millisecond) // 让写者进入稳态
		start := time.Now()
		for i := 0; i < 5; i++ {
			if _, err := listTenantsByChannels(conn, []string{"web", "feishu"}); err != nil {
				t.Fatalf("query: %v", err)
			}
		}
		avg := time.Since(start) / 5
		close(stop)
		wg.Wait()
		return avg
	}

	slow := measure(4)
	fast := measure(64)
	t.Logf("并发 %d 写者时：MaxOpenConns=4 → 读平均 %.1fms；MaxOpenConns=64 → %.1fms（提升 %.1fx）",
		writers, float64(slow.Microseconds())/1000, float64(fast.Microseconds())/1000,
		float64(slow)/float64(max64(fast, time.Microsecond)))
}

func max64(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
