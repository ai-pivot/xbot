package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
)

// ── 数据安全守卫（2026-09-16 用户要求：防止 xbot-cli 运行覆盖已有 db 数据）──────
//
// SQLite 的行为是：**打开不存在的文件时会静默新建一个空库**。危险场景是"路径写错或
// 文件被误删 ⇒ 真实数据被孤儿化，服务却正常启动但空空如也"，随后被新写入覆盖。
//
// 设计铁律（用户 2026-09-16 明确要求）：**启动路径永不阻塞**。xbot-server 由
// supervisor 托管，Open() 一旦返回错误就会形成「启动失败 → 自动重启 → 再失败」的崩溃
// 循环，所以这里的两个函数只负责**识别与取证**，由 Open() 打 WARN 日志；它们
// **绝不**阻止启动，也绝不改写/截断任何既有文件。
//   · 文件存在但 0 字节 → 可能新库（首次写入前确实是 0 字节）也可能数据丢失 ⇒ WARN；
//   · 文件存在且非空但不是 SQLite → WARN，文件原样不动，是否可用交给驱动判定；
//   · 文件不存在但同目录有备份 → WARN 并列出备份路径，供人工恢复。

// sqliteMagic 是 SQLite 数据库文件的前 16 字节魔数。
const sqliteMagic = "SQLite format 3\x00"

// verifySQLiteHeader 校验 path 是否为 SQLite 数据库文件（读前 16 字节魔数）。
// 仅用于**告警取证**：返回错误表示该文件不像 SQLite 数据库，调用方只打 WARN，
// 不据此阻塞启动（也不是拒绝打开的理由）。
func verifySQLiteHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open for header check: %w", err)
	}
	defer f.Close()
	buf := make([]byte, len(sqliteMagic))
	n, err := f.Read(buf)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	if n != len(sqliteMagic) || string(buf) != sqliteMagic {
		return fmt.Errorf("not a SQLite database (bad magic)")
	}
	return nil
}

// siblingDBBackups 返回 path 同目录下看起来是"该库的备份"的文件列表。
// 命中任一即说明"这里本该有一个库"，用于在 Open() 里发出更醒目的告警（列出可恢复的
// 备份路径）。**不用于阻塞启动**。
func siblingDBBackups(path string) []string {
	patterns := []string{
		path + ".bak*",
		path + ".pre-*",
		path + ".backup*",
		filepath.Join(filepath.Dir(path), filepath.Base(path)+".recover*"),
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if m == path || seen[m] {
				continue
			}
			if fi, statErr := os.Stat(m); statErr == nil && fi.Size() > 0 {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}
