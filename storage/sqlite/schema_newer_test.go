package sqlite

// schema_newer_test.go — P0-1 验收：库比二进制新 ⇒ ERROR 级别 + 进入健康端点。
//
// 为什么必须用「真实 logrus hook + 真实 SQLite 文件」而不用打桩：
// 2026-11-02 的事故不是缺检测（detection 一直在），而是**级别太低**——
// 一行 WARN 在日志里躺了 13 个小时没人看。所以验收必须断言「这条日志确实
// 以 error 级别发出」，而不是断言「某段代码被调用过」。把 db.go 里的
// Error(...) 降回 Warn(...)，本用例立刻变红（变异自证见证据目录）。
//
// 同一条用例还要证明：不匹配这件事**进了健康端点**（schema_newer=true /
// status=degraded），因为 401 风暴时外部探针只能读这个端点。

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"

	"xbot/internal/runtimeinfo"
)

// levelRecorder 收集所有日志条目（含级别），用于断言严重级别。
type levelRecorder struct {
	mu      sync.Mutex
	entries []*logrus.Entry
}

func (r *levelRecorder) Levels() []logrus.Level { return logrus.AllLevels }

func (r *levelRecorder) Fire(e *logrus.Entry) error {
	// Clone the entry: logrus reuses the Data map across hooks.
	clone := *e
	r.mu.Lock()
	r.entries = append(r.entries, &clone)
	r.mu.Unlock()
	return nil
}

func (r *levelRecorder) find(level logrus.Level, substr string) *logrus.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.Level == level && strings.Contains(e.Message, substr) {
			return e
		}
	}
	return nil
}

// installLevelRecorder swaps the process logger's hooks for a recorder and
// restores the previous hooks when the test ends.
func installLevelRecorder(t *testing.T) *levelRecorder {
	t.Helper()
	logger := logrus.StandardLogger()
	prevHooks := logger.ReplaceHooks(make(logrus.LevelHooks))
	prevLevel := logger.GetLevel()
	logger.SetLevel(logrus.TraceLevel)

	rec := &levelRecorder{}
	logger.AddHook(rec)
	t.Cleanup(func() {
		logger.ReplaceHooks(prevHooks)
		logger.SetLevel(prevLevel)
	})
	return rec
}

func TestSchemaNewerLogsAtErrorLevelAndReachesHealth(t *testing.T) {
	rec := installLevelRecorder(t)

	prevBinary, prevDB := runtimeinfo.Default().SchemaVersions()
	t.Cleanup(func() { runtimeinfo.Default().SetSchemaVersions(prevBinary, prevDB) })
	runtimeinfo.Default().SetSchemaVersions(0, 0)

	path := t.TempDir() + "/xbot.db"

	// 1. A normal database at the current schema version.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fresh db: %v", err)
	}
	// 2. Simulate "the database was migrated by a NEWER binary" — exactly the
	//    11-02 state (binary 73, database 80; here relational: n / n+1).
	newer := schemaVersion + 1
	if _, err := db.Conn().Exec("UPDATE schema_version SET version = ?", newer); err != nil {
		t.Fatalf("bump schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 3. Re-opening with this (older) binary must NOT fail — startup path rule.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("startup must never fail on a newer database, got: %v", err)
	}
	t.Cleanup(func() { db2.Close() })

	// 4. The mismatch is logged at ERROR, with both versions in the fields.
	entry := rec.find(logrus.ErrorLevel, "NEWER than this binary supports")
	if entry == nil {
		t.Fatalf("schema mismatch was not logged at ERROR level (a WARN here is the 13-hour blind spot this ticket exists for)")
	}
	if got := entry.Data["db_schema"]; got != newer {
		t.Fatalf("log field db_schema = %v, want %v", got, newer)
	}
	if got := entry.Data["binary_schema"]; got != schemaVersion {
		t.Fatalf("log field binary_schema = %v, want %v", got, schemaVersion)
	}

	// 5. ... and it is reported by the self-check state behind GET /api/health.
	binary, dbVersion := runtimeinfo.Default().SchemaVersions()
	if binary != schemaVersion || dbVersion != newer {
		t.Fatalf("schema pair = %d/%d, want %d/%d", binary, dbVersion, schemaVersion, newer)
	}
	h := runtimeinfo.Default().Health()
	if !h.SchemaNewer {
		t.Fatalf("health schema_newer = false for %d/%d", h.SchemaBinary, h.SchemaDB)
	}
	if h.Status != runtimeinfo.StatusDegraded {
		t.Fatalf("health status = %q, want %q", h.Status, runtimeinfo.StatusDegraded)
	}
	if h.SchemaDB != newer || h.SchemaBinary != schemaVersion {
		t.Fatalf("health schema pair = %d/%d, want %d/%d", h.SchemaBinary, h.SchemaDB, schemaVersion, newer)
	}
}

