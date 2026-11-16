# HANDOVER 2026-11-07 — memory provider 重做 + SSE 凭据过滤（重启前交接）

> 本会话（2026-11-06/07）结束于一次**自我重启**：xbot-cli serve (PID 3640473, smith)
> 被替换为新二进制。以下是重启后必须核对的事实与待办。

## 一、本次落地的改动（两个 commit，待合并进主干）

| commit | 位置 | 内容 |
|---|---|---|
| （主工作树）`fix(session+web)` | `/home/smith/src/xbot` | ① SleepSession（TTL 到期降级休眠保库，不再 DestroySession）② `channel/web/sse_secret_filter.go`（SSE 出站只过滤 tool 输出物的固定格式凭据）③ `protocol/secretfmt.go`（9 种固定格式：ghp_/github_pat_/gh[ousr]_/sk-/AKIA/xox/AIza/glpat/JWT）④ 飞书删除内容级扫描脱敏（pwd/signature/session_id 误杀），title/args 保持原文 |
| `d55d181a` | `/home/smith/src/xbot-memredo`（分支 `fix/memory-provider-redo`） | memory 二段式（ProposeMemory/ConfirmProposal/memory_confirm 工具）+ core 机制彻底移除（关联注入删除、core_memory_delete/clear 新增）+ 写入校验层（长度/注入模式/草稿标记/西里尔片假名/去重）+ 提取 prompt 收紧。21 文件 +1663/-309，7 组 mutation 自证 |

**合并方式**：主工作树 `git cherry-pick d55d181a`（与本地改动零文件重叠，无冲突预期）。

## 二、重启后必验（缺一不算完）

```bash
# 1. 服务健康（新进程 PID != 旧 3640473）
ps -eo pid,etime,args | grep "xbot-cli serve" | grep smith
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/health   # 端口以 config 为准

# 2. core 段消失：新会话 system prompt 不得含 "## Core"
#    （读取函数与注入代码已删；MEMORY.md 已隔离到 ~/.xbot/memory/_quarantine/，50 份）

# 3. memory 二段式：memory_add 返回 PROPOSAL（不落库），memory_confirm 后落库
sqlite3 ~/.xbot/xbot.db "SELECT COUNT(*) FROM xbot_long_term_memories"   # add 前后相同

# 4. SSE 过滤：前端看 tool 输出里 ghp_<36> 应变 ***；模型文本/args 不变
#    单测：go test ./channel/web/ -run "RedactFixed|FilterProgress" -v
```

## 三、关键事实（换会话必读）

### G2 / DPSK 部署（spot 18.61.184.96，勿 stop）
- **部署形态三开关（commit f0f460f56f）**：`SGLANG_SERIAL_LORA_H_IN_DISPATCH=1` + `SGLANG_SERIAL_LORA_DELTA_INKERNEL=0` + `DG_DETERMINISTIC_DISPATCH=1`。漏任一 = 静默退化（死循环/输出不确定）。当前 cg2 unit 已按此配置验证 PASS。
- **正确性闭环（11-07 已验）**：LoRA 3×600tok sha 全同 `2da0ffb2`；verify_da 12 次唯一 lp0=-2.233472（base -0.501661）。
- **TPOT 现状（工部 11-07 同形态对照）**：lora 94-95ms/step vs base 14-16ms；**INKERNEL 开关无关**（差 1.1%）；profiler 定位：armed mega_moe 同一编译实例下 base 367µs vs lora **4128µs**，≈19.3 次/步 ×4.13ms ≈ **80ms/步来自 kernel 内 armed 路径**。prem 实验已到 prem50+（实验方在跑）。
- **严禁 systemd 起 SGLang**（皇上钦定）：用 `nohup setsid ... & disown`。
- 探针：`SGLANG_SERIAL_{H9_EVERY,RANK_PROBE,DEBUG_MASK}` 必须为 0（否则 per_step 恒定 +8ms 假慢）。

### 记忆系统事故（本次根因，已修）
- `updateCoreSummary`（memory/xbot/xbot.go:1314）曾把 LLM 原始输出零校验写进 `MEMORY.md` → 27 轮累积 894 行，含注入探针串（`rm -rf /`、`ignore all previous instructions`）+ 乱码 + 内心独白草稿。已整机制删除 + 50 份文件隔离。
- 注入串来源：曾作为一条用户输入进入会话，被 core-summary 维护任务当素材吸收；`session_messages` / `cron_jobs` 主库均无命中（已清理）。
- 记忆写入现在：global 走 proposal→confirm；session 直写但要过校验层（长度 2000/注入模式/草稿标记/西里尔假名/去重 Jaccard≥0.8）。

## 四、待办

- [ ] cherry-pick d55d181a → 构建 → 重启 → 跑 §二 四条
- [ ] 重启后确认 sleep session 修复生效（idle 会话不再被 TTL 硬删）
- [ ] TPOT：armed mega_moe 内核内分段归因（80ms/步拆解）——唯一有硬数据支撑的优化方向
- [ ] prem 系列实验（实验方）出结果后做正式验收
