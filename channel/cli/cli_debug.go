package cli

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"xbot/clipanic"
	log "xbot/logger"

	"xbot/config"

	tea "charm.land/bubbletea/v2"
)

const (
	debugDir        = "debug" // relative to XbotHome() (which is $HOME/.xbot or $XBOT_HOME)
	debugSockName   = "ctl.sock"
	debugUIFile     = "ui_capture.log"
	debugCaptureMax = 2000 // max lines to keep in capture log (ring buffer)
)

// parseKeyInput parses a human-readable key string into a tea.KeyPressMsg.
// Supports: plain chars (a, A, 1), special keys (enter, tab, esc, up, down, etc.),
// and modifier combos (ctrl+c, ctrl+z, alt+enter, shift+tab).
func parseKeyInput(input string) tea.KeyPressMsg {
	input = strings.TrimSpace(input)
	if input == "" {
		return tea.KeyPressMsg{}
	}

	var mod tea.KeyMod
	// Parse modifiers (left to right)
	for {
		if strings.HasPrefix(input, "ctrl+") {
			mod |= tea.ModCtrl
			input = input[5:]
		} else if strings.HasPrefix(input, "alt+") {
			mod |= tea.ModAlt
			input = input[4:]
		} else if strings.HasPrefix(input, "shift+") {
			mod |= tea.ModShift
			input = input[6:]
		} else {
			break
		}
	}

	lower := strings.ToLower(input)

	// Special keys
	switch lower {
	case "enter", "return":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: mod}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: mod}
	case "esc", "escape":
		return tea.KeyPressMsg{Code: tea.KeyEsc, Mod: mod}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Mod: mod}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Mod: mod}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft, Mod: mod}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight, Mod: mod}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome, Mod: mod}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd, Mod: mod}
	case "pgup", "pageup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp, Mod: mod}
	case "pgdown", "pagedown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: mod}
	case "backspace", "bs":
		return tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: mod}
	case "delete", "del":
		return tea.KeyPressMsg{Code: tea.KeyDelete, Mod: mod}
	case "insert", "ins":
		return tea.KeyPressMsg{Code: tea.KeyInsert, Mod: mod}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Mod: mod}
	case "f1":
		return tea.KeyPressMsg{Code: tea.KeyF1, Mod: mod}
	case "f2":
		return tea.KeyPressMsg{Code: tea.KeyF2, Mod: mod}
	case "f3":
		return tea.KeyPressMsg{Code: tea.KeyF3, Mod: mod}
	case "f4":
		return tea.KeyPressMsg{Code: tea.KeyF4, Mod: mod}
	case "f5":
		return tea.KeyPressMsg{Code: tea.KeyF5, Mod: mod}
	case "f6":
		return tea.KeyPressMsg{Code: tea.KeyF6, Mod: mod}
	case "f7":
		return tea.KeyPressMsg{Code: tea.KeyF7, Mod: mod}
	case "f8":
		return tea.KeyPressMsg{Code: tea.KeyF8, Mod: mod}
	case "f9":
		return tea.KeyPressMsg{Code: tea.KeyF9, Mod: mod}
	case "f10":
		return tea.KeyPressMsg{Code: tea.KeyF10, Mod: mod}
	case "f11":
		return tea.KeyPressMsg{Code: tea.KeyF11, Mod: mod}
	case "f12":
		return tea.KeyPressMsg{Code: tea.KeyF12, Mod: mod}
	}

	// Single printable character
	runes := []rune(input)
	if len(runes) == 1 {
		if mod != 0 {
			// With modifier: don't set Text so String() returns Keystroke() (e.g. "ctrl+c")
			// instead of raw Text (e.g. "c"). This ensures key.String() matches
			// what the real terminal produces.
			return tea.KeyPressMsg{Code: runes[0], Mod: mod}
		}
		return tea.KeyPressMsg{Code: runes[0], Text: input, Mod: mod}
	}

	// Fallback: treat as text
	if mod != 0 {
		return tea.KeyPressMsg{Code: runes[0], Mod: mod}
	}
	return tea.KeyPressMsg{Code: runes[0], Text: input, Mod: mod}
}

