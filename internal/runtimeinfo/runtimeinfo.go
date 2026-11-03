// Package runtimeinfo records what this process knows about itself: which
// binary is running, whether that binary changed on disk underneath it, and
// whether the database schema it opened is newer than this binary understands.
//
// Why this exists (2026-11-03, P0-1): an instance was deployed with an old
// binary while its own database had already been migrated to schema v80 by a
// newer binary (11-02 13:11:18). From 13:30:29 that process logged exactly one
// WARN line ("Stored schema version is NEWER than this binary supports") and
// then went on serving; because `validateSession` is a pure in-memory map
// (channel/web/web_auth.go), every browser presented a token the process had
// never issued ⇒ a 401 storm with **no business logs at all**. Nothing about
// that failure was reachable without an operator reading the right log file on
// the right host.
//
// So the two facts an operator (or an external probe) needs are now first
// class:
//
//  1. the boot-time fingerprint of the running binary (sha256 + mtime) and the
//     schemaVersion compiled into it;
//  2. periodic comparison of that fingerprint against the file on disk (a
//     binary replaced under a running process is a deployment that forgot to
//     restart *this* instance), plus the schema pair (binary vs database).
//
// Both are surfaced by GET /api/health (channel/web/web_health.go), which stays
// readable while everything else answers 401.
//
// Hard rule inherited from the repo: **the startup path never fails**. Every
// error in here is logged and swallowed; nothing blocks or aborts a boot.
package runtimeinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

const (
	// DefaultDriftInterval is how often the on-disk binary is re-read when no
	// interval is configured.
	DefaultDriftInterval = 60 * time.Second

	// DriftIntervalEnv overrides DefaultDriftInterval. Accepts anything
	// time.ParseDuration understands ("30s", "5m"); invalid or non-positive
	// values fall back to the default.
	DriftIntervalEnv = "XBOT_BINARY_DRIFT_INTERVAL"

	// shaExposedLen is how many hex characters of the digest the health
	// endpoint exposes. Enough to tell two builds apart, short enough that a
	// probe payload stays readable.
	shaExposedLen = 16
)

// Health status values.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
)

// Snapshot is the boot-time fingerprint of the running executable.
type Snapshot struct {
	// Path is kept for re-reading the file at check time. It is deliberately
	// NOT part of the health payload (no filesystem paths leave the process).
	Path   string
	SHA256 string
	MTime  time.Time
	Size   int64
}

// Observation is one on-disk reading of the executable path.
//
// Exists=false means the path is gone (a binary removed/replaced under us),
// which counts as drift, not as an error.
type Observation struct {
	Exists bool
	SHA256 string
	MTime  time.Time
	Size   int64
}

// ObserveFile fingerprints path: mtime + size from stat, sha256 streamed from
// the file (109 MB ≈ 0.3 s, which is why the callers below are allowed to do
// this on a 60 s ticker but not per HTTP request).
func ObserveFile(path string) (Observation, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Observation{}, nil
		}
		return Observation{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Observation{}, nil
		}
		return Observation{}, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Observation{}, err
	}
	return Observation{
		Exists: true,
		SHA256: hex.EncodeToString(h.Sum(nil)),
		MTime:  fi.ModTime(),
		Size:   fi.Size(),
	}, nil
}

// Drifted reports whether a current on-disk observation no longer matches the
// boot-time snapshot. It is the single pure decision behind `binary_changed`,
// and covers exactly three states:
//
//	identical fingerprint   → false
//	mtime (or size) differs → true
//	sha256 differs          → true
//
// A missing file is also drift: the build we started from is gone, so we can no
// longer claim the running code is what is on disk. Comparison for the rest is
// deliberately strict — "same content, new mtime" still means *somebody
// replaced the file*, which is exactly the deployment event an operator needs
// to know about.
func Drifted(snap Snapshot, obs Observation) bool {
	if !obs.Exists {
		return true
	}
	if !obs.MTime.Equal(snap.MTime) || obs.Size != snap.Size {
		return true
	}
	return !strings.EqualFold(obs.SHA256, snap.SHA256)
}

