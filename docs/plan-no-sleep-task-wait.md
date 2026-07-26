# 计划：禁止前台 sleep + 新增 task_wait 工具

> 生成时间：2026-07-26
> 状态：待确认

## 背景与目标

xbot 非常喜欢用 `sleep N` 前台阻塞等待后台任务完成，这浪费迭代和上下文。根因是多个工具描述和输出文本中**明确建议模型使用 sleep**。

**目标**：
1. 清除所有 prompt/工具描述中鼓励 sleep 的文本，改为引导使用 `task_wait` 工具或继续做其他工作
2. 在 Shell 工具描述中加入"健康检查"示例——用后台 shell 循环 poll 接口
3. 新增 `task_wait` 工具：在 timeout 内阻塞等待后台任务完成，替代 sleep 轮询

## 现状分析

### 鼓励 sleep 的位置（共 5 处）

| 文件 | 行 | 当前文本 | 性质 |
|------|-----|---------|------|
| `tools/task_tools.go` | :20 | `"use Shell with \"sleep 3\" (or longer) to wait"` | **最直接**——工具描述教模型 sleep |
| `tools/task_tools.go` | :155 | `"run: sleep 3 (wait at least 3s before next check)"` | 运行时输出再次强化 |
| `tools/shell.go` | :28 | `"do other work or sleep 3+ seconds before checking again"` | Shell 描述把 sleep 列为合法选项 |
| `tools/shell.go` | :237 | `"wait or do other work first"` | 后台启动输出暗示 wait=sleep |
| `tools/shell.go` | :330 | `"wait or do other work first"` | 超时升级输出暗示 wait=sleep |

### 后台任务系统现状

- `BackgroundTaskManager`（`tools/task_manager.go`）管理所有后台任务
- 任务状态：`running` / `done` / `error` / `killed`
- `Status(taskID)` 返回 `*BackgroundTask`（含状态、输出、退出码）
- 任务完成时通过 `NotifyCh` 自动通知引擎，输出自动注入对话
- 现有工具：`task_status`（查状态）、`task_read`（读输出）、`task_kill`（终止）
- 工具注册：`registry_helpers.go:DefaultRegistry()` 中 `r.RegisterCore(&Tool{})`
- **无工具级超时**：工具执行使用 agent ctx，没有 per-tool timeout wrapper（Shell 自己管理 timeout 参数）。`task_wait` 可以安全阻塞

### 风险点
- **旧对话中已有 sleep 指导文本**：已持久化的消息无法修改，仅影响新对话
- **task_wait 阻塞迭代**：task_wait 执行期间该迭代被阻塞，但不会阻塞其他后台任务的通知注入（通知走独立 goroutine）
- **remote sandbox 不支持 KeepAlive**：但 task_wait 只查询状态，不依赖 KeepAlive

## 详细计划

### 阶段一：清除所有 sleep 鼓励文本

- [ ] **1.1** `tools/task_tools.go:15-24` — 重写 `TaskStatusTool.Description()`：移除 "use Shell with sleep 3"，改为 `"If status is 'running', use task_wait to block until completion, or continue with other work — the result will be injected automatically when the task finishes."`
- [ ] **1.2** `tools/task_tools.go:155` — 重写运行时输出：将 `"run: sleep 3"` 改为 `"Use task_wait to wait for completion, or continue with other work."`
- [ ] **1.3** `tools/shell.go:22-38` — 重写 `ShellTool.Description()` 的 BACKGROUND MODE 段：移除 `"sleep 3+ seconds"`，改为 `"If status is 'running', use task_wait to block until completion, or continue with other work."`
- [ ] **1.4** `tools/shell.go:237` — 后台启动输出：将 `"wait or do other work first"` 改为 `"Use task_wait to wait for completion, or continue with other work."`
- [ ] **1.5** `tools/shell.go:330` — 超时升级输出：同 1.4

### 阶段二：Shell 描述加入健康检查示例

- [ ] **2.1** `tools/shell.go` Description — 在 BACKGROUND MODE 段末尾加入示例：
  ```
  Example — poll a health endpoint until ready:
    {"command": "for i in $(seq 1 60); do curl -sf http://localhost:8080/health && exit 0; sleep 2; done; exit 1", "background": true}
  Then use task_wait to block until the endpoint is up.
  ```
  注意：这里的 `sleep 2` 是在 **后台 shell 脚本内部**，不是前台阻塞——完全合法。

### 阶段三：新增 task_wait 工具

- [ ] **3.1** 创建 `tools/task_wait.go`：
  - 结构体 `TaskWaitTool struct{}`
  - `Name() → "task_wait"`
  - `Description()` — 说明用途：阻塞等待后台任务完成，替代 sleep 轮询
  - `Parameters()` — `task_id` (string, required) + `timeout` (number, optional, 默认 60, 最大 300)
  - `Execute()` 逻辑：
    1. nil 检查 `toolCtx.BgTaskManager`
    2. 解析参数
    3. 如果任务已完成（非 running），立即返回状态
    4. 否则进入轮询循环：每 1s 检查一次状态，`select` 监听 `ctx.Done()`（支持 Ctrl+C 中断）和 timeout
    5. 任务完成或超时或取消时返回当前状态 + 输出预览
  - `var _ Tool = (*TaskWaitTool)(nil)`
- [ ] **3.2** `tools/registry_helpers.go` — 在 `DefaultRegistry()` 中注册：`r.RegisterCore(&TaskWaitTool{})`

### 阶段四：系统 prompt 补充禁止前台 sleep 的规则

- [ ] **4.1** `prompt/base/environment.md` — 在 Background Tasks 段后加入禁止前台 sleep 的规则：
  ```markdown
  - **No foreground sleep**: Never run `sleep N` as a foreground Shell command to wait for a condition. Instead:
    1. Start the wait operation as a background Shell task (e.g. a polling loop), then use `task_wait` to block until it completes.
    2. Or use `task_wait` directly on an existing background task.
  - `task_wait` blocks the current iteration until the task finishes or the timeout expires — no wasted iterations on sleep polling.
  ```

## 验证方案

- `go build ./...` 编译通过
- `go test ./tools/...` 现有工具测试不回归
- 手动验证：启动一个后台 sleep 任务，调用 `task_wait` 确认能阻塞到完成
- 手动验证：`task_wait` timeout 到期后返回 "still running"
- 手动验证：`task_wait` 期间 Ctrl+C 能中断
- 检查工具描述：`grep -rn "sleep" tools/` 确认无前台 sleep 鼓励

## 回滚策略

- 所有改动为文本和新文件，无 DB migration，无破坏性变更
- 删除 `tools/task_wait.go` + 移除注册行即可回滚工具
- prompt 文本改动可 git revert

## 注意事项

- task_wait 的轮询间隔用 1s（内部 sleep，不是 LLM 发起的 sleep 调用）
- task_wait 最大 timeout 300s（5分钟），避免无限阻塞
- 后台 shell 脚本内部的 `sleep` 是合法的（不是前台阻塞）——仅禁止前台 `sleep N` 作为等待手段
- `prompt/base/environment.md` 是 embed 文件，修改后需要 `go build` 重新嵌入
