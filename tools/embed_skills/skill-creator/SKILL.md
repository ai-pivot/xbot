---
name: skill-creator
description: Create, update, or delete skills. Use when the user asks to create a new skill, modify an existing skill, package scripts/assets into a skill, or discusses skill design and structure.
---

# Skill Creator

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
3. **Tool loading** — LLM **immediately** calls `load_tools` for all tools listed in the skill's "Required Tools" section
4. **File listing** — `Skill(name=..., action=list_files)` returns full paths of all files in the skill
5. **Execution** — LLM runs scripts via `Shell` tool using the paths from `list_files`

## Creating a Skill

### 1. Discover relevant tools

Before writing SKILL.md, use `search_tools` to find tools the skill will need:

```
search_tools(query="send feishu message")  → finds feishu_send_message, etc.
search_tools(query="github pull request")  → finds mcp_github_create_pr, etc.
```

Include the discovered tool names in the skill body so the LLM knows which tools to `load_tools` after activating the skill.

### 2. Write SKILL.md

```markdown
---
name: my-skill
description: What this skill does and WHEN to activate it. Be specific — this is the only trigger.
---

# My Skill

## Required Tools
After loading this skill, immediately call `load_tools` for these tools:
- feishu_send_message
- feishu_search_wiki

## Instructions
Step-by-step instructions for the LLM...
```

**Critical**: Every skill MUST include a "Required Tools" section listing tools to load. After `Skill(action=load)` returns, the LLM must **immediately** call `load_tools` for all listed tools before doing anything else.

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
2. `Edit` tool — modify SKILL.md or other files
3. `Skill(name=..., action=list_files)` — verify file layout

## Writing Guidelines

**Frontmatter:**
- `name`: lowercase with hyphens (e.g. `pdf-editor`)
- `description`: WHAT it does + WHEN to use it — this is the sole activation trigger

**Body:**
- Keep under 300 lines (auto-truncated beyond this)
- Imperative form, concise — only include what the LLM doesn't already know
- **🚫 NEVER use absolute paths** (e.g. `/home/user/...`, `/opt/...`). Use relative paths for internal references (`scripts/run.sh`), environment variables (`$XBOT_SRC`, `$HOME`), or runtime discovery (`Skill(action=list_files)` to get paths). Absolute paths break portability across machines.

**Scripts:**
- Shebangs: `#!/usr/bin/env bash` or `#!/usr/bin/env python3`
- Accept arguments via `$@` or `$1`/`$2` for flexibility
- Use `set -euo pipefail` in bash scripts