// debugCaptureUI dumps the current TUI view to the capture log file.
func (m *cliModel) debugCaptureUI() {
	home := config.XbotHome()
	dir := filepath.Join(home, debugDir)
	os.MkdirAll(dir, 0700)

	view := m.View().Content
	if view == "" {
		return
	}

	path := filepath.Join(dir, debugUIFile)

	// Ring buffer: keep last N captures separated by timestamps
	lines := strings.Split(view, "\n")

	// Read existing content to append
	var existing []string
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		existing = strings.Split(string(data), "\n")
	}

	// Trim to keep size bounded
	header := fmt.Sprintf("--- %s ---", time.Now().Format("15:04:05"))
	newLines := []string{"", header}
	newLines = append(newLines, lines...)
	combined := append(existing, newLines...)

	// Keep last debugCaptureMax lines
	if len(combined) > debugCaptureMax {
		combined = combined[len(combined)-debugCaptureMax:]
	}

	_ = os.WriteFile(path, []byte(strings.Join(combined, "\n")), 0600)
}

// debugSockListener manages the Unix socket for key injection.
type debugSockListener struct {
	listener net.Listener
	done     chan struct{}
	wg       sync.WaitGroup
}

// startDebugSock creates and starts listening on the debug Unix socket.
// Each accepted connection reads lines, parses them as key inputs, and
// injects them into the tea program via sendFn.
func startDebugSock(sockPath string, sendFn func(tea.Msg)) (*debugSockListener, error) {
	// Remove stale socket
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("debug socket: %w", err)
	}

	dl := &debugSockListener{
		listener: listener,
		done:     make(chan struct{}),
	}

	dl.wg.Add(1)
	go dl.acceptLoop(sendFn)

	return dl, nil
}

func (dl *debugSockListener) acceptLoop(sendFn func(tea.Msg)) {
	defer dl.wg.Done()
	for {
		conn, err := dl.listener.Accept()
		if err != nil {
			select {
			case <-dl.done:
				return
			default:
				continue
			}
		}
		dl.wg.Add(1)
		go dl.handleConn(conn, sendFn)
	}
}

func (dl *debugSockListener) handleConn(conn net.Conn, sendFn func(tea.Msg)) {
	defer dl.wg.Done()
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "quit") || strings.EqualFold(line, "exit") {
			return
		}
		key := parseKeyInput(line)
		if key.Code != 0 || key.Text != "" {
			sendFn(key)
		}
	}
}

func (dl *debugSockListener) Stop() {
	close(dl.done)
	dl.listener.Close()
	dl.wg.Wait()
}

// debugSockPath returns the Unix socket path for the debug control interface.
func debugSockPath() (string, error) {
	home := config.XbotHome()
	dir := filepath.Join(home, debugDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, debugSockName), nil
}

