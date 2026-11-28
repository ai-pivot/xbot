package sqlite

import "time"

// testAsyncWait 是「等异步信号/事件到达」的上限。
//
// ⛔ 不要改回个位数秒：这些 deadline 只回答「等多久判失败」，条件一成立就立即
// 返回 —— 放宽它不降低判别力，只是把「机器够不够快」从断言里剔除。
//
// 为什么必须这么宽（2026-11-27 Windows CI 实测）：modernc/sqlite 每次 WAL 提交走
// FlushFileBuffers，叠加 Defender 扫描，含会话库打开/写入的链路可以跑到 14s+
// （TestAskUserInterruptReapsExpiredThroughProcessLoop 现场：14:53:52 触发收回 →
// 14:54:07 才写完 ask_answer），而同一份代码在 Linux 上是毫秒级。个位数秒的
// 上限在慢 runner 上就是「必红 + 红的时机看机器负载」= 偶发变红。
const testAsyncWait = 2 * time.Minute