// Health is the JSON payload of GET /api/health. Field names are part of the
// contract for external probes (and for scripts/restart-all-instances.sh) — do
// not rename them, and never add filesystem paths.
type Health struct {
	Status        string `json:"status"`         // "ok" | "degraded"
	BinarySHA256  string `json:"binary_sha256"`  // first 16 hex chars of the boot binary
	BinaryMTime   string `json:"binary_mtime"`   // RFC3339 UTC, boot binary
	BinaryChanged bool   `json:"binary_changed"` // on-disk binary != boot binary
	SchemaBinary  int    `json:"schema_binary"`  // schemaVersion compiled into this binary
	SchemaDB      int    `json:"schema_db"`      // schema version the opened database reports
	SchemaNewer   bool   `json:"schema_newer"`   // database newer than this binary
	CheckedAt     string `json:"checked_at"`     // RFC3339 UTC of the last drift check
}

// State is the process-wide self-check state. It is safe for concurrent use;
// the zero value is NOT usable — build it with New().
type State struct {
	mu sync.RWMutex

	snap    Snapshot
	hasSnap bool
	changed bool
	// lastSeenSHA is the most recent on-disk digest we logged about, so a
	// second deployment under the same stale process still produces a second
	// ERROR line instead of silence.
	lastSeenSHA string

	schemaBinary int
	schemaDB     int

	lastCheck time.Time

	// observe is the fingerprint source; a field so tests can inject a fake
	// without touching the real executable.
	observe func(string) (Observation, error)

	stopCh   chan struct{}
	started  bool
	stopOnce sync.Once
}

// New returns an empty State wired to the real filesystem.
func New() *State {
	return &State{observe: ObserveFile}
}

var defaultState = New()

// Default returns the process-wide state used by the startup path, the SQLite
// opener and the health endpoint.
func Default() *State { return defaultState }

// Capture fingerprints the running executable (os.Executable) and stores it as
// the boot snapshot. It returns an error for the caller to log; startup must
// continue either way.
func (s *State) Capture() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Resolve symlinks so the periodic re-read watches the same file the
	// service manager points at (install layouts link the binary).
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return s.CapturePath(exe)
}

// CapturePath is Capture for an explicit path (tests, and callers that know
// better than os.Executable where the binary lives).
func (s *State) CapturePath(path string) error {
	obs, err := s.observe(path)
	if err != nil {
		return err
	}
	if !obs.Exists {
		return os.ErrNotExist
	}
	s.mu.Lock()
	s.snap = Snapshot{Path: path, SHA256: obs.SHA256, MTime: obs.MTime, Size: obs.Size}
	s.hasSnap = true
	s.changed = false
	s.lastSeenSHA = obs.SHA256
	s.lastCheck = time.Now().UTC()
	s.mu.Unlock()
	return nil
}

// Snapshot returns the boot snapshot, and whether one was captured at all.
func (s *State) Snapshot() (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.hasSnap
}

// SetBinarySchemaVersion records the schemaVersion compiled into this binary.
func (s *State) SetBinarySchemaVersion(v int) {
	s.mu.Lock()
	s.schemaBinary = v
	s.mu.Unlock()
}

// SetDBSchemaVersion records the schema version the opened database reports.
func (s *State) SetDBSchemaVersion(v int) {
	s.mu.Lock()
	s.schemaDB = v
	s.mu.Unlock()
}

// SetSchemaVersions records both halves of the pair in one call.
func (s *State) SetSchemaVersions(binary, db int) {
	s.mu.Lock()
	s.schemaBinary, s.schemaDB = binary, db
	s.mu.Unlock()
}

// SchemaVersions returns (binary, db).
func (s *State) SchemaVersions() (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.schemaBinary, s.schemaDB
}

// SchemaNewer reports whether the opened database is newer than this binary.
// Unknown on either side (0) is never "newer" — a missing report must not turn
// a healthy process into a degraded one.
func (s *State) SchemaNewer() bool {
	b, d := s.SchemaVersions()
	return d > 0 && b > 0 && d > b
}

// BinaryChanged reports whether drift has been observed since boot. It is
// sticky: one replaced binary is one deployment mistake, and a later matching
// check must not make the operator's evidence disappear.
func (s *State) BinaryChanged() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.changed
}

