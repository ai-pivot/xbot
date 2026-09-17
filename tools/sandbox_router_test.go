package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"xbot/config"
)

// testWSConn returns a real (connected) WebSocket client conn so fixture
// runnerConnections behave like production ones — notably, Close() on a
// zero-value websocket.Conn panics, which would hide bugs behind test-only
// panics.
func testWSConn(t *testing.T) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial test websocket: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Close()
	})
	return conn
}

// ============================================================================
// 测试夹具
//
// 单用户 + 会话级路由：SandboxRouter 只按 session key（"channel:chatID"）解析，
// 不再有 per-user 维度、active runner 或多用户分支。
// ============================================================================

type fakeBindingStore struct{ m map[string]string }

func (f *fakeBindingStore) SetSessionRunner(sessionKey, runnerName string) error {
	if runnerName == "" {
		delete(f.m, sessionKey)
	} else {
		f.m[sessionKey] = runnerName
	}
	return nil
}

func (f *fakeBindingStore) GetSessionRunner(sessionKey string) (string, error) {
	return f.m[sessionKey], nil
}

// newRemoteRouter 构造一个持有若干"已连接"runner 的路由器。
func newRemoteRouter(onlineRunners ...string) (*SandboxRouter, *fakeBindingStore) {
	rs := &RemoteSandbox{
		runners:  map[string]*runnerConnection{},
		versions: map[string]string{},
	}
	for _, name := range onlineRunners {
		rs.runners[name] = &runnerConnection{runnerName: name, workspace: "/workspace", shell: "/bin/bash"}
	}
	store := &fakeBindingStore{m: map[string]string{}}
	r := &SandboxRouter{
		remote:       rs,
		none:         &NoneSandbox{},
		offline:      &OfflineRunnerSandbox{},
		defaultMode:  "remote",
		bindingStore: store,
	}
	rs.sessionRunners = &r.sessionRunners
	rs.bindingStore = store
	return r, store
}

func newNoneRouter() *SandboxRouter {
	return &SandboxRouter{
		none:         &NoneSandbox{},
		offline:      &OfflineRunnerSandbox{},
		defaultMode:  "none",
		bindingStore: &fakeBindingStore{m: map[string]string{}},
	}
}

// ============================================================================
// Name()
// ============================================================================

func TestSandboxRouter_Name(t *testing.T) {
	if got := newNoneRouter().Name(); got != "none" {
		t.Errorf("Name() = %q, want none", got)
	}
	r, _ := newRemoteRouter("m1")
	if got := r.Name(); got != "remote" {
		t.Errorf("Name() = %q, want remote", got)
	}
}

// ============================================================================
// SandboxForSession — 核心路由契约
// ============================================================================

// 未绑定会话 → 本机执行。
func TestSandboxForSession_UnboundRunsLocally(t *testing.T) {
	r, _ := newRemoteRouter("m1")
	for _, key := range []string{"cli:/repo", "", "web:chat_1"} {
		if got := r.SandboxForSession(key).Name(); got != "none" {
			t.Errorf("SandboxForSession(%q).Name() = %q, want none", key, got)
		}
	}
}

// 已绑定且 runner 在线 → 远端执行。
func TestSandboxForSession_BoundOnlineRoutesRemote(t *testing.T) {
	r, store := newRemoteRouter("m1")
	store.m["cli:/repo"] = "m1"
	if got := r.SandboxForSession("cli:/repo").Name(); got != "remote" {
		t.Errorf("bound+online must route remote, got %q", got)
	}
}

// 已绑定但 runner 离线 → 硬失败（绝不静默回退本机）。
func TestSandboxForSession_BoundOfflineFailsLoudly(t *testing.T) {
	r, store := newRemoteRouter("other") // m1 未连接
	store.m["cli:/repo"] = "m1"

	sb := r.SandboxForSession("cli:/repo")
	if sb.Name() == "none" {
		t.Fatal("bound-but-offline must NOT fall back to the local host")
	}
	if sb.Name() != "runner-offline" {
		t.Fatalf("got %q, want runner-offline", sb.Name())
	}
	if _, err := sb.Exec(context.Background(), ExecSpec{Command: "echo"}); err == nil {
		t.Fatal("offline sandbox must refuse to execute")
	} else if !contains(err.Error(), "m1") {
		t.Errorf("error should name the unreachable machine, got: %v", err)
	}
}

// 绑定不存在的机器 → 同样硬失败（不是回退本机）。
func TestSandboxForSession_BoundUnknownRunnerFailsLoudly(t *testing.T) {
	r, store := newRemoteRouter()
	store.m["cli:/repo"] = "ghost"
	if got := r.SandboxForSession("cli:/repo").Name(); got != "runner-offline" {
		t.Fatalf("got %q, want runner-offline", got)
	}
}

