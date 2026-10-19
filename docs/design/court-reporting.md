# 朝会 / 奏折 / 批红 —— 详细设计

> 状态：设计稿（**不含实现代码**；脚本契约以 schema 形式给出）
> 基线：`origin/master@619ec6c3`
> 工作分支：`feat/court-orchestration`
> 关联：`court-orchestration.md`（总体方案）、`sendmessage-structured.md`（①信封）、`message-sender-id.md`（③）、`group-hardening.md`（④）

---

## 0. 一页纸结论

1. **唯一事实源是文件**：`court/state/**/*.yaml`，有严格 schema、证据强制、单调性约束；汇报不得只转述。
2. **奏折是结构化消息**（`kind=report`），必须携带 `state_ref` + `state_sha256` + `evidence[]`；批红系统先校验哈希再看内容。
3. **批红是确定性规则引擎**（`court/bin/validate_state.py`），规则用 YAML 描述（schema/证据/阈值/白名单/黑名单/单调性/矛盾检测），输入输出都是 JSON，**不调用 LLM 做判定**。
4. **朝会由 cron 驱动**：cron 注入议程 → 各部门写 state → 部门发 `kind=report` → 汇总校验 → 首辅上奏 → 用户 `kind=decision`（准/驳/改）→ 异步回调对应 agent。
5. **`correlation_id` 贯穿全链**：朝会实例 `cor_<date>` → 每份奏折 `cor_<date>/<dept>` → 批红 `reply_to=report.message_id`。
6. **用户批红 = `sender_type=user` 的消息**（③），在目标 agent 会话中以 `role=user` 投影，UI 显示「皇帝批红」。

---

## 1. 目标与非目标

### 1.1 目标

| # | 目标 | 判据 |
|---|---|---|
| G1 | 状态可机器校验、可人审、可 git diff | 所有 state 文件通过 JSON Schema；`validate_state.py` 0 error |
| G2 | 汇报不脱离证据 | 每条 `done/blocked` 任务至少有 1 条存在的 evidence；哈希校验通过 |
| G3 | 批红确定性 | 同一输入两次运行输出逐字节一致；不依赖网络/LLM |
| G4 | 批红可追溯 | 每次批红写 `decision` 消息 + `court/log/decisions/*.jsonl` |
| G5 | 全链异步 | 朝会不阻塞用户；批红结果异步回调 agent |
| G6 | 失败可回滚 | 规则误判可 `--waive` 并记录理由；状态文件可 git revert |

### 1.2 非目标

- 不做 BPM/审批流引擎；不做多级会签。
- 不让 LLM 决定「准/驳」；LLM 只做汇总格式化（可选）。
- 不在 core 解析奏折格式（格式属于 skill/脚本）。
- 不做实时监控告警（阈值规则只在校验时跑）。
- 不实现签名/加密（`signature` 预留）。

---

## 2. 目录与产物

```text
court/
  org.yaml                          # 编制（部门/汇报线/角色）
  schemas/
    state-v1.json                   # state JSON Schema
    report-v1.json                  # report payload JSON Schema
    decision-v1.json                # decision payload JSON Schema
    rules-v1.json                   # 规则文件 JSON Schema
  state/
    departments/<dept>.yaml         # 唯一事实源（每部门一个）
    global.yaml                     # 全局状态（可选）
  log/
    <dept>/<YYYY-MM-DD>.md          # append-only 工作日志
    decisions/<YYYY-MM-DD>.jsonl    # 批红审计流
  reports/
    <YYYY-MM-DD>/<dept>.yaml        # 奏折快照（与 report 消息同源）
    <YYYY-MM-DD>/summary.yaml       # 首辅汇总
  meetings/
    <YYYY-MM-DD>-<topic>.md         # 会议纪要（来自 transcript）
  evidence/
    <dept>/<task-id>/...            # 证据文件（日志/截图/命令输出）
  bin/
    validate_state.py               # 确定性校验
    apply_decision.py               # 批红落地（改/驳）
  templates/
    morning.md                      # 朝会议程
    report.md                       # 奏折模板
```

规则：**只有 owner_agent 能写自己的 state**；首辅只读；批红脚本只读 state、只写 `reports/`/`log/decisions/`。

---

