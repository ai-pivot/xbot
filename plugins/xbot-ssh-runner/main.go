// Command ssh-runner implements the xbot.ssh-runner stdio plugin backend.
//
// It is a pure stdio IPC plugin: xbot spawns this binary and drives it via
// JSON-over-stdio (protocol.Run). The web panel calls
// ctx.rpc.call('xbot.ssh-runner.<method>', params), which the host routes to
// handleWebPluginRPC. Everything this plugin does crosses the wire as SSH
// commands executed by the local ssh(1) client.
//
// Division of labour: this plugin is the CONTROL PLANE only — probe a machine,
// install/download/start xbot-runner there, report status. The DATA PLANE (the
// remote runner connecting back to the server over its own WebSocket protocol
// and serving tool execution) is NOT this plugin's job.
//
// Methods:
//   - probe        — read-only environment detection for an SSH target
//   - provision    — async install + service setup; returns {job_id} immediately
//   - job_status   — poll a provision/deprovision job (never blocks)
//   - deprovision  — async stop service (+ optional uninstall)
//   - status       — sync service/binary status (read-only)
//   - logs         — sync recent service logs (read-only)
//
// SSH credential policy: the `ssh` parameter is an opaque command prefix the
// user typed (e.g. `ssh -i ~/.ssh/k user@host -p 2222`). It is passed verbatim
// to the local ssh client (never persisted, never sent anywhere else) and it is
// always masked to "<prog> <user@host> <redacted>" in logs and step details.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-pivot/xbot/plugin/protocol"
)

func main() {
	protocol.Run(&protocol.Handler{
		Activate: func(params *protocol.ActivateParams) (*protocol.ActivateResult, error) {
			// Re-arm supervised pipes so a plugin/server restart heals itself.
			defaultService.resumeSupervised()
			return &protocol.ActivateResult{Result: "ok"}, nil
		},
		Deactivate: func() {
			// Host is unloading us: close every pipe (and kill the remote runners)
			// instead of leaving orphaned ssh children behind.
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			defaultService.sups.StopAll(ctx)
		},
		WebPluginRPC: handleWebPluginRPC,
	})
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// defaultDownloadBase mirrors plugin.json's contributes.configuration default.
	defaultDownloadBase = "https://github.com/ai-pivot/xbot/releases/latest/download"
	defaultInstallDir   = "/usr/local/bin"

	// sshProbeTimeout bounds read-only probe calls (connect + a few commands).
	sshProbeTimeout = 15 * time.Second
	// sshDownloadTimeout bounds the download step (binary + checksums.txt).
	sshDownloadTimeout = 300 * time.Second
	// sshCommandTimeout bounds every other provisioning/deprovisioning command.
	// These all run inside async jobs, so they are not bound by the host RPC cap.
	sshCommandTimeout = 60 * time.Second
	// sshSyncTimeout bounds the SYNCHRONOUS RPCs (status/logs). The host kills
	// the plugin process when a plugin call exceeds its 30s timeout
	// (plugin/runtime.go pluginCallTimeout), so synchronous calls must stay
	// well below that budget.
	sshSyncTimeout = 20 * time.Second

	defaultLogLines = 200
	maxLogLines     = 2000

	// maxJobs caps the in-memory job table; finished jobs are pruned first.
	maxJobs = 128

	// jobStepMarker is prepended to every remote script so step routing is
	// observable (used by tests, and by humans reading plugin logs).
	jobStepMarker = "# xbot-ssh-runner:step="

	runnerAssetPrefix = "xbot-runner"
)

var (
	pluginLog = log.New(os.Stderr, "[xbot.ssh-runner] ", log.LstdFlags)

	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func logf(format string, args ...any) { pluginLog.Printf(format, args...) }

// ---------------------------------------------------------------------------
// RPC entry
// ---------------------------------------------------------------------------

// defaultService is the process-wide service instance used by the real plugin.
var defaultService = newService(executeSSH)

// handleWebPluginRPC is the protocol entry point (wired in main). It delegates
// to a service so tests can substitute the SSH executor.
func handleWebPluginRPC(p *protocol.WebPluginRPCParams) (*protocol.WebPluginRPCResult, error) {
	return defaultService.handleRPC(p)
}

type service struct {
	exec  execFunc
	jobs  *jobStore
	sups  *supervisorManager
	state *stateStore
}

func newService(exec execFunc) *service {
	return &service{
		exec:  exec,
		jobs:  newJobStore(),
		sups:  newSupervisorManager(exec),
		state: newStateStore(defaultStatePath()),
	}
}

// resumeSupervised re-arms the pipes that were flagged auto_connect.
//
// The pipes live in this process, so a plugin restart (server restart, or the
// host killing us after a 30s RPC overrun) drops every connection. Re-arming on
// activation is what makes "every connection automatically opens an SSH pipe"
// hold without requiring the UI to be open.
func (s *service) resumeSupervised() {
	if err := s.state.Load(); err != nil {
		logf("supervision state load failed: %v", err)
		return
	}
	targets := s.state.AutoConnectTargets()
	if len(targets) == 0 {
		return
	}
	logf("resuming %d supervised connection(s)", len(targets))
	for _, t := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := s.sups.Connect(ctx, targetSpec{
			SSHField:   t.SSH,
			Name:       t.Name,
			ConnectCmd: t.ConnectCmd,
			InstallDir: t.InstallDir,
			ConnMode:   t.ConnMode,
		})
		cancel()
		if err != nil {
			// Non-fatal: the target stays in the state file so the next activation
			// (or an explicit connect from the UI) retries.
			logf("auto-connect %q failed: %v", t.Name, err)
			continue
		}
		logf("auto-connected %q", t.Name)
	}
}

func (s *service) handleRPC(p *protocol.WebPluginRPCParams) (*protocol.WebPluginRPCResult, error) {
	if p.Method == "" {
		return rpcErr("method is required"), nil
	}
	var params map[string]any
	if len(p.Params) > 0 {
		if err := json.Unmarshal(p.Params, &params); err != nil {
			return rpcErr("invalid params: " + err.Error()), nil
		}
	}
	switch p.Method {
	case "probe":
		return s.handleProbe(params)
	case "provision":
		return s.handleProvision(params)
	case "connect":
		return s.handleConnect(params)
	case "disconnect":
		return s.handleDisconnect(params)
	case "job_status":
		return s.handleJobStatus(params)
	case "deprovision":
		return s.handleDeprovision(params)
	case "status":
		return s.handleStatus(params)
	case "logs":
		return s.handleLogs(params)
	default:
		return rpcErr(fmt.Sprintf("unknown method: %s", p.Method)), nil
	}
}

