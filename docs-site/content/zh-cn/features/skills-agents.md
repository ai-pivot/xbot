---
title: "Skills & Agents"
weight: 40
---

# Skills & Agents

xbot 有两种扩展方式：**Skills** 和 **Agents**。两者都是 Markdown 文件，AI 可以帮你创建和管理。

## 最简单的用法：让 AI 帮你

> 「帮我创建一个 skill，在调试的时候自动检查常见错误模式」

> 「创建一个 code-reviewer agent，专门帮我 review 代码」

> 「帮我看看现有的 skill 有哪些」

AI 会帮你完成从创建到生效的全部过程。

![子 Agent 与群聊](/img/cli/subagents.gif)

## Skills（技能包）

Skill 就是一份「指导文档」，告诉 AI 在特定任务上应该怎么做。比如「调试时按这个流程走」「提交代码前按这个检查清单来」。

### 怎么用

Skill 是按需加载的——当你的任务匹配某个 skill 的场景时，AI 会自动加载对应的指导。你也可以主动触发：

- 在对话中输入 `/skill名称`（如 `/debug`、`/plan`）
- 或者直接描述你的需求，AI 会自动匹配合适的 skill

### 内置 Skills

| Skill | 干什么 |
|-------|--------|
| `debug` | 调试 bug：定位、分析、修复 |
| `plan` | 改代码之前先做计划 |
| `post-dev` | 写完代码后的收尾工作（更新文档、提交） |
| `agent-creator` | 创建新的 Agent 角色 |
| `skill-creator` | 创建新的 Skill |
| `plugin-creator` | 创建插件 |
| `hook-creator` | 创建生命周期钩子 |
| `ai-config` | 配置主题、订阅、TUI 布局 |
| `worktree` | 多 Agent 并行工作 |

### Skill 放哪里

```
~/.xbot/skills/
└── my-skill/
    └── SKILL.md       ← 技能定义文件
```

也可以嵌在项目里，共享给团队：
```
<项目>/.xbot/skills/
└── project-skill/
    └── SKILL.md
```

## Agents（子代理）

Agent 是一个「有独立能力的助手」。你可以把任务委派给它，它会独立完成。

### 什么时候用 Agent

- **需要专门角色**：「让安全专家 review 这段代码」
- **需要并行工作**：「同时让 3 个人审查不同方面」
- **需要独立上下文**：「帮我探索这个模块，别干扰当前对话」

### SubAgent 调用模式

| 模式 | 行为 |
|------|------|
| **单次**（默认） | 在前台运行一次，返回最终结果 |
| **交互式** | 持久化多轮会话。创建后发送消息，完成时卸载 |
| **后台** | 交互式会话在后台运行，你可以同时做其他工作，稍后回头查看 |

### 内置 Agents

| Agent | 角色 | 擅长什么 |
|-------|------|----------|
| `explore` | 探索者 | 分析代码结构、追踪调用链、总结模块功能 |
| `chancellery` | 审查官 | 方案审查、质量把关 |
| `secretariat` | 规划官 | 制定方案、架构设计 |
| `department-state` | 调度官 | 把方案拆成任务分给别人 |
| `ministry-works` | 工程师 | 写代码、重构 |
| `ministry-justice` | 质检员 | 找 bug、边界情况分析 |
| `ministry-personnel` | 风格审查 | 代码风格、命名规范 |
| `ministry-revenue` | 性能分析 | 性能优化、依赖审查 |
| `ministry-defense` | 安全审查 | 漏洞扫描、权限检查 |
| `ministry-rites` | 文档审查 | 文档质量、注释规范 |

### 怎么用

直接跟 AI 说就行：

> 「帮我探索一下这个模块是怎么工作的」

> 「让安全专家 review 这段代码」

> 「同时从安全、性能、代码风格三个方面审查这个 PR」

AI 会自动选择合适的 agent 并委派任务。

## Group Chat（会议模式）

![SubAgents 与群聊](/img/cli/subagents.gif)

Group Chat 是一种**主持式多 Agent 讨论**。你创建一个群组，邀请 Agent，通过 @提及来控制谁发言。

### 会议模式如何工作

1. **创建群组** — 使用 `CreateChat(type="group", members=[...])`
2. **发送消息** — **不**带 @提及的消息只添加到讨论历史
3. **触发发言者** — 在消息中 `@agent:role/instance` 会触发该 Agent 回应
4. **完整上下文** — 被触发的 Agent 看到完整的讨论历史加上你的消息
5. **所有回复保留** — 每个 Agent 的回复都会添加到历史中，供后续参考

### 工作流示例

```
1. CreateChat(type="group", members=["agent:reviewer/r1", "agent:tester/t1"])
   → 返回 "group:g1"

2. SendMessage(to="group:g1", message="我们来讨论一下 API 设计。")
   → 没有触发任何 Agent。只添加到历史。

3. SendMessage(to="group:g1", message="@agent:reviewer/r1 你觉得怎么样？")
   → Reviewer 以完整上下文回应。

4. SendMessage(to="group:g1", message="@agent:tester/t1 有什么顾虑吗？")
   → Tester 回应时也能看到 Reviewer 之前的回复。
```

{{< hint type=note >}}
群组在 `max_rounds` 次带 @提及的主持人消息后自动关闭（默认 10 轮）。使用 `SendMessage(to="group:...", message="...")` 继续讨论。
{{< /hint >}}

## 参考手册

### Skill 文件格式

每个 skill 是一个目录，包含一个 `SKILL.md` 文件：

```markdown
---
name: my-skill
description: 一句话描述这个 skill 做什么
---

# Skill 名称

## 目标
...

## 步骤
...
```

`---` 之间的部分是 frontmatter（元数据），下面是正文——就是给 AI 看的指导文档。

### Agent 文件格式

每个 agent 是一个 Markdown 文件，存放在 `~/.xbot/agents/` 中：

```markdown
---
role: my-role
description: 一句话描述这个 agent 做什么
tools: Read, Grep, Glob, Shell
model: swift
---

# Agent 角色描述

你是 XXX，擅长 XXX...
```

| 字段 | 用途 |
|------|------|
| `role` | 唯一角色名，用于 `SubAgent(role="...")` |
| `description` | Agent 能做什么——在列表中展示 |
| `tools` | Agent 可以使用的工具，逗号分隔列表 |
| `model` | 模型层级：`vanguard`（最强）、`balance`（默认）、`swift`（最快） |

### Agent 存储位置

```
~/.xbot/agents/
├── explore.md          # 代码探索
├── chancellery.md      # 方案审查
├── secretariat.md      # 架构规划
└── ...
```

### Market（市场）

xbot 支持技能和 Agent 的发布、浏览和安装：

| 命令 | 说明 |
|------|------|
| `/browse` | 浏览市场 |
| `/install <name>` | 安装一个 skill 或 agent |
| `/uninstall <name>` | 卸载 |
| `/publish` | 发布自己的 skill 或 agent |
| `/unpublish` | 取消发布 |

## 参见
- [内置工具](/zh-cn/features/tools/) — 所有可用工具
- [MCP](/zh-cn/features/mcp/) — 外部工具集成
- [高级技巧](/zh-cn/tips/) — 子 Agent 委派模式