## 3. State 文件 schema（字段级）

### 3.1 顶层

| 字段 | 类型 | 必填 | 约束 |
|---|---|---|---|
| `schema` | string | 是 | 固定 `court/state/v1` |
| `department` | string | 是 | 必须存在于 `court/org.yaml` |
| `owner_agent` | string | 是 | `role:instance`，与 org 一致 |
| `updated_at` | string | 是 | RFC3339；每次写入刷新 |
| `phase` | enum | 是 | `planning`/`executing`/`blocked`/`done` |
| `progress` | integer | 是 | 0..100；**单调不减**（除非 `phase=blocked` 且 `blocked_by` 非空） |
| `objective` | string | 是 | ≤ 500 字 |
| `tasks` | array | 是 | 0..200 项 |
| `risks` | array | 否 | 0..50 项 |
| `handoffs` | array | 否 | 交接记录 |
| `meta` | object | 否 | 扩展（不得影响校验） |

### 3.2 task

| 字段 | 类型 | 必填 | 约束 |
|---|---|---|---|
| `id` | string | 是 | 部门内唯一；`^[a-z0-9][a-z0-9-]{1,63}$` |
| `title` | string | 是 | ≤ 200 |
| `status` | enum | 是 | `todo`/`doing`/`blocked`/`done` |
| `progress` | integer | 是 | 0..100 |
| `owner` | string | 否 | `role:instance`；缺省 = 部门 owner |
| `updated_at` | string | 是 | RFC3339 |
| `evidence` | array | 条件 | `status=done` 时至少 1 条 |
| `blocked_by` | string[] | 条件 | `status=blocked` 时非空；元素为 task id 或外部引用 |
| `next_action` | string | 否 | ≤ 300 |
| `deadline` | string | 否 | RFC3339 |
| `depends_on` | string[] | 否 | task id 列表（跨任务一致性校验） |

### 3.3 evidence

| 字段 | 类型 | 必填 | 约束 |
|---|---|---|---|
| `path` | string | 是 | 相对 `court/` 的路径；**必须存在且非空** |
| `kind` | enum | 是 | `log`/`file`/`command`/`url`/`screenshot` |
| `sha256` | string | 条件 | `kind in (log,file,screenshot)` 时**强制**；64 hex |
| `summary` | string | 否 | ≤ 200 |
| `captured_at` | string | 否 | RFC3339；不得晚于 `updated_at` |

### 3.4 示例

```yaml
schema: court/state/v1
department: gongbu
owner_agent: "gongbu/main"
updated_at: "2026-10-19T10:00:00Z"
phase: executing
progress: 62
objective: "PPU 集群部署"
tasks:
  - id: ppu-deploy-03
    title: "部署 prefill 节点"
    status: doing
    progress: 40
    owner: "gongbu/node-1"
    updated_at: "2026-10-19T09:58:00Z"
    evidence:
      - path: "evidence/gongbu/ppu-deploy-03/deploy.log"
        kind: log
        sha256: "9f2c…"
        captured_at: "2026-10-19T09:57:30Z"
    blocked_by: []
    next_action: "等待 3 个节点就绪后执行 smoke test"
    deadline: "2026-10-20T18:00:00Z"
  - id: ppu-smoke-04
    title: "smoke test"
    status: blocked
    progress: 0
    owner: "gongbu/node-2"
    updated_at: "2026-10-19T09:50:00Z"
    evidence:
      - path: "evidence/gongbu/ppu-smoke-04/blocked.md"
        kind: file
        sha256: "1a2b…"
    blocked_by: ["ppu-deploy-03"]
    next_action: "prefill 就绪后自动排队"
risks:
  - id: r1
    desc: "spot 实例可能被回收"
    mitigation: "预留 on-demand 节点"
```

### 3.5 校验规则（脚本强制）

