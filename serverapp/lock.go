package serverapp

// serverapp/lock.go —— serve 进程对 XBOT_HOME 的独占锁。
//
// 背景（2026-10-21 主库损坏事故）：
//   服务器上同时存在两个 xbot serve 进程，其中一个从 /tmp/xbot-court 启动、
//   没有设置 XBOT_HOME，于是落到 HOME=/home/smith，用的主库与 supervisord
//   管理的生产进程完全相同（/home/smith/.xbot/xbot.db），但**代码版本不同**
//   （一个 schema v75，一个 v76）。SQLite 的多进程锁只在所有参与者都通过
//   SQLite 正常协作时成立；当一方退出并在 Close 时 checkpoint + unlink -wal，
//   另一方正在写的事务就会读到结构损坏的页 —— `database disk image is
//   malformed (11)`，page 248/247/244 全坏，只能从备份恢复。
//
// 正确的防线不是「禁止任何两个进程打开同一个 db 文件」（那会拦住 CLI、测试、
// seed 脚本），而是「**同一份配置只允许一个 serve 进程**」—— 因为一个 home
// 对应一份配置、一个库、一个 schema 版本。

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// serverLock 是当前 serve 进程持有的 home 级独占锁。
type serverLock struct {
	f    *os.File
	path string
}

// AcquireServerLock 在 <home>/.xbot-server.lock 上取 flock(LOCK_EX|LOCK_NB)。
//
// 已有 serve 进程持锁时返回错误；调用方应当**直接退出**（不重试、不轮询），
// 因为那意味着这个 home 已经有服务在跑 —— 再起一个只会争抢同一个库。
func AcquireServerLock(home string) (*serverLock, error) {
	if strings.TrimSpace(home) == "" {
		return nil, fmt.Errorf("cannot acquire server lock: xbot home is empty")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, fmt.Errorf("create xbot home %s: %w", home, err)
	}

	p := filepath.Join(home, ".xbot-server.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open server lock %s: %w", p, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// 把现有持锁者读出来，报错信息要能直接定位是哪个进程占着。
		buf := make([]byte, 64)
		n, _ := f.ReadAt(buf, 0)
		holder := strings.TrimSpace(string(buf[:n]))
		_ = f.Close()
		if holder == "" {
			holder = "unknown"
		}
		return nil, fmt.Errorf(
			"another xbot server is already running for %s (pid %s, lock %s) — refusing to start a "+
				"second one: two processes sharing one database can corrupt it", home, holder, p)
	}

	_ = f.Truncate(0)
	if _, werr := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); werr != nil {
		// 记录失败不阻断启动，只是报错信息会少一个 pid。
		_, _ = fmt.Fprintf(os.Stderr, "warning: failed to record server lock owner pid: %v\n", werr)
	}
	return &serverLock{f: f, path: p}, nil
}

// Release 释放锁（幂等）。进程崩溃时 OS 也会释放。
func (l *serverLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// Path 返回锁文件路径（测试与日志用）。
func (l *serverLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