// CheckOnce re-reads the on-disk binary and updates the drift flag. It logs an
// ERROR the first time drift is seen, and again whenever a *different* stale
// binary appears — that is precisely the "deployed without restarting this
// instance" event behind the 401 storm. Returns whether drift is present.
func (s *State) CheckOnce() bool {
	s.mu.RLock()
	snap, hasSnap, observe := s.snap, s.hasSnap, s.observe
	s.mu.RUnlock()
	if !hasSnap || observe == nil {
		return false
	}

	obs, err := observe(snap.Path)
	now := time.Now().UTC()
	if err != nil {
		// Transient read failure (permissions, IO): keep the previous verdict,
		// but record that a check happened so checked_at stays meaningful.
		s.mu.Lock()
		s.lastCheck = now
		s.mu.Unlock()
		log.WithError(err).WithField("path", snap.Path).
			Warn("Binary drift check failed; keeping the previous verdict")
		return s.BinaryChanged()
	}

	drifted := Drifted(snap, obs)

	s.mu.Lock()
	s.lastCheck = now
	newlyDrifted := drifted && !s.changed
	s.changed = s.changed || drifted
	differentStaleBuild := drifted && obs.Exists && !strings.EqualFold(obs.SHA256, s.lastSeenSHA)
	if obs.Exists {
		s.lastSeenSHA = obs.SHA256
	}
	s.mu.Unlock()

	if newlyDrifted || differentStaleBuild {
		log.WithFields(log.Fields{
			"path":        snap.Path,
			"boot_sha256": shortSHA(snap.SHA256),
			"disk_sha256": shortSHA(obs.SHA256),
			"boot_mtime":  snap.MTime.UTC().Format(time.RFC3339),
			"disk_mtime":  obs.MTime.UTC().Format(time.RFC3339),
			"boot_size":   snap.Size,
			"disk_size":   obs.Size,
			"disk_exists": obs.Exists,
		}).Error("Running binary no longer matches the file on disk — this instance was NOT restarted after the binary was replaced; restart it (supervisorctl restart <program>) and re-check GET /api/health")
	}
	return drifted
}

// StartWatcher begins periodic drift detection. Idempotent; interval <= 0 uses
// DefaultDriftInterval.
func (s *State) StartWatcher(interval time.Duration) {
	if interval <= 0 {
		interval = DefaultDriftInterval
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	stop := make(chan struct{})
	s.stopCh = stop
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.CheckOnce()
			}
		}
	}()
}

// StopWatcher stops the periodic check. Safe without a running watcher and safe
// to call more than once.
func (s *State) StopWatcher() {
	s.mu.RLock()
	stop := s.stopCh
	s.mu.RUnlock()
	if stop == nil {
		return
	}
	s.stopOnce.Do(func() { close(stop) })
}

// Health renders the current self-check state for GET /api/health.
func (s *State) Health() Health {
	s.mu.RLock()
	snap, hasSnap, changed := s.snap, s.hasSnap, s.changed
	schemaBinary, schemaDB := s.schemaBinary, s.schemaDB
	lastCheck := s.lastCheck
	s.mu.RUnlock()

	h := Health{
		Status:        StatusOK,
		BinaryChanged: changed,
		SchemaBinary:  schemaBinary,
		SchemaDB:      schemaDB,
		SchemaNewer:   schemaDB > 0 && schemaBinary > 0 && schemaDB > schemaBinary,
	}
	if hasSnap {
		h.BinarySHA256 = shortSHA(snap.SHA256)
		h.BinaryMTime = snap.MTime.UTC().Format(time.RFC3339)
	}
	if changed || h.SchemaNewer {
		h.Status = StatusDegraded
	}
	if lastCheck.IsZero() {
		lastCheck = time.Now().UTC()
	}
	h.CheckedAt = lastCheck.UTC().Format(time.RFC3339)
	return h
}

// Start wires the process-wide self-check into a server boot: fingerprint the
// binary, then start the drift ticker. It never returns an error — a boot must
// not fail because a self-check could not run (repo rule).
func Start() {
	st := Default()
	if err := st.Capture(); err != nil {
		log.WithError(err).Warn("Could not fingerprint the running binary; GET /api/health will report an empty binary_sha256")
	} else if snap, ok := st.Snapshot(); ok {
		log.WithFields(log.Fields{
			"sha256": shortSHA(snap.SHA256),
			"mtime":  snap.MTime.UTC().Format(time.RFC3339),
			"size":   snap.Size,
		}).Info("Running binary fingerprinted for drift detection")
	}
	st.StartWatcher(IntervalFromEnv())
}

// IntervalFromEnv resolves the drift check period: XBOT_BINARY_DRIFT_INTERVAL
// when it parses to a positive duration, otherwise DefaultDriftInterval.
func IntervalFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(DriftIntervalEnv))
	if raw == "" {
		return DefaultDriftInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.WithField("value", raw).
			Warn("Ignoring invalid " + DriftIntervalEnv + " (want a positive duration such as 60s)")
		return DefaultDriftInterval
	}
	return d
}

func shortSHA(sha string) string {
	if len(sha) <= shaExposedLen {
		return sha
	}
	return sha[:shaExposedLen]
}