| 规则 | 级别 | 说明 |
|---|---|---|
| JSON Schema 合法 | block | 缺字段/类型错/枚举错 |
| `department`/`owner_agent` 存在 | block | 与 `org.yaml` 对照 |
| `updated_at` 可解析且 ≤ now+5m | block | 时钟漂移防护 |
| `progress` 单调不减 | block | 与上一快照比较；回退需 `phase=blocked` + `blocked_by` |
| `task.id` 唯一 | block | 部门内 |
| `status=done ⇒ evidence ≥ 1` | block | 证据强制 |
| `evidence.path` 存在 | block | 文件系统检查 |
| `evidence.sha256` 匹配 | block | 重算哈希 |
| `captured_at ≤ updated_at` | block | 逻辑一致 |
| `blocked_by` 引用存在或为外部 URL | warn | 悬空引用 |
| `deadline` 已过且未 done | warn | 超期 |
| `updated_at` 超过 24h | warn | stale |

---

## 4. 奏折（`kind=report`）schema

复用 ① 的 `report` payload（§5.6），补充与 state 的绑定约束：

| 字段 | 必填 | 约束 |
|---|---|---|
| `report_id` | 是 | `rpt_<YYYYMMDD>_<dept>_<n>`；全局唯一 |
| `period.from/to` | 是 | RFC3339；`from < to` |
| `department` | 是 | 与 state 一致 |
| `state_ref` | 是 | `court/state/departments/<dept>.yaml` |
| `state_sha256` | 是 | 读取 state 时计算；**必须与当前文件一致** |
| `summary` | 是 | ≤ 4000 字；不得只写「详见文件」 |
| `evidence` | 是 | ≥ 1；所有 `status=done` 的 task 证据必须被覆盖 |
| `blockers` | 否 | 与 state 的 blocked task 对齐 |
| `asks` | 否 | 内嵌问题（等价 ask_user）；必须带 `question_id` |
| `confidence` | 否 | 0..1，仅自评 |

### 4.1 奏折消息示例

```json
{
  "schema": "xbot.message/v1",
  "kind": "report",
  "from": {"type": "agent", "id": "gongbu/main", "name": "工部"},
  "to": {"type": "agent", "id": "shoufu/main", "name": "首辅"},
  "group_id": "grp_court_daily",
  "correlation_id": "cor_20261019_gongbu",
  "idempotency_key": "gongbu:2026-10-19:rpt_20261019_gongbu_1",
  "payload": {
    "report_id": "rpt_20261019_gongbu_1",
    "period": {"from": "2026-10-18T00:00:00Z", "to": "2026-10-19T00:00:00Z"},
    "department": "gongbu",
    "state_ref": "court/state/departments/gongbu.yaml",
    "state_sha256": "c0ffee…",
    "summary": "PPU 集群 8 节点已部署 6 台；prefill smoke test 阻塞在节点 3 网络策略。",
    "evidence": [
      {"path": "court/evidence/gongbu/ppu-deploy-03/deploy.log", "kind": "log", "sha256": "9f2c…"},
      {"path": "court/evidence/gongbu/ppu-deploy-03/nodes.txt", "kind": "file", "sha256": "7d1e…"}
    ],
    "blockers": [
      {"id": "ppu-deploy-03", "desc": "节点 3 网络策略未放行", "blocked_by": ["infra-netpol"]}
    ],
    "asks": [
      {"question_id": "q1", "question": "是否批准临时放开节点 3 网络策略？", "options": ["准", "驳"]}
    ]
  },
  "text": "工部奏：PPU 集群进度 62%，prefill 阻塞于节点 3 网络策略。"
}
```

### 4.2 校验（规则引擎 `report` 类）

1. `state_ref` 文件存在，`state_sha256` 与当前文件一致（防转述/防篡改）。
2. `report.department` = `state.department`。
3. `summary` 非空，且不得仅包含 `state_ref`。
4. `report.evidence` ⊇ `state.tasks[*].evidence`（按 path 集合）。
5. `blockers` 与 `state.tasks[status=blocked]` 一一对应（id 集合相等）。
6. `asks[].question_id` 唯一；有 `asks` 时必须能在 `payload` 中找到对应结构（或内嵌 `ask_user`）。

---

## 5. 批红规则引擎

### 5.1 定位

- **确定性**：纯函数式脚本；输入文件 + 规则文件 → JSON 报告；无网络、无 LLM、无随机。
- **可安装/可替换**：规则是数据（YAML），脚本是解释器；朝廷可换规则不换 core。
- **双层**：`block` 级规则决定「能否上奏」；`warn` 级只提示，不阻断。

### 5.2 执行契约

