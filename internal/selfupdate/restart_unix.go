//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// restartSupported 报告本平台是否支持「自动重启」。Unix 支持：向自身投递 SIGTERM，
// 走 serverapp.Run 的优雅停机（WAL checkpoint + 待续跑标记 + 监听器关闭）。
const restartSupported = true

// terminateSelf 优雅地终止自身：SIGTERM → serverapp.Run 的 signal 处理。调用方
// （Restart）先让 HTTP 响应 flush 再触发，所以重启对前端是「请求成功 → 连接断开 →
// 服务回来后重连」。
func terminateSelf() error {
	return syscall.Kill(os.Getpid(), syscall.SIGTERM)
}