// handleConnect establishes (or re-establishes) the SSH-supervised runner for a
// target. It is synchronous but returns immediately: the supervisor arms itself
// and owns the long-lived SSH session in the background (an RPC cannot hold the
// session open — the host kills the plugin process after 30s).
//
// Semantics: kill-old-then-start. A reconnect therefore never leaves two
// runners competing for the same registry slot.
func (s *service) handleConnect(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	sshField := strParam(params, "ssh")
	if sshField == "" {
		return rpcErr(`ssh is required (e.g. "ssh user@host")`), nil
	}
	name := strParam(params, "name")
	if err := validateTargetName(name); err != nil {
		return rpcErr(err.Error()), nil
	}
	connectCmd := strParam(params, "connect_cmd")
	if strings.TrimSpace(connectCmd) == "" {
		return rpcErr("connect_cmd is required (from runner_create / RunnerConnectCmd)"), nil
	}
	installDir := strParam(params, "install_dir")
	if installDir == "" {
		installDir = "/usr/local/bin"
	}
	mode := strParam(params, "connection_mode")
	if mode == "" {
		mode = connModeTunnel
	}
	autoConnect := boolParam(params, "auto_connect")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := s.sups.Connect(ctx, targetSpec{
		SSHField:   sshField,
		Name:       name,
		ConnectCmd: connectCmd,
		InstallDir: installDir,
		ConnMode:   mode,
	})
	if err != nil {
		return rpcErr(fmt.Sprintf("connect %s: %v", name, err)), nil
	}
	// Remember the target so a plugin/server restart can re-arm it (self-healing
	// "every connection automatically opens an SSH pipe"). Opt-in via auto_connect.
	if sErr := s.state.Put(supervisedTarget{
		SSH:         sshField,
		Name:        name,
		ConnectCmd:  connectCmd,
		InstallDir:  installDir,
		ConnMode:    st.Mode,
		AutoConnect: autoConnect,
	}); sErr != nil {
		// The connection is up; failing to persist only affects self-healing.
		logf("persist supervision state for %q failed: %v", name, sErr)
	}
	logf("connect target=%q ssh=%s mode=%s auto_connect=%v", name, maskSSH(sshField), st.Mode, autoConnect)
	return rpcOK(st), nil
}

// handleDisconnect tears the pipe down: cancel the supervisor and kill the
// remote runner (the supervisor also kills on every reconnect, so this is the
// explicit "stop" path).
func (s *service) handleDisconnect(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	sshField := strParam(params, "ssh")
	name := strParam(params, "name")
	if err := validateTargetName(name); err != nil {
		return rpcErr(err.Error()), nil
	}
	if s.sups.StatusOf(name).Mode == "" && sshField == "" {
		return rpcErr(`ssh is required to kill a runner this process does not supervise`), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.sups.Stop(ctx, name)
	if sErr := s.state.Delete(name); sErr != nil {
		logf("clear supervision state for %q failed: %v", name, sErr)
	}
	logf("disconnect target=%q", name)
	return rpcOK(map[string]any{"connected": false}), nil
}

func rpcErr(msg string) *protocol.WebPluginRPCResult {
	return &protocol.WebPluginRPCResult{Error: msg}
}

func rpcOK(v any) *protocol.WebPluginRPCResult {
	data, err := json.Marshal(v)
	if err != nil {
		return &protocol.WebPluginRPCResult{Error: err.Error()}
	}
	return &protocol.WebPluginRPCResult{Result: string(data)}
}

// ---------------------------------------------------------------------------
// Param helpers
// ---------------------------------------------------------------------------

func strParam(params map[string]any, key string) string {
	v, _ := params[key].(string)
	return strings.TrimSpace(v)
}

func boolParam(params map[string]any, key string) bool {
	v, _ := params[key].(bool)
	return v
}

func intParam(params map[string]any, key string, def int) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	case int:
		return v
	}
	return def
}

func validateTargetName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: use letters, digits, '.', '-' or '_' (max 64 chars, must start alphanumeric)", name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SSH execution
// ---------------------------------------------------------------------------

// execFunc runs one remote script over the user-supplied ssh prefix and returns
// its combined stdout+stderr.
type execFunc func(ctx context.Context, sshField string, timeout time.Duration, script string) (string, error)

// defaultSSHOptions are injected unless the user already supplied the same
// option key (checked case-insensitively below).
var defaultSSHOptions = [][2]string{
	{"BatchMode", "yes"},                    // never hang on a password prompt
	{"ConnectTimeout", "10"},                // fail fast on unreachable hosts
	{"StrictHostKeyChecking", "accept-new"}, // no interactive host key prompt
}

// buildSSHArgv assembles the local ssh argv:
//
//	<prog> [<missing default -o options>] <user args...> <script>
//
// The defaults are inserted directly after the program name (i.e. before the
// user's arguments) so they are always parsed as ssh options even when the user
// wrote the host first (e.g. `ssh user@host -p 2222`). The user's own argument
// order is preserved verbatim and the script is always the final argv element.
func buildSSHArgv(sshField, script string) ([]string, error) {
	parts := strings.Fields(sshField)
	if len(parts) == 0 {
		return nil, errors.New(`ssh command is required (e.g. "ssh user@host")`)
	}
	if script == "" {
		return nil, errors.New("remote script is required")
	}
	// Collect -o option keys the user already supplied (both "-o K=V" and
	// "-oK=V" spellings).
	present := map[string]bool{}
	for i := 1; i < len(parts); i++ {
		tok := parts[i]
		var kv string
		switch {
		case tok == "-o" && i+1 < len(parts):
			kv = parts[i+1]
			i++
		case strings.HasPrefix(tok, "-o") && len(tok) > 2:
			kv = tok[2:]
		default:
			continue
		}
		key := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			key = kv[:idx]
		}
		if key != "" {
			present[strings.ToLower(key)] = true
		}
	}
	argv := make([]string, 0, len(parts)+2*len(defaultSSHOptions)+1)
	argv = append(argv, parts[0])
	for _, opt := range defaultSSHOptions {
		if present[strings.ToLower(opt[0])] {
			continue
		}
		argv = append(argv, "-o", opt[0]+"="+opt[1])
	}
	argv = append(argv, parts[1:]...)
	argv = append(argv, script)
	return argv, nil
}

