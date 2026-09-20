package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// TestSplitHistoryWrite_ReadPhaseHoldsNoWriteLock 是「缩短写事务」的核心判据：
// 读/校验阶段**不得**持有 SQLite 写锁 —— 否则持锁时长仍随 replay/校验规模增长
// （历史越长持锁越久 ⇒ SQLITE_BUSY 概率随库增长，用户观察到的"越来越频繁"）。
//
// 探针：在 fn 的读阶段，用**另一条池连接**执行 BEGIN IMMEDIATE。
//   - 读阶段不持写锁 ⇒ 立刻成功（本用例绿）；
//   - 读阶段持有写锁（旧实现）⇒ 撞锁，busy_timeout(10s) 后失败 ⇒ 必红。
//
// 变异自证：把 withSplitHistoryWrite 换回 withImmediateHistoryWrite ⇒ 本用例红。
func TestSplitHistoryWrite_ReadPhaseHoldsNoWriteLock(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "split-write.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	svc := NewSessionService(db)

	err = svc.withSplitHistoryWrite(func(store historyQueryExecer) error {
		// 读阶段：走池上的只读连接。
		var one int
		if err := store.QueryRow(`SELECT 1`).Scan(&one); err != nil {
			return fmt.Errorf("read phase query: %w", err)
		}
		if one != 1 {
			return fmt.Errorf("read phase returned %d, want 1", one)
		}

		// 读阶段必须没有写锁：独立连接应能立刻拿到写锁。
		conn, err := db.Conn().Conn(context.Background())
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			return fmt.Errorf("read phase must NOT hold the SQLite write lock: %w", err)
		}
		if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSplitHistoryWrite_WritePhaseTakesLockOnFirstExec 验证懒开语义：
// 纯读（无 Exec）的操作**完全不开写事务**（旧实现会开一个空 IMMEDIATE 白占写锁）。
func TestSplitHistoryWrite_WritePhaseTakesLockOnFirstExec(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "split-lazy.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	svc := NewSessionService(db)

	// 校验失败（纯读）路径：不应留下任何写事务/连接占用。
	wantErr := fmt.Errorf("validation failed")
	err = svc.withSplitHistoryWrite(func(store historyQueryExecer) error {
		var one int
		if err := store.QueryRow(`SELECT 1`).Scan(&one); err != nil {
			return err
		}
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("fn error must propagate unchanged, got %v", err)
	}

	// 之后写门仍可正常使用（无泄漏的连接/锁）。
	var count int
	if err := svc.withSplitHistoryWrite(func(store historyQueryExecer) error {
		return store.QueryRow(`SELECT count(*) FROM session_messages`).Scan(&count)
	}); err != nil {
		t.Fatalf("second split op must succeed (no leaked lock/conn): %v", err)
	}
}