// startAutoInput parses a comma-separated key sequence (e.g. "1,enter,ctrl+c")
// and injects each key into the tea program after an initial delay.
// Items can be:
//   - Special keys: enter, tab, esc, up, down, left, right, backspace, ctrl+c, etc.
//   - Single characters: a, 1, etc.
//   - Multi-character text: "hello" (sent char by char)
//   - Sleep: "sleep:2" to wait 2 seconds before next key
//
// Keys are sent via asyncCh to avoid competing with handleAsyncDrain on program.Send().
func startAutoInput(sequence string, asyncCh chan<- tea.Msg, stopCh <-chan struct{}) {
	if sequence == "" {
		return
	}

	// Parse: split by comma, but handle "sleep:N" specially
	type keyItem struct {
		keys  []tea.KeyPressMsg // one item may produce multiple key events (multi-char text)
		sleep time.Duration     // if non-zero, sleep before sending these keys
	}

	items := strings.Split(sequence, ",")
	var parsed []keyItem
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "sleep:") {
			secs, err := time.ParseDuration(item[6:] + "s")
			if err == nil && secs > 0 {
				parsed = append(parsed, keyItem{sleep: secs})
			}
			continue
		}
		// Check if it's a known special key or modifier combo
		lower := strings.ToLower(item)
		if isSpecialKey(lower) {
			parsed = append(parsed, keyItem{keys: []tea.KeyPressMsg{parseKeyInput(item)}})
		} else {
			// Multi-character text: send each rune as a separate key event
			var keys []tea.KeyPressMsg
			for _, r := range item {
				keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			if len(keys) > 0 {
				parsed = append(parsed, keyItem{keys: keys})
			}
		}
	}

	if len(parsed) == 0 {
		return
	}

	clipanic.Go("ch.startAutoInput", func() {
		log.WithField("sequence", sequence).Info("Auto-input: waiting for splash to finish")
		// Wait for splash to finish and UI to stabilize
		select {
		case <-stopCh:
			return
		case <-time.After(2 * time.Second):
		}

		for _, p := range parsed {
			if p.sleep > 0 {
				select {
				case <-stopCh:
					log.Info("Auto-input: aborted during sleep")
					return
				case <-time.After(p.sleep):
				}
				continue
			}
			for _, key := range p.keys {
				select {
				case <-stopCh:
					log.Info("Auto-input: aborted")
					return
				case asyncCh <- key:
					log.WithField("key", fmt.Sprintf("%+v", key)).Debug("Auto-input: sent key")
				}
				// Small delay between chars for realistic typing
				time.Sleep(50 * time.Millisecond)
			}
			// Delay between items for UI to process
			time.Sleep(300 * time.Millisecond)
		}
		log.Info("Auto-input: sequence complete")
	})
}

// isSpecialKey checks if the input is a recognized special key or modifier combo.
func isSpecialKey(s string) bool {
	s = strings.ToLower(s)
	switch {
	case strings.HasPrefix(s, "ctrl+"), strings.HasPrefix(s, "alt+"), strings.HasPrefix(s, "shift+"):
		return true
	}
	switch s {
	case "enter", "return", "tab", "esc", "escape",
		"up", "down", "left", "right",
		"home", "end", "pgup", "pageup", "pgdown", "pagedown",
		"backspace", "bs", "delete", "del", "insert", "ins",
		"space",
		"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		return true
	}
	return false
}

// ── /debug command handlers for runtime diagnostics ──

// handleDebugCommand processes /debug subcommands for runtime diagnostics.
func (m *cliModel) handleDebugCommand(cmd string) {
	parts := strings.Fields(cmd)
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	switch sub {
	case "stats":
		m.showSystemMsg(m.renderDebugStats(), feedbackInfo)

	case "mem", "memory":
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		m.showSystemMsg(m.renderDebugMemory(&mem), feedbackInfo)

	case "goroutines", "goro":
		m.showSystemMsg(m.renderDebugGoroutines(), feedbackInfo)

	case "heap":
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		m.showSystemMsg(m.renderDebugHeap(&mem), feedbackInfo)

	case "profile":
		m.showSystemMsg(m.renderDebugCPUProfile(), feedbackInfo)

	case "gc":
		m.showSystemMsg(m.renderDebugGC(), feedbackInfo)

	case "gc-force":
		runtime.GC()
		m.showSystemMsg("GC forced", feedbackInfo)

	default:
		m.showSystemMsg(`/debug stats       runtime overview (goroutines, memory, GC)
/debug mem         detailed memory breakdown
/debug goroutines  goroutine count + stack dump
/debug heap        heap profile summary + GC goal
/debug profile     CPU profiling instructions
/debug gc          GC statistics + recent pauses
/debug gc-force    force immediate GC cycle`, feedbackInfo)
	}
}