// maskSSH redacts everything but the program and host of an ssh command prefix.
// It is deliberately a heuristic (not a full ssh option parser): it only needs
// to be right for the flags users actually write, and being conservative means
// leaking at most the program name + a non-secret token.
func maskSSH(field string) string {
	parts := strings.Fields(field)
	if len(parts) == 0 {
		return "(empty ssh command)"
	}
	host := ""
	for i := 1; i < len(parts); i++ {
		tok := parts[i]
		if strings.HasPrefix(tok, "-") {
			if sshFlagTakesArg(tok) && i+1 < len(parts) {
				i++
			}
			continue
		}
		host = tok
		break
	}
	if host == "" {
		return parts[0] + " <redacted>"
	}
	return parts[0] + " " + host + " <redacted>"
}

// sshFlagsWithArg lists ssh(1) short options that consume a separate argument.
const sshFlagsWithArg = "BbcDEeFIiJLlmOopQRSWw"

func sshFlagTakesArg(tok string) bool {
	if len(tok) != 2 || tok[0] != '-' {
		return false
	}
	return strings.IndexByte(sshFlagsWithArg, tok[1]) >= 0
}

// executeSSH runs one script over ssh. The script is passed as a single argv
// element; the remote login shell executes it.
func executeSSH(ctx context.Context, sshField string, timeout time.Duration, script string) (string, error) {
	argv, err := buildSSHArgv(sshField, script)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	out := buf.String()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("ssh command timed out after %s: %s", timeout, summarizeOutput(out))
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return out, fmt.Errorf("remote command failed (exit %d): %s", exitErr.ExitCode(), summarizeOutput(out))
		}
		return out, fmt.Errorf("ssh execution failed: %w", runErr)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// probe
// ---------------------------------------------------------------------------

type probeResult struct {
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	User             string `json:"user"`
	IsRoot           bool   `json:"is_root"`
	HasSystemd       bool   `json:"has_systemd"`
	HasCurl          bool   `json:"has_curl"`
	HasWget          bool   `json:"has_wget"`
	InstalledVersion string `json:"installed_version"`
	InstallDir       string `json:"install_dir"`
}

func (s *service) handleProbe(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	sshField := strParam(params, "ssh")
	if sshField == "" {
		return rpcErr(`ssh is required (e.g. "ssh user@host")`), nil
	}
	// install_dir is an optional extra hint (the frontend may know the target's
	// configured install dir); probe only uses it to look for the binary.
	installDir := strParam(params, "install_dir")
	out, err := s.exec(context.Background(), sshField, sshProbeTimeout, probeScript(installDir))
	if err != nil {
		return rpcErr(fmt.Sprintf("probe %s failed: %v", maskSSH(sshField), err)), nil
	}
	kv := parseKeyValueLines(out)
	osName := strings.TrimSpace(kv["OS"])
	archName := strings.TrimSpace(kv["ARCH"])
	if plat, perr := normalizePlatform(osName, archName); perr == nil {
		osName, archName = plat.OS, plat.Arch
	}
	res := probeResult{
		OS:               osName,
		Arch:             archName,
		User:             strings.TrimSpace(kv["USER"]),
		IsRoot:           strings.TrimSpace(kv["UID"]) == "0",
		HasSystemd:       strings.TrimSpace(kv["SYSTEMCTL"]) != "",
		HasCurl:          strings.TrimSpace(kv["CURL"]) != "",
		HasWget:          strings.TrimSpace(kv["WGET"]) != "",
		InstalledVersion: strings.TrimSpace(kv["VERSION"]),
	}
	if bin := strings.TrimSpace(kv["BIN"]); bin != "" {
		res.InstallDir = dirOf(bin)
	}
	return rpcOK(res), nil
}

// ---------------------------------------------------------------------------
// provision
// ---------------------------------------------------------------------------

type provisionParams struct {
	SSH          string
	Name         string
	ConnectCmd   string
	DownloadBase string
	InstallDir   string
	DryRun       bool
}

func parseProvisionParams(params map[string]any) (provisionParams, error) {
	p := provisionParams{
		SSH:          strParam(params, "ssh"),
		Name:         strParam(params, "name"),
		ConnectCmd:   strParam(params, "connect_cmd"),
		DownloadBase: strParam(params, "download_base"),
		InstallDir:   strParam(params, "install_dir"),
		DryRun:       boolParam(params, "dry_run"),
	}
	if p.SSH == "" {
		return p, errors.New(`ssh is required (e.g. "ssh user@host")`)
	}
	if err := validateTargetName(p.Name); err != nil {
		return p, err
	}
	if p.ConnectCmd == "" {
		return p, errors.New(`connect_cmd is required (e.g. "--server ws://host:8082/ws --token <token>")`)
	}
	if p.DownloadBase == "" {
		p.DownloadBase = defaultDownloadBase
	}
	p.DownloadBase = strings.TrimRight(p.DownloadBase, "/")
	if p.InstallDir == "" {
		p.InstallDir = defaultInstallDir
	}
	return p, nil
}

func (s *service) handleProvision(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	p, err := parseProvisionParams(params)
	if err != nil {
		return rpcErr(err.Error()), nil
	}
	job := s.jobs.create("provision", p.Name)
	logf("provision job=%s target=%q ssh=%s dry_run=%v", job.id, p.Name, maskSSH(p.SSH), p.DryRun)
	// Async: the host kills the plugin process when a single RPC exceeds 30s,
	// so anything longer than a validation must run in the background. All
	// state is surfaced via job_status.
	go s.runProvision(job.id, p)
	return rpcOK(map[string]string{"job_id": job.id}), nil
}

