package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-pivot/xbot/plugin/protocol"
)

// ---------------------------------------------------------------------------
// Test doubles / helpers
// ---------------------------------------------------------------------------

var (
	testShaAsset = strings.Repeat("ab", 32) // 64 hex chars
	testShaOther = strings.Repeat("cd", 32)
)

// fakeExecutor stands in for executeSSH. It routes canned responses by step
// marker, records every script, and can block (for async assertions).
type fakeExecutor struct {
	mu        sync.Mutex
	responses map[string]string
	failures  map[string]error
	scripts   []string
	block     chan struct{}
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{responses: map[string]string{}, failures: map[string]error{}}
}

func (f *fakeExecutor) setResponse(step, out string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[step] = out
}

func (f *fakeExecutor) setFailure(step string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[step] = err
}

func (f *fakeExecutor) exec(ctx context.Context, sshField string, timeout time.Duration, script string) (string, error) {
	step := stepOf(script)
	f.mu.Lock()
	f.scripts = append(f.scripts, script)
	block := f.block
	out := f.responses[step]
	err := f.failures[step]
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return out, err
}

func (f *fakeExecutor) stepNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.scripts))
	for _, s := range f.scripts {
		names = append(names, stepOf(s))
	}
	return names
}

func (f *fakeExecutor) scriptsContain(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.scripts {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// stepOf mirrors markScript's marker extraction.
func stepOf(script string) string {
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, jobStepMarker) {
			return strings.TrimPrefix(line, jobStepMarker)
		}
	}
	return ""
}

func callRaw(t *testing.T, svc *service, method string, params map[string]any) *protocol.WebPluginRPCResult {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		raw = data
	}
	res, err := svc.handleRPC(&protocol.WebPluginRPCParams{Method: method, Params: raw})
	if err != nil {
		t.Fatalf("%s: transport error: %v", method, err)
	}
	return res
}

func callOK(t *testing.T, svc *service, method string, params map[string]any) map[string]any {
	t.Helper()
	res := callRaw(t, svc, method, params)
	if res.Error != "" {
		t.Fatalf("%s returned error: %s", method, res.Error)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Result), &out); err != nil {
		t.Fatalf("%s: bad result JSON: %v (%s)", method, err, res.Result)
	}
	return out
}

func waitJob(t *testing.T, svc *service, jobID string) jobSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, ok := svc.jobs.snapshot(jobID)
		if !ok {
			t.Fatalf("job %s not found", jobID)
		}
		if snap.State != "running" {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not reach a terminal state; steps=%v", jobID, stepNamesOf(snap))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func stepNamesOf(snap jobSnapshot) []string {
	names := make([]string, 0, len(snap.Steps))
	for _, s := range snap.Steps {
		names = append(names, s.Name)
	}
	return names
}