```text
court/bin/validate_state.py
  --root <repo/court>
  --date 2026-10-19
  --rules court/rules/court-rules.yaml
  --report court/reports/2026-10-19/gongbu.yaml
  --json
  [--waive RULE_ID --reason "..."]   # 记录豁免，输出 waivers[]
  [--baseline <previous-state-dir>]  # 单调性比较
```

输出（stdout，JSON）：

```json
{
  "schema": "court/validation/v1",
  "generated_at": "2026-10-19T10:05:00Z",
  "inputs": {
    "state_files": ["court/state/departments/gongbu.yaml"],
    "report": "court/reports/2026-10-19/gongbu.yaml",
    "rules_sha256": "…"
  },
  "verdict": "pass|warn|block",
  "results": [
    {
      "rule_id": "state.schema",
      "severity": "block",
      "status": "pass",
      "subject": "court/state/departments/gongbu.yaml",
      "message": "",
      "evidence": []
    },
    {
      "rule_id": "evidence.hash",
      "severity": "block",
      "status": "fail",
      "subject": "court/evidence/gongbu/ppu-deploy-03/deploy.log",
      "message": "sha256 mismatch: expected 9f2c… got 0bad…",
      "evidence": ["court/state/departments/gongbu.yaml:12"]
    }
  ],
  "waivers": [],
  "exit_code": 2
}
```

退出码：`0=pass`、`1=warn`、`2=block`、`3=内部错误`。

### 5.3 规则文件 schema

```yaml
schema: court/rules/v1
defaults:
  on_missing: block          # block|warn|skip
  evidence_required_for: [done, blocked]
rules:
  - id: state.schema
    type: schema
    severity: block
    target: "state/departments/*.yaml"
    schema_ref: "schemas/state-v1.json"

  - id: state.owner_exists
    type: cross_ref
    severity: block
    left: "state.department"
    right: "org.departments[*].id"

  - id: progress.monotonic
    type: monotonic
    severity: block
    field: "progress"
    compare: "previous_snapshot"
    allow_decrease_when:
      phase: blocked
      blocked_by_non_empty: true

  - id: evidence.exists
    type: evidence
    severity: block
    require: [path]
    root: "."

  - id: evidence.hash
    type: hash
    severity: block
    algorithms: [sha256]
    when:
      kind: [log, file, screenshot]

  - id: evidence.freshness
    type: freshness
    severity: warn
    field: captured_at
    max_age: "24h"

  - id: report.state_hash
    type: hash_match
    severity: block
    left: "report.state_sha256"
    right_file: "report.state_ref"

  - id: report.summary_nonempty
    type: required
    severity: block
    field: "report.summary"
    min_len: 10
    forbid_only_refs: true

  - id: report.evidence_superset
    type: cross_ref
    severity: block
    left: "report.evidence[*].path"
    right: "state.tasks[*].evidence[*].path"
    mode: superset

  - id: disk.threshold
    type: threshold
    severity: warn
    metric: "evidence.command.df_percent"
    op: ">="
    value: 85

  - id: commands.allowlist
    type: allowlist
    severity: block
    field: "evidence.command.argv[0]"
    allow: ["bash", "python3", "kubectl", "systemctl", "curl"]
    deny: ["rm", "dd", "mkfs", "shutdown", "reboot"]

  - id: paths.blacklist
    type: blacklist
    severity: block
    field: "report.evidence[*].path"
    deny_globs: ["/etc/shadow", "**/*.pem", "**/id_rsa", ".env"]

  - id: contradiction.progress_phase
    type: contradiction
    severity: block
    when:
      phase: done
      progress: "<100"
    message: "phase=done but progress<100"

  - id: contradiction.task_done_evidence
    type: contradiction
    severity: block
    when:
      task.status: done
      task.evidence: empty
    message: "done task without evidence"

  - id: contradiction.blocked_without_reason
    type: contradiction
    severity: block
    when:
      task.status: blocked
      task.blocked_by: empty
    message: "blocked task without blocked_by"

  - id: contradiction.timestamps
    type: contradiction
    severity: block
    when:
      task.updated_at: "> department.updated_at"
    message: "task updated after department snapshot"

  - id: dedupe.report_id
    type: unique
    severity: block
    field: "report.report_id"
    scope: "reports/**/*.yaml"
```