func (s *service) runProvision(jobID string, p provisionParams) {
	ctx := context.Background()
	fail := func(step string, err error) {
		s.jobs.addStep(jobID, step, false, err.Error())
		s.jobs.finish(jobID, err)
		logf("provision job=%s failed at %s: %v", jobID, step, err)
	}

	// 1. detect platform.
	out, err := s.exec(ctx, p.SSH, sshCommandTimeout, detectScript())
	if err != nil {
		fail("detect", fmt.Errorf("platform detection failed: %v", err))
		return
	}
	kv := parseKeyValueLines(out)
	plat, err := normalizePlatform(kv["OS"], kv["ARCH"])
	if err != nil {
		fail("detect", err)
		return
	}
	isRoot := strings.TrimSpace(kv["UID"]) == "0"
	s.jobs.addStep(jobID, "detect", true,
		fmt.Sprintf("%s/%s (root=%v)", plat.OS, plat.Arch, isRoot))

	// 2. choose install dir (non-root fallback: ~/.local/bin).
	out, err = s.exec(ctx, p.SSH, sshCommandTimeout, prepareDirScript(p.InstallDir, p.DryRun))
	if err != nil {
		fail("prepare-dir", fmt.Errorf("prepare install directory failed: %v", err))
		return
	}
	kv = parseKeyValueLines(out)
	installDir := strings.TrimSpace(kv["INSTALL_DIR"])
	if installDir == "" {
		fail("prepare-dir", errors.New("remote could not choose an install directory"))
		return
	}
	dirDetail := installDir
	if why := strings.TrimSpace(kv["INSTALL_DIR_WHY"]); why != "" && why != "requested" {
		dirDetail += " (" + why + ")"
	}
	s.jobs.addStep(jobID, "prepare-dir", true, dirDetail)

	if p.DryRun {
		s.planProvision(jobID, p, plat, installDir)
		return
	}

	// 3. download runner binary + checksums.txt.
	out, err = s.exec(ctx, p.SSH, sshDownloadTimeout, downloadScript(p.DownloadBase, plat.Asset, p.Name, installDir))
	if err != nil {
		fail("download", fmt.Errorf("download runner artifacts failed: %v", err))
		return
	}
	kv = parseKeyValueLines(out)
	binRemote := strings.TrimSpace(kv["BIN_PATH"])
	tmpDir := strings.TrimSpace(kv["TMP_DIR"])
	downloader := strings.TrimSpace(kv["DOWNLOADER"])
	if binRemote == "" || tmpDir == "" {
		fail("download", errors.New("download step did not report artifact paths"))
		return
	}
	checksums, ok := extractBetween(out, "CHECKSUMS_BEGIN=", "CHECKSUMS_END")
	if !ok {
		fail("download", errors.New("checksums.txt content missing from download output"))
		return
	}
	s.jobs.addStep(jobID, "download", true,
		fmt.Sprintf("downloaded %s via %s", plat.Asset, orDefault(downloader, "unknown downloader")))

	// 4. sha256 verification (checksum extracted locally, verified remotely).
	expected, err := parseChecksums(checksums, plat.Asset)
	if err != nil {
		fail("verify", err)
		return
	}
	out, err = s.exec(ctx, p.SSH, sshCommandTimeout, verifyScript(binRemote, expected))
	if err != nil {
		fail("verify", fmt.Errorf("sha256 verification failed: %v", err))
		return
	}
	s.jobs.addStep(jobID, "verify", true, "sha256 ok: "+clipRunes(expected, 12)+"…")

	// 5. kill any previous runner process for this name (kill-old-first: a runner
	// must never survive a re-provision, or two processes would fight over the
	// same registry slot).
	out, err = s.exec(ctx, p.SSH, sshCommandTimeout, markScript("kill-old", killRunnerScript(p.Name)))
	if err != nil {
		fail("kill-old", fmt.Errorf("kill previous runner failed: %v", err))
		return
	}
	s.jobs.addStep(jobID, "kill-old", true, "ensured no stale xbot-runner for this name is running")

	// 6. install (atomic replace: download dir lives on the same filesystem, so
	// mv is a rename — this also avoids ETXTBSY when the old binary is running).
	out, err = s.exec(ctx, p.SSH, sshCommandTimeout, installScript(binRemote, installDir, tmpDir))
	if err != nil {
		fail("install", fmt.Errorf("install binary failed: %v", err))
		return
	}
	kv = parseKeyValueLines(out)
	installedBin := strings.TrimSpace(kv["INSTALLED_BIN"])
	installedVer := strings.TrimSpace(kv["INSTALLED_VERSION"])
	if installedBin == "" {
		fail("install", errors.New("remote did not confirm the installed binary path"))
		return
	}
	installDetail := installedBin
	if installedVer != "" {
		installDetail += " (version: " + installedVer + ")"
	}
	s.jobs.addStep(jobID, "install", true, installDetail)

	// 7. done — install only.
	//
	// The runner is deliberately NOT started here. Connections are established by
	// `connect`, which runs the runner in the FOREGROUND of an SSH session (VS Code
	// Remote model): the pipe owns the runner's lifetime, and every (re)connect
	// kills the previous runner before starting a new one. Provisioning therefore
	// leaves no resident state behind and stays idempotent.
	s.jobs.addStep(jobID, "ready", true,
		"installed "+installedBin+"; call connect to start it over an SSH session")
	s.jobs.finish(jobID, nil)
	logf("provision job=%s done (install only): target=%q bin=%s", jobID, p.Name, installedBin)
}

// planProvision records the dry-run plan (no writes are performed).
func (s *service) planProvision(jobID string, p provisionParams, plat platform, installDir string) {
	plan := func(name, detail string) { s.jobs.addStep(jobID, name, true, "dry-run: "+detail) }
	plan("download", fmt.Sprintf("would download %s/%s and %s/checksums.txt", p.DownloadBase, plat.Asset, p.DownloadBase))
	plan("verify", "would verify sha256("+plat.Asset+") against checksums.txt")
	plan("kill-old", "would kill any running xbot-runner for name "+p.Name)
	plan("install", "would atomically install to "+strings.TrimRight(installDir, "/")+"/xbot-runner")
	plan("ready", "would be ready; the runner itself is started later by `connect` over an SSH session")
	s.jobs.finish(jobID, nil)
	logf("provision job=%s done (dry-run): target=%q", jobID, p.Name)
}

// ---------------------------------------------------------------------------
// job_status
// ---------------------------------------------------------------------------

func (s *service) handleJobStatus(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	id := strParam(params, "job_id")
	if id == "" {
		return rpcErr("job_id is required"), nil
	}
	snap, ok := s.jobs.snapshot(id)
	if !ok {
		return rpcErr("unknown job: " + id), nil
	}
	return rpcOK(snap), nil
}

// ---------------------------------------------------------------------------
// deprovision
// ---------------------------------------------------------------------------

