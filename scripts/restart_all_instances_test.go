package main

// restart_all_instances_test.go — P0-1 验收：部署脚本必须「漏一个实例就失败」。
//
// 纪律（工部 P0-1 第 5 条）：二进制更新后要对**所有**实例 restart 并逐个校验，
// 漏一个即部署失败（exit≠0 且打印漏了谁）。这条纪律如果只写在 runbook 里，
// 下一次还是会漏 —— 11-02 就是漏了 daidai 那一个。所以脚本本身可执行，
// 并且用**注入的假 supervisorctl / 假 curl / 假日志**跑一遍：
//
//  1. 全部实例重启并校验通过 ⇒ exit 0；
//  2. supervisor 里在跑、但实例清单少了一个（=漏重启）⇒ exit≠0 且点名；
//  3. 实例还跑着旧二进制（/api/health 的 binary_sha256 对不上新二进制）⇒ exit≠0；
//  4. 实例日志尾部仍有 schema 不匹配告警 ⇒ exit≠0；
//  5. GET / 不是 200 ⇒ exit≠0。
//
// 这些假实现全部写在 t.TempDir() 里，通过 SUPERVISORCTL_BIN / CURL_BIN 注入，
// 不碰真实 supervisord、不碰真实端口、不重启任何进程。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeInstance 描述一个被脚本检查的实例及其「假运行状态」。
type fakeInstance struct {
	program  string
	port     string
	logBody  string // 日志内容
	httpCode string // GET / 的状态码
	health   string // /api/health 的 JSON（空 ⇒ 返回空）
}

// fakeWorld 是一次测试用的假部署环境。
type fakeWorld struct {
	dir       string
	instances string // instances.list 路径
	binary    string // 新二进制路径
	supctl    string
	curl      string
	restarted string // 记录被 restart 过的 program
	shaOfNew  string
}

