---
name: skill-creator
description: "Create, update, delete, or install skills (SKILL.md), and design/fix a skill's description & activation conditions. Activate when the task is to build a skill or improve how it triggers — 用户说『帮我设计/创建/写一个 skill』『优化/改一下这个 skill 的描述或触发条件』『这个 skill 老是不激活/乱激活』『安装/导入 skill』『把这个脚本打包成 skill』；also when installing/importing skills from GitHub or URLs, packaging scripts/assets into a skill, or discussing skill structure. Skills are universal agent capabilities — install whatever the user wants. 不适用于普通代码改动（除非改的就是 SKILL.md 本身）。"
---

# Skill Creator

Skills are **universal agent capabilities**. They are not limited to xbot-specific
features — any workflow, integration, or domain knowledge can be packaged as a
skill. When a user asks to install or create a skill, **always do it**, regardless
of the skill's purpose or origin.

## Installing External Skills

Users may ask to install a skill from GitHub, a URL, or another source. This is
the most common skill workflow — treat it as first-class.

### From GitHub

```bash
# Clone into skills directory
git clone https://github.com/user/agent-skill-foo.git ~/.xbot/skills/foo

# Or download a single file
curl -fsSL https://raw.githubusercontent.com/user/repo/main/SKILL.md \
  -o ~/.xbot/skills/foo/SKILL.md
```

After downloading, verify:
1. The file is `SKILL.md` with valid frontmatter (`name:` and `description:`)
2. Scripts are executable (`chmod +x`)
3. `Skill(name="foo", action=load)` works

### Searching for Skills

When searching the web for skills, search for **"agent skill"** or **"AI agent skill"**,
NOT "xbot skill". The skill format (SKILL.md with YAML frontmatter) is a universal
convention used across agent frameworks. Good search queries:

- `"agent skill" <topic> github`
- `"SKILL.md" <domain>`
- `AI agent skill <capability>`

### Installing from User Description

When a user says "I want a skill that does X":
1. Search the web for existing agent skills for X
2. If found → download and install
3. If not found → create from scratch following the guide below

## Skill Structure

```
skills/{skill-name}/
├── SKILL.md              # Required: frontmatter + instructions
├── scripts/              # Optional: executable scripts
│   └── setup.sh
├── references/           # Optional: docs loaded on demand
└── assets/               # Optional: templates, config files
```

**IMPORTANT**: Skills can be created in two locations:

1. **Global skills** — Create under the directory shown in the system prompt's **"Skills 存储目录"** line (e.g. `~/.xbot/skills/`). These are available in ALL projects and sessions. This is the default choice for general-purpose skills.

2. **Project-local skills** — Create under the current project's `.xbot/skills/` directory (e.g. `<project-root>/.xbot/skills/{skill-name}/`). These are ONLY available when working inside that project. This is ideal for project-specific workflows, domain-specific skills, or team-shared skills that live alongside the code.

   To determine the project root, check the system prompt's **"📂 默认工作目录"** or the **"项目 Skills 目录"** line if present.

   **When to use project-local**: the skill is specific to this codebase, uses project conventions, references project files, or should be version-controlled with the project (commit the `.xbot/skills/` directory).

The system prompt also shows a **"项目 Skills 目录"** line when project-local skills are detected — use this path when creating project-local skills.

To find the correct path, look at the system prompt section `# Available Skills` → `**Skills 存储目录**` (global) and `**项目 Skills 目录**` (project-local).

## Lifecycle

1. **Discovery** — Every message, all skill names + descriptions appear in the system prompt
2. **Loading** — LLM calls `Skill(name=..., action=load)` to read SKILL.md
3. **Tool usage** — All tools are always available; use them directly
   as listed in the skill's "Required Tools" section
4. **File listing** — `Skill(name=..., action=list_files)` returns full paths of all files in the skill
5. **Execution** — LLM runs scripts via `Shell` tool using the paths from `list_files`

## Creating a Skill

### 1. Discover relevant tools

Before writing SKILL.md, use `search_tools` to find tools the skill will need:

```
search_tools(query="send feishu message")  → finds feishu_send_message, etc.
search_tools(query="github pull request")  → finds mcp_github_create_pr, etc.
```

Include the discovered tool names in the skill body so the LLM knows which tools the skill expects to use.

### 2. Write SKILL.md

> ⚠️ **`description` 是唯一的激活依据 —— 必须写全「所有」激活条件，而不是笼统介绍。**
> 详见下面的《Description: enumerate EVERY activation condition》。

```markdown
---
name: my-skill
description: What this skill does and WHEN to activate it. Be specific — this is the only trigger.
---

# My Skill

## Required Tools
These tools are used by this skill (all are always available):
- feishu_send_message
- feishu_search_wiki

## Instructions
Step-by-step instructions for the LLM...
```

**Note**: Every skill SHOULD include a "Required Tools" section listing which tools the skill expects to use. All tools are always available to the agent — this section is documentation, not a load trigger.

### Description: enumerate EVERY activation condition

The `description` is the **sole** activation trigger: the agent sees only
`name` + `description` in its system prompt and decides from those whether to load
the skill. A vague description means the skill is never activated — or activated at
the wrong time.

**写法模板（照结构写，不要写成"介绍"；触发条件用「在需要 … 的时候激活」句式穷举）：**