func (m *cliModel) renderDebugStats() string {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	var sb strings.Builder
	sb.WriteString("═══ Runtime Stats ═══\n\n")
	fmt.Fprintf(&sb, "Goroutines:   %d\n", runtime.NumGoroutine())
	fmt.Fprintf(&sb, "CPU cores:    %d\n", runtime.NumCPU())
	fmt.Fprintf(&sb, "Go version:   %s\n", runtime.Version())

	fmt.Fprintf(&sb, "\n── Memory ──\n")
	fmt.Fprintf(&sb, "Alloc:      %s\n", humanBytes(mem.Alloc))
	fmt.Fprintf(&sb, "TotalAlloc: %s\n", humanBytes(mem.TotalAlloc))
	fmt.Fprintf(&sb, "Sys:        %s\n", humanBytes(mem.Sys))
	fmt.Fprintf(&sb, "HeapAlloc:  %s\n", humanBytes(mem.HeapAlloc))
	fmt.Fprintf(&sb, "HeapInuse:  %s\n", humanBytes(mem.HeapInuse))
	fmt.Fprintf(&sb, "StackInuse: %s\n", humanBytes(mem.StackInuse))

	fmt.Fprintf(&sb, "\n── GC ──\n")
	fmt.Fprintf(&sb, "NumGC:      %d\n", mem.NumGC)
	fmt.Fprintf(&sb, "CPU frac:   %.4f\n", mem.GCCPUFraction)
	fmt.Fprintf(&sb, "PauseTotal: %s\n", time.Duration(mem.PauseTotalNs))
	fmt.Fprintf(&sb, "NextGC:     %s\n", humanBytes(mem.NextGC))

	fmt.Fprintf(&sb, "\n── Session ──\n")
	fmt.Fprintf(&sb, "Messages:   %d\n", len(m.messages))
	if m.progressState.current != nil {
		fmt.Fprintf(&sb, "Iteration:  %d\n", m.progressState.current.Iteration)
		fmt.Fprintf(&sb, "History:    %d snapshots\n", len(m.progressState.iterations))
	}
	fmt.Fprintf(&sb, "Queue:      %d\n", len(m.messageQueue))
	fmt.Fprintf(&sb, "Viewport H: %d\n", m.viewport.Height())

	return sb.String()
}

func (m *cliModel) renderDebugMemory(mem *runtime.MemStats) string {
	var sb strings.Builder
	sb.WriteString("═══ Memory Details ═══\n\n")
	fmt.Fprintf(&sb, "Alloc:         %s\n", humanBytes(mem.Alloc))
	fmt.Fprintf(&sb, "TotalAlloc:    %s\n", humanBytes(mem.TotalAlloc))
	fmt.Fprintf(&sb, "Sys:           %s\n", humanBytes(mem.Sys))
	fmt.Fprintf(&sb, "Mallocs:       %d\n", mem.Mallocs)
	fmt.Fprintf(&sb, "Frees:         %d\n", mem.Frees)
	fmt.Fprintf(&sb, "Live objects:  %d\n", mem.Mallocs-mem.Frees)

	fmt.Fprintf(&sb, "\n── Heap ──\n")
	fmt.Fprintf(&sb, "HeapAlloc:     %s\n", humanBytes(mem.HeapAlloc))
	fmt.Fprintf(&sb, "HeapSys:       %s\n", humanBytes(mem.HeapSys))
	fmt.Fprintf(&sb, "HeapIdle:      %s\n", humanBytes(mem.HeapIdle))
	fmt.Fprintf(&sb, "HeapInuse:     %s\n", humanBytes(mem.HeapInuse))
	fmt.Fprintf(&sb, "HeapReleased:  %s\n", humanBytes(mem.HeapReleased))
	fmt.Fprintf(&sb, "HeapObjects:   %d\n", mem.HeapObjects)

	fmt.Fprintf(&sb, "\n── Stack ──\n")
	fmt.Fprintf(&sb, "StackInuse:    %s\n", humanBytes(mem.StackInuse))
	fmt.Fprintf(&sb, "StackSys:      %s\n", humanBytes(mem.StackSys))

	fmt.Fprintf(&sb, "\n── Off-heap ──\n")
	fmt.Fprintf(&sb, "MSpanInuse:    %s\n", humanBytes(mem.MSpanInuse))
	fmt.Fprintf(&sb, "MCacheInuse:   %s\n", humanBytes(mem.MCacheInuse))
	fmt.Fprintf(&sb, "GCSys:         %s\n", humanBytes(mem.GCSys))
	fmt.Fprintf(&sb, "OtherSys:      %s\n", humanBytes(mem.OtherSys))

	return sb.String()
}

