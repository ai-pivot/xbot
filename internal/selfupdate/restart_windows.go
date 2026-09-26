//go:build windows

package selfupdate

// restartSupported = false：Windows 没有可以向**自身**投递、且能被
// signal.Notify 捕获的终止信号 ——
//   · `syscall.Kill` 在 Windows 上根本不存在（编译期就失败）；
//   · `os.Process.Signal(os.Interrupt)` 也不被 Windows 支持（只支持 os.Kill）；
//   · `TerminateProcess`/`os.Exit` 是**硬退出**，会跳过 serverapp.Run 的安全停机
//     （WAL checkpoint + 待续跑标记）—— 那正是 2026-09-17「committed 数据丢失」的形态。
//
// 所以 Windows 上 Restart 显式返回错误（提示用户手动重启），绝不为了「按钮能点」
// 而硬退出丢数据。
const restartSupported = false

// terminateSelf：Windows 走不到这里（Restart 在 restartSupported=false 时提前返回）。
// 保留签名以满足构建（并作为将来接 console ctrl event / x/sys/windows 的落点）。
func terminateSelf() error {
	return errRestartUnsupported
}