```yaml
description: "<做什么（一句话）>。在需要 <场景A> / <场景B> / <场景C> 的时候激活：
  <任务类型>、<任务复杂度/规模类条件>、<用户可能说的话与中英关键词>、<产物名/文件名/命令/
  配置键>、<报错串>、<显式触发 /my-skill>。不适用于 <负向范围>（用 <另一个 skill>）。"
```

**Requirements:**

1. **List every trigger situation**, not a summary of the topic. Cover all of:
   - the **task types** that should load it ("配环境 / 部署 / 排查 CI 失败 / 写迁移脚本…"),
   - the **complexity / scale classes** whenever the skill targets them — 例如面向重度编排
     的 skill 必须写出 **「在需要做复杂任务/大任务/高难度任务的时候激活」**，并把"复杂/大/难"
     落成可判定的判据（多条互相独立的线索可并行、跨多文件/多服务、需要长时间实验、
     容易踩坑/返工、用户明说"大任务/难/系统性做…"）。**只写主题不写复杂度，会让 skill 在
     最该被激活的时候不激活** —— 这是最常见的失效模式。
   - the **user phrasings & keywords** a user might actually say (synonyms, Chinese *and*
     English terms, tool/product names, error strings),
   - the **artifacts** involved (file names/extensions, config keys, CLI commands,
     dashboard names),
   - **explicit triggers** if any (`/my-skill`).
2. **Say what it does in one clause, then spend the rest on when** — "做什么" 一句话，
   其余全部用来列触发条件。
3. **Be specific, not generic.** ❌「帮助处理数据库相关任务」 ✅「Go 项目 SQLite schema
   迁移：新增/删除列、写 migration、排查 `no such column` 报错、回滚方案」。
4. **Include negative scope when it matters** ("不要用于 X，X 用另一个 skill")。
5. **Don't be shy about length**: 3–6 行、覆盖 5–10 个具体触发条件，比一句笼统介绍有用得多。

**Example — bad (never activates reliably):**

```yaml
description: A skill for database work.
```

**Example — good:**

```yaml
description: "SQLite/Postgres schema 迁移与排错。触发：新增/删除/改名表列、写 migration
  文件、`no such column`/`table ... has no column named` 报错、回滚迁移、schema 版本
  冲突、sqlite3 CLI 操作、批量数据修复脚本。和 /migrate 命令配合使用。不适用于查询性能
  优化（用 perf 相关 skill）。"
```

写完后自检（逐条过，不能只回答"能"）：

1. 一个没读过这个 skill 的人，**只看 description**，能不能判断出**所有**该激活它的情形？
2. **用户会不会用别的说法描述同一件事**（同义词、中英混说、口头语、报错原文）？都列了吗？
3. **有没有「最该激活却不会激活」的场景**？—— 尤其是**任务规模/难度**这类抽象条件，必须落成
   具体判据（例：`在需要做复杂任务/大任务/高难度任务的时候激活：多条独立线索、跨多服务、
   需要并行调度、用户说"系统性排查/大改造"`）。
4. **有没有过度宽泛导致乱激活**？→ 补负向范围与判据。

任一条答不上来或答"否" → 继续补 description，直到四条全过。

### 3. Add scripts (optional)

```bash
#!/usr/bin/env bash
# scripts/setup.sh
set -euo pipefail
echo "Running setup with args: $@"
```

Make scripts executable: `chmod +x scripts/*.sh`

Reference scripts in SKILL.md with relative paths from the skill root:

```markdown
Run setup:
`Shell` tool: `bash scripts/setup.sh <args>` (working directory: the skill root)

Or use `Skill(name=my-skill, action=list_files)` to get the absolute path,
then call `Shell` with the full path from any working directory.
```

### 4. Add references (optional)

Large docs or API specs go in `references/`. Load them with:
```
Skill(name=my-skill, action=load, file=references/api-spec.md)
```

## Updating a Skill

1. `Skill(name=..., action=load)` — read current content
2. `FileReplace`/`FileCreate` tool — modify SKILL.md or other files
3. `Skill(name=..., action=list_files)` — verify file layout

## Writing Guidelines

**Frontmatter:**
- `name`: lowercase with hyphens (e.g. `pdf-editor`)
- `description`: WHAT it does (one clause) + **EVERY activation condition** — this is the
  sole activation trigger. Enumerate all task types, user phrasings/keywords (中英都要),
  artifact/command/error names, and explicit triggers; add negative scope when relevant.
  A generic one-liner is a bug (see《Description: enumerate EVERY activation condition》)  ← 必须写全，不能笼统

**Body:**
- Keep under 300 lines (auto-truncated beyond this)
- Imperative form, concise — only include what the LLM doesn't already know
- **🚫 NEVER use absolute paths** (e.g. `/home/user/...`, `/opt/...`). Use relative paths for internal references (`scripts/run.sh`), environment variables (`$XBOT_SRC`, `$HOME`), or runtime discovery (`Skill(action=list_files)` to get paths). Absolute paths break portability across machines.

**Scripts:**
- Shebangs: `#!/usr/bin/env bash` or `#!/usr/bin/env python3`
- Accept arguments via `$@` or `$1`/`$2` for flexibility
- Use `set -euo pipefail` in bash scripts
