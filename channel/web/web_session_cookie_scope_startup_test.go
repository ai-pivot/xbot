package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/bus"
)

// ---------------------------------------------------------------------------
// Identity resolution: every source, with provenance
// ---------------------------------------------------------------------------

func TestResolveInstanceIDWithSource_AllSources(t *testing.T) {
	// 1) explicit server.instance_id wins over everything else
	t.Setenv("XBOT_INSTANCE_ID", "from-env")
	t.Setenv("XBOT_HOME", "/home/smith/.xbot")
	if id, src := ResolveInstanceIDWithSource("from-config"); id != "from-config" || src != instanceIDSourceConfig {
		t.Errorf("explicit: got (%q,%q), want (%q,%q)", id, src, "from-config", instanceIDSourceConfig)
	}

	// 2) env override beats XBOT_HOME
	if id, src := ResolveInstanceIDWithSource(""); id != "from-env" || src != instanceIDSourceEnv {
		t.Errorf("env: got (%q,%q), want (%q,%q)", id, src, "from-env", instanceIDSourceEnv)
	}

	// 2b) config.applyEnvOverrides already folded the env value into the config
	//     value: the provenance must still say env, not config.json.
	if id, src := ResolveInstanceIDWithSource("from-env"); id != "from-env" || src != instanceIDSourceEnv {
		t.Errorf("env folded into config: got (%q,%q), want (%q,%q)", id, src, "from-env", instanceIDSourceEnv)
	}

	// 3) XBOT_HOME basename (owner dir for the canonical /home/<user>/.xbot)
	t.Setenv("XBOT_INSTANCE_ID", "")
	if id, src := ResolveInstanceIDWithSource(""); id != "smith" || src != instanceIDSourceHome {
		t.Errorf("home: got (%q,%q), want (%q,%q)", id, src, "smith", instanceIDSourceHome)
	}

	// 4) nothing configured ⇒ unknown identity (the channel may still fall back
	//    to the listen port; resolution itself stays honest about it)
	t.Setenv("XBOT_HOME", "")
	if id, src := ResolveInstanceIDWithSource(""); id != "" || src != "" {
		t.Errorf("empty: got (%q,%q), want (\"\",\"\")", id, src)
	}
}