### 5.4 规则类型（字段级）

| type | 输入 | 语义 | 失败输出 |
|---|---|---|---|
| `schema` | glob + schema_ref | JSON Schema 校验 | 逐字段错误 |
| `cross_ref` | left/right 路径 | 引用存在/集合关系（`subset`/`superset`/`equal`） | 缺失/多余项 |
| `monotonic` | field + compare | 与上一快照比较；条件允许回退 | 前后值 |
| `evidence` | require 字段 | 证据必填/路径存在/非空 | 缺失字段 |
| `hash` | algorithms + when | 重算文件哈希 | expected/actual |
| `hash_match` | left/right_file | 消息哈希 = 文件当前哈希 | 两值 |
| `freshness` | field + max_age | 时间新鲜度 | 年龄 |
| `required` | field + min_len | 必填/长度/禁止仅引用 | 实际值 |
| `threshold` | metric + op + value | 指标阈值 | 实际值 |
| `allowlist` | field + allow/deny | 白名单（不在 allow 即失败）/黑名单 | 违规值 |
| `blacklist` | field + deny_globs | 路径/模式黑名单 | 匹配项 |
| `contradiction` | when 条件 | 条件同时成立即矛盾 | 条件快照 |
| `unique` | field + scope | 全局唯一 | 重复项 |
| `regex` | field + pattern | 格式校验 | 实际值 |
| `custom` | script + args | 外部确定性脚本（stdin JSON→stdout JSON） | 脚本输出 |

### 5.5 白名单/黑名单 schema

```yaml
allowlist:
  paths: ["court/state/**", "court/evidence/**", "court/reports/**"]
  commands: ["bash", "python3", "kubectl", "systemctl", "curl", "git"]
  models: ["gpt-*", "claude-*"]          # 仅审计用
  recipients: ["agent:shoufu/*", "user:*"]
blacklist:
  paths: ["**/*.pem", "**/id_rsa", "**/.env", "/etc/shadow"]
  commands: ["rm", "dd", "mkfs", "shutdown", "reboot", ":(){:|:&};:"]
  patterns: ["AKIA[0-9A-Z]{16}", "sk-[A-Za-z0-9]{20,}"]
```

- 白名单/黑名单**只作用于校验输入**（证据路径、命令 argv、报告字段），不执行命令。
- 命中黑名单 = `block`；不在白名单 = `block`（默认拒绝）。
- 规则文件自身也过 `rules-v1.json` schema；`rules_sha256` 写入输出，保证可复现。

### 5.6 矛盾检测清单（本期必须实现）

| ID | 条件 | 级别 |
|---|---|---|
| C1 | `phase=done` 但 `progress<100` | block |
| C2 | `task.status=done` 但 `evidence` 为空 | block |
| C3 | `task.status=blocked` 但 `blocked_by` 为空 | block |
| C4 | `task.updated_at > department.updated_at` | block |
| C5 | `report.state_sha256 != sha256(state_ref)` | block |
| C6 | `report.department != state.department` | block |
| C7 | `report.evidence` 未覆盖某个 done task 的证据 | block |
| C8 | 同一 `report_id` 出现两次且内容不同 | block |
| C9 | `progress` 下降但 `phase != blocked` | block |
| C10 | `deadline < now` 且 `status != done` | warn |
| C11 | `owner` 不在 `org.yaml` | block |
| C12 | `evidence.path` 指向 `court/` 之外 | warn（可配 block） |

### 5.7 批红判定（确定性）

```text
if any block failed and not waived:  verdict = reject（驳）
elif any warn failed:                verdict = approve_with_notes（准，附注）
else:                                verdict = approve（准）
user may override:                   verdict = modify（改，附 modifications）
```

> 规则引擎只产出「建议裁定」；最终 `decision` 由用户签发。脚本永不直接改 state。

---

## 6. 批红（`kind=decision`）

### 6.1 payload

复用 ① §5.8：

```json
{
  "decision_id": "dec_20261019_0001",
  "target_report_id": "rpt_20261019_gongbu_1",
  "target_message_id": "msg_01J...",
  "verdict": "modify",
  "comments": "先扩 4 节点，观察 24h 再决定剩余 4 台。",
  "modifications": [
    {"path": "court/state/departments/gongbu.yaml#tasks[0].next_action",
     "op": "set", "value": "先扩 4 节点并观察 24h"}
  ],
  "signature": ""
}
```

