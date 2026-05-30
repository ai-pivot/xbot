package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helper: build a scriptPlugin + PluginContext ready for Activate / Deactivate
// ---------------------------------------------------------------------------

func newTestScriptPlugin(t *testing.T, entry string) *scriptPlugin {
	t.Helper()
	dir := t.TempDir()
	m := PluginManifest{
		ID:          "com.test.script",
		Name:        "test-script",
		Version:     "1.0.0",
		Runtime:     RuntimeScript,
		Entry:       entry,
		Permissions: []string{PermUIContribute, PermHooksSubscribe},
		Contributes: &PluginContributes{
			UI: []UISlotContribution{
				{ID: "w1", Slot: "infoBar", Priority: 10, RefreshInterval: "1h"},
			},
		},
	}
	p, err := NewScriptRuntime().Create(&m, dir)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*scriptPlugin)
}

func newTestPluginContext(t *testing.T, sp *scriptPlugin, workDir string) *pluginContextImpl {
	t.Helper()
	pctx := newPluginContext(&sp.manifest, &noopStorage{}, newPluginLogger(sp.manifest.ID, nil), nil, nil, nil)
	wr := NewWidgetRegistry()
	pctx.SetWidgetRegistry(wr)
	pctx.SetSessionMetadata(workDir, "test-channel", "test-chat", 0)
	return pctx
}

func activateAndWait(t *testing.T, sp *scriptPlugin, pctx *pluginContextImpl, timeout time.Duration) {
	t.Helper()
	if err := sp.Activate(pctx); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	deadline := time.After(timeout)
	for {
		sp.outputMu.RLock()
		n := len(sp.outputs)
		wd := ""
		if pctx != nil {
			wd = pctx.WorkingDir()
		}
		got := sp.outputs[wd]
		sp.outputMu.RUnlock()
		if n > 0 && got != "" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for first runAndUpdate")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 1: Per-WorkDir Output Isolation
// ---------------------------------------------------------------------------

func TestScriptPlugin_PerWorkDirOutput(t *testing.T) {
	t.Parallel()

	workDirA := t.TempDir()
	// Use a script that echoes XBOT_WORK_DIR env var for cross-platform
	// compatibility.  We write a small shell script to avoid platform
	// differences in how "echo $VAR" is interpreted.
	scriptName := "print_workdir.sh"
	if runtime.GOOS == "windows" {
		scriptName = "print_workdir.bat"
	}
	scriptPath := filepath.Join(t.TempDir(), scriptName)
	if runtime.GOOS == "windows" {
		os.WriteFile(scriptPath, []byte("@echo %XBOT_WORK_DIR%"), 0o644)
	} else {
		os.WriteFile(scriptPath, []byte("#!/bin/sh\necho \"$XBOT_WORK_DIR\""), 0o755)
	}
	entry := "sh " + scriptPath
	if runtime.GOOS == "windows" {
		entry = scriptPath
	}

	sp := newTestScriptPlugin(t, entry)
	pctx := newTestPluginContext(t, sp, workDirA)

	activateAndWait(t, sp, pctx, 3*time.Second)
	t.Cleanup(func() { sp.Deactivate(pctx) })

	// Verify initial workDir output
	sp.outputMu.RLock()
	outA := strings.TrimSpace(sp.outputs[workDirA])
	sp.outputMu.RUnlock()
	if outA != workDirA {
		t.Errorf("initial output = %q, want %q", outA, workDirA)
	}

	// Trigger a second workDir
	workDirB := t.TempDir()
	sp.OnWorkDirChanged(workDirB)

	// Poll until workDirB output appears (Windows CI can be slow).
	deadline := time.After(3 * time.Second)
	for {
		sp.outputMu.RLock()
		outB := strings.TrimSpace(sp.outputs[workDirB])
		sp.outputMu.RUnlock()
		if outB != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for workDirB output")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	sp.outputMu.RLock()
	outA2 := strings.TrimSpace(sp.outputs[workDirA])
	outB := strings.TrimSpace(sp.outputs[workDirB])
	sp.outputMu.RUnlock()

	// Both workDirs should have independent outputs
	if outA2 != workDirA {
		t.Errorf("workDirA output = %q, want %q", outA2, workDirA)
	}
	if outB != workDirB {
		t.Errorf("workDirB output = %q, want %q", outB, workDirB)
	}
}

// ---------------------------------------------------------------------------
// Test 2: RenderForWorkDir
// ---------------------------------------------------------------------------

func TestScriptPlugin_RenderForWorkDir(t *testing.T) {
	t.Parallel()

	sp := newTestScriptPlugin(t, "echo test-output")
	_ = newTestPluginContext(t, sp, "") // context for setup, not used as pctx

	dir := t.TempDir()

	// Pre-populate output for a specific workDir
	sp.outputMu.Lock()
	sp.outputs = map[string]string{dir: "test-output"}
	sp.outputMu.Unlock()

	spans := sp.RenderForWorkDir(0, dir)
	if len(spans) == 0 {
		t.Fatal("expected at least 1 span")
	}
	if spans[0].Text != "test-output" {
		t.Errorf("RenderForWorkDir(hit) = %q, want %q", spans[0].Text, "test-output")
	}

	// Cache miss for unknown dir — should run script synchronously.
	otherDir := t.TempDir()
	spansMiss := sp.RenderForWorkDir(0, otherDir)
	if len(spansMiss) == 0 {
		t.Fatal("expected at least 1 span for cache miss")
	}
	// "echo test-output" → runScript resolves "test-output" relative to plugin dir
	// The raw output is that resolved path. We just verify a non-empty span is returned.
	if spansMiss[0].Text == "" {
		t.Error("RenderForWorkDir(miss) returned empty text")
	}
}

// ---------------------------------------------------------------------------
// Test 3: triggerCh No-Block (buffered channel + default case)
// ---------------------------------------------------------------------------

func TestScriptPlugin_TriggerCh_NoBlock(t *testing.T) {
	t.Parallel()

	sp := newTestScriptPlugin(t, "echo trigger-test")
	pctx := newTestPluginContext(t, sp, t.TempDir())
	if err := sp.Activate(pctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.Deactivate(pctx) })

	// Fire more triggers than the buffer (8) — must not block.
	// The workDir doesn't need to exist; we're testing channel behavior only.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 9; i++ {
			// Use empty dir to skip runScript (OnWorkDirChanged ignores empty dir
			// for pendingDirs but still sends to triggerCh)
			sp.OnWorkDirChanged("")
		}
	}()

	select {
	case <-done:
		// success — no block
	case <-time.After(2 * time.Second):
		t.Fatal("OnWorkDirChanged blocked — triggerCh overflow")
	}
}