func stepByName(snap jobSnapshot, name string) *jobStep {
	for i := range snap.Steps {
		if snap.Steps[i].Name == name {
			return &snap.Steps[i]
		}
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func fakeHappyProvision(f *fakeExecutor) {
	f.setResponse("detect", "OS=Linux\nARCH=x86_64\nUID=1000\nSYSTEMCTL=/usr/bin/systemctl\n")
	f.setResponse("prepare-dir", "INSTALL_DIR=/home/dev/.local/bin\nINSTALL_DIR_WHY=install_dir not writable as non-root; using ~/.local/bin\n")
	f.setResponse("download", "DOWNLOADER=curl\n"+
		"TMP_DIR=/home/dev/.local/bin/.xbot-runner-dl-m1\n"+
		"BIN_PATH=/home/dev/.local/bin/.xbot-runner-dl-m1/xbot-runner-linux-amd64\n"+
		"CHECKSUMS_BEGIN=/home/dev/.local/bin/.xbot-runner-dl-m1/checksums.txt\n"+
		testShaAsset+"  xbot-runner-linux-amd64\n"+
		testShaOther+"  checksums.txt\n"+
		"CHECKSUMS_END\n")
	f.setResponse("verify", "SHA_OK="+testShaAsset+"\n")
	f.setResponse("kill-old", "KILLED=0\n")
	f.setResponse("install", "INSTALLED_BIN=/home/dev/.local/bin/xbot-runner\nINSTALLED_VERSION=xbot-runner v0.0.99\nCLEANUP=ok\n")
	f.setResponse("ready", "")
}

func defaultProvisionParams() map[string]any {
	return map[string]any{
		"ssh":         "ssh -i /home/dev/.ssh/id_ed25519 dev@10.0.0.9 -p 2222",
		"name":        "m1",
		"connect_cmd": "--server ws://xbot.example:8082/ws --token s3cr3t-token",
	}
}

// ---------------------------------------------------------------------------
// SSH argv assembly
// ---------------------------------------------------------------------------

func TestBuildSSHArgv_InjectsDefaultsBeforeUserArgsScriptLast(t *testing.T) {
	argv, err := buildSSHArgv("ssh user@1.2.3.4 -p 2222", "echo hi")
	if err != nil {
		t.Fatalf("buildSSHArgv: %v", err)
	}
	want := []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		"user@1.2.3.4", "-p", "2222",
		"echo hi",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv mismatch:\n got %v\nwant %v", argv, want)
	}
}

func TestBuildSSHArgv_PreservesUserOptionAndHostOrder(t *testing.T) {
	defaults := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new"}

	// Options before host.
	argv, err := buildSSHArgv("ssh -p 2222 user@host", "uname -m")
	if err != nil {
		t.Fatalf("buildSSHArgv: %v", err)
	}
	want := append(append([]string{"ssh"}, defaults...), "-p", "2222", "user@host", "uname -m")
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv mismatch:\n got %v\nwant %v", argv, want)
	}

	// Host before options (the user's own order is preserved verbatim).
	argv, err = buildSSHArgv("ssh user@host -p 2222", "uname -m")
	if err != nil {
		t.Fatalf("buildSSHArgv: %v", err)
	}
	want = append(append([]string{"ssh"}, defaults...), "user@host", "-p", "2222", "uname -m")
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv mismatch:\n got %v\nwant %v", argv, want)
	}
}

func TestBuildSSHArgv_DoesNotRepeatUserSuppliedOptions(t *testing.T) {
	argv, err := buildSSHArgv("ssh -o BatchMode=no -oConnectTimeout=30 user@host", "true")
	if err != nil {
		t.Fatalf("buildSSHArgv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, banned := range []string{"BatchMode=yes", "ConnectTimeout=10"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("default %q must not be injected when the user supplied the same key: %v", banned, argv)
		}
	}
	for _, want := range []string{"BatchMode=no", "ConnectTimeout=30", "StrictHostKeyChecking=accept-new"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %q: %v", want, argv)
		}
	}
}

func TestBuildSSHArgv_OptionKeyMatchIsCaseInsensitive(t *testing.T) {
	argv, err := buildSSHArgv("ssh -o batchmode=no host", "true")
	if err != nil {
		t.Fatalf("buildSSHArgv: %v", err)
	}
	if strings.Contains(strings.Join(argv, " "), "BatchMode=yes") {
		t.Fatalf("case-insensitive key match failed: %v", argv)
	}
}

func TestBuildSSHArgv_RejectsEmptyInputs(t *testing.T) {
	if _, err := buildSSHArgv("", "true"); err == nil {
		t.Fatal("expected error for an empty ssh command")
	}
	if _, err := buildSSHArgv("ssh host", ""); err == nil {
		t.Fatal("expected error for an empty script")
	}
}

// ---------------------------------------------------------------------------
// SSH masking
// ---------------------------------------------------------------------------