### 6.2 落地

- **准（approve）**：不修改 state；记录 decision；agent 继续执行。
- **驳（reject）**：不修改 state；agent 必须在下一次奏折中回应驳回理由（`causation_id=decision.message_id`）。
- **改（modify）**：`court/bin/apply_decision.py` 按 `modifications` 做受限 patch（只允许 `court/**`，只允许 `set/append/remove`），生成新 state + 新 `updated_at`，并写 `log/decisions/*.jsonl`。
- 所有 decision 都写审计行：

```json
{"at":"2026-10-19T12:04:11Z","decision_id":"dec_...","report_id":"rpt_...","verdict":"modify","by":"u_10086","message_id":"msg_...","rules_sha256":"...","state_sha256":"..."}
```

### 6.3 与 ③ 的关系

- 批红消息 `from.type=user`, `from.id=<user id>` → 投影行 `sender_type='user'`, `sender_id=<user id>`。
- 目标 agent 的下一轮上下文看到 `role=user` 的批红文本；UI 显示「皇帝批红」徽标。
- 群组场景：`group_id` 必填，transcript 记录决策事件；非成员不可见（按 visibility）。

---

## 7. 朝会流程（cron 驱动）

### 7.1 cron 配置

复用现有 `cron_jobs` 表（`storage/sqlite/schema.go:252-267`）与调度器（`cron/scheduler.go:205-248`）。

```json
{
  "name": "court-morning",
  "cron_expr": "0 8 * * *",
  "channel": "agent",
  "chat_id": "shoufu/main",
  "sender_id": "<owner>",
  "message": "【朝会】读取 court/templates/morning.md，按议程主持今日朝会。",
  "timezone": "Asia/Shanghai"
}
```

### 7.2 议程模板（`court/templates/morning.md`）

```markdown
# 朝会 {{date}}

## 议程
1. 各部门 08:30 前更新 court/state/departments/<dept>.yaml
2. 08:45 前提交奏折（SendMessage kind=report，correlation_id=cor_{{date}}_{{dept}}）
3. 首辅运行 court/bin/validate_state.py 汇总
4. 09:00 上奏（SendMessage kind=report 给 user）
5. 皇帝批红（kind=decision）
6. 首辅分发批红回调

## 约束
- 无 evidence 的 done 不算完成
- 汇报必须带 state_sha256
- 不允许在奏折里粘贴大段日志（给路径+哈希）
```

### 7.3 流程

```text
08:00 cron 触发
  └─ 首辅 agent 收到朝会 prompt
       ├─ 读 org.yaml / state/*.yaml
       ├─ 向各部门发 SendMessage(kind=command, command=report_now)
       │    correlation_id=cor_<date>_<dept>
       ├─ 部门 agent 更新 state（写文件，刷 updated_at）
       ├─ 部门发 SendMessage(kind=report) 给首辅
       ├─ 首辅运行 validate_state.py
       ├─ 首辅汇总 summary.yaml
       └─ 首辅发 SendMessage(kind=report) 给 user（上奏）
09:00 用户收到奏折
  ├─ 用户批红 SendMessage(kind=decision, reply_to=report.message_id)
  └─ 回调注入各部门 agent（异步）
       ├─ approve → 继续执行
       ├─ reject  → 下轮奏折必须回应
       └─ modify  → apply_decision.py 改 state 后继续
```

### 7.4 幂等与重试

- 朝会实例 `correlation_id=cor_<date>`；同一天重复触发只处理一次（`idempotency_key=court:<date>:morning`）。
- 部门奏折 `idempotency_key=<dept>:<date>:<report_id>`。
- 用户批红 `idempotency_key=decision:<report_id>`；重复提交返回首次结果。
- 超时：部门 30 分钟未交 → 首辅发 `kind=event(severity=warn)`，并在汇总中标记 `missing`。
- 重试：投递失败 3 次；仍失败写 `log/decisions` 并告警。

---

## 8. 端到端时序（ASCII，correlation_id 贯穿）