func (m *cliModel) renderDebugGoroutines() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "═══ Goroutines ═══\n\n")
	fmt.Fprintf(&sb, "Count: %d\n\n", runtime.NumGoroutine())

	buf := make([]byte, 8192)
	n := runtime.Stack(buf, true)
	sb.WriteString("Stack dump (top 8KB):\n")
	sb.WriteString(string(buf[:n]))
	if n == len(buf) {
		sb.WriteString("\n... (truncated, use go tool pprof for full dump)")
	}

	return sb.String()
}

func (m *cliModel) renderDebugHeap(mem *runtime.MemStats) string {
	var sb strings.Builder
	sb.WriteString("═══ Heap Profile ═══\n\n")
	fmt.Fprintf(&sb, "HeapAlloc:    %s\n", humanBytes(mem.HeapAlloc))
	fmt.Fprintf(&sb, "HeapObjects:  %d\n", mem.HeapObjects)
	fmt.Fprintf(&sb, "HeapIdle:     %s\n", humanBytes(mem.HeapIdle))
	fmt.Fprintf(&sb, "HeapInuse:    %s\n", humanBytes(mem.HeapInuse))
	fmt.Fprintf(&sb, "HeapReleased: %s\n", humanBytes(mem.HeapReleased))

	fmt.Fprintf(&sb, "\nNextGC goal:  %s\n", humanBytes(mem.NextGC))
	if mem.NextGC > 0 {
		pct := float64(mem.HeapAlloc) / float64(mem.NextGC) * 100
		fmt.Fprintf(&sb, "Progress:     %.1f%%\n", pct)
	}

	sb.WriteString("\n── For detailed heap profile ──\n")
	sb.WriteString("  go tool pprof http://localhost:<port>/debug/pprof/heap\n")

	return sb.String()
}

func (m *cliModel) renderDebugCPUProfile() string {
	return `═══ CPU Profile ═══

	CPU profiling requires the pprof HTTP server.

	1. Start with:  xbot --pprof [--pprof-port 6060]
	2. Capture:     go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
	3. In pprof:    top20 / web / list <func>

	Quick 30s capture (if server running):
	curl -o /tmp/cpu.prof http://localhost:6060/debug/pprof/profile?seconds=30
	go tool pprof /tmp/cpu.prof
	`
}

func (m *cliModel) renderDebugGC() string {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	var gcStats debug.GCStats
	debug.ReadGCStats(&gcStats)

	var sb strings.Builder
	sb.WriteString("═══ GC Statistics ═══\n\n")
	fmt.Fprintf(&sb, "NumGC:         %d\n", mem.NumGC)
	fmt.Fprintf(&sb, "GCCPUFraction: %.6f\n", mem.GCCPUFraction)
	fmt.Fprintf(&sb, "PauseTotal:    %s\n", time.Duration(mem.PauseTotalNs))
	fmt.Fprintf(&sb, "LastGC:        %s\n", gcStats.LastGC.Format(time.RFC3339))

	if mem.NumGC > 0 {
		sb.WriteString("\n── Recent GC pauses ──\n")
		start := 0
		if len(gcStats.Pause) > 5 {
			start = len(gcStats.Pause) - 5
		}
		for i := start; i < len(gcStats.Pause); i++ {
			fmt.Fprintf(&sb, "  %s\n", gcStats.Pause[i])
		}
	}

	sb.WriteString("\n  Force GC: /debug gc-force\n")

	return sb.String()
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	if int(exp) >= len(units) {
		exp = len(units) - 1
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), units[exp])
}