// ============================================================================
// 绑定读写（权威 = binding store）
// ============================================================================

func TestSetAndGetSessionRunner(t *testing.T) {
	r, store := newRemoteRouter("m1")

	if err := r.SetSessionRunner("cli:/repo", "m1"); err != nil {
		t.Fatalf("SetSessionRunner: %v", err)
	}
	if got := r.GetSessionRunner("cli:/repo"); got != "m1" {
		t.Errorf("GetSessionRunner = %q, want m1", got)
	}
	if store.m["cli:/repo"] != "m1" {
		t.Error("binding must be persisted to the store (restart survival)")
	}

	// 空名 = 解绑回本机
	if err := r.SetSessionRunner("cli:/repo", ""); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if got := r.GetSessionRunner("cli:/repo"); got != "" {
		t.Errorf("after unbind GetSessionRunner = %q, want empty", got)
	}
	if _, ok := store.m["cli:/repo"]; ok {
		t.Error("unbind must delete the persisted binding")
	}

	// 空 session key 必须报错（否则会写坏 tenants 行）
	if err := r.SetSessionRunner("", "m1"); err == nil {
		t.Error("empty session key must be rejected")
	}
}

// 内存缓存未命中时必须回落到 store（重启后仍能路由）。
func TestGetSessionRunner_BackfillsFromStore(t *testing.T) {
	r, store := newRemoteRouter("m1")
	store.m["cli:/repo"] = "m1" // 只在"DB"里，进程内缓存为空

	if got := r.GetSessionRunner("cli:/repo"); got != "m1" {
		t.Fatalf("GetSessionRunner = %q, want m1 (from store)", got)
	}
	if v, ok := r.sessionRunners.Load("cli:/repo"); !ok || v.(string) != "m1" {
		t.Error("store hit must backfill the in-memory cache")
	}
}

func TestSetSessionRunner_WithoutStoreFails(t *testing.T) {
	r := &SandboxRouter{none: &NoneSandbox{}, offline: &OfflineRunnerSandbox{}}
	if err := r.SetSessionRunner("cli:/repo", "m1"); err == nil {
		t.Error("SetSessionRunner without a binding store must error, not silently succeed")
	}
}

func TestForgetSession(t *testing.T) {
	r, store := newRemoteRouter("m1")
	_ = r.SetSessionRunner("cli:/repo", "m1")
	store.m["cli:/repo"] = "m1"

	r.ForgetSession("cli:/repo")
	if _, ok := r.sessionRunners.Load("cli:/repo"); ok {
		t.Error("ForgetSession must drop the cached binding")
	}
}

// SessionsForRunner 用于 runner 上下线时对绑定会话做副作用（如 ProxyLLM）。
func TestSessionsForRunner(t *testing.T) {
	r, _ := newRemoteRouter("m1", "m2")
	_ = r.SetSessionRunner("cli:/a", "m1")
	_ = r.SetSessionRunner("web:chat_1", "m1")
	_ = r.SetSessionRunner("cli:/b", "m2")

	got := r.SessionsForRunner("m1")
	if len(got) != 2 || got[0] != "cli:/a" || got[1] != "web:chat_1" {
		t.Errorf("SessionsForRunner(m1) = %v, want [cli:/a web:chat_1]", got)
	}
	if n := len(r.SessionsForRunner("m2")); n != 1 {
		t.Errorf("SessionsForRunner(m2) len = %d, want 1", n)
	}
	if n := len(r.SessionsForRunner("")); n != 0 {
		t.Errorf("empty runner name must return nothing, got %d", n)
	}
}

// ============================================================================
// runner 可观测性
// ============================================================================

func TestRunnerOnlineAndVersion(t *testing.T) {
	r, _ := newRemoteRouter("m1", "m2")
	r.remote.versions["m2"] = "0.0.52"

	if !r.IsRunnerOnline("m1") || r.IsRunnerOnline("ghost") {
		t.Error("IsRunnerOnline misreported")
	}
	if got := r.RunnerVersion("m2"); got != "0.0.52" {
		t.Errorf("RunnerVersion = %q, want 0.0.52", got)
	}
	if got := r.RunnerVersion("m1"); got != "" {
		t.Errorf("unknown version must be empty, got %q", got)
	}
	if names := r.OnlineRunnerNames(); len(names) != 2 || names[0] != "m1" || names[1] != "m2" {
		t.Errorf("OnlineRunnerNames = %v, want sorted [m1 m2]", names)
	}
}