func (s *service) handleDeprovision(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	sshField := strParam(params, "ssh")
	if sshField == "" {
		return rpcErr(`ssh is required (e.g. "ssh user@host")`), nil
	}
	name := strParam(params, "name")
	if err := validateTargetName(name); err != nil {
		return rpcErr(err.Error()), nil
	}
	uninstall := boolParam(params, "uninstall")
	job := s.jobs.create("deprovision", name)
	logf("deprovision job=%s target=%q ssh=%s uninstall=%v", job.id, name, maskSSH(sshField), uninstall)
	go s.runDeprovision(job.id, sshField, name, uninstall)
	return rpcOK(map[string]string{"job_id": job.id}), nil
}

func (s *service) runDeprovision(jobID, sshField, name string, uninstall bool) {
	ctx := context.Background()
	fail := func(step string, err error) {
		s.jobs.addStep(jobID, step, false, err.Error())
		s.jobs.finish(jobID, err)
		logf("deprovision job=%s failed at %s: %v", jobID, step, err)
	}

	// Tear the pipe down first: the runner lives inside our SSH session, so
	// stopping the supervisor + killing the remote process is what actually ends
	// it (there is no resident service to stop any more).
	s.sups.Stop(ctx, name)
	s.jobs.addStep(jobID, "disconnect", true, "SSH pipe closed and remote runner killed")

	out, err := s.exec(ctx, sshField, sshCommandTimeout, deprovisionStopScript(name))
	if err != nil {
		fail("stop", fmt.Errorf("stop service failed: %v", err))
		return
	}
	kv := parseKeyValueLines(out)
	stopped := strings.TrimSpace(kv["STOPPED"])
	if stopped == "" {
		stopped = "no leftover process found"
	}
	s.jobs.addStep(jobID, "stop", true, stopped)

	out, err = s.exec(ctx, sshField, sshCommandTimeout, deprovisionCleanupScript(name, uninstall))
	if err != nil {
		fail("cleanup", fmt.Errorf("cleanup failed: %v", err))
		return
	}
	kv = parseKeyValueLines(out)
	removed := strings.TrimSpace(kv["REMOVED"])
	if removed == "" {
		removed = "nothing to remove"
	}
	s.jobs.addStep(jobID, "cleanup", true, removed)

	if uninstall {
		out, err = s.exec(ctx, sshField, sshCommandTimeout, removeBinaryScript())
		if err != nil {
			fail("remove-binary", fmt.Errorf("remove binary failed: %v", err))
			return
		}
		kv = parseKeyValueLines(out)
		bin := strings.TrimSpace(kv["BIN_REMOVED"])
		if bin == "" {
			bin = "binary not found (already removed?)"
		}
		s.jobs.addStep(jobID, "remove-binary", true, bin)
	}
	s.jobs.finish(jobID, nil)
	logf("deprovision job=%s done: target=%q uninstall=%v", jobID, name, uninstall)
}

// ---------------------------------------------------------------------------
// status / logs
// ---------------------------------------------------------------------------

func (s *service) handleStatus(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	sshField := strParam(params, "ssh")
	if sshField == "" {
		return rpcErr(`ssh is required (e.g. "ssh user@host")`), nil
	}
	name := strParam(params, "name")
	if err := validateTargetName(name); err != nil {
		return rpcErr(err.Error()), nil
	}
	out, err := s.exec(context.Background(), sshField, sshSyncTimeout, statusScript(name))
	if err != nil {
		return rpcErr(fmt.Sprintf("status %s failed: %v", maskSSH(sshField), err)), nil
	}
	kv := parseKeyValueLines(out)
	detail := strings.TrimSpace(kv["STATUS_DETAIL"])
	if bin := strings.TrimSpace(kv["STATUS_BIN"]); bin != "" {
		if detail != "" {
			detail = "binary=" + bin + "; " + detail
		} else {
			detail = "binary=" + bin
		}
	}

	// The authoritative connection state is the supervisor's: the runner lives in
	// the foreground of our SSH session, so the remote has no service to inspect.
	sup := s.sups.StatusOf(name)
	state := "disconnected"
	switch {
	case sup.Connected:
		state = "connected"
	case sup.Mode != "":
		state = "reconnecting"
	}
	if sup.RemotePort > 0 {
		detail = fmt.Sprintf("tunnel 127.0.0.1:%d; %s", sup.RemotePort, detail)
	}
	if sup.LastError != "" {
		detail = strings.TrimSpace(detail + "; last error: " + clipRunes(sup.LastError, 200))
	}

	return rpcOK(map[string]any{
		"installed_version": strings.TrimSpace(kv["STATUS_VERSION"]),
		"service_state":     state,
		"detail":            detail,
		"connected":         sup.Connected,
		"connection_mode":   sup.Mode,
		"restarts":          sup.Restarts,
		"connected_at":      sup.ConnectedAt,
		"remote_port":       sup.RemotePort,
		"last_error":        sup.LastError,
	}), nil
}

func (s *service) handleLogs(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	sshField := strParam(params, "ssh")
	if sshField == "" {
		return rpcErr(`ssh is required (e.g. "ssh user@host")`), nil
	}
	name := strParam(params, "name")
	if err := validateTargetName(name); err != nil {
		return rpcErr(err.Error()), nil
	}
	lines := intParam(params, "lines", defaultLogLines)
	if lines <= 0 {
		lines = defaultLogLines
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}

	// With the SSH-pipe model the runner's output IS the session's output, so the
	// supervisor's ring buffer is the authoritative log source.
	if sup, ok := s.sups.get(name); ok {
		if buf := sup.Tail(lines); len(buf) > 0 {
			return rpcOK(map[string]any{"lines": buf, "source": "ssh-session"}), nil
		}
	}

	// Nothing captured yet (never connected in this process): fall back to a log
	// file left behind by an older installation, if any.
	out, err := s.exec(context.Background(), sshField, sshSyncTimeout, logsScript(name, lines))
	if err != nil {
		return rpcErr(fmt.Sprintf("logs %s failed: %v", maskSSH(sshField), err)), nil
	}
	return rpcOK(map[string]any{"lines": splitLines(out), "source": "remote-log"}), nil
}

// ---------------------------------------------------------------------------
// Remote scripts
// ---------------------------------------------------------------------------