```text
 cron        shoufu(agent)        departments(agent)          validator        user
  │               │                     │                       │              │
  │ 08:00 fire    │                     │                       │              │
  ├──────────────▶│                     │                       │              │
  │               │ SendMessage(command,report_now)              │              │
  │               │ cor=cor_1019_gongbu│                       │              │
  │               ├────────────────────▶│                       │              │
  │               │                     │ 写 state/*.yaml       │              │
  │               │                     │ (updated_at 刷新)      │              │
  │               │                     │                       │              │
  │               │                     │ SendMessage(report)   │              │
  │               │                     │ cor=cor_1019_gongbu   │              │
  │               │◀────────────────────┤ reply_to=command.msg  │              │
  │               │                     │                       │              │
  │               │ validate_state.py ─────────────────────────▶│              │
  │               │                     │                       │ 读 state     │
  │               │◀────────────────────────────────────────────┤ JSON verdict │
  │               │                     │                       │              │
  │               │ 汇总 summary.yaml   │                       │              │
  │               │ SendMessage(report) │                       │              │
  │               │ cor=cor_1019_summary│                       │              │
  │               ├─────────────────────────────────────────────────────────────▶│
  │               │                     │                       │              │
  │               │                     │                       │   批红 decision
  │               │                     │                       │◀─────────────┤
  │               │◀────────────────────────────────────────────────────────────┤
  │               │ 回调注入（异步）     │                       │  reply_to=   │
  │               ├────────────────────▶│                       │  report.msg  │
  │               │ (approve/reject/modify)                      │              │
  │               │                     │                       │              │
  │               │ 若 modify: apply_decision.py ───────────────▶│              │
  │               │                     │                       │ 写 state     │
  │               │                     │                       │              │
  ▼               ▼                     ▼                       ▼              ▼

correlation 树：
cor_20261019
├─ cor_20261019_gongbu
│   ├─ msg(command)  → msg(report) → msg(decision)
│   └─ msg(apply_decision receipt)
├─ cor_20261019_hubu
└─ cor_20261019_summary → msg(report) → msg(decision)
```

---

## 9. 迁移策略

### 9.1 从现有方案迁移

| 现有 | 新 |
|---|---|
| `court-orchestration.md` §5.6 state schema | 本文件 §3（字段级 + JSonschema + 校验规则） |
| 「批红 = 确定性脚本 + 汇总 agent」 | §5 规则引擎 schema + §5.6 矛盾清单 |
| 朝会 = cron | §7 cron 配置 + 议程模板 |
| 无结构化消息 | ① 的 envelope；report/decision kind |

### 9.2 落地顺序

1. **脚本先行**（零 core 改动）：`state-v1.json`、`rules-v1.yaml`、`validate_state.py`、`apply_decision.py`。
2. **模板/议程**：`templates/morning.md`、`report.md`。
3. **cron 注册**：`court-morning` job。
4. **结构化消息**：依赖 ① 落地；在此之前可用纯文本 + 文件引用降级运行。
5. **UI**：奏折/批红卡片（依赖 ① 的 structured_json）。

### 9.3 回滚

| 场景 | 回滚 |
|---|---|
| 规则误判 | `--waive RULE_ID --reason`，记录 `waivers[]`；不改规则 |
| 批红错误 | `apply_decision.py --revert <decision_id>` 用备份 state 还原 |
| 朝会 cron 噪音 | 暂停 cron job（`cron` 工具/DB） |
| 结构化消息未就绪 | 降级为文本消息 + 文件路径 |

---

## 10. 风险与回滚

| 风险 | 影响 | 缓解 | 回滚 |
|---|---|---|---|
| 规则过严导致全员 block | 朝会无法上奏 | `warn`/`--waive`；规则分级；先 shadow 跑一周 | 放宽规则 |
| state 文件并发写 | 数据损坏/哈希不一致 | 原子写（tmp+rename）；建议单写者；`updated_at` 校验 | git revert |
| 证据哈希计算慢 | 朝会延迟 | 只对声明的 evidence 计算；大文件用 `sha256sum` 流式 | 跳过 hash（warn） |
| 用户批红丢失 | agent 不继续 | decision 持久化 + 回调重试 + 审计流 | 重新签发 |
| 汇总 agent 幻觉 | 奏折失真 | `state_sha256` + `evidence` 强制；首辅只读原始文件 | 以 state 为准 |
| cron 时区错误 | 朝会错时 | 显式 `timezone`；启动校验 | 改 cron |
| 规则文件被篡改 | 批红失真 | `rules_sha256` 写入输出；git 审计 | 恢复规则 |