func TestMaskSSH_KeepsOnlyHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ssh -i /home/dev/.ssh/id_ed25519 dev@10.0.0.9 -p 2222", "ssh dev@10.0.0.9 <redacted>"},
		{"ssh -o BatchMode=no -oConnectTimeout=30 ubuntu@host", "ssh ubuntu@host <redacted>"},
		{"ssh -p2222 host", "ssh host <redacted>"},
		{"", "(empty ssh command)"},
	}
	for _, tc := range cases {
		if got := maskSSH(tc.in); got != tc.want {
			t.Errorf("maskSSH(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMaskSSH_DoesNotLeakOptionDetails(t *testing.T) {
	masked := maskSSH("ssh -i /home/dev/.ssh/id_ed25519 -p 2222 -o ProxyCommand=nc%h%p user@1.2.3.4")
	for _, leak := range []string{"id_ed25519", "/home/dev", "2222", "ProxyCommand", "nc%h%p"} {
		if strings.Contains(masked, leak) {
			t.Fatalf("masked ssh leaks %q: %q", leak, masked)
		}
	}
	if !strings.Contains(masked, "user@1.2.3.4") {
		t.Fatalf("masked ssh lost the host: %q", masked)
	}
}

// ---------------------------------------------------------------------------
// Platform detection
// ---------------------------------------------------------------------------

func TestNormalizePlatform(t *testing.T) {
	cases := []struct {
		unameS, unameM string
		wantOS         string
		wantArch       string
		wantAsset      string
		wantErr        bool
	}{
		{"Linux", "x86_64", "linux", "amd64", "xbot-runner-linux-amd64", false},
		{"linux", "amd64", "linux", "amd64", "xbot-runner-linux-amd64", false},
		{"Darwin", "arm64", "darwin", "arm64", "xbot-runner-darwin-arm64", false},
		{"Darwin", "aarch64", "darwin", "arm64", "xbot-runner-darwin-arm64", false},
		{"linux", "arm64", "linux", "arm64", "xbot-runner-linux-arm64", false},
		{"Windows", "x86_64", "", "", "", true},
		{"linux", "i686", "", "", "", true},
		{"", "", "", "", "", true},
	}
	for _, tc := range cases {
		p, err := normalizePlatform(tc.unameS, tc.unameM)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizePlatform(%q, %q): expected error", tc.unameS, tc.unameM)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizePlatform(%q, %q): %v", tc.unameS, tc.unameM, err)
			continue
		}
		if p.OS != tc.wantOS || p.Arch != tc.wantArch || p.Asset != tc.wantAsset {
			t.Errorf("normalizePlatform(%q, %q) = %+v, want %s/%s/%s", tc.unameS, tc.unameM, p, tc.wantOS, tc.wantArch, tc.wantAsset)
		}
	}
}

// ---------------------------------------------------------------------------
// checksums.txt parsing
// ---------------------------------------------------------------------------

func TestParseChecksums(t *testing.T) {
	content := testShaOther + "  xbot-cli-linux-amd64\r\n" +
		testShaAsset + "  xbot-runner-linux-amd64\n" +
		strings.Repeat("ef", 32) + " *xbot-runner-darwin-arm64\n"

	sha, err := parseChecksums(content, "xbot-runner-linux-amd64")
	if err != nil || sha != testShaAsset {
		t.Fatalf("parseChecksums = (%q, %v), want %q", sha, err, testShaAsset)
	}
	// "*" binary marker and uppercase hex are accepted.
	sha, err = parseChecksums(strings.ToUpper(testShaAsset)+" *xbot-runner-linux-amd64\n", "xbot-runner-linux-amd64")
	if err != nil || sha != testShaAsset {
		t.Fatalf("parseChecksums with binary marker = (%q, %v), want %q", sha, err, testShaAsset)
	}
}

