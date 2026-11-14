# RPC 延迟度量说明（`/api/rpc-stats` 与慢请求分档日志）

> 面向运维/排障读者。实现见 `channel/web/web_rpc_stats.go`、
> `channel/web/web.go`（`requestTimingMiddleware`）。
> 最后更新：2026-10-30（G-17 事后准入整改）。

---

## 1. 这套度量回答什么问题

`/api/rpc` 是 web 端插件 / 设置 / 会话接口的**单一入口**：所有方法都以
`{"method":"...","params":{...}}` 的形式 POST 到同一个 path。历史上只有
「整个 path 一条 p99」的粗口径，慢的时候无法回答「**哪个 method** 拖尾」。

G-17 在这个唯一咽喉点按 method 打点，输出：

- 按 method 的累计调用次数 `n` 与错误数 `errors`；
- 按 method 的 p50 / p95 / p99 / max / last 耗时；
- 周期性日志（默认 30s 一次，或攒够 1000 个样本提前打）；
- 一个只读查询接口 `/api/rpc-stats`。

另外，HTTP 中间件 `requestTimingMiddleware` 对**所有 `/api/*` 路径**做慢请求分档
（100ms / 200ms / 1s）计数与分级日志 —— 这是 path 维度的补充，和 method 维度
的 `/api/rpc-stats` 是两套口径，不要混用。

---

## 2. `/api/rpc-stats` 返回什么

- 方法：`GET` 或 `POST /api/rpc-stats`（handler 不校验 HTTP method；走同一套
  cookie/session 鉴权，未登录返回 401）；
- 无请求体；
- 只读：查询**不清零**任何计数或上报游标。

响应（与其他 REST 接口一致的统一信封）：

```json
{
  "ok": true,
  "data": {
    "generated_at": "2026-10-30T00:51:27.123456789Z",
    "ring_size": 8192,
    "total_samples": 1234,
    "methods": [
      {
        "method": "get_session_usage_stats",
        "n": 500,
        "samples": 500,
        "errors": 0,
        "p50_ms": 3.01,
        "p95_ms": 4.22,
        "p99_ms": 5.04,
        "max_ms": 6.1,
        "last_ms": 3.4
      }
    ]
  },
  "error": null
}
```

### 字段含义

| 字段 | 类型 | 含义 |
|---|---|---|
| `generated_at` | string | 快照生成时间（UTC，RFC3339Nano） |
| `ring_size` | int | 每个 method 的环形采样窗口容量（当前 8192 个样本） |
| `total_samples` | uint64 | 自进程启动以来 `/api/rpc` 的累计样本总数（所有 method 之和） |
| `methods[].method` | string | RPC method 名；特殊桶见 §3 |
| `methods[].n` | uint64 | 该 method 的**累计调用次数**（分母，永不回绕、不受窗口大小影响） |
| `methods[].samples` | uint64 | 环形窗口内**实际参与分位数计算**的样本数，等于 `min(n, ring_size)` |
| `methods[].errors` | uint64 | 该 method 累计 `status != 200` 的次数 |
| `methods[].p50_ms` / `p95_ms` / `p99_ms` | float64 | 在窗口样本上计算的线性插值分位数（毫秒，与 `bench.py` 的 `pct()` 同口径） |
| `methods[].max_ms` | float64 | 窗口内最大耗时（毫秒） |
| `methods[].last_ms` | float64 | 最近一次调用的耗时（毫秒） |

注意：`n` 是累计量，`samples` 是窗口量。长期运行的进程里 `n` 会远大于
`ring_size`，但分位数始终只反映**最近 8192 个样本**的分布。

---

## 3. 分母与归桶口径（读数字前必须知道）

### 3.1 p50 / p99 的分母

分位数的分母是**抵达 `handleRPC` 的全部调用**，**不按状态码过滤**：

- 成功（200）、参数错误（400）、鉴权/权限（403）、内部错误（500）、
  服务不可用（503）**全部计入**该 method（或 `(invalid)`）的样本；
- 也就是说 p50/p99 是「全量请求」的分位数，**不是**「仅成功请求」的分位数。
  想知道错误占比请同时看 `errors / n`。