---

## 11. 验收判据

### 11.1 State

1. 合法 state 通过 `validate_state.py`，退出码 0。
2. 缺 `evidence` 的 done task → block（C2）。
3. `progress` 回退且非 blocked → block（C9）。
4. `sha256` 不匹配 → block（evidence.hash）。
5. 同一 state 两次校验输出逐字节一致（除 `generated_at`）。

### 11.2 Report

6. `state_sha256` 与文件不一致 → block（C5）。
7. `report.evidence` 未覆盖 done task 证据 → block（C7）。
8. 同一 `report_id` 重复且内容不同 → block（C8）。
9. 奏折缺 `summary` → block。

### 11.3 Decision

10. `approve` 不改 state；`reject` 不改 state；`modify` 只改允许路径。
11. 重复提交同一 decision → 幂等，不重复回调。
12. 批红消息投影行 `sender_type=user`, `sender_id=<user>`。
13. `apply_decision.py --revert` 能恢复到 decision 前状态。

### 11.4 端到端

14. 模拟 cron 触发：首辅发 command → 部门写 state → 发 report → 校验 → 上奏 → 用户 decision → 回调注入部门会话。
15. 全链 `correlation_id` 可从 `agent_messages` 反查（`cor_20261019_gongbu` 链完整）。
16. 部门超时未交 → 汇总标记 `missing` + warn 事件。

### 11.5 回归

17. 不依赖 ① 时，纯文本降级路径可用。
18. `validate_state.py` 无网络调用（断网运行通过）。

---

## 12. 受影响代码/文件清单

### 12.1 新增（数据/脚本，非 core）

| 文件 | 作用 |
|---|---|
| `court/schemas/state-v1.json` | state JSON Schema |
| `court/schemas/report-v1.json` | report payload schema |
| `court/schemas/decision-v1.json` | decision payload schema |
| `court/schemas/rules-v1.json` | 规则文件 schema |
| `court/rules/court-rules.yaml` | 规则实例 |
| `court/bin/validate_state.py` | 确定性校验 |
| `court/bin/apply_decision.py` | 批红落地/回滚 |
| `court/templates/morning.md` | 朝会议程 |
| `court/templates/report.md` | 奏折模板 |
| `court/org.yaml`（已有示例） | 编制 |
| `court/state/departments/*.yaml` | 运行时数据 |

### 12.2 依赖 core 的能力（由其它文档交付）

| 能力 | 来源 |
|---|---|
| 结构化消息 envelope / report / decision | `sendmessage-structured.md` |
| sender_id 落库与展示 | `message-sender-id.md` |
| 群组 transcript / 轮次 | `group-hardening.md` |
| cron 注入 | 现有 `cron/scheduler.go`、`tools/cron.go` |

### 12.3 需要修改的现有文件（最小）

| 文件:行 | 改动 |
|---|---|
| `docs/design/court-orchestration.md` §5.6 | 指向本文件 |
| `docs/agent/tools.md`（若描述奏折） | 更新 |
| `prompt/`（朝会提示词） | 新增/更新朝会议程提示 |
| `.xbot/agents/*.md`（部门 role） | 增加「写 state / 发 report」工作协议 |

---

## 13. 开放问题（需用户拍板）

1. **批红是否允许「部分准」**：本设计 `modify` 通过 modifications 表达；若需要逐 task 准/驳，需要扩展 decision schema（`task_verdicts[]`）。
2. **state 文件是否进 git**：本设计假设进（可 diff/回滚）；若运行时高频写入，建议 `court/state/` 单独分支/仓库。
3. **朝会频率/时区**：默认每日 08:00 Asia/Shanghai；需要确认。
4. **规则引擎用 Python 还是 Go**：本设计用 Python（零编译、易改）；若要求单二进制，可改为 `xbot court validate` 子命令。
5. **批红签名**：预留 `signature`，本期不校验；是否需要接入现有 crypto 体系？