// markScript tags a remote script with its step name (comment line, harmless).
func markScript(step, body string) string {
	return jobStepMarker + step + "\n" + body + "\n"
}

// shellQuote single-quotes a value for POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const probeScriptTemplate = `printf 'OS=%s\n' "$(uname -s 2>/dev/null)"
printf 'ARCH=%s\n' "$(uname -m 2>/dev/null)"
printf 'USER=%s\n' "$(id -un 2>/dev/null)"
printf 'UID=%s\n' "$(id -u 2>/dev/null)"
printf 'SYSTEMCTL=%s\n' "$(command -v systemctl 2>/dev/null || true)"
printf 'CURL=%s\n' "$(command -v curl 2>/dev/null || true)"
printf 'WGET=%s\n' "$(command -v wget 2>/dev/null || true)"
BIN=""
for cand in __XBOT_PROBE_CANDIDATES__; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then BIN="$cand"; break; fi
done
if [ -z "$BIN" ]; then BIN="$(command -v xbot-runner 2>/dev/null || true)"; fi
printf 'BIN=%s\n' "$BIN"
VER=""
if [ -n "$BIN" ]; then VER="$("$BIN" --version 2>/dev/null | head -n1)"; fi
printf 'VERSION=%s\n' "$VER"`

// probeScript is read-only: it never creates or modifies anything remotely.
func probeScript(installDir string) string {
	cands := []string{}
	if trimmed := strings.TrimRight(installDir, "/"); trimmed != "" {
		cands = append(cands, shellQuote(trimmed+"/xbot-runner"))
	}
	cands = append(cands, `"$HOME/.local/bin/xbot-runner"`, `"/usr/local/bin/xbot-runner"`)
	// strings.Replace (not Sprintf): the template carries printf '%s' verbs of
	// its own, so format verbs would collide with them.
	body := strings.Replace(probeScriptTemplate, "__XBOT_PROBE_CANDIDATES__", strings.Join(cands, " "), 1)
	return markScript("probe", body)
}

func detectScript() string {
	return markScript("detect", `printf 'OS=%s\n' "$(uname -s 2>/dev/null)"
printf 'ARCH=%s\n' "$(uname -m 2>/dev/null)"
printf 'UID=%s\n' "$(id -u 2>/dev/null)"
printf 'SYSTEMCTL=%s\n' "$(command -v systemctl 2>/dev/null || true)"`)
}

// prepareDirScript resolves the effective install dir. Non-root users fall
// back to ~/.local/bin when the requested dir is not writable; in dry-run mode
// nothing is created.
func prepareDirScript(installDir string, dryRun bool) string {
	if dryRun {
		body := "WANT=" + shellQuote(installDir) + `
CHOOSE="$WANT"
WHY="requested"
if [ "$(id -u 2>/dev/null)" != "0" ]; then
  if [ ! -d "$WANT" ] || [ ! -w "$WANT" ]; then
    CHOOSE="$HOME/.local/bin"
    WHY="install_dir not writable as non-root; would use ~/.local/bin"
  fi
fi
printf 'INSTALL_DIR=%s\n' "$CHOOSE"
printf 'INSTALL_DIR_WHY=%s\n' "$WHY"`
		return markScript("prepare-dir", body)
	}
	body := "WANT=" + shellQuote(installDir) + `
CHOOSE="$WANT"
WHY="requested"
if [ "$(id -u 2>/dev/null)" != "0" ]; then
  mkdir -p "$WANT" 2>/dev/null || true
  if [ ! -w "$WANT" ]; then
    CHOOSE="$HOME/.local/bin"
    WHY="install_dir not writable as non-root; using ~/.local/bin"
  fi
fi
mkdir -p "$CHOOSE" 2>/dev/null || true
if [ ! -w "$CHOOSE" ]; then
  printf 'INSTALL_DIR_ERROR=cannot write to %s\n' "$CHOOSE"
  exit 11
fi
printf 'INSTALL_DIR=%s\n' "$CHOOSE"
printf 'INSTALL_DIR_WHY=%s\n' "$WHY"`
	return markScript("prepare-dir", body)
}

// downloadScript downloads the runner binary + checksums.txt into a temp dir
// on the SAME filesystem as the install dir (so the later replace is a rename).
// Downloader fallback chain: curl → wget → python3.
func downloadScript(downloadBase, asset, name, installDir string) string {
	base := shellQuote(downloadBase)
	tmp := shellQuote(strings.TrimRight(installDir, "/") + "/.xbot-runner-dl-" + name)
	body := "ASSET=" + shellQuote(asset) + "\n" +
		"BASE=" + base + "\n" +
		"TMP=" + tmp + "\n" + `
rm -rf "$TMP"
mkdir -p "$TMP" || { printf 'DOWNLOAD_ERROR=cannot create %s\n' "$TMP"; exit 12; }
DL=""
fetch() {
  if command -v curl >/dev/null 2>&1; then
    DL=curl
    curl -fsSL --connect-timeout 15 --max-time 280 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    DL=wget
    wget -q --timeout=60 -O "$2" "$1"
  elif command -v python3 >/dev/null 2>&1; then
    DL=python3
    python3 -c 'import sys,urllib.request; urllib.request.urlretrieve(sys.argv[1], sys.argv[2])' "$1" "$2"
  else
    printf 'DOWNLOAD_ERROR=no downloader available (need curl, wget or python3)\n'
    return 1
  fi
}
fetch "$BASE/$ASSET" "$TMP/$ASSET" || { printf 'DOWNLOAD_ERROR=failed to download %s\n' "$BASE/$ASSET"; exit 13; }
fetch "$BASE/checksums.txt" "$TMP/checksums.txt" || { printf 'DOWNLOAD_ERROR=failed to download %s\n' "$BASE/checksums.txt"; exit 14; }
printf 'DOWNLOADER=%s\n' "$DL"
printf 'TMP_DIR=%s\n' "$TMP"
printf 'BIN_PATH=%s\n' "$TMP/$ASSET"
printf 'CHECKSUMS_BEGIN=%s\n' "$TMP/checksums.txt"
cat "$TMP/checksums.txt"
printf 'CHECKSUMS_END\n'`
	return markScript("download", body)
}

func verifyScript(path, expected string) string {
	body := "FILE=" + shellQuote(path) + "\n" +
		"EXPECT=" + shellQuote(expected) + "\n" + `
