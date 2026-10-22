package clipanic

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"xbot/config"
	log "xbot/logger"
)

var (
	configMu sync.RWMutex
	writeMu  sync.Mutex
	enabled  bool
	logPath  string
	// crashOut 是 **唯一** 的追加句柄：既供 debug.SetCrashOutput 捕获未恢复的
	// panic（任意 goroutine / fatal error），也供 ReportWithStack 写已恢复的 panic。
	// 进程生命周期内只打开一次（见 EnableFileLogging）。
	crashOut *os.File
)

func DefaultLogPath() string {
	return filepath.Join(config.XbotHome(), "logs", "cli-panic.log")
}

// EnableFileLogging 打开 panic 日志文件并把它注册为**进程级崩溃输出**。
//
// 为什么必须用 debug.SetCrashOutput（2026-10-22 05:26 生产事故）：
// 只记路径 + `defer Recover(...)` 的组合只能兜住 main goroutine 里**被 Recover
// 覆盖到**的 panic。任何其它 goroutine 的未恢复 panic（以及运行时 fatal error）
// 只会写到 stderr —— 而 supervisor 通常把 stderr 丢掉，现场只剩下
// "exited (exit status 2)"，堆栈永久丢失（logs/cli-panic.log 实测从未生成）。
// SetCrashOutput 让运行时把崩溃输出（含**全部** goroutine 的堆栈）追加进本文件，
// 与 panic 发生在哪个 goroutine 无关。
//
// 文件只打开一次：O_CREATE|O_APPEND|O_WRONLY + 0600。句柄此后**永不关闭** ——
// ① SetCrashOutput 会 dup 一份 fd，但运行时可能在任意时刻从崩溃的 goroutine 写入，
// 提前关闭会让崩溃路径静默失效；② ReportWithStack 复用同一句柄（不再每次 open/close）。
//
// 幂等：重复调用同一路径不会重复打开；换路径会重新注册（旧句柄关闭）。
func EnableFileLogging(path string) {
	if path == "" {
		path = DefaultLogPath()
	}
	configMu.Lock()
	defer configMu.Unlock()
	if enabled && logPath == path && crashOut != nil {
		return // 已启用且路径未变 —— 不重复打开
	}

	f, err := openAppendFile(path)
	if err != nil {
		// 启动路径永不因日志失败而阻塞 —— 降级为"只有 logger 输出"。
		log.WithError(err).WithField("path", path).Warn("clipanic: cannot open panic log file (crash output disabled)")
		if crashOut != nil {
			_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
			_ = crashOut.Close()
			crashOut = nil
		}
		enabled = false
		logPath = ""
		return
	}

	// Go 1.26：CrashOptions 为空结构（旧的 CrashOnPanic flag 已并入默认行为）——
	// 只要注册了文件，panic/fatal 就会连同堆栈一起写入。
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		log.WithError(err).WithField("path", path).Warn("clipanic: debug.SetCrashOutput failed (crash output disabled)")
		_ = f.Close()
		enabled = false
		logPath = ""
		return
	}

	prev := crashOut
	crashOut = f
	enabled = true
	logPath = path
	if prev != nil && prev != f {
		_ = prev.Close()
	}
}

// DisableFileLogging 注销崩溃输出并关闭句柄。主要用于测试。
func DisableFileLogging() {
	configMu.Lock()
	defer configMu.Unlock()
	if crashOut != nil {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		_ = crashOut.Close()
		crashOut = nil
	}
	enabled = false
	logPath = ""
}

// openAppendFile 以 0600 创建/追加打开日志文件（父目录自动创建）。
// 权限收紧到 0600：panic 堆栈会带出环境变量值、URL、token 片段。
func openAppendFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func Recover(where string, msg any, repanic bool) {
	if r := recover(); r != nil {
		ReportWithStack(where, msg, r, debug.Stack())
		if repanic {
			panic(r)
		}
	}
}

func Report(where string, msg any, recovered any) {
	ReportWithStack(where, msg, recovered, debug.Stack())
}

func ReportWithStack(where string, msg any, recovered any, stack []byte) {
	msgLabel := formatMessage(msg)
	fields := log.Fields{"panic": recovered}
	if where != "" {
		fields["where"] = where
	}
	if msgLabel != "" {
		fields["msg"] = msgLabel
	}
	log.WithFields(fields).Error("CLI panic recovered")

	f := currentCrashFile()
	if f == nil {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n==== %s panic ====\n", time.Now().Format(time.RFC3339))
	if where != "" {
		fmt.Fprintf(&b, "where=%s\n", where)
	}
	if msgLabel != "" {
		fmt.Fprintf(&b, "msg=%s\n", msgLabel)
	}
	fmt.Fprintf(&b, "panic=%v\n%s\n", recovered, stack)

	// 与运行时崩溃输出共用同一 O_APPEND 句柄：writeMu 串行化本包自己的写入，
	// 单次 WriteString 保证一条记录连续落盘。
	writeMu.Lock()
	defer writeMu.Unlock()
	_, _ = f.WriteString(b.String())
	_ = f.Sync()
}

// currentCrashFile 返回启用中的日志句柄（nil = 未启用）。
func currentCrashFile() *os.File {
	configMu.RLock()
	defer configMu.RUnlock()
	if !enabled || crashOut == nil {
		return nil
	}
	return crashOut
}

func Go(where string, fn func()) {
	go func() {
		defer Recover(where, nil, false)
		fn()
	}()
}

// currentLogPath 返回启用中的日志路径（"" = 未启用）。
func currentLogPath() string {
	configMu.RLock()
	defer configMu.RUnlock()
	if !enabled {
		return ""
	}
	return logPath
}

func formatMessage(msg any) string {
	switch v := msg.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprintf("%T", msg)
	}
}
