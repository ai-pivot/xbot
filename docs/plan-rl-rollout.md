# 计划：xbot-cli 作为 RL Rollout Engine — P0（参数注入 + logprobs 全链路高效落盘）

> 生成时间：2026-08-22
> 状态：待确认
> 范围：P0 = `--llm-params`（采样参数/extra body/extra headers）+ **token-level logprobs 采集、二进制高效存储、npz 导出** + 导出 usage 死字段修复。P1（finish_reason/退出码/reward 回填/per-message token 对齐）另立计划。

---

## 1. 设计依据（两个不可绕过的技术事实）

### 1.1 Tokenization 非双射 —— logprobs 必须在 rollout 时记录，不能事后重算

Byte-level BPE 的 text→token **不是函数**：切分依赖合并路径。词表中 `(`、`\`、`\(` 都是独立 token；模型实际生成的是单 token `\(`，但训练侧拿最终文本重新 encode，贪心路径可能切出 `\` + `(` 两个 token。两个 token 的 logprob 之积 ≠ 原单 token 的 logprob，且**序列本身不同**——PPO/GRPO 的 importance ratio `π_θnew(a)/π_θold(a)` 要求分子分母作用在**同一 token 序列**上，re-tokenization 之后分母根本对不上。

此外即便切分巧合一致，rollout 记录的也是**采样分布**下的 logprob（`log softmax(logits/T)`，含 temperature/top_p/top_k 作用后）。没有原始 logits 就无法从文本复原；而有 logits 的 forward 重算成本等于再跑一遍推理。因此：

> **数据契约的核心：rollout 引擎必须原样保存模型实际生成的 token ids + 采样时 logprob。** 文本与 ids 的双向校验（decode(ids)==content）作为 invariant 附带保存，用于自检而非重建。

### 1.2 存储效率 —— 不存 JSON 字符串

每 token 最小数据 = `(token_id int32, logprob float32)` = **8 bytes**。JSON 编码同样的数据（`{"id":151660,"lp":-0.4213}`）≈ 30+ bytes 且解析慢；SQLite 热表（`iteration_history` 前端每次加载迭代历史都读）塞 MB 级 TEXT 会拖垮查询。

体积估算（chosen-only）：
| 场景 | token 数 | 8B/token |
|---|---|---|
| 单迭代（2K token 输出） | 2,048 | 16 KB |
| 单 turn（50 迭代） | ~100K | ~800 KB |
| 单批（512 case） | ~51M | ~400 MB（npz，训练侧 np.load 零解析） |

float32 而非 float16：ratio 计算对 logprob 精度敏感，f16 的 ~3 位有效数字不可接受。

---

## 2. 数据契约

### 2.1 运行时存储：新表 `iteration_logprobs`（v59 migration）

与 `iteration_history` **分表**——热表加载迭代历史时不拖 logprobs；本表只在写入与导出时触碰。

```sql
CREATE TABLE iteration_logprobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id INTEGER NOT NULL,
  turn_id INTEGER NOT NULL,
  iteration INTEGER NOT NULL,
  token_count INTEGER NOT NULL,
  ids BLOB NOT NULL,         -- little-endian int32[token_count]，模型实际生成的 token id 序列
  logprobs BLOB NOT NULL,    -- little-endian float32[token_count]，采样分布下的 logprob（含 T 作用）
  top_k INTEGER DEFAULT 0,   -- 0 = 未存备选；>0 时下列两列有效
  top_ids BLOB,              -- int32[token_count*top_k]（按 token 顺序平铺）
  top_lps BLOB,              -- float32[token_count*top_k]
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(tenant_id, turn_id, iteration)
);
```

BLOB 头部不带 magic（类型由列语义保证，省 8 字节/token_count 的开销可忽略正确性收益；DB 层 roundtrip 由单测守护字节序）。

### 2.2 导出：主 JSON + 旁路 npz

`--export-after out.session.json` 且存在 logprobs 数据时：

- 同目录写 **`out.logprobs.npz`**（zip 容器内的裸 .npy 数组；Go 用标准库 `archive/zip` 手写 .npy header——npy 格式仅 magic+version+dict header+raw bytes，约 60 行，无第三方依赖）。npz 训练侧 `np.load` 零解析成本，不压缩（float 数组压缩率低，徒增 CPU）。
- entry 命名：`t{turn}_i{iter}_ids` / `t{turn}_i{iter}_lp`；top_k>0 时附 `t{turn}_i{iter}_tid` / `t{turn}_i{iter}_tlp`；每迭代另存 `t{turn}_i{iter}_txt`（string 数组，= 该次 LLM 调用的完整 content 文本）作 decode 自检。
- 主 JSON 顶层加**轻量元数据**（不含本体）：
```json
"logprobs": {
  "file": "out.logprobs.npz",
  "model": "glm-5.2",
  "iterations": [
    {"turn_id": 3, "iteration": 1, "token_count": 2048,
     "lp_sha1": "…", "covers": "content", "top_k": 0}
  ]
}
```
- 单文件契约升级为「主 JSON + 可选 npz」：`bench.sh` 与既有 `--session-dir` 流程零改动；训练侧按 npz 存在性适配。
- SIGINT/SIGTERM 优雅退出路径与正常完成路径**同一时机**写 npz（`--export-after` 已有的两处调用点）。

### 2.3 已知覆盖限制（写进契约，训练侧须知）

| 段 | logprobs 覆盖 | 说明 |
|---|---|---|
| content 文本 | ✅ | OpenAI/sglang 协议 `choices[].logprobs.content[]` |
| tool_calls arguments | ❌（协议不返回） | ratio 校验只能覆盖文本段；tool_calls 段训练侧自行 mask |
| reasoning_content | 待 M0 spike 确认 | DeepSeek/GLM 实现不一，spike 定契约字段 `covers` |
| `lp_sha1` | — | ids/lp 数组字节 SHA1，训练加载时完整性校验 |

---

## 3. 全链路改动清单（文件级）

### M1 — llm 层：RequestParams + OpenAI 兼容路径注入与解析

| 文件 | 改动 |
|---|---|
| `llm/types.go` | 新增 `RequestParams`（全指针字段，nil=不设置，保证 nil 时请求与现状逐字节一致）：`Temperature *float64; TopP *float64; TopK *int; Logprobs *bool; TopLogprobs *int; Seed *int64; Stop []string; MaxTokens *int; ExtraBody map[string]any; ExtraHeaders map[string]string`。`LLMResponse` 加 `Logprobs *TokenLogprobs`（`IDs []int32; Logprobs []float32; TopK int; TopIDs []int32; TopLogprobs []float32`）。`StreamEvent` 加 `EventLogprob` 类型（增量 ids+lp，只进聚合器**不广播前端**） |
| `llm/interface.go` | 接口新增 `GenerateStreamAndCollectWithParams(ctx, req *GenerateRequest) (*LLMResponse, error)`；`GenerateRequest{Model, Messages, Tools, ThinkingMode, Params *RequestParams}`。现有方法保留为兼容包装（Params=nil）——RetryLLM/ProxyLLM/MockLLM/压缩管线零改动 |
| `llm/openai.go` | `buildParams` 合并 Params：`Temperature/TopP/Logprobs/TopLogprobs/Seed/Stop/MaxTokens` 映射 SDK 字段（per-call，SDK 天然支持）；`ExtraBody` 经 `option.WithJSONSet`、`ExtraHeaders` 经 `option.WithHeader` 追加进两个调用点（非流式 Generate ~:897、流式 ~:1045）。与 thinking JSON 通道（Format 3）冲突时 **RequestParams 覆盖**。非流式 `Generate` 读取 `choice.Logprobs`（SDK 已解析，现丢弃处 ~:934）；流式 `processStream` 聚合每 chunk 的 `choice.Logprobs.Content[]` 增量（~:1100） |
| `llm/retry.go` | `GenerateStreamAndCollectWithParams` 透传（重试语义不变） |
| `llm/` 单测 | httptest mock：SSE 吐含 logprobs chunk → 断言 ids/lp 顺序聚合、content 对齐、无 logprobs 时行为不变；`ExtraBody/ExtraHeaders` 进了请求体/头；Params=nil 字节级回归 |

范围裁剪：Anthropic（无 logprobs API）、Responses API、ProxyLLM（remote runner）本里程碑不实现注入（结构体字段预留，方法返回「not supported」），RL bench 全走 `--local` 的 OpenAI 兼容路径。

### M2 — engine 层：RunConfig 携带 + 迭代快照

| 文件 | 改动 |
|---|---|
| `agent/engine.go` | `RunConfig.LLMParams *llm.RequestParams`；`IterationSnapshot.Logprobs *llm.TokenLogprobs` |
| `agent/engine.go` `generateResponse` | Params 非空走 `GenerateStreamAndCollectWithParams` |
| `agent/engine_run.go` `callLLM` | response.Logprobs 存入 `structuredProgress`（供快照）；`snapshotCompletedIteration`（engine_run_tools.go）带出 |

### M3 — 存储与导出

| 文件 | 改动 |
|---|---|
| `storage/sqlite/schema.go` + `migrations.go` | v59：`iteration_logprobs` 建表（**migration 与 schema.go CREATE TABLE/`INSERT INTO schema_version` 三处同步 + 幂等**——v58 的教训）。`TenantSession` API：`SaveIterationLogprobs(tenantID, turnID, iter, *TokenLogprobs)`（BLOB 打包 little-endian）、`GetAllIterationLogprobs(tenantID)` |
| `internal/npy`（新包） | `WriteArray(w io.Writer, dtype, shape, data)` + zip 封装 `WriteNPZ(path, map[string]Array)`；roundtrip 单测 |
| `agent/agent_backend_methods.go` | `GetExportIterations` 旁取 logprobs；`--export-after` 两处调用点（正常+SIGINT）写 npz + 元数据 |
| 顺手修 | `protocol/session_export.go` `usage` 死字段：从 `iteration_history.tokens` 汇总 output、`tenant_state.last_prompt_tokens` 填 input（现在恒 0，误导训练脚本） |

### M4 — CLI 层

| 位置 | 改动 |
|---|---|
| `cmd/xbot-cli/main.go` | `--llm-params '<json>'`（schema 即 RequestParams 字段名 snake_case）+ 环境变量 `XBOT_LLM_PARAMS`；解析进 `RunConfig.LLMParams`（local 直达；remote 模式 P0 不支持，显式报错） |
| `subscription_models` | 加 `request_params TEXT` 列（per-model 持久化）；优先级：**flag > env > per-model > 默认** |
| xbot-bench skill | `cases.jsonl` 每行支持 `"llm_params": {...}` → bench.sh 透传（`--llm-params "$(jq -c .llm_params)"`）；skill 文档更新 |

---

## 4. 里程碑与验证

| 里程碑 | 内容 | 验证 | 预估 |
|---|---|---|---|
| **M0 spike** | 用 curl/python 直连 sglang（pd-b300 端点）发 `logprobs:true` + tools 请求，确认：GLM 是否支持、reasoning 段覆盖、tool_calls 段行为、top_logprobs 上限 | 真实响应样本归档 `docs/rl/`，据此定 `covers` 契约 | 0.5d |
| **M1** | llm 层全套 | mock server 单测全绿 + Params=nil 字节级回归 | 1d |
| **M2** | engine 贯通 | agent 包单测（snapshot 携带） | 0.5d |
| **M3** | DB + npz + 导出 | BLOB/npy roundtrip；ephemeral 端到端导出含 npz | 1d |
| **M4** | CLI + bench | 手跑单 case：`--llm-params '{"logprobs":true,"temperature":0.7}'` | 0.5d |
| **M5** | 端到端冒烟 | 对 sglang 真实跑 3 case：npz 加载、`decode(ids)==content` invariant、`lp_sha1` 校验、usage 汇总非 0 | 0.5d |

## 5. 风险与缓解

- **provider 拒绝 logprobs+tools 组合**（OpenAI 官方部分版本 400；sglang/vLLM 支持）：错误原样透传，M0 spike 先确认目标集群；`RequestParams` 文档注明调用方自担。
- **SDK 字段覆盖**：openai-go 对 `top_logprobs` 的解析若缺失 → 降级仅存 chosen（契约字段 `top_k:0`），M1 首项验证。
- **SSE 体积**：logprob 增量只进聚合器不广播（前端零感知）；top_k 默认 0。
- **npz 手写风险**：npy 格式固定，roundtrip 单测 + numpy 实际加载双向验证。
- **回滚**：全为新增（表/列/flag/接口方法/旁路文件）；Params=nil 路径与现状逐字节一致，可直接 revert。

## 6. P1 展望（本计划不做，仅列出）

finish_reason + 语义化退出码；reward 回填工具（harness 测试结果 → session JSON `reward` 字段）；per-message token 对齐（`session_messages.completion_tokens`）；RL 模式 AskUser 自动应答；SubAgent 轨迹聚合导出。
