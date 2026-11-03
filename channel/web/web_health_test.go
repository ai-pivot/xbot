package web

// web_health_test.go — GET /api/health 的验收测试（2026-11-03 P0-1）。
//
// 验收点：
//  1. 路由真实存在（走 newServeMux，不是直接调 handler）：GET 免登录 200；
//  2. 载荷是**扁平** JSON（不套 {ok,data} 信封）且字段名与 internal/runtimeinfo
//     的契约一致 —— 部署脚本与外部探针按字段名取值；
//  3. schema 错配（binary=73 / db=80，即 11-02 那次事故的真实数字）⇒
//     schema_newer=true、status=degraded；
//  4. 非 GET 一律 405，且响应里不含文件系统路径。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"xbot/bus"
	"xbot/internal/runtimeinfo"
)

// newHealthTestChannel builds a channel with NO storage and NO session: the
// whole point of the endpoint is that it answers without authentication, so any
// token check anywhere in the path would make this test fail with 401.
func newHealthTestChannel(t *testing.T) *WebChannel {
	t.Helper()
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	t.Cleanup(wc.Stop)
	return wc
}

func TestHealthEndpointIsUnauthenticatedGET(t *testing.T) {
	// Isolate the process-wide self-check state; restore it for other tests.
	prevBinary, prevDB := runtimeinfo.Default().SchemaVersions()
	t.Cleanup(func() { runtimeinfo.Default().SetSchemaVersions(prevBinary, prevDB) })
	runtimeinfo.Default().SetSchemaVersions(80, 80)
	// Fingerprint the test binary so the payload carries a real sha256/mtime —
	// the field contract must be exercised with non-empty values.
	if err := runtimeinfo.Default().Capture(); err != nil {
		t.Fatalf("capture running binary: %v", err)
	}

	wc := newHealthTestChannel(t)
	mux := wc.newServeMux()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/health = %d, want 200 (no auth may be required): %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	if strings.Contains(rec.Body.String(), `"ok":`) {
		t.Fatalf("health payload must be flat, not the {ok,data} envelope: %s", rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health payload: %v (%s)", err, rec.Body.String())
	}
	t.Logf("GET /api/health → %s", rec.Body.String())

	want := []string{
		"status", "binary_sha256", "binary_mtime", "binary_changed",
		"schema_binary", "schema_db", "schema_newer", "checked_at",
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sort.Strings(want)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("health fields = %v, want %v", keys, want)
	}
	if got["status"] != runtimeinfo.StatusOK {
		t.Fatalf("status = %v, want ok for aligned schema", got["status"])
	}
	if got["schema_newer"] != false {
		t.Fatalf("schema_newer = %v, want false", got["schema_newer"])
	}
	if sha, _ := got["binary_sha256"].(string); len(sha) != 16 {
		t.Fatalf("binary_sha256 = %q, want 16 hex chars (the captured binary fingerprint)", sha)
	}
	if mtime, _ := got["binary_mtime"].(string); mtime == "" {
		t.Fatalf("binary_mtime is empty; the boot fingerprint must reach the probe")
	}
	for field, value := range got {
		if s, ok := value.(string); ok && strings.ContainsAny(s, "/") && field != "checked_at" && field != "binary_mtime" {
			t.Fatalf("%s = %q must not carry a filesystem path", field, s)
		}
	}

	// Method contract: probes use GET; anything else is refused explicitly
	// (not a 404 fall-through, which would hide a typo'd route).
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/health", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/health = %d, want 405", method, rec.Code)
		}
	}
}

// TestHealthEndpointThroughProductionMiddlewareChain proves the probe survives
// the exact handler chain Serve() installs (compression → request timing →
// security headers → mux). A route that only works on a bare mux is useless in
// a 401 storm.
func TestHealthEndpointThroughProductionMiddlewareChain(t *testing.T) {
	prevBinary, prevDB := runtimeinfo.Default().SchemaVersions()
	t.Cleanup(func() { runtimeinfo.Default().SetSchemaVersions(prevBinary, prevDB) })
	runtimeinfo.Default().SetSchemaVersions(80, 80)

	wc := newHealthTestChannel(t)
	handler := CompressionMiddleware(wc.requestTimingMiddleware(wc.securityHeadersMiddleware(wc.newServeMux())))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/health through the production chain = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health payload (compression must not break it): %v", err)
	}
	for _, field := range []string{"status", "binary_sha256", "binary_mtime", "binary_changed", "schema_binary", "schema_db", "schema_newer", "checked_at"} {
		if _, ok := got[field]; !ok {
			t.Fatalf("health payload through the production chain is missing %q: %s", field, rec.Body.String())
		}
	}
}

// TestHealthEndpointReportsSchemaNewer is the incident replay: an old binary
// (schema 73) serving a database migrated to 80 must be visible from the probe,
// not just from a log line.
func TestHealthEndpointReportsSchemaNewer(t *testing.T) {
	prevBinary, prevDB := runtimeinfo.Default().SchemaVersions()
	t.Cleanup(func() { runtimeinfo.Default().SetSchemaVersions(prevBinary, prevDB) })
	runtimeinfo.Default().SetSchemaVersions(73, 80)

	wc := newHealthTestChannel(t)
	rec := httptest.NewRecorder()
	wc.newServeMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/health = %d, want 200", rec.Code)
	}

	var got struct {
		Status        string `json:"status"`
		SchemaBinary  int    `json:"schema_binary"`
		SchemaDB      int    `json:"schema_db"`
		SchemaNewer   bool   `json:"schema_newer"`
		BinaryChanged bool   `json:"binary_changed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.SchemaNewer {
		t.Fatalf("schema_newer = false for binary=73/db=80: %s", rec.Body.String())
	}
	if got.Status != runtimeinfo.StatusDegraded {
		t.Fatalf("status = %q, want %q", got.Status, runtimeinfo.StatusDegraded)
	}
	if got.SchemaBinary != 73 || got.SchemaDB != 80 {
		t.Fatalf("schema pair = %d/%d, want 73/80", got.SchemaBinary, got.SchemaDB)
	}
	if got.BinaryChanged {
		t.Fatalf("binary_changed = true without any drift: %s", rec.Body.String())
	}
	t.Logf("GET /api/health (schema 73/80) → %s", rec.Body.String())
}
