package agent

import "time"

// testCleanupWait 是「关闭输入后等 goroutine 收尾」的等待窗口。
//
// 与 testAsyncWait 区分开：收尾等待**不是判据**（超时只 t.Log，不影响 pass/fail），
// 所以不能跟着断言上限一起放大 —— 否则每个用例的收尾都会拖满那个上限
// （2026-11-27 实测：把这里的 3s 改成 2 分钟后，单个用例的耗时从毫秒级变成 120s，
// 因为投递进 loop 的消息会走 processMessage，而没有 LLM 配置时它很久才返回）。
const testCleanupWait = 5 * time.Second

// testAsyncWait 是「等异步 goroutine 把条件变成真」的上限。
//
// ⛔ 不要改回个位数秒。这些 deadline 只回答「等多久判失败」，条件一成立就立即
// 返回 —— 放宽它不降低任何判别力，只是把「机器够不够快」这个变量从断言里剔除。
//
// 为什么必须这么宽（2026-11-27 现场日志，TestAskUserInterruptReapsExpiredThroughProcessLoop）：
//
//	14:53:52  Auto-cancelling stale pending AskUser (TTL expired)
//	14:53:53  WAL state … sessions\web\68\chat-loop-stale-….db (open)
//	14:54:07  Failed to append ask_answer pairing …            ← 14 秒后才写完
//
// Windows runner 上 modernc/sqlite 每次 WAL 提交都走 FlushFileBuffers，叠加
// Defender 扫描，**首次打开会话库 + 一次写入实测 14s+**（同一份代码在 Linux 上
// 是毫秒级，所以固定 10s 的轮询在 Windows 上是必红，只是红的时机取决于机器负载），
// 这也是 .github/workflows/ci.yml 的 test-windows 要 `-timeout 30m` 的原因。
const testAsyncWait = 2 * time.Minute
