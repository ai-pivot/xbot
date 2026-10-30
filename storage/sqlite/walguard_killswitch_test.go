package sqlite

// G-20261027-14 kill switch 判据（运维现场不许改二进制就能关守卫）。
//
//	XBOT_WAL_GUARD=on|off           默认 on
//	XBOT_WAL_GUARD_HEAL=heal|warn   默认 warn（与 storage/sqlite/walguard.go 的
//	                                walGuardHealEnabled = false 一致：只告警，不换池）
//
// 用例钉住：默认（on + warn）、非法值回落默认、off 完全停摆（连检测都不做）、
// warn 只告警不自愈（用 deleted fd 实测，不是看日志说了什么）、heal 必须显式 opt-in。

import (
	"os"
	"path/filepath"
	"testing"
)

func withWALGuardGlobals(t *testing.T, fn func()) {
	t.Helper()
	en, he := walGuardEnabled, walGuardHealEnabled
	defer func() { walGuardEnabled, walGuardHealEnabled = en, he }()
	fn()
}

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestWALGuardKillSwitchDefaults(t *testing.T) {
	withWALGuardGlobals(t, func() {
		walGuardEnabled, walGuardHealEnabled = false, false
		applyWALGuardEnv(lookupFrom(nil)) // 什么都没设
		if !walGuardEnabled {
			t.Fatalf("XBOT_WAL_GUARD unset must default to on")
		}
		if walGuardHealEnabled {
			t.Fatalf("XBOT_WAL_GUARD_HEAL unset must default to warn (alert-only) — heal reopens a live pool and is unsafe with concurrent writers")
		}
	})
}

func TestWALGuardKillSwitchInvalidValueFallsBackToDefault(t *testing.T) {
	withWALGuardGlobals(t, func() {
		walGuardEnabled, walGuardHealEnabled = false, false
		applyWALGuardEnv(lookupFrom(map[string]string{
			envWALGuard:     "definitely-not-a-value",
			envWALGuardHeal: "??",
		}))
		if !walGuardEnabled || walGuardHealEnabled {
			t.Fatalf("illegal values must fall back to defaults (on/warn), got enabled=%v heal=%v",
				walGuardEnabled, walGuardHealEnabled)
		}
	})
}

// off：守卫整体停摆 —— 外部 unlink 之后既不检测也不自愈，deleted fd 留在那儿（RED 语义）。
func TestWALGuardKillSwitchOffDisablesDetectionAndHeal(t *testing.T) {
	withWALGuardGlobals(t, func() {
		applyWALGuardEnv(lookupFrom(map[string]string{envWALGuard: "off"}))
		if walGuardEnabled {
			t.Fatalf("XBOT_WAL_GUARD=off must disable the guard")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "xbot.db")
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Conn().Exec("CREATE TABLE probe (id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatalf("create: %v", err)
		}
		for _, s := range []string{"-wal", "-shm"} {
			if err := os.Remove(path + s); err != nil {
				t.Fatalf("unlink %s: %v", s, err)
			}
		}
		if healed := walGuardTick(); healed != 0 {
			t.Fatalf("guard disabled by kill switch but healed=%d", healed)
		}
		if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
			t.Fatalf("-wal must stay missing while the guard is off")
		}
		if got := deletedSidecarFDs(t, path); len(got) == 0 {
			t.Fatalf("expected orphaned (deleted) sidecar fds while the guard is off")
		}
	})
}

// warn：仍然检测（不装作没看见），但**不动作** —— 判据是 deleted fd 还在。
func TestWALGuardKillSwitchWarnDoesNotHeal(t *testing.T) {
	withWALGuardGlobals(t, func() {
		applyWALGuardEnv(lookupFrom(map[string]string{
			envWALGuard:     "on",
			envWALGuardHeal: "warn",
		}))
		if !walGuardEnabled || walGuardHealEnabled {
			t.Fatalf("want enabled=true heal=false, got %v/%v", walGuardEnabled, walGuardHealEnabled)
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "xbot.db")
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Conn().Exec("CREATE TABLE probe (id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatalf("create: %v", err)
		}
		for _, s := range []string{"-wal", "-shm"} {
			if err := os.Remove(path + s); err != nil {
				t.Fatalf("unlink %s: %v", s, err)
			}
		}
		if healed := walGuardTick(); healed != 0 {
			t.Fatalf("warn mode must not heal, got healed=%d", healed)
		}
		if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
			t.Fatalf("-wal must stay missing in warn mode (nothing was healed)")
		}
		if got := deletedSidecarFDs(t, path); len(got) == 0 {
			t.Fatalf("warn mode must leave the orphaned (deleted) sidecar fds in place")
		}
	})
}

// heal 必须显式 opt-in（默认 warn）——这条钉住「默认不冒险」。
func TestWALGuardKillSwitchHealRequiresOptIn(t *testing.T) {
	withWALGuardGlobals(t, func() {
		applyWALGuardEnv(lookupFrom(map[string]string{envWALGuardHeal: "heal"}))
		if !walGuardHealEnabled {
			t.Fatalf("XBOT_WAL_GUARD_HEAL=heal must enable the heal path")
		}
		applyWALGuardEnv(lookupFrom(map[string]string{envWALGuardHeal: "warn"}))
		if walGuardHealEnabled {
			t.Fatalf("XBOT_WAL_GUARD_HEAL=warn must disable the heal path")
		}
	})
}