func TestParseChecksums_MissingEntryIsError(t *testing.T) {
	content := testShaOther + "  xbot-cli-linux-amd64\n"
	_, err := parseChecksums(content, "xbot-runner-linux-amd64")
	if err == nil {
		t.Fatal("expected an error when the asset has no checksum entry")
	}
	if !strings.Contains(err.Error(), "no entry for xbot-runner-linux-amd64") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseChecksums_InvalidShaIsError(t *testing.T) {
	_, err := parseChecksums("not-a-sha  xbot-runner-linux-amd64\n", "xbot-runner-linux-amd64")
	if err == nil {
		t.Fatal("expected an error for a malformed sha256")
	}
}

// ---------------------------------------------------------------------------
// probe
// ---------------------------------------------------------------------------

func TestProbe_ReportsEnvironment(t *testing.T) {
	f := newFakeExecutor()
	f.setResponse("probe", "OS=Linux\n"+
		"ARCH=aarch64\n"+
		"USER=ubuntu\n"+
		"UID=1000\n"+
		"SYSTEMCTL=/bin/systemctl\n"+
		"CURL=/usr/bin/curl\n"+
		"WGET=\n"+
		"BIN=/home/ubuntu/.local/bin/xbot-runner\n"+
		"VERSION=xbot-runner v0.0.42\n")
	svc := newService(f.exec)

	res := callRaw(t, svc, "probe", map[string]any{"ssh": "ssh ubuntu@host"})
	if res.Error != "" {
		t.Fatalf("probe error: %s", res.Error)
	}
	var pr probeResult
	if err := json.Unmarshal([]byte(res.Result), &pr); err != nil {
		t.Fatalf("bad probe JSON: %v (%s)", err, res.Result)
	}
	if pr.OS != "linux" || pr.Arch != "arm64" {
		t.Fatalf("platform wrong: %+v", pr)
	}
	if pr.User != "ubuntu" || pr.IsRoot {
		t.Fatalf("user/root wrong: %+v", pr)
	}
	if !pr.HasSystemd || !pr.HasCurl || pr.HasWget {
		t.Fatalf("capability flags wrong: %+v", pr)
	}
	if pr.InstalledVersion != "xbot-runner v0.0.42" {
		t.Fatalf("installed_version wrong: %+v", pr)
	}
	if pr.InstallDir != "/home/ubuntu/.local/bin" {
		t.Fatalf("install_dir wrong: %+v", pr)
	}
	if len(f.stepNames()) != 1 {
		t.Fatalf("probe must be a single read-only ssh call, got %v", f.stepNames())
	}
	if f.scriptsContain("mkdir") || f.scriptsContain("rm -f") {
		t.Fatal("probe must not modify anything remotely")
	}
}

func TestProbe_RootDetection(t *testing.T) {
	f := newFakeExecutor()
	f.setResponse("probe", "OS=Darwin\nARCH=arm64\nUSER=root\nUID=0\n")
	svc := newService(f.exec)
	res := callRaw(t, svc, "probe", map[string]any{"ssh": "ssh root@host"})
	var pr probeResult
	if err := json.Unmarshal([]byte(res.Result), &pr); err != nil {
		t.Fatalf("bad probe JSON: %v", err)
	}
	if !pr.IsRoot {
		t.Fatalf("uid=0 must map to is_root=true: %+v", pr)
	}
	if pr.InstallDir != "" || pr.InstalledVersion != "" {
		t.Fatalf("absent binary must yield empty fields: %+v", pr)
	}
}

// ---------------------------------------------------------------------------
// provision (async) — immediate return + job_status polling
// ---------------------------------------------------------------------------

func TestProvision_ReturnsImmediatelyAndJobStatusPollable(t *testing.T) {
	f := newFakeExecutor()
	fakeHappyProvision(f)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	f.block = release
	svc := newService(f.exec)

	params, err := json.Marshal(defaultProvisionParams())
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	type callResult struct {
		res *protocol.WebPluginRPCResult
		err error
	}
	done := make(chan callResult, 1)
	start := time.Now()
	go func() {
		res, err := svc.handleRPC(&protocol.WebPluginRPCParams{Method: "provision", Params: params})
		done <- callResult{res, err}
	}()

	var r callResult
	select {
	case r = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("provision did not return immediately; it must run the job in the background")
	}
	if r.err != nil || r.res.Error != "" {
		t.Fatalf("provision failed: err=%v msg=%s", r.err, r.res.Error)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("provision returned after %s; it must return immediately", elapsed)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(r.res.Result), &out); err != nil {
		t.Fatalf("bad provision JSON: %v", err)
	}
	jobID := out["job_id"]
	if jobID == "" {
		t.Fatal("provision response must carry job_id")
	}

	// While the job is blocked on its first ssh call, job_status must answer
	// promptly and report state=running.
	statusStart := time.Now()
	js := callOK(t, svc, "job_status", map[string]any{"job_id": jobID})
	if js["state"] != "running" {
		t.Fatalf("job must be running while blocked; got %v", js["state"])
	}
	if elapsed := time.Since(statusStart); elapsed > 2*time.Second {
		t.Fatalf("job_status blocked for %s; it must return immediately", elapsed)
	}

	releaseAll()
	snap := waitJob(t, svc, jobID)
	if snap.State != "done" {
		t.Fatalf("job state=%s error=%s", snap.State, snap.Error)
	}
	if len(snap.Steps) == 0 {
		t.Fatal("job recorded no steps")
	}
}

func TestProvision_HappyPathRecordsAllSteps(t *testing.T) {
	f := newFakeExecutor()
	fakeHappyProvision(f)
	svc := newService(f.exec)

	out := callOK(t, svc, "provision", defaultProvisionParams())
	jobID, _ := out["job_id"].(string)
	snap := waitJob(t, svc, jobID)
	if snap.State != "done" {
		t.Fatalf("state=%s error=%s", snap.State, snap.Error)
	}
	want := []string{"detect", "prepare-dir", "download", "verify", "kill-old", "install", "persist", "ready"}
	if got := stepNamesOf(snap); !reflect.DeepEqual(got, want) {
		t.Fatalf("steps mismatch:\n got %v\nwant %v", got, want)
	}
	for _, st := range snap.Steps {
		if !st.OK {
			t.Fatalf("step %s not ok: %s", st.Name, st.Detail)
		}
	}
	if v := stepByName(snap, "verify"); !strings.Contains(v.Detail, "sha256 ok") {
		t.Fatalf("verify detail wrong: %q", v.Detail)
	}
	if inst := stepByName(snap, "install"); !strings.Contains(inst.Detail, "xbot-runner v0.0.99") {
		t.Fatalf("install detail wrong: %q", inst.Detail)
	}
	if ready := stepByName(snap, "ready"); ready == nil || !strings.Contains(ready.Detail, "installed /home/dev/.local/bin/xbot-runner") {
		t.Fatalf("ready detail wrong: %+v", ready)
	}

	// job_status JSON contract: {state, steps:[{name,ok,detail}], error}
	js := callOK(t, svc, "job_status", map[string]any{"job_id": jobID})
	steps, ok := js["steps"].([]any)
	if !ok || len(steps) != len(want) {
		t.Fatalf("job_status steps wrong: %v", js["steps"])
	}
	first, _ := steps[0].(map[string]any)
	if first["name"] != "detect" || first["ok"] != true || first["detail"] == "" {
		t.Fatalf("first step wrong: %v", first)
	}
}

func TestProvision_ShaMismatchFailsJob(t *testing.T) {
	f := newFakeExecutor()
	fakeHappyProvision(f)
	f.setFailure("verify", errors.New("remote command failed (exit 17): SHA_EXPECT=abab; SHA_ACTUAL=cdcd; VERIFY_ERROR=sha256 mismatch"))
	svc := newService(f.exec)

	out := callOK(t, svc, "provision", defaultProvisionParams())
	snap := waitJob(t, svc, out["job_id"].(string))
	if snap.State != "failed" {
		t.Fatalf("want failed, got %s", snap.State)
	}
	if !strings.Contains(snap.Error, "sha256") {
		t.Fatalf("error must mention sha256: %q", snap.Error)
	}
	v := stepByName(snap, "verify")
	if v == nil || v.OK {
		t.Fatalf("verify step must be recorded as failed: %+v", v)
	}
	if stepByName(snap, "install") != nil {
		t.Fatal("install must not run after a checksum failure")
	}
}

func TestProvision_MissingChecksumEntryFailsBeforeVerify(t *testing.T) {
	f := newFakeExecutor()
	fakeHappyProvision(f)
	// checksums.txt downloaded, but it does not cover our asset.
	f.setResponse("download", "DOWNLOADER=curl\n"+
		"TMP_DIR=/tmp/x\n"+
		"BIN_PATH=/tmp/x/xbot-runner-linux-amd64\n"+
		"CHECKSUMS_BEGIN=/tmp/x/checksums.txt\n"+
		testShaOther+"  xbot-cli-linux-amd64\n"+
		"CHECKSUMS_END\n")
	svc := newService(f.exec)

	out := callOK(t, svc, "provision", defaultProvisionParams())
	snap := waitJob(t, svc, out["job_id"].(string))
	if snap.State != "failed" {
		t.Fatalf("want failed, got %s", snap.State)
	}
	if !strings.Contains(snap.Error, "no entry for xbot-runner-linux-amd64") {
		t.Fatalf("unexpected error: %q", snap.Error)
	}
	if contains(f.stepNames(), "verify") {
		t.Fatal("verify ssh call must not run when the asset has no checksum")
	}
}

func TestProvision_DryRunPlanDoesNotWrite(t *testing.T) {
	f := newFakeExecutor()
	fakeHappyProvision(f)
	f.setResponse("prepare-dir", "INSTALL_DIR=/usr/local/bin\nINSTALL_DIR_WHY=requested\n")
	svc := newService(f.exec)
	params := defaultProvisionParams()
	params["dry_run"] = true

	out := callOK(t, svc, "provision", params)
	snap := waitJob(t, svc, out["job_id"].(string))
	if snap.State != "done" {
		t.Fatalf("state=%s error=%s", snap.State, snap.Error)
	}
	if got := stepNamesOf(snap); !reflect.DeepEqual(got, []string{"detect", "prepare-dir", "download", "verify", "kill-old", "install", "ready"}) {
		t.Fatalf("dry-run plan steps wrong: %v", got)
	}
	for _, st := range snap.Steps {
		if st.Name == "download" && !strings.HasPrefix(st.Detail, "dry-run:") {
			t.Fatalf("dry-run step must be marked: %+v", st)
		}
	}
	for _, banned := range []string{"download", "verify", "install", "kill-old", "ready"} {
		if contains(f.stepNames(), banned) {
			t.Fatalf("dry-run must not execute %q (executed: %v)", banned, f.stepNames())
		}
	}
}

// provision 是 install-only ⇒ **不要求 connect_cmd**。
// 2026-09-18 生产踩坑：面板的 provision 只传 ssh/name/download_base/install_dir，
// 旧校验把「点 Provision」变成硬失败（"connect_cmd is required"），二进制装不上。
// 反向守护：connect 仍然必须带它（启动 runner 需要 --server/--token）。
func TestProvision_ConnectCmdOptional(t *testing.T) {
	p, err := parseProvisionParams(map[string]any{"ssh": "ssh h", "name": "m1"})
	if err != nil {
		t.Fatalf("provision must not require connect_cmd: %v", err)
	}
	if p.ConnectCmd != "" {
		t.Fatalf("connect_cmd should stay empty when omitted, got %q", p.ConnectCmd)
	}

	// 端到端（dry-run，不跑 ssh）：无 connect_cmd 也必须走完计划
	f := newFakeExecutor()
	fakeHappyProvision(f)
	f.setResponse("prepare-dir", "INSTALL_DIR=/usr/local/bin\nINSTALL_DIR_WHY=requested\n")
	svc := newService(f.exec)
	params := defaultProvisionParams()
	delete(params, "connect_cmd")
	params["dry_run"] = true
	out := callOK(t, svc, "provision", params)
	snap := waitJob(t, svc, out["job_id"].(string))
	if snap.State != "done" {
		t.Fatalf("dry-run provision without connect_cmd must succeed: state=%s error=%s", snap.State, snap.Error)
	}

	// 反向：connect 仍必须要求 connect_cmd
	res := callRaw(t, svc, "connect", map[string]any{"ssh": "ssh h", "name": "m1"})
	if !strings.Contains(res.Error, "connect_cmd is required") {
		t.Fatalf("connect must still require connect_cmd, got %q", res.Error)
	}
}

// ---------------------------------------------------------------------------
// deprovision
// ---------------------------------------------------------------------------

func TestDeprovision_UninstallRunsAllSteps(t *testing.T) {
	f := newFakeExecutor()
	f.setResponse("stop", "STOPPED=systemd unit stopped\n")
	f.setResponse("cleanup", "REMOVED=unit removed data removed\n")
	f.setResponse("remove-binary", "BIN_REMOVED=/home/dev/.local/bin/xbot-runner\n")
	svc := newService(f.exec)

	out := callOK(t, svc, "deprovision", map[string]any{"ssh": "ssh h", "name": "m1", "uninstall": true})
	snap := waitJob(t, svc, out["job_id"].(string))
	if snap.State != "done" {
		t.Fatalf("state=%s error=%s", snap.State, snap.Error)
	}
	if got := stepNamesOf(snap); !reflect.DeepEqual(got, []string{"disconnect", "stop", "cleanup", "remove-binary"}) {
		t.Fatalf("steps wrong: %v", got)
	}
	if d := stepByName(snap, "remove-binary").Detail; d != "/home/dev/.local/bin/xbot-runner" {
		t.Fatalf("remove-binary detail wrong: %q", d)
	}
}

func TestDeprovision_KeepsBinaryWhenNotUninstalling(t *testing.T) {
	f := newFakeExecutor()
	f.setResponse("stop", "STOPPED=\n")
	f.setResponse("cleanup", "REMOVED=\n")
	svc := newService(f.exec)

	out := callOK(t, svc, "deprovision", map[string]any{"ssh": "ssh h", "name": "m1"})
	snap := waitJob(t, svc, out["job_id"].(string))
	if snap.State != "done" {
		t.Fatalf("state=%s error=%s", snap.State, snap.Error)
	}
	if got := stepNamesOf(snap); !reflect.DeepEqual(got, []string{"disconnect", "stop", "cleanup"}) {
		t.Fatalf("steps wrong: %v", got)
	}
	if contains(f.stepNames(), "remove-binary") {
		t.Fatal("binary removal must not run without uninstall")
	}
	if stop := stepByName(snap, "stop"); stop.Detail != "no leftover process found" {
		t.Fatalf("stop detail wrong: %q", stop.Detail)
	}
}

// ---------------------------------------------------------------------------
// status / logs
// ---------------------------------------------------------------------------

func TestStatus_ReportsVersionAndConnectionState(t *testing.T) {
	f := newFakeExecutor()
	f.setResponse("status", "STATUS_BIN=/home/dev/.local/bin/xbot-runner\n"+
		"STATUS_VERSION=xbot-runner v0.0.9\n"+
		"STATUS_STATE=active\n"+
		"STATUS_DETAIL=leftover process: 4242\n")
	svc := newService(f.exec)

	out := callOK(t, svc, "status", map[string]any{"ssh": "ssh h", "name": "m1"})
	// With the SSH-pipe model there is no remote service: `service_state` reports
	// the connection state and `connected` is the boolean.
	if out["service_state"] != "disconnected" {
		t.Fatalf("service_state wrong: %v", out)
	}
	if out["connected"] != false {
		t.Fatalf("connected wrong: %v", out)
	}
	if out["installed_version"] != "xbot-runner v0.0.9" {
		t.Fatalf("installed_version wrong: %v", out)
	}
	detail, _ := out["detail"].(string)
	if !strings.Contains(detail, "binary=/home/dev/.local/bin/xbot-runner") {
		t.Fatalf("detail wrong: %q", detail)
	}
}

// A live supervisor must surface as connected, with its restart counter.
func TestStatus_ReflectsSupervisorState(t *testing.T) {
	svc := newService(newFakeExecutor().exec)
	svc.sups.put(&supervisor{
		spec:      targetSpec{Name: "m1", ConnMode: connModeTunnel},
		exec:      svc.exec,
		connected: true, remotePort: 39042, restarts: 3,
	})

	out := callOK(t, svc, "status", map[string]any{"ssh": "ssh h", "name": "m1"})
	if out["service_state"] != "connected" || out["connected"] != true {
		t.Fatalf("connected state wrong: %v", out)
	}
	if toFloat(out["restarts"]) != 3 || toFloat(out["remote_port"]) != 39042 {
		t.Fatalf("supervisor fields wrong: %v", out)
	}
	if d, _ := out["detail"].(string); !strings.Contains(d, "tunnel 127.0.0.1:39042") {
		t.Fatalf("detail must show the tunnel endpoint: %q", d)
	}
}

func TestLogs_ReturnsLinesAndCarriesCount(t *testing.T) {
	f := newFakeExecutor()
	f.setResponse("logs", "line one\nline two\n\n")
	svc := newService(f.exec)

	out := callOK(t, svc, "logs", map[string]any{"ssh": "ssh h", "name": "m1", "lines": 7})
	lines, ok := out["lines"].([]any)
	if !ok || len(lines) != 2 || lines[0] != "line one" || lines[1] != "line two" {
		t.Fatalf("lines wrong: %v", out["lines"])
	}
	if !f.scriptsContain("N=7") {
		t.Fatalf("line count must reach the remote script: %v", f.stepNames())
	}
}

func TestLogs_ClampsLineCount(t *testing.T) {
	f := newFakeExecutor()
	f.setResponse("logs", "")
	svc := newService(f.exec)
	callOK(t, svc, "logs", map[string]any{"ssh": "ssh h", "name": "m1", "lines": 999999})
	if !f.scriptsContain("N=2000") {
		t.Fatal("line count must be clamped to 2000")
	}
}

// ---------------------------------------------------------------------------
// dispatch & validation
// ---------------------------------------------------------------------------

func TestHandleRPC_UnknownMethodReturnsError(t *testing.T) {
	svc := newService(newFakeExecutor().exec)
	res := callRaw(t, svc, "not-a-method", nil)
	if res.Error != "unknown method: not-a-method" {
		t.Fatalf("got %q", res.Error)
	}
}

func TestHandleRPC_ProvisionValidationRunsNoSSH(t *testing.T) {
	f := newFakeExecutor()
	svc := newService(f.exec)
	cases := []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{}, "ssh is required"},
		{map[string]any{"ssh": "ssh h"}, "name is required"},
		{map[string]any{"ssh": "ssh h", "name": "bad name!"}, "invalid name"},
	}
	for _, tc := range cases {
		res := callRaw(t, svc, "provision", tc.params)
		if !strings.Contains(res.Error, tc.want) {
			t.Errorf("params %v: got error %q, want containing %q", tc.params, res.Error, tc.want)
		}
	}
	if len(f.stepNames()) != 0 {
		t.Fatalf("validation failures must not run ssh: %v", f.stepNames())
	}
}