func sha16(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// newBuildContent is the "freshly deployed" binary every test pretends to have
// installed; newBuildSHA16() is what /api/health must report afterwards.
var newBuildContent = []byte("the-new-build-20261103")

func newBuildSHA16() string { return sha16(newBuildContent) }

func healthJSON(sha string, schemaNewer bool, binaryChanged bool) string {
	status := "ok"
	if schemaNewer || binaryChanged {
		status = "degraded"
	}
	return fmt.Sprintf(`{"status":%q,"binary_sha256":%q,"binary_mtime":"2026-11-03T02:51:00Z","binary_changed":%t,"schema_binary":80,"schema_db":80,"schema_newer":%t,"checked_at":"2026-11-03T13:30:00Z"}`,
		status, sha, binaryChanged, schemaNewer)
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newFakeWorld builds fake supervisorctl/curl binaries plus per-instance state.
func newFakeWorld(t *testing.T, ins []fakeInstance, supervisorRunning []string) *fakeWorld {
	t.Helper()
	dir := t.TempDir()
	w := &fakeWorld{
		dir:       dir,
		instances: filepath.Join(dir, "instances.list"),
		binary:    filepath.Join(dir, "xbot-cli"),
		supctl:    filepath.Join(dir, "supervisorctl"),
		curl:      filepath.Join(dir, "curl"),
		restarted: filepath.Join(dir, "restarted.txt"),
	}

	newBinary := newBuildContent
	if err := os.WriteFile(w.binary, newBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	w.shaOfNew = sha16(newBinary)

	// instances.list: program|base_url|log_file
	var list strings.Builder
	list.WriteString("# program|base_url|log_file\n")
	for _, in := range ins {
		logPath := filepath.Join(dir, "logs", in.program+".log")
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(logPath, []byte(in.logBody), 0o644); err != nil {
			t.Fatal(err)
		}
		healthPath := filepath.Join(dir, "health", in.program+".json")
		if err := os.MkdirAll(filepath.Dir(healthPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(healthPath, []byte(in.health), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "code-"+in.port), []byte(in.httpCode), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "port-"+in.port), []byte(in.program), 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&list, "%s|http://127.0.0.1:%s|%s\n", in.program, in.port, logPath)
	}
	if err := os.WriteFile(w.instances, []byte(list.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake supervisorctl: `status` prints the configured program list,
	// `restart <prog>` records the program (the file is the audit trail the
	// test asserts on).
	var status strings.Builder
	for _, name := range supervisorRunning {
		fmt.Fprintf(&status, "%s                RUNNING   pid 4242, uptime 0:10:00\n", name)
	}
	if err := os.WriteFile(filepath.Join(dir, "supervisor_status.txt"), []byte(status.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExec(t, w.supctl, `#!/usr/bin/env bash
set -u
d="${FAKE_WORLD_DIR:?}"
if [ "${1-}" = "status" ]; then
  cat "$d/supervisor_status.txt"
  exit 0
fi
if [ "${1-}" = "restart" ]; then
  echo "${2-}" >> "$d/restarted.txt"
  exit 0
fi
echo "unknown supervisorctl command: $*" >&2
exit 1
`)

	// Fake curl: last http arg picks the instance by port; `-w '%{http_code}'`
	// means a status-code probe for GET /, otherwise the health payload.
	writeExec(t, w.curl, `#!/usr/bin/env bash
set -u
d="${FAKE_WORLD_DIR:?}"
url=""
want_code=0
prev=""
for a in "$@"; do
  case "$a" in
    *'{http_code}'*) want_code=1 ;;
    http*) url="$a" ;;
  esac
  prev="$a"
done
[ -z "$url" ] && { echo "fake curl: no url" >&2; exit 7; }
port="$(printf '%s' "$url" | sed -E 's#^https?://[^:/]+:([0-9]+).*#\1#')"
prog_file="$d/port-$port"
if [ ! -f "$prog_file" ]; then
  echo "fake curl: unknown port $port" >&2
  exit 7
fi
prog="$(cat "$prog_file")"
if [ "$want_code" = "1" ]; then
  cat "$d/code-$port"
  exit 0
fi
case "$url" in
  */api/health) cat "$d/health/$prog.json" ;;
  *) echo "" ;;
esac
exit 0
`)

	return w
}

// run executes the real deployment script against the fake world.
func (w *fakeWorld) run(t *testing.T, extraEnv ...string) (string, int) {
	t.Helper()
	args := []string{"restart-all-instances.sh", "--instances", w.instances, "--binary", w.binary}
	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(),
		"FAKE_WORLD_DIR="+w.dir,
		"SUPERVISORCTL_BIN="+w.supctl,
		"CURL_BIN="+w.curl,
		"XBOT_PROGRAM_PREFIX=xbot",
		"XBOT_READY_TIMEOUT=0",
		"XBOT_READY_INTERVAL=0",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run script: %v\n%s", err, out)
		}
	}
	return string(out), code
}

func (w *fakeWorld) restartedPrograms(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(w.restarted)
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

func TestRestartAllInstancesHappyPath(t *testing.T) {
	w := newFakeWorld(t, []fakeInstance{
		{program: "xbot-prod", port: "8080", logBody: "INFO started\n", httpCode: "200"},
		{program: "xbot-daidai", port: "8081", logBody: "INFO started\n", httpCode: "200"},
	}, []string{"xbot-prod", "xbot-daidai"})
	// Fill in health payloads now that the expected sha is known.
	for _, p := range []struct{ program, port string }{{"xbot-prod", "8080"}, {"xbot-daidai", "8081"}} {
		if err := os.WriteFile(filepath.Join(w.dir, "health", p.program+".json"),
			[]byte(healthJSON(w.shaOfNew, false, false)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, code := w.run(t)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\n%s", code, out)
	}
	if strings.Count(out, "OK    ") != 2 {
		t.Fatalf("expected both instances verified OK:\n%s", out)
	}
	got := w.restartedPrograms(t)
	if len(got) != 2 || got[0] != "xbot-prod" || got[1] != "xbot-daidai" {
		t.Fatalf("restarted programs = %v, want [xbot-prod xbot-daidai]", got)
	}
}

// TestRestartAllInstancesMissedInstanceFails is the incident replay: supervisor
// runs three xbot programs, the deployment list only knows two ⇒ the script must
// fail and name the missed one. This is the daidai case.
func TestRestartAllInstancesMissedInstanceFails(t *testing.T) {
	w := newFakeWorld(t, []fakeInstance{
		{program: "xbot-prod", port: "8080", logBody: "INFO started\n", httpCode: "200"},
	}, []string{"xbot-prod", "xbot-daidai", "xbot-backup"})
	// health payloads reference the new binary, so only the MISSED check can fail
	if err := os.WriteFile(filepath.Join(w.dir, "health", "xbot-prod.json"),
		[]byte(healthJSON(w.shaOfNew, false, false)), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := w.run(t)
	if code == 0 {
		t.Fatalf("a running instance absent from the list must fail the deploy:\n%s", out)
	}
	for _, missed := range []string{"xbot-daidai", "xbot-backup"} {
		if !strings.Contains(out, "MISSED  "+missed) {
			t.Fatalf("output must name the missed instance %q:\n%s", missed, out)
		}
	}
	if !strings.Contains(out, "DEPLOY FAILED") {
		t.Fatalf("output must state DEPLOY FAILED:\n%s", out)
	}
}

// TestRestartAllInstancesStaleBinaryFails covers the sneaky case: the instance
// answers 200 on / and its log is clean, but /api/health proves it is still the
// OLD process (binary_sha256 mismatch).
func TestRestartAllInstancesStaleBinaryFails(t *testing.T) {
	w := newFakeWorld(t, []fakeInstance{
		{program: "xbot-prod", port: "8080", logBody: "INFO started\n", httpCode: "200",
			health: healthJSON(newBuildSHA16(), false, false)},
		{program: "xbot-daidai", port: "8081", logBody: "INFO started\n", httpCode: "200",
			health: healthJSON("ffffffffffffffff", false, false)},
	}, []string{"xbot-prod", "xbot-daidai"})

	out, code := w.run(t)
	if code == 0 {
		t.Fatalf("an instance still running the old binary must fail the deploy:\n%s", out)
	}
	if !strings.Contains(out, "FAIL  xbot-daidai") || !strings.Contains(out, "still running the OLD binary") {
		t.Fatalf("output must name the stale instance and the reason:\n%s", out)
	}
	if strings.Contains(out, "FAIL  xbot-prod") {
		t.Fatalf("healthy instance must not be reported as failed:\n%s", out)
	}
}

// TestRestartAllInstancesSchemaWarningInLogFails: the log tail is the other
// place the mismatch shows up; the script must not declare success over it.
func TestRestartAllInstancesSchemaWarningInLogFails(t *testing.T) {
	w := newFakeWorld(t, []fakeInstance{
		{program: "xbot-daidai", port: "8081", httpCode: "200",
			logBody: "INFO started\nlevel=error msg=\"Stored schema version is NEWER than this binary supports\" binary_schema=73 db_schema=80\n",
			health:  healthJSON(newBuildSHA16(), false, false)},
	}, []string{"xbot-daidai"})

	out, code := w.run(t)
	if code == 0 {
		t.Fatalf("a schema mismatch warning in the log tail must fail the deploy:\n%s", out)
	}
	if !strings.Contains(out, "schema mismatch warning") {
		t.Fatalf("output must explain the log finding:\n%s", out)
	}
}

// TestRestartAllInstancesSchemaNewerHealthFails: even with a clean log and the
// right binary, /api/health saying schema_newer=true is a failed deploy.
func TestRestartAllInstancesSchemaNewerHealthFails(t *testing.T) {
	w := newFakeWorld(t, []fakeInstance{
		{program: "xbot-daidai", port: "8081", logBody: "INFO started\n", httpCode: "200",
			health: healthJSON(newBuildSHA16(), true, false)},
	}, []string{"xbot-daidai"})

	out, code := w.run(t)
	if code == 0 {
		t.Fatalf("schema_newer=true must fail the deploy:\n%s", out)
	}
	if !strings.Contains(out, "schema_newer=true") {
		t.Fatalf("output must name the health finding:\n%s", out)
	}
}

// TestRestartAllInstancesNon200Fails keeps the original contract: GET / == 200.
func TestRestartAllInstancesNon200Fails(t *testing.T) {
	w := newFakeWorld(t, []fakeInstance{
		{program: "xbot-prod", port: "8080", logBody: "INFO started\n", httpCode: "502",
			health: healthJSON(newBuildSHA16(), false, false)},
	}, []string{"xbot-prod"})

	out, code := w.run(t)
	if code == 0 {
		t.Fatalf("GET / != 200 must fail the deploy:\n%s", out)
	}
	if !strings.Contains(out, "GET / returned 502") {
		t.Fatalf("output must state the status code:\n%s", out)
	}
}

// TestRestartAllInstancesRefusesEmptyList: an empty instance list must be a
// configuration error, never a silent success.
func TestRestartAllInstancesRefusesEmptyList(t *testing.T) {
	w := newFakeWorld(t, nil, nil)
	if err := os.WriteFile(w.instances, []byte("# nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := w.run(t)
	if code != 2 {
		t.Fatalf("empty instance list must exit 2, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "no instances") {
		t.Fatalf("output must explain the empty list:\n%s", out)
	}
}