func TestInstanceIDFromHome_CanonicalLayouts(t *testing.T) {
	cases := map[string]string{
		"/home/smith/.xbot":       "smith",     // canonical per-user home
		"/home/daidai/.xbot":      "daidai",    // second user, same machine
		"/tmp/p03-home/instance2": "instance2", // non-hidden home
		"/root/.xbot":             "root",      // containers collide here ⇒ must set instance_id
		"":                        "",
	}
	for home, want := range cases {
		if got := instanceIDFromHome(home); got != want {
			t.Errorf("instanceIDFromHome(%q) = %q, want %q", home, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Startup line: Start() must advertise the scope, once, with real provenance
// ---------------------------------------------------------------------------

func TestStartLogsSessionCookieScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", filepath.Join(home, "smith"))
	t.Setenv("XBOT_INSTANCE_ID", "")

	var (
		mu    sync.Mutex
		lines []string
		got   = make(chan struct{}, 1)
	)
	orig := sessionCookieScopeLogf
	sessionCookieScopeLogf = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
		select {
		case got <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { sessionCookieScopeLogf = orig })

	// Ephemeral port: the identity comes from XBOT_HOME, not from the port.
	wc := NewWebChannel(WebChannelConfig{Host: "127.0.0.1", Port: 0}, bus.NewMessageBus())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = wc.Start()
	}()

	select {
	case <-got:
	case <-time.After(10 * time.Second):
		wc.Stop()
		<-served
		t.Fatal("Start() never logged the session cookie scope line")
	}

	mu.Lock()
	line := lines[0]
	count := len(lines)
	mu.Unlock()
	if count != 1 {
		t.Errorf("expected exactly one scope line per Start, got %d: %v", count, lines)
	}
	if want := "session cookie scope: name=xbot_session_smith source=home_basename"; line != want {
		t.Fatalf("startup line = %q, want %q", line, want)
	}
	// Contract: the line is emitted after every Start() step and before Serve,
	// so "line seen" ⇒ listener and server are already wired. Moving the log
	// back to the top of Start would race Stop() against ptyMgr.Start().
	if wc.listener == nil || wc.server == nil {
		t.Fatalf("scope line emitted before Start() finished wiring (listener=%v server=%v)", wc.listener != nil, wc.server != nil)
	}

	// Wait until Serve is actually accepting before stopping: Stop() must not
	// race the startup sequence (it is only meant to run after Start is serving).
	// A completed HTTP exchange proves the accept loop is up.
	addr := wc.listener.Addr().String() // safe: written before the log line we just received
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		wc.Stop()
		<-served
		t.Fatalf("server never accepted a request on %s: %v", addr, err)
	}
	resp.Body.Close()

	wc.Stop()
	<-served
}

func TestSessionCookieScope_LogLineFormatIsStable(t *testing.T) {
	// The runbook tells operators to `grep "session cookie scope:"`, so the
	// prefix is part of the contract.
	line := fmt.Sprintf("session cookie scope: name=%s source=%s", webSessionCookieNameFor("smith"), instanceIDSourceConfig)
	if !strings.HasPrefix(line, "session cookie scope: name=") || !strings.Contains(line, " source=") {
		t.Fatalf("startup line format drifted: %q", line)
	}
}

// A channel that knows its identity must NOT print the scope line as legacy and
// must resolve the name from the same identity the log advertises.
func TestCookieScope_ReportsConfiguredSource(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{InstanceID: "smith", InstanceIDSource: instanceIDSourceHome, Port: 8080}, bus.NewMessageBus())
	name, src := wc.cookieScope()
	if name != "xbot_session_smith" || src != instanceIDSourceHome {
		t.Fatalf("cookieScope = (%q,%q)", name, src)
	}

	// Port fallback only when identity is unknown; the source must say so.
	t.Setenv("XBOT_HOME", "")
	t.Setenv("XBOT_INSTANCE_ID", "")
	wc2 := NewWebChannel(WebChannelConfig{Port: 8080}, bus.NewMessageBus())
	name2, src2 := wc2.cookieScope()
	if name2 != "xbot_session_8080" || src2 != instanceIDSourcePort {
		t.Fatalf("port fallback cookieScope = (%q,%q)", name2, src2)
	}

	// Legacy fallback: no identity, no port (unstarted channel in tests).
	wc3 := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	if name3, src3 := wc3.cookieScope(); name3 != "" || src3 != "" {
		t.Fatalf("unknown identity cookieScope = (%q,%q), want empty", name3, src3)
	}
	if got := wc3.sessionCookieName(); got != webSessionCookieName {
		t.Fatalf("unknown identity name = %q, want legacy %q", got, webSessionCookieName)
	}
}

// Two instances that would collide must be distinguishable from the log alone.
func TestCookieScope_TwoInstancesDifferentNames(t *testing.T) {
	t.Setenv("XBOT_INSTANCE_ID", "")

	t.Setenv("XBOT_HOME", "/home/smith/.xbot")
	a, srcA := NewWebChannel(WebChannelConfig{Port: 8080}, bus.NewMessageBus()).cookieScope()

	t.Setenv("XBOT_HOME", "/home/daidai/.xbot")
	b, srcB := NewWebChannel(WebChannelConfig{Port: 8081}, bus.NewMessageBus()).cookieScope()

	if a == b {
		t.Fatalf("two per-user instances share cookie name %q — the eviction bug is back", a)
	}
	if srcA != instanceIDSourceHome || srcB != instanceIDSourceHome {
		t.Fatalf("sources = %q / %q, want home_basename both", srcA, srcB)
	}
}

// Guard: the log line must never be produced for an unstarted channel that has
// no identity (otherwise every unit test would emit noise) — and Start() is the
// only caller.
func TestLogSessionCookieScope_SilentWithoutIdentity(t *testing.T) {
	t.Setenv("XBOT_HOME", "")
	t.Setenv("XBOT_INSTANCE_ID", "")

	var called int
	orig := sessionCookieScopeLogf
	sessionCookieScopeLogf = func(string, ...any) { called++ }
	t.Cleanup(func() { sessionCookieScopeLogf = orig })

	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	wc.logSessionCookieScope()
	if called != 0 {
		t.Fatalf("logged a scope line without any identity (%d calls)", called)
	}
}