func TestJobStatus_UnknownJobReturnsError(t *testing.T) {
	svc := newService(newFakeExecutor().exec)
	res := callRaw(t, svc, "job_status", map[string]any{"job_id": "nope"})
	if !strings.Contains(res.Error, "unknown job") {
		t.Fatalf("got %q", res.Error)
	}
}

// ---------------------------------------------------------------------------
// systemd unit
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// job store concurrency / snapshot semantics
// ---------------------------------------------------------------------------

func TestJobStore_ConcurrentAppendsAndSnapshots(t *testing.T) {
	st := newJobStore()
	rec := st.create("test", "t")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			st.addStep(rec.id, fmt.Sprintf("s%d", i), true, "d")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			st.snapshot(rec.id)
		}
	}()
	wg.Wait()

	snap, ok := st.snapshot(rec.id)
	if !ok || len(snap.Steps) != 300 {
		t.Fatalf("lost steps: %d", len(snap.Steps))
	}
	st.finish(rec.id, errors.New("boom"))
	snap, _ = st.snapshot(rec.id)
	if snap.State != "failed" || snap.Error != "boom" {
		t.Fatalf("state/error wrong: %+v", snap)
	}
}

func TestJobStore_SnapshotIsCopy(t *testing.T) {
	st := newJobStore()
	rec := st.create("k", "n")
	st.addStep(rec.id, "a", true, "d")
	snap, _ := st.snapshot(rec.id)
	snap.Steps[0].Name = "mutated"
	again, _ := st.snapshot(rec.id)
	if again.Steps[0].Name != "a" {
		t.Fatal("snapshot must not alias internal state")
	}
}

// toFloat reads a number out of a JSON-decoded RPC result (numbers arrive as
// float64 after the marshal/unmarshal round trip).
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return -1
	}
}