if [ ! -f "$FILE" ]; then
  printf 'VERIFY_ERROR=file missing: %s\n' "$FILE"
  exit 15
fi
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$FILE" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$FILE" | awk '{print $1}')
else
  printf 'VERIFY_ERROR=no sha256sum or shasum available\n'
  exit 16
fi
if [ "$ACTUAL" != "$EXPECT" ]; then
  printf 'SHA_EXPECT=%s\n' "$EXPECT"
  printf 'SHA_ACTUAL=%s\n' "$ACTUAL"
  printf 'VERIFY_ERROR=sha256 mismatch\n'
  exit 17
fi
printf 'SHA_OK=%s\n' "$EXPECT"`
	return markScript("verify", body)
}

func installScript(src, installDir, tmpDir string) string {
	dest := strings.TrimRight(installDir, "/") + "/xbot-runner"
	body := "SRC=" + shellQuote(src) + "\n" +
		"DEST=" + shellQuote(dest) + "\n" +
		"TMP=" + shellQuote(tmpDir) + "\n" + `
NEW="$DEST.new.$$"
if [ ! -f "$SRC" ]; then
  printf 'INSTALL_ERROR=source missing: %s\n' "$SRC"
  exit 18
fi
if command -v install >/dev/null 2>&1; then
  install -m 0755 "$SRC" "$NEW" || { printf 'INSTALL_ERROR=install failed\n'; exit 19; }
else
  cp "$SRC" "$NEW" && chmod 0755 "$NEW" || { printf 'INSTALL_ERROR=copy failed\n'; exit 20; }
fi
mv -f "$NEW" "$DEST" || { printf 'INSTALL_ERROR=atomic replace failed\n'; exit 21; }
VER="$("$DEST" --version 2>/dev/null | head -n1)"
printf 'INSTALLED_BIN=%s\n' "$DEST"
printf 'INSTALLED_VERSION=%s\n' "$VER"
rm -rf "$TMP"
printf 'CLEANUP=ok\n'`
	return markScript("install", body)
}

func statusScript(name string) string {
	body := "NAME=" + shellQuote(name) + `
BIN=""
for cand in "$HOME/.local/bin/xbot-runner" "/usr/local/bin/xbot-runner"; do
  if [ -x "$cand" ]; then BIN="$cand"; break; fi
done
if [ -z "$BIN" ]; then BIN="$(command -v xbot-runner 2>/dev/null || true)"; fi
VER=""
if [ -n "$BIN" ]; then VER="$("$BIN" --version 2>/dev/null | head -n1)"; fi
# There is no resident service in the SSH-pipe model: report whether a stray
# runner process for this name is still around (that WOULD be a problem, and
# connect/provision kill it).
STRAY="$(pgrep -f "xbot-runner.*--name[= ]$NAME([[:space:]]|$)" 2>/dev/null | head -n1 || true)"
if [ -z "$BIN" ]; then
  printf 'STATUS_STATE=not-installed\n'
  printf 'STATUS_DETAIL=no xbot-runner binary found\n'
  printf 'STATUS_VERSION=\nSTATUS_BIN=\n'
  exit 0
  fi
  if [ -n "$STRAY" ]; then
  printf 'STATUS_DETAIL=stray runner process pid=%s (cleared on next connect)\n' "$STRAY"
  else
  printf 'STATUS_DETAIL=no residual runner process\n'
  fi
  printf 'STATUS_STATE=installed\n'
  printf 'STATUS_VERSION=%s\n' "$VER"
  printf 'STATUS_BIN=%s\n' "$BIN"`
	return markScript("status", body)
}

func logsScript(name string, lines int) string {
	body := "NAME=" + shellQuote(name) + "\n" +
		fmt.Sprintf("N=%d\n", lines) + `
# Fallback only: the authoritative log source is the SSH session's own output
# (source=ssh-session, served from the supervisor's ring buffer). This path
# surfaces what an OLDER resident installation left behind, if anything.
LOG="$HOME/.xbot-runner/$NAME.log"
if [ -f "$LOG" ]; then tail -n "$N" "$LOG" 2>/dev/null || true; fi
UNIT="$HOME/.config/systemd/user/xbot-runner-$NAME.service"
if [ -f "$UNIT" ] && command -v journalctl >/dev/null 2>&1; then
  journalctl --user -u "xbot-runner-$NAME.service" -n "$N" --no-pager 2>/dev/null || true
fi`
	return markScript("logs", body)
}

func deprovisionStopScript(name string) string {
	body := "NAME=" + shellQuote(name) + `
STOPPED=""
# Authoritative path: kill the runner process itself (it lives in an SSH session).
if pkill -f "xbot-runner.*--name[= ]$NAME([[:space:]]|$)" >/dev/null 2>&1; then
  STOPPED="runner process killed"
  sleep 0.3
  pkill -9 -f "xbot-runner.*--name[= ]$NAME([[:space:]]|$)" >/dev/null 2>&1 || true
  fi
  # Leftovers from an older resident-service installation, if any.
  if [ -f "$HOME/.config/systemd/user/xbot-runner-$NAME.service" ] && command -v systemctl >/dev/null 2>&1; then
  systemctl --user disable --now "xbot-runner-$NAME.service" >/dev/null 2>&1 || true
  STOPPED="$STOPPED legacy-unit-disabled"
  fi
PIDFILE="$HOME/.xbot-runner/$NAME.pid"
if [ -f "$PIDFILE" ]; then
  PID="$(cat "$PIDFILE" 2>/dev/null || true)"
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    STOPPED="$STOPPED legacy-pid:$PID killed"
  fi
  rm -f "$PIDFILE" 2>/dev/null || true
  fi
printf 'STOPPED=%s\n' "$STOPPED"`
	return markScript("stop", body)
}

func deprovisionCleanupScript(name string, uninstall bool) string {
	uninstallFlag := "0"
	if uninstall {
		uninstallFlag = "1"
	}
	body := "NAME=" + shellQuote(name) + "\n" +
		"UNINSTALL=" + uninstallFlag + `
REMOVED=""
UNIT="$HOME/.config/systemd/user/xbot-runner-$NAME.service"
if [ -f "$UNIT" ]; then
  rm -f "$UNIT" && REMOVED="unit removed"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user daemon-reload >/dev/null 2>&1 || true
  fi
