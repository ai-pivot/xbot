package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"xbot/llm"
)

// countSessionDBFiles 数一下会话库目录下的 .db 文件（销毁后必须为 0）。
func countSessionDBFiles(t *testing.T, mainDBPath string) int {
	t.Helper()
	root := filepath.Dir(mainDBPath)
	n := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".db" && filepath.Base(path) != filepath.Base(mainDBPath) {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk session dir: %v", err)
	}
	return n
}

// TestDestroyedTenantSessionDBIsNotReopened 复现/钉死 2026-10-22 05:26 的
// use-after-close 签名：
//
//	DestroySession  checkpoint + close + unlink 会话库
//	并发路径又 GetOrCreateSession / SessionServiceFor(同 tenantID) → open 同一个文件
//	→ "SQLite database closed" 之后紧跟 "WAL state ... where=open"
//
// 契约：租户一旦销毁，任何仍持有旧 tenantID 的路径都必须拿到明确错误
// （errSessionDestroyed），**绝不能**重新把库文件 open 出来。
func TestDestroyedTenantSessionDBIsNotReopened(t *testing.T) {
	mainDB := filepath.Join(t.TempDir(), "main.db")
	mt, err := NewMultiTenant(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mt.Close() })

	sess, err := mt.GetOrCreateSession("agent", "web:chat/role:inst")
	if err != nil {
		t.Fatal(err)
	}
	tenantID := sess.TenantID()
	if err := sess.AddMessage(llm.NewUserMessage("hello")); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if got := countSessionDBFiles(t, mainDB); got != 1 {
		t.Fatalf("session db files before destroy = %d, want 1", got)
	}

	if err := mt.DestroySession("agent", "web:chat/role:inst"); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}
	if got := countSessionDBFiles(t, mainDB); got != 0 {
		t.Fatalf("session db files after destroy = %d, want 0 (destroy must reclaim the file)", got)
	}

	// ① SessionServiceFor（storage.SessionStore 直连路径，如 rpc contextUsage / usage 查询）
	if svc, err := mt.SessionServiceFor(tenantID); err == nil {
		t.Fatalf("BUG: SessionServiceFor re-opened the destroyed tenant's DB (svc=%v) — this is the use-after-close signature (close → where=open)", svc)
	} else if !errors.Is(err, errSessionDestroyed) {
		t.Fatalf("SessionServiceFor error = %v, want errSessionDestroyed", err)
	}

	// ② SessionServiceForSQLite（SQLite 专属路径：rewind / usage / replay）
	if svc, err := mt.SessionServiceForSQLite(tenantID); err == nil {
		t.Fatalf("BUG: SessionServiceForSQLite re-opened the destroyed tenant's DB (svc=%v)", svc)
	} else if !errors.Is(err, errSessionDestroyed) {
		t.Fatalf("SessionServiceForSQLite error = %v, want errSessionDestroyed", err)
	}

	// ③ 被拒绝的 reopen 绝不能留下一个新库文件（旧实现会新建空库！）
	if got := countSessionDBFiles(t, mainDB); got != 0 {
		t.Fatalf("session db files after refused reopen = %d, want 0 — the destroyed DB file was resurrected", got)
	}
}

// TestNewSessionAfterDestroyGetsFreshTenant 守护"墓碑不能误伤合法重建"：
// 销毁同一个 chat key 之后重新 GetOrCreateSession 必须成功（新租户、新库）。
func TestNewSessionAfterDestroyGetsFreshTenant(t *testing.T) {
	mainDB := filepath.Join(t.TempDir(), "main.db")
	mt, err := NewMultiTenant(mainDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mt.Close() })

	first, err := mt.GetOrCreateSession("agent", "web:chat/role:inst")
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.TenantID()
	if err := mt.DestroySession("agent", "web:chat/role:inst"); err != nil {
		t.Fatal(err)
	}

	second, err := mt.GetOrCreateSession("agent", "web:chat/role:inst")
	if err != nil {
		t.Fatalf("re-create after destroy must succeed (fresh tenant): %v", err)
	}
	if second.TenantID() == firstID {
		t.Fatalf("re-created session reused the destroyed tenant id %d", firstID)
	}
	if err := second.AddMessage(llm.NewUserMessage("after recreate")); err != nil {
		t.Fatalf("write into re-created session: %v", err)
	}
}