func TestDisconnectRunner(t *testing.T) {
	r, _ := newRemoteRouter()
	r.remote.runners["m1"] = &runnerConnection{runnerName: "m1", wsConn: testWSConn(t)}

	if !r.DisconnectRunner("m1") {
		t.Error("DisconnectRunner should report true for a connected runner")
	}
	if r.DisconnectRunner("m1") {
		t.Error("second disconnect must report false")
	}
	if r.IsRunnerOnline("m1") {
		t.Error("runner must be gone after disconnect")
	}
}

// ============================================================================
// 委托方法：无会话身份时必须显式失败（禁止猜机器）
// ============================================================================

func TestRouterDelegation_RefusesWithoutSession(t *testing.T) {
	r, _ := newRemoteRouter("m1")
	ctx := context.Background()

	if _, err := r.Exec(ctx, ExecSpec{Command: "ls"}); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("Exec err = %v, want errSandboxNeedsSession", err)
	}
	if _, err := r.ReadFile(ctx, "/tmp/x", "cli:/repo"); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("ReadFile err = %v, want errSandboxNeedsSession", err)
	}
	if err := r.WriteFile(ctx, "/tmp/x", []byte("x"), os.FileMode(0o644), "cli:/repo"); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("WriteFile err = %v, want errSandboxNeedsSession", err)
	}
	if _, err := r.Stat(ctx, "/tmp/x", "cli:/repo"); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("Stat err = %v, want errSandboxNeedsSession", err)
	}
	if _, err := r.ReadDir(ctx, "/tmp", "cli:/repo"); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("ReadDir err = %v, want errSandboxNeedsSession", err)
	}
	if err := r.MkdirAll(ctx, "/tmp/x", 0o755, "cli:/repo"); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("MkdirAll err = %v, want errSandboxNeedsSession", err)
	}
	if err := r.Remove(ctx, "/tmp/x", "cli:/repo"); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("Remove err = %v, want errSandboxNeedsSession", err)
	}
	if err := r.RemoveAll(ctx, "/tmp/x", "cli:/repo"); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("RemoveAll err = %v, want errSandboxNeedsSession", err)
	}
	if err := r.DownloadFile(ctx, "http://x", "/tmp/x", "cli:/repo"); !errors.Is(err, errSandboxNeedsSession) {
		t.Errorf("DownloadFile err = %v, want errSandboxNeedsSession", err)
	}
}

// GetShell / Workspace 是只读的部署默认值，不参与路由，可直接回答。
func TestRouterDelegation_ShellAndWorkspaceUseLocalDefault(t *testing.T) {
	r := newNoneRouter()
	if got, err := r.GetShell("cli:/repo", "/tmp"); err != nil {
		t.Errorf("GetShell: %v", err)
	} else if want, _ := (&NoneSandbox{}).GetShell("cli:/repo", "/tmp"); got != want {
		t.Errorf("GetShell = %q, want the local sandbox's %q", got, want)
	}
	if got, want := r.Workspace("cli:/repo"), (&NoneSandbox{}).Workspace("cli:/repo"); got != want {
		t.Errorf("Workspace = %q, want the local sandbox's %q", got, want)
	}
}

// ============================================================================
// 生命周期
// ============================================================================

func TestCloseAndCloseForUser(t *testing.T) {
	r := newNoneRouter()
	if err := r.Close(); err != nil {
		t.Errorf("Close on a router without remote must be a no-op, got %v", err)
	}
	if err := r.CloseForUser("cli:/repo"); err != nil {
		t.Errorf("CloseForUser: %v", err)
	}
}

func TestImplementsSandboxAndResolver(t *testing.T) {
	var _ Sandbox = (*SandboxRouter)(nil)
	var _ SandboxResolver = (*SandboxRouter)(nil)
}

// ============================================================================
// SplitSessionKey
// ============================================================================

func TestSplitSessionKey(t *testing.T) {
	cases := []struct{ in, ch, id string }{
		{"cli:/repo", "cli", "/repo"},
		{"web:chat_1", "web", "chat_1"},
		{"cli:/path:Agent-x", "cli", "/path:Agent-x"}, // chatID 自身含冒号
		{"nodots", "", "nodots"},
	}
	for _, c := range cases {
		ch, id := SplitSessionKey(c.in)
		if ch != c.ch || id != c.id {
			t.Errorf("SplitSessionKey(%q) = (%q,%q), want (%q,%q)", c.in, ch, id, c.ch, c.id)
		}
	}
}

// ============================================================================
// 新路由器的默认部署行为
// ============================================================================

func TestNewSandboxRouter_DefaultsToNone(t *testing.T) {
	r := NewSandboxRouter(config.SandboxConfig{}, t.TempDir())
	if r.Name() != "none" {
		t.Errorf("Name() = %q, want none (no remote configured)", r.Name())
	}
	if r.Sandbox().Name() != "none" {
		t.Error("default Sandbox() must be the local host — never a guessed machine")
	}
}
