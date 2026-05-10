---
name: worktree
description: "Multi-agent workspace collaboration via git worktrees. Use when managing parallel agents in the same repo, creating isolated workspaces, coordinating merge between agent branches, or running Best-of-N parallel execution."
---

# Worktree — Multi-Agent Workspace Collaboration

## Overview

当多个 Agent 在同一个 Git 仓库中并行工作时，使用 `git worktree` 创建隔离的工作目录，避免文件冲突。
支持三种场景：对等 Session 自动检测、SubAgent 模式、Best-of-N 并行执行。

## 核心概念

| 概念 | 说明 |
|------|------|
| **主工作区** | 原始仓库目录，第一个 Agent 直接使用 |
| **Worktree** | 独立工作目录，链接到主仓库 `.git` 对象库 |
| **对等发现** | 新 Agent 启动时检测到同伴后自动创建 worktree |
| **合并协调** | Agent 完成后通过 SendMessage 通知同伴协商合并 |

## 使用方法

### 场景一：对等 Session（自动检测）

当你被启动时，Worktree 工具会自动检测当前仓库是否有其他 Agent 在工作：
- 只有一个 Agent → 直接使用主项目
- 已有其他 Agent → 自动创建 worktree 隔离

### 场景二：SubAgent 模式

主 Agent 为子 Agent 创建 worktree：

```bash
# 创建 worktree
git worktree add ../.xbot-worktrees/<agent-id> -b agent/<agent-id>/<timestamp>
# 创建 SubAgent 时传递 worktree 路径作为 parent_cwd
```

子 Agent 在 worktree 中独立工作，完成后主 Agent 合并。

### 场景三：Best-of-N 并行执行

对同一任务并行尝试多个方案，选最优结果：

**操作步骤：**

1. 调用 Worktree(init) N 次创建 N 个 worktree：
```bash
# Agent 为每个 Best-of-N 实例调用 Worktree tool
Worktree(action="init", role="bestof", instance="bestof-1", task="实现用户登录")
Worktree(action="init", role="bestof", instance="bestof-2", task="实现用户登录")
Worktree(action="init", role="bestof", instance="bestof-3", task="实现用户登录")
```

2. 在每个 worktree 中启动 SubAgent，传入相同任务：
```
SubAgent(task="实现用户登录功能...", role="implementer", instance="bestof-1", interactive=true)
SubAgent(task="实现用户登录功能...", role="implementer", instance="bestof-2", interactive=true)
SubAgent(task="实现用户登录功能...", role="implementer", instance="bestof-3", interactive=true)
```

3. 所有 Agent 完成后，收集结果对比：
   - 代码质量（可读性、健壮性）
   - 测试覆盖
   - 性能表现

4. 向用户展示对比结果，用户选择最佳方案

5. 合并选中方案的分支：`git merge --no-ff agent/bestof/bestof-N/<task>`

6. 清理未选中的 worktree：`Worktree(action="cleanup")`

## 合并协调

### 通信协议

Agent 之间通过 SendMessage 使用结构化 JSON 报文：

```json
{
  "protocol": "xbot.merge-coordination.v1",
  "type": "ready | conflict-proposal | accept | escalate",
  "agent": {"id": "...", "branch": "...", "worktree": "..."},
  "payload": {
    "files": ["src/auth/login.go"],
    "diff_brief": "重构了 JWT 验证逻辑",
    "conflicts": [{"file": "...", "line_range": "42-67"}],
    "proposed_resolution": "...",
    "rationale": "..."
  }
}
```

### 冲突解决规则

| 冲突类型 | 处理方式 |
|---------|---------|
| 文件无重叠 | 自动合并 |
| 测试文件冲突 | tester 版本优先 |
| 源码文本冲突 Agent 达成一致 | 协商解决 |
| 3 轮协商无果或语义冲突 | 提交用户仲裁 |

### 合并策略

主 Agent 执行 `git merge --no-ff <branch>`，保留每个 Agent 工作为独立 merge commit。

## 清理

```bash
# 提交更改
git add -A && git commit -m "Agent work completed"
# 切回主工作区后删除 worktree
git worktree remove ../.xbot-worktrees/<agent-id> --force
git branch -d agent/<agent-id>/<timestamp>
```

## 安全注意

- Worktree 必须在主仓库**外部**（Git 硬约束），路径 `{repo}/../.xbot-worktrees/`
- 不要在 worktree 内嵌套创建 worktree
- 确保退出前清理 worktree，避免磁盘泄漏