// ---------------------------------------------------------------------------
// Test 4: Concurrent OnWorkDirChanged + runAndUpdate no race
// ---------------------------------------------------------------------------

func TestScriptPlugin_ConcurrentRunAndUpdate(t *testing.T) {
	t.Parallel()

	sp := newTestScriptPlugin(t, "echo concurrent-ok")
	pctx := newTestPluginContext(t, sp, t.TempDir())
	if err := sp.Activate(pctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.Deactivate(pctx) })

	// Pre-create temp dirs for concurrent OnWorkDirChanged
	dirs := make([]string, 20)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sp.OnWorkDirChanged(dirs[idx])
		}(i)
	}
	wg.Wait()

	// Wait for pending triggers to settle
	time.Sleep(300 * time.Millisecond)

	// Verify outputs map is not corrupted — just read all values
	sp.outputMu.RLock()
	for _, v := range sp.outputs {
		_ = v // no panic is sufficient
	}
	sp.outputMu.RUnlock()
}

// ---------------------------------------------------------------------------
// Test 5: WidgetRegistry RenderZoneForWorkDir — static vs WorkDirRenderer
// ---------------------------------------------------------------------------

func TestWidgetRegistry_RenderZoneForWorkDir(t *testing.T) {
	t.Parallel()

	r := NewWidgetRegistry()
	r.SetDefaultRenderFn(BasicANSIRender)

	// 1) staticWidget (no WorkDirRenderer) — fallback to Render()
	err := r.Register("p1", "static-w", "infoBar", &staticWidget{"fallback-text"}, 10)
	if err != nil {
		t.Fatal(err)
	}

	got := r.RenderZoneForWorkDir("infoBar", "/any/dir")
	if !strings.Contains(got, "fallback-text") {
		t.Errorf("static fallback = %q, should contain 'fallback-text'", got)
	}

	// 2) mockWorkDirWidget — implements WorkDirRenderer
	wdWidget := &mockWorkDirWidget{
		outputs: map[string]string{
			"/repo1": "main ✓",
			"/repo2": "develop",
		},
	}
	err = r.Register("p2", "wd-w", "infoBar", wdWidget, 20)
	if err != nil {
		t.Fatal(err)
	}

	got1 := r.RenderZoneForWorkDir("infoBar", "/repo1")
	got2 := r.RenderZoneForWorkDir("infoBar", "/repo2")
	if !strings.Contains(got1, "main ✓") {
		t.Errorf("RenderZoneForWorkDir(/repo1) = %q, should contain 'main ✓'", got1)
	}
	if !strings.Contains(got2, "develop") {
		t.Errorf("RenderZoneForWorkDir(/repo2) = %q, should contain 'develop'", got2)
	}
}