fi
if [ "$UNINSTALL" = "1" ]; then
  rm -f "$HOME/.xbot-runner/$NAME.pid" "$HOME/.xbot-runner/$NAME.log" 2>/dev/null || true
  REMOVED="$REMOVED data removed"
fi
printf 'REMOVED=%s\n' "$REMOVED"`
	return markScript("cleanup", body)
}

func removeBinaryScript() string {
	return markScript("remove-binary", `REMOVED=""
for cand in "$HOME/.local/bin/xbot-runner" "/usr/local/bin/xbot-runner"; do
  if [ -f "$cand" ]; then
    rm -f "$cand" 2>/dev/null && REMOVED="$cand"
  fi
done
if [ -z "$REMOVED" ]; then
  BIN="$(command -v xbot-runner 2>/dev/null || true)"
  if [ -n "$BIN" ] && [ -f "$BIN" ]; then
    rm -f "$BIN" 2>/dev/null && REMOVED="$BIN"
  fi
fi
printf 'BIN_REMOVED=%s\n' "$REMOVED"`)
}

// ---------------------------------------------------------------------------
// Platform
// ---------------------------------------------------------------------------

type platform struct {
	OS    string
	Arch  string
	Asset string
}

// normalizePlatform maps `uname -s` / `uname -m` output to GOOS/GOARCH and the
// release asset name (xbot-runner-{os}-{arch}).
func normalizePlatform(unameS, unameM string) (platform, error) {
	var p platform
	switch strings.ToLower(strings.TrimSpace(unameS)) {
	case "linux":
		p.OS = "linux"
	case "darwin":
		p.OS = "darwin"
	default:
		return p, fmt.Errorf("unsupported remote OS %q: only linux and darwin are supported", strings.TrimSpace(unameS))
	}
	switch strings.ToLower(strings.TrimSpace(unameM)) {
	case "x86_64", "amd64":
		p.Arch = "amd64"
	case "aarch64", "arm64":
		p.Arch = "arm64"
	default:
		return p, fmt.Errorf("unsupported remote architecture %q: only x86_64 and aarch64/arm64 are supported", strings.TrimSpace(unameM))
	}
	p.Asset = runnerAssetPrefix + "-" + p.OS + "-" + p.Arch
	return p, nil
}

// ---------------------------------------------------------------------------
// checksums.txt parsing
// ---------------------------------------------------------------------------

// parseChecksums extracts the sha256 for filename from checksums.txt content.
// Accepted line shapes: "<sha>  <file>" (sha256sum) and "<sha> *<file>" (binary
// marker). A missing entry is an error — never install unverified bytes.
func parseChecksums(content, filename string) (string, error) {
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name != filename {
			continue
		}
		sha := strings.ToLower(fields[0])
		if !isHexSHA256(sha) {
			return "", fmt.Errorf("checksums.txt entry for %s is not a valid sha256: %q", filename, fields[0])
		}
		return sha, nil
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", filename)
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Job store (concurrency-safe; long work never happens under the lock)
// ---------------------------------------------------------------------------

type jobStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type jobRecord struct {
	id     string
	kind   string
	name   string
	state  string // running | done | failed
	steps  []jobStep
	errMsg string
}

type jobSnapshot struct {
	ID    string    `json:"job_id"`
	Kind  string    `json:"kind"`
	Name  string    `json:"name"`
	State string    `json:"state"`
	Steps []jobStep `json:"steps"`
	Error string    `json:"error"`
}

type jobStore struct {
	mu   sync.Mutex
	jobs map[string]*jobRecord
}

func newJobStore() *jobStore {
	return &jobStore{jobs: map[string]*jobRecord{}}
}

func newJobID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (st *jobStore) create(kind, name string) *jobRecord {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked()
	rec := &jobRecord{id: newJobID(), kind: kind, name: name, state: "running", steps: []jobStep{}}
	st.jobs[rec.id] = rec
	return rec
}

// pruneLocked drops finished jobs once the table is over the cap. Callers hold
// the lock; the new job is not in the map yet, so it can never be pruned.
func (st *jobStore) pruneLocked() {
	for id, rec := range st.jobs {
		if len(st.jobs) <= maxJobs {
			return
		}
		if rec.state != "running" {
			delete(st.jobs, id)
		}
	}
}

func (st *jobStore) addStep(id, name string, ok bool, detail string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	rec := st.jobs[id]
	if rec == nil {
		return
	}
	rec.steps = append(rec.steps, jobStep{Name: name, OK: ok, Detail: detail})
}

func (st *jobStore) finish(id string, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	rec := st.jobs[id]
	if rec == nil {
		return
	}
	if err != nil {
		rec.state = "failed"
		rec.errMsg = err.Error()
	} else {
		rec.state = "done"
	}
}

func (st *jobStore) snapshot(id string) (jobSnapshot, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.jobs[id]
	if !ok {
		return jobSnapshot{}, false
	}
	steps := make([]jobStep, len(rec.steps))
	copy(steps, rec.steps)
	return jobSnapshot{
		ID:    rec.id,
		Kind:  rec.kind,
		Name:  rec.name,
		State: rec.state,
		Steps: steps,
		Error: rec.errMsg,
	}, true
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// parseKeyValueLines parses "KEY=value" lines (unknown lines are ignored).
func parseKeyValueLines(out string) map[string]string {
	m := map[string]string{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		m[line[:idx]] = line[idx+1:]
	}
	return m
}

// extractBetween returns the lines between a line starting with beginKey and a
// line exactly equal to endLine.
func extractBetween(out, beginKey, endLine string) (string, bool) {
	var b strings.Builder
	started := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if !started {
			if strings.HasPrefix(line, beginKey) {
				started = true
			}
			continue
		}
		if strings.TrimSpace(line) == endLine {
			return b.String(), true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return "", false
}

// splitLines returns trimmed-right, non-empty lines (caps at maxLogLines).
func splitLines(out string) []string {
	lines := []string{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= maxLogLines {
			break
		}
	}
	return lines
}

func summarizeOutput(out string) string {
	parts := []string{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		parts = append(parts, clipRunes(line, 200))
		if len(parts) >= 4 {
			break
		}
	}
	if len(parts) == 0 {
		return "no output"
	}
	return strings.Join(parts, "; ")
}

func clipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// dirOf returns the directory of a remote POSIX path ("" when there is none).
func dirOf(p string) string {
	if !strings.Contains(p, "/") {
		return ""
	}
	return path.Dir(p)
}
