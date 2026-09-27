---
title: "共享黑板"
weight: 45
---

# 共享黑板（Blackboard）

**黑板**是多个 agent 共享的持久工作面。主 agent 与它所在会话的**全部 SubAgent 自动共享同一块板**；任何会话也可以通过命名板（`@my-board`）跨会话协作。它用**机制**而不是约定，正面回答并行 agent 的三大失败模式：

| 失败模式 | 机制 |
|---|---|
| 两个人做同一件事 | **原子认领**（`Blackboard(action="claim")`）+ 租约：持有者崩了租约自动过期，条目重新可被接管 —— 无需清扫线程，也不会死锁 |
| 互相覆盖 | **revision CAS**：每次写入都带自己读到的 revision；落后者收到**回带当前条目**的冲突，而不是覆盖别人的成果 |
| 前置条件没就绪就开工 | **依赖**（`blocked_by`）：依赖全部关闭前条目 `ready=false`，且**认领会被直接拒绝** |

## 用法

```jsonc
// 拆计划：第二条依赖第一条
Blackboard(action="post", key="api-design", kind="task", title="设计 /v2 API", body="…")
Blackboard(action="post", key="api-impl",   kind="task", title="实现 /v2 API", blocked_by=["api-design"])

// 找活干（只有 ready 的才可认领）
Blackboard(action="list", prefix="api-")

// 认领 → 汇报 → 收尾（claim 返回 claim_token，续租/释放都靠它）
Blackboard(action="claim", key="api-impl")
Blackboard(action="update", key="api-impl", expected_revision=4, body="进度 60%，接口草案已附")
Blackboard(action="close",  key="api-impl", expected_revision=5)

// 让变更来找你，而不是轮询
Blackboard(action="watch")   // 别人的改动会作为通知送达；unwatch 退订
```

`kind`（`task` / `finding` / `decision` / `note` / 任意自定义）与 `body` 对**宿主不透明**：黑板只负责存储与协调，不知道"任务"是什么。这正是它能承载任意协作范式、而不是写死一张任务表的原因。

## 事件驱动，而非轮询

订阅是**显式**的、按会话的。被订阅的板发生变化时，通知走与 cron / 同伴消息**同一条**后台通知管线：**忙的 agent 注入当前迭代，闲的 agent 开启新一轮**。同一块板 3 秒内的突发会合并成至多一条通知，且**写入者永远不会被自己的改动唤醒**。

## Web 面板

**黑板**面板（底部面板 chips / 侧栏）显示当前会话的板：派生计数（可认领 / 认领中 / 阻塞 / 未关闭）、每条状态、持有者的**租约倒计时**、依赖关系 —— 并且**任何会话**改动该板时都会实时刷新。你也可以亲自在板上写：新增条目、关闭/重开、释放卡住的认领、删除。

## 与 Claude Code / Codex 对比

| | Claude Code Agent Teams | Codex 并行 agent | xbot 黑板 |
|---|---|---|---|
| 共享工作面 | 任务列表 | 无（只有 worktree 隔离） | 通用，载荷不透明 |
| 认领 | self-claim，**轮询** | — | 原子 + **租约**（自愈） |
| 并发正确性 | file locking | — | revision **CAS** |
| 依赖 | 有 | — | 有（阻塞时认领被拒） |
| 唤醒 | 无（轮询） | 无 | **推送**（忙注入 / 闲开轮） |
| 持久性 | 实验特性；resume 后成员会丢 | — | SQLite（revision/租约/依赖都过重启） |
| 子代理参与 | 否 | 否 | **是**（主 + 全部 SubAgent 默认共享） |