// ---------------------------------------------------------------------------
// Test 6: WidgetRegistry Debounce
// ---------------------------------------------------------------------------

func TestWidgetRegistry_Debounce(t *testing.T) {
	t.Parallel()

	r := NewWidgetRegistry()
	r.SetDebounce(50 * time.Millisecond)

	var callCount int64
	r.OnUpdated(func() {
		atomic.AddInt64(&callCount, 1)
	})

	// Fire 10 rapid NotifyUpdated calls — all within a single debounce window.
	// Total duration ~2ms, well within the 100ms debounce window.
	for i := 0; i < 10; i++ {
		r.NotifyUpdated()
	}

	// Wait for debounce to elapse (Windows CI scheduler can be slow)
	time.Sleep(300 * time.Millisecond)

	count := atomic.LoadInt64(&callCount)
	if count != 1 {
		t.Errorf("expected debounce to coalesce 10 calls into 1, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Test 7: parseScriptOutput style hints
// ---------------------------------------------------------------------------

func TestParseScriptOutput_StyleHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		text  string
		style StyleClass
	}{
		{"dim|some text", "some text", StyleDim},
		{"ok|all good", "all good", StyleSuccess},
		{"warn|caution", "caution", StyleWarning},
		{"err|failure", "failure", StyleError},
		{"info|note", "note", StyleInfo},
		{"accent|highlight", "highlight", StyleAccent},
		{"plain text", "plain text", StyleNormal},
		{"unknown|text", "text", StyleNormal}, // unknown hint → normal
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			spans := parseScriptOutput(tt.input)
			if len(spans) == 0 {
				t.Fatalf("expected at least 1 span, got 0")
			}
			if spans[0].Text != tt.text {
				t.Errorf("text = %q, want %q", spans[0].Text, tt.text)
			}
			if spans[0].Style != tt.style {
				t.Errorf("style = %q, want %q", spans[0].Style, tt.style)
			}
		})
	}

	// Empty input
	if spans := parseScriptOutput(""); spans != nil {
		t.Errorf("expected nil for empty input, got %v", spans)
	}
}

// ---------------------------------------------------------------------------
// Test 8: Env Injection (XBOT_WORK_DIR, XBOT_TOOL_*)
// ---------------------------------------------------------------------------

func TestScriptPlugin_EnvInjection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	workDir := t.TempDir() // must exist — cmd.Dir = workDir

	// Write a script that prints env vars injected by runScript.
	// Cross-platform: Windows uses .bat with %VAR% syntax, Unix uses /bin/sh.
	var scriptPath, entry string
	if runtime.GOOS == "windows" {
		scriptPath = filepath.Join(dir, "env.bat")
		os.WriteFile(scriptPath, []byte(
			"@echo WORKDIR=%XBOT_WORK_DIR% TOOL=%XBOT_TOOL_NAME% OUTPUT=%XBOT_TOOL_OUTPUT% INPUT=%XBOT_TOOL_INPUT%",
		), 0o644)
		entry = scriptPath
	} else {
		scriptPath = filepath.Join(dir, "env.sh")
		os.WriteFile(scriptPath, []byte(`#!/bin/sh
echo "WORKDIR=$XBOT_WORK_DIR TOOL=$XBOT_TOOL_NAME OUTPUT=$XBOT_TOOL_OUTPUT INPUT=$XBOT_TOOL_INPUT"
`), 0o755)
		entry = "sh " + scriptPath
	}

	m := PluginManifest{
		ID:          "com.test.env",
		Name:        "env-test",
		Version:     "1.0.0",
		Runtime:     RuntimeScript,
		Entry:       entry,
		Permissions: []string{PermUIContribute},
		Contributes: &PluginContributes{
			UI: []UISlotContribution{
				{ID: "w1", Slot: "infoBar", Priority: 10, RefreshInterval: "1h"},
			},
		},
	}
	p, err := NewScriptRuntime().Create(&m, dir)
	if err != nil {
		t.Fatal(err)
	}
	sp := p.(*scriptPlugin)

	// Set lastHook payload with full tool data
	sp.lastHookMu.Lock()
	sp.lastHook = &HookPayload{
		ToolName:   "Shell",
		ToolOutput: "test-output-data",
		ToolInput:  `{"command":"ls"}`,
	}
	sp.lastHookMu.Unlock()

	// Run the script with a real temp workDir
	output, err := sp.runScript(workDir)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(output, "WORKDIR="+workDir) {
		t.Errorf("output = %q, should contain WORKDIR=%s", output, workDir)
	}
	if !strings.Contains(output, "TOOL=Shell") {
		t.Errorf("output = %q, should contain TOOL=Shell", output)
	}
	if !strings.Contains(output, "OUTPUT=test-output-data") {
		t.Errorf("output = %q, should contain OUTPUT=test-output-data", output)
	}
	if !strings.Contains(output, `INPUT={"command":"ls"}`) {
		t.Errorf("output = %q, should contain INPUT={\"command\":\"ls\"}", output)
	}
}