// TestAlignedSchemaDoesNotLogError is the negative control: an aligned database
// must produce no schema ERROR (a test that always passes would be worthless).
func TestAlignedSchemaDoesNotLogError(t *testing.T) {
	rec := installLevelRecorder(t)
	// 用例自洽（2026-11-03 P0-1 复验）：runtimeinfo.Default() 是进程级单例，
	// 必须由本用例自己建立起始态并在结束时还原 —— 否则它会读到上一个用例留下的
	// （binary, db）版本对，`-run` 过滤或 -shuffle 时必红。
	prevBinary, prevDB := runtimeinfo.Default().SchemaVersions()
	t.Cleanup(func() { runtimeinfo.Default().SetSchemaVersions(prevBinary, prevDB) })
	runtimeinfo.Default().SetSchemaVersions(0, 0)

	path := t.TempDir() + "/xbot.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// 新建库必须把版本对登记成 (schemaVersion, schemaVersion)：这条断言同时
	// 证明「对齐不是靠环境蒙对的」，而是 Open 真的写了。
	binary, dbVersion := runtimeinfo.Default().SchemaVersions()
	if binary != schemaVersion || dbVersion != schemaVersion {
		t.Fatalf("fresh database must register (%d,%d), got (%d,%d)", schemaVersion, schemaVersion, binary, dbVersion)
	}

	if entry := rec.find(logrus.ErrorLevel, "NEWER than this binary supports"); entry != nil {
		t.Fatalf("aligned database must not log a schema ERROR: %s", entry.Message)
	}
	if h := runtimeinfo.Default().Health(); h.SchemaNewer || h.Status != runtimeinfo.StatusOK {
		t.Fatalf("aligned database health = %+v, want ok/%d/%d", h, schemaVersion, schemaVersion)
	}
}

// TestFreshDatabaseRegistersSchemaVersionOnSingleOpen —— 「只 Open 一次」路径的硬证据
// （工部/首辅点名条件）：
//
// 真机上 fresh DB 的 health 报 80/80，**不是因为建库分支登记了**，而是因为 serve 里
// 还有第二次 `sqlite.Open(dbPath)`（serverapp/server.go 的 tokenDB），它走的是「库
// 已存在」分支，在那儿把版本对登记了。于是「新建库不登记」这个缺口只会在**单次
// Open** 的调用方暴露 —— CLI 工具、单测、以及任何只开一次库的进程。
//
// 所以本用例刻意：起始态清零 ⇒ `Open()` **恰好一次** ⇒ 直接读 Health()。中间没有
// 第二次 Open 帮忙兜底，登记行一失效就必红（变异自证见证据 14-*.log）。
func TestFreshDatabaseRegistersSchemaVersionOnSingleOpen(t *testing.T) {
	prevBinary, prevDB := runtimeinfo.Default().SchemaVersions()
	t.Cleanup(func() { runtimeinfo.Default().SetSchemaVersions(prevBinary, prevDB) })
	runtimeinfo.Default().SetSchemaVersions(0, 0)

	path := filepath.Join(t.TempDir(), "fresh.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fresh db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	h := runtimeinfo.Default().Health()
	if h.SchemaBinary != schemaVersion || h.SchemaDB != schemaVersion {
		t.Fatalf("single Open of a fresh database must register (%d,%d), got (%d,%d)",
			schemaVersion, schemaVersion, h.SchemaBinary, h.SchemaDB)
	}
	if h.SchemaNewer {
		t.Fatalf("schema_newer = true for a fresh database (%d/%d)", h.SchemaBinary, h.SchemaDB)
	}
	if h.Status != runtimeinfo.StatusOK {
		t.Fatalf("status = %q, want %q for a fresh database", h.Status, runtimeinfo.StatusOK)
	}
}