日志节流（见 §4）只影响**日志行数**，不影响任何计数与分位数样本。

### 3.2 特殊桶 `(invalid)`

`method` 字段为空时统一归入 `(invalid)` 桶，避免非法/异常请求污染真实
method 的分位数，同时不被静默丢弃。具体包括：

- 请求体不是合法 JSON → 400；
- 请求体缺 `method` 字段 → 400；
- **`RPCHandler` 未装配** → 直接返回 **503 `RPC service unavailable`**。
  这个分支发生在 method 解析之前，因此 **503 被计入 `(invalid)` 桶**，
  同时该桶的 `errors` 递增。

> 结论：若在 `/api/rpc-stats` 里看到 `(invalid)` 桶的 `errors` 增长，
> 优先排查客户端请求体格式，以及服务端 RPC handler 是否装配成功。

### 3.3 不计入的情况

- 在 `authMiddleware` 阶段就被拒绝（未进入 `handleRPC`）的请求；
- WS 通道的 RPC（见 §5）；
- 非 `/api/rpc` 的其它 `/api/*` 路径（它们只有 path 维度的分档计数）。

---

## 4. 慢请求分档日志（path 维度）

中间件 `requestTimingMiddleware` 对 `/api/*`（排除 `/api/sse`）计时并分档：

| 档位 | 条件 | 日志 |
|---|---|---|
| `tier=1s` | `elapsed >= 1s` | `Warn Slow API request` |
| `tier=200ms` | `elapsed >= 200ms` | `Warn Slow API request` |
| `tier=100ms` | `elapsed >= 100ms` | `Info API request over 100ms`（节流） |

分档是**累计口径**：`over100ms ≥ over200ms ≥ over1s`，高耗时请求会同时计入
低档位的计数。

### `count_ge_100ms`

100ms 档日志行里带字段 `count_ge_100ms`，它是**「≥100ms 累计」计数**
（含 200ms / 1s 档，即 `over100ms`），**不是**「本行所属的 100–200ms 档样本数」。
该字段曾用名 `count_100ms`（2026-10-30 更名，旧名会让人误读成「恰好 100ms 档」；
查历史日志时按旧名 grep）。

100ms 档样本量比 200ms 档大一到两个数量级，逐条打日志会把日志刷爆，因此该档
**节流**：每 1 秒最多一行，行内用 `folded=N` 补报本窗口内被折叠掉的条数
（信息不丢、日志不炸）。

---

## 5. 边界：覆盖范围**仅限 `POST /api/rpc`**

- **WS 通道的 `MsgTypeRPC` 没有打点**。CLI RemoteBackend 通过 WebSocket 发送
  `MsgTypeRPC`（由客户端指定 method，见 `web.go` 的 `case protocol.MsgTypeRPC`），
  该路径**不经过** `handleRPC`，因此它的耗时**不会**出现在 `/api/rpc-stats`
  里，也不进慢请求分档日志之外的任何 method 统计。需要覆盖 WS 通道时**另立条目**，
  **不要**把这里的数字当成「全部 rpc 的延迟」。
- 本文所有数字默认指 web 端 `POST /api/rpc` 的同步调用；SSE、`/ws` 长连接等
  不在计时范围内。

---

## 6. 用法示例

```bash
# 假设已登录并使用 cookie 文件
curl -s -X POST http://127.0.0.1:8080/api/rpc-stats \
  -H 'Content-Type: application/json' \
  -b cookies.txt | jq '.data.methods | sort_by(-.p99_ms) | .[:5]'

# 看哪个 method 拖尾（p99 降序），以及它的错误占比
curl -s -X POST http://127.0.0.1:8080/api/rpc-stats \
  -H 'Content-Type: application/json' \
  -b cookies.txt | jq -r '.data.methods[] | "\(.method)\tn=\(.n)\terr=\(.errors)\tp50=\(.p50_ms)\tp99=\(.p99_ms)"'
```

日志侧：

```bash
# 100ms 档（节流后的汇总行）
grep 'API request over 100ms' xbot.log | tail
# 200ms / 1s 档
grep 'Slow API request' xbot.log | tail
```