// ---------------------------------------------------------------------------
// Snapshot & Edge Case Tests
// ---------------------------------------------------------------------------

func TestScriptPlugin_RenderForWorkDir_EmptyOutput(t *testing.T) {
	t.Parallel()
	// "true" 是一个不产生 stdout 输出的命令
	// RenderForWorkDir 在 outputs 中查不到（或为空）时会调用 runScript
	// runScript("true") 返回空字符串 → 走空输出分支
	sp := newTestScriptPlugin(t, "true")
	dir := t.TempDir()

	spans := sp.RenderForWorkDir(0, dir)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Text != "" {
		t.Errorf("Text = %q, want empty", spans[0].Text)
	}
	if spans[0].Style != StyleDim {
		t.Errorf("Style = %q, want %q", spans[0].Style, StyleDim)
	}
}

func TestScriptPlugin_RenderForWorkDir_MissingEntry(t *testing.T) {
	t.Parallel()

	sp := newTestScriptPlugin(t, "echo fallback-output")
	dirA := t.TempDir()
	dirB := t.TempDir()

	// 预设 dirA 的输出
	sp.outputMu.Lock()
	sp.outputs = map[string]string{dirA: "existing-output"}
	sp.outputMu.Unlock()

	// 请求 dirB — 不在 outputs 中，runScript 会执行并缓存
	spans := sp.RenderForWorkDir(0, dirB)
	if len(spans) < 1 {
		t.Fatal("expected at least 1 span")
	}
	if spans[0].Text == "" {
		t.Error("expected non-empty text for missing entry (runScript should have produced output)")
	}

	// dirA 不受影响
	sp.outputMu.RLock()
	if sp.outputs[dirA] != "existing-output" {
		t.Errorf("dirA output changed: got %q, want %q", sp.outputs[dirA], "existing-output")
	}
	sp.outputMu.RUnlock()
}

func TestParseScriptOutput_Snapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantJSON string
	}{
		{
			name:     "empty",
			input:    "",
			wantJSON: "null",
		},
		{
			name:     "plain_text",
			input:    "hello world",
			wantJSON: `[{"Text":"hello world","Style":"normal"}]`,
		},
		{
			name:     "dim_prefix",
			input:    "dim|faded",
			wantJSON: `[{"Text":"faded","Style":"dim"}]`,
		},
		{
			name:     "ok_prefix",
			input:    "ok|git:main ✓",
			wantJSON: `[{"Text":"git:main ✓","Style":"success"}]`,
		},
		{
			name:     "warn_prefix",
			input:    "warn|caution!",
			wantJSON: `[{"Text":"caution!","Style":"warning"}]`,
		},
		{
			name:     "err_prefix",
			input:    "err|fatal",
			wantJSON: `[{"Text":"fatal","Style":"error"}]`,
		},
		{
			name:     "info_prefix",
			input:    "info|note",
			wantJSON: `[{"Text":"note","Style":"info"}]`,
		},
		{
			name:     "accent_prefix",
			input:    "accent|highlight",
			wantJSON: `[{"Text":"highlight","Style":"accent"}]`,
		},
		{
			name:     "pipe_in_text",
			input:    "ok|branch:main|feature",
			wantJSON: `[{"Text":"branch:main|feature","Style":"success"}]`,
		},
		{
			name:     "empty_text_after_pipe",
			input:    "dim|",
			wantJSON: `[{"Text":"","Style":"dim"}]`,
		},
		{
			name:     "only_pipe",
			input:    "|rest",
			wantJSON: `[{"Text":"rest","Style":"normal"}]`,
		},
		{
			name:     "unknown_hint",
			input:    "custom|text",
			wantJSON: `[{"Text":"text","Style":"normal"}]`,
		},
		{
			name:     "whitespace_only",
			input:    "   ",
			wantJSON: `[{"Text":"   ","Style":"normal"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spans := parseScriptOutput(tt.input)
			got, err := json.Marshal(spans)
			if err != nil {
				t.Fatalf("json.Marshal error: %v", err)
			}
			if string(got) != tt.wantJSON {
				t.Errorf("got %s\nwant %s", got, tt.wantJSON)
			}
		})
	}
}
