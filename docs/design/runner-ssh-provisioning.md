# SSH 自动纳管远程 Runner（内置插件）+ Runner 体系重构

> 状态：**已实现**（2026-09-17）· 分支 `feat/runner-ssh-provision`
> 本文件是设计依据与落地索引；实现见下「交付清单」。
> 需求：用户提供一条 SSH 命令 → 自动创建并管理「远程机器目标」+ 自动在机器上安装 runner → 会话可切换过去。
> 硬约束（用户）：**虽为内置插件，但不得与主模块耦合，只能使用通用插件接口能力。**

---

## 0. 结论先行

| # | 结论 |
|---|---|
| 1 | 真实生效的 runner 体系 = `tools/` 的 `SandboxRouter + RemoteSandbox + RunnerTokenStore`；`runner/` 包是 2026-06 #179 的**半成品骨架，整条链路已死**。 |
| 2 | 现存 **8 处死代码**、**10 处半接线**、**9 处设计过时**，另有 **9 处文档过时**（详见 §1）。 |
| 3 | **生产 DB 实证**：runner 状态与代码预期**已不一致**（`active_runner` 指向不存在的 runner、双表漂移、维度错位）—— 见 §2。 |
| 4 | 落地该插件**不需要为 SSH 增加任何核心扩展点**（通用接口已足够：view 贡献点 / `ctx.rpc` / `web_plugin_rpc` / `/api/rpc` / `contributes.configuration`）。这是「不耦合」的硬证据（§4.3 能力矩阵）。 |
| 5 | 真正需要补的核心能力只有 **3 项、且全部与 SSH 无关**：**R1** 发布 `xbot-runner` 产物；**R2** 会话级绑定持久化 + 通用 RPC；**R3** runner 归属维度收口（单一权威）。 |
| 6 | 另有 **R4–R8** 属于"体系失修"清理（死码、token 泄露、静默回退、协议版本、文档），建议与插件一并落地才自洽。 |

---

## 1. 现状调研（证据）

### 1.1 真实生效的执行链路

```
agent loop ──工具调用──▶ tools.Registry.GetForSession
                              │
                       ToolContext.Sandbox =
                         SandboxRouter.SandboxForSession("channel:chatID", userId)
                              │
                    ┌─────────┴──────────┐
               RemoteSandbox          NoneSandbox / DeniedSandbox
            （runner WS 长连接）
                    │
            getRunnerForSession：sessionRunners[会话] → active_runner(DB) → entry.active
                    │
            WebSocket ─▶ cmd/runner（runnerclient.Handler）─▶ 远端执行
```

锚点：每次工具调用**重解析** sandbox（`agent/engine_wire.go:1206`、`agent/engine.go:984`）；路由 `tools/sandbox_router.go:174`；连接解析 `tools/remote_sandbox.go:412`；runner 端 `cmd/runner/main.go:178`。

### 1.2 并行的抽象：1 套活 + 1 套死 + 4 套绑定

| 抽象 | 状态 | 证据 |
|---|---|---|
| `runner.Manager/Instance/ToolProvider`（`runner/`，V5「runner 一等公民」） | **死**：`BindSession` 零调用者 ⇒ `ResolveSession` 恒返回 local；`Agent.ResolveTool()`/`ToolProviders()`/`RunnerManager()` 零调用者 | `runner/manager.go:83/96/107`；`agent/agent.go:2115-2118, 4385-4403` |
| `tools.SandboxRouter/RemoteSandbox` | **唯一生效** | §1.1 |
| session→runner 绑定**四份**：`SandboxRouter.sessionRunners`(活/内存) · `Registry.sessionRunners`(死) · `runner.Manager.sessions`(死) · `tenants.runner_id`(死) | 无单一权威 | `sandbox_router.go:36`、`registry.go:42`、`manager.go:20`、`tenant.go:292/313` |

### 1.3 死代码（可直接删）

| # | 现象 | 证据 |
|---|---|---|
| A1 | `runner.NewLocal` 无调用方 | `runner/local.go:14` |
| A2 | `Manager.ResolveSession/BindSession/UnbindSession` 无调用方 | `runner/manager.go:83/96/107` |
| A3 | `runner.ToolProvider` 整链死（注册了但消费方 `ResolveTool` 无调用者） | `agent/agent.go:2115-2118, 4390-4403` |
| A4 | `SetLocalTools` + 写 `Local().Skills/Agents` **只写不读** | `agent/agent.go:2298/2310/2319` |
| A5 | `Manager.Add/Remove/Get/List/SetStatus` 无调用方 | `runner/manager.go:59-152` |
| A6 | `Registry.RegisterForRunner/ReplaceRunnerTools/UnregisterRunnerTools/SetSessionRunner` 零调用（读点恒空转） | `tools/registry.go:149/162/176/183`；读 `:216, :418-436, :509-530` |
| A7 | `tenants.runner_id`（v38）及 `SetTenantRunner/GetTenantRunner` 零调用 | `storage/sqlite/tenant.go:292/313`、`schema.go:21` |
| A8 | docker 删除后的残留接口面（`ExportAndImport` no-op、`ReinitSandbox`/`SetSandbox`） | `tools/sandbox_runner.go:28-38`、`remote_sandbox.go:691`、`denied_sandbox.go:26` |

### 1.4 半接线 / 缺陷（需修）

| # | 现象 | 证据 | 危害 |
|---|---|---|---|
| **B1** | `/api/runners` **无任何前端消费方**，且 list 响应**原样返回 `RunnerInfo.Token`**（只 mask 了 LLMAPIKey） | `channel/web/web_api.go:345-478`、`:360-364`；`web/src` 仅 5 处 runner（全是 `runner_status` 类型） | token 泄露到浏览器/日志/历史；白留死 API |
| **B2** | `runner_status` SSE 服务端推送完整，**Web 前端无 UI 消费**（CLI 消费） | `serverapp/server.go:369-371`、`channel/web/web.go:1184`；`sseConnection.ts:50/330` 仅类型 | 上下线不可见 |
| **B3** | v63 迁移把 `runners.user_id`/`runner_tokens.user_id` 折叠为 `cli_user`，但运行时按**原始 sender** 查询（web 为 `web-<n>`） | 迁移 `migrations.go:961/1048`；读 `runner_tokens.go:354/372`；web sender `web_auth.go:419`；`server.go:781-783` 的 `bizID = senderID` | **迁移后的 runner 对 web 不可见**（同 AGENTS.md v68 max_concurrency 事故同型） |
| **B4** | 系统提示的 `promptWorkDir` 用 `msg.SenderID`，沙箱路由用 `sandboxUserID` —— 两个维度 | `agent/agent.go:4143` vs `agent/engine_wire.go:147` | 工作目录与实际执行目录不一致（飞书登录 web 场景） |
| **B5** | `DeniedSandbox` + `WEB_USER_SERVER_RUNNER` 分支 **实际不可达**（唯一接线 `IsAdminIdentity` 恒 true） | `sandbox_router.go:224-250`、`web_api.go:1717-1719`、`server.go:367` | 死安全策略，误导 |
| **B6** | 全局 `sandbox.auth_token` 与 per-user token 双轨：任一全局 token 即可注册为**任意 userID**（path/claim 双绑由攻击者自填） | `remote_sandbox.go:213-214, 229-237` | 共享 token 泄漏 = 身份冒用 |
| **B7** | runner 侧 docker（`--mode docker`、`DockerExecutor` 389 行、飞书卡片入口）与 server 侧 docker 删除不同步 | `cmd/runner/main.go:26/85-92`、`runnerclient/docker.go`、`feishu_settings.go:738`、`agent/sandbox_mode.go:20` | 语义混乱 |
| **B8** | `sessionRunners` 纯内存、无持久化；`tenants.runner_id` 已死 | `sandbox_router.go:36/162-172`、`remote_sandbox.go:119` | 重启后绑定全丢（CWD 却是 DB 持久 → 不同步） |
| **B9** | `runner_tokens` 与 `runners` 双表并存（双写 + 双查） | `runner_tokens.go:90-118, 209-240, 394` | 双源漂移（生产已实证，见 §2） |
| **B10** | `RunnerConnectCmdGet` 与 `RunnerTokenGet` 两代回调并存 | `feishu.go:120-140`、`server.go:2173-2188` | 新旧路径无清理标记 |
| **B11** | 会话绑定 runner **离线时静默回退本机执行** | `sandbox_router.go:180-183`（`return r.none`） | 用户以为在远端，实际在 server 上跑（**生产已实证**，见 §2） |

### 1.5 设计过时（需重构）

| # | 现象 | 重构方向 |
|---|---|---|
| C1 | `runner.Instance`（死）vs `tools.Sandbox`（活）两套抽象 | 删 `runner/` 包，概念收口到 `tools/`（或提炼 `internal/remoterunner`） |
| C2 | 四套 session→runner 绑定 | 只留一套 + 持久化 |
| C3 | runner 归属维度 = 原始 sender，与 v63 单 operator 不一致；但**沙箱/工作区又确实需要按原始身份隔离**（多设备/多人共享 server） | 明确决策：**沙箱属"身份"而非"settings"**；给 `active_runner` 定义规范 key（同 v68 模式） |
| C4 | `RemoteSandbox` 1021 行 god file（exec/文件/PTY/bg/MCP/LLM 代理全堆一起）+ 5 个辅助文件 | 收口为独立包 |
| C5 | `runnerproto` **无协议版本号**，两端必须同源构建 | 加 `protocol_version` + 最低版本拒绝 |
| C6 | `GetActiveRunner/SetActiveRunner` 越过 settings 体系直写 `user_settings`，硬编码 `channel='web'` | 收口到 SettingsService + 规范 key |
| C7 | server 侧 docker 分支残留（`sandboxWorkspace` 的 `case "docker"` 等） | 彻底清 |
| C8 | 无 runner 版本上报 / 无健康探针 / 无自愈 | 注册消息加 `version`，加探针与自愈 |
| C9 | 无 Web 管理界面（`RunnerPanel.tsx`/`RunnerTab.tsx` 已删） | 由插件提供（本方案） |

### 1.6 文档过时（9 处）

`docs/agent/architecture.md:13/71/77/239-241`、`docs/agent/tools.md:10-12/226`、`docs-site/en/guides/sandbox.md:102`（示例带 `--name`，但 `cmd/runner` 无此参数）、`docs-site/{en,zh-cn}/architecture.md:473-486`、`configuration.md:19`、`faq.md:167-178`（仍是 DockerSandbox/dual-mode 描述）。`AGENTS.md:346/664` 内容准确。

---

## 2. 生产 DB 实证（本轮新增，最强证据）

只读查询 `~/.xbot/xbot.db`：

```
runners (5 行，user_id 全为 cli_user):
  ('cli_user','default',    'docker','')
  ('cli_user','ubuntu',     'docker','/home/smith/xbot-workspace/sandbox-test')
  ('cli_user','web1',       'docker','/home/smith/xbot')
  ('cli_user','remote-arch','native','/tmp/runner-test')
  ('cli_user','linked',     'remote','')                    ← mode='remote' 不是合法值

runner_tokens (1 行): ('cli_user','native')                 ← 与 runners 5 行漂移（B9 实证）
user_settings:  ('web','cli_user','active_runner','main')   ← 'main' 在 runners 里不存在（悬空）
```

由此实证三个缺陷：

1. **B3 维度错位**：5 行属 `cli_user`，而 web RPC 用 `bizID = web-<n>` 查询 → **web 侧完全看不到这些 runner**。
2. **B11 静默回退**：`active_runner='main'` 是悬空值 → `IsRunnerOnline(...,'main')=false` → `SandboxForUser` 返回 `none` → **所有工具都在 server 本机执行**，而 UI/记录显示"已选 runner"。
3. **B9 双表漂移**：`runners`(5) vs `runner_tokens`(1)，`mode` 字段还出现非法值 `'remote'`（无枚举校验）。

> 结论：**在动插件之前必须先把 runner 归属/绑定收口，否则"自动纳管"只会在这堆不一致上再叠一层。**

---

## 3. 设计原则（用户约束的落地）

1. **核心零 SSH 概念**：core 不出现 plugin id / `ssh` / 主机名 / 安装脚本。
2. **插件零主模块耦合**：插件是独立 Go module，`go.mod` 只依赖 `plugin/protocol`（范式 `plugins/xbot-git-fancy/go.mod`），运行时只经 stdio NDJSON。
3. **控制面 / 数据面分离**：不新增 runner 协议、不动执行链路；插件只做「纳管 + 装机 + 状态」，执行仍走既有 WS 协议与既有 runner。
4. **单一权威**：会话 ↔ runner 绑定只有一份存储（DB），内存仅缓存。
5. **失败必须可见**：绝不静默回退本机。

---

## 4. 目标架构

### 4.1 分层

```
┌─ web 前端（浏览器 = 已登录 operator）───────────────────────────┐
│  xbot.ssh-runner 面板（通用 view 贡献点）                       │
│   目标列表 / 新增(SSH 命令) / 状态 / 「切到该机器|切回本机」      │
└──────────┬─────────────────────────────────────────────────────┘
           │  /api/rpc（无点号 → 核心，cookie 身份）+ web_plugin_rpc（含点号 → 插件进程）
┌──────────┴─────────── 核心（零 SSH 概念）──────────────────────┐
│ runner_create / list / delete / set_active / rename   ← 已有     │
│ runner_session_get / set（会话级绑定）                ← R2 新增 │
│ RunnerTokenStore（单表、规范 operator 维度）          ← R3 改造 │
│ RemoteSandbox（数据面，不变）                                   │
└──────────┬─────────────────────────────────────────────────────┘
           │ stdio NDJSON（通用插件协议）
┌──────────┴──────── xbot.ssh-runner（内置插件，独立进程）────────┐
│ probe / provision / deprovision / status / logs                 │
│ 无状态执行器：每次调用自带 target 规格                           │
│ 目标注册表 = 插件自身 config（宿主托管，跨设备）                 │
└──────────┬─────────────────────────────────────────────────────┘
           │ ssh
   远端机器：xbot-runner + systemd unit（或 nohup 兜底）
```

### 4.2 核心改造 R1–R8

| ID | 内容 | 解决 | 必要性 |
|---|---|---|---|
| **R1** | `release.yml` 新增 `xbot-runner-{os}-{arch}`（5 平台，`CGO_ENABLED=0 go build ./cmd/runner`）并纳入 `checksums.txt` | G1 硬阻塞 | **必须**（否则无从下载） |
| **R2** | 会话级绑定持久化（权威 = `tenants.runner_id`，写内存时落库 + 启动回填）+ 通用 RPC `runner_session_get/set {channel,chat_id,name}`；三层降级收敛为两层（删 `entry.active`） | G2/G5/B8/C2 | **必须**（"切会话到机器"的入口） |
| **R3** | 归属维度收口：单一规范 operator key（同 v68 模式），`active_runner` 走 SettingsService；**合并 `runner_tokens` → `runners`**（迁移 + DROP）；删 `web-*`/`WEB_USER_SERVER_RUNNER` 死分支 | G4/B3/B5/B9/C3/C6 | **必须**（否则插件建的目标 web 读不到） |
| **R4** | 删死代码：`runner/` 整包 + `agent` 接线 + `Registry` runner 工具簇 + `tenant.go` 死 setter + docker 残留接口面 | A1–A8/C7 | 高（降复杂度） |
| **R5** | `/api/runners` 列表**不回传 token**；删除或补齐 REST 消费方 | B1 | 高（安全） |
| **R6** | 修静默回退：绑定 runner 离线 ⇒ 显式失败 + 前端提示 | B11 | 高（正确性） |
| **R7** | 协议加 `protocol_version` + 能力协商；注册消息带 runner `version` | C5/C8 | 中（跨版本兼容） |
| **R8** | 文档同步（§1.6 九处 + AGENTS.md 段落指向新结构） | — | 中 |

> R1–R3 是插件的**前置依赖**（在飞顺序：R3 → R2 → 插件）。

### 4.3 通用接口能力矩阵（「不耦合」硬证据）

| 需要的能力 | 通用接口 | 声明 / 调用点 | 现状 |
|---|---|---|---|
| UI 面板 | `web.contributes[kind=view]` | `plugin.json` + 前端 `ctx.ui` | ✅（git-fancy 范式） |
| 前端 → 插件后端 | `ctx.rpc.call('xbot.ssh-runner.x')` | `web_plugin_rpc` → `CallPluginRPC`（`rpc_table.go:2204-2255`） | ✅ |
| 前端 → 核心 RPC | `ctx.rpc.call('runner_create')` | `/api/rpc`（无点号直查 RPC 表，`rpc_table.go:2214-2218`；登录即授权 `web_rest.go:375-379`） | ✅ |
| 插件配置 / 跨设备状态 | `contributes.configuration` + `plugin_config(_set)` | `rpc_table.go:2257+` | ✅ |
| 前端本地状态 | `ctx.state` | `plugin-api/state.ts` | ✅ |
| 事件刷新 | `ctx.events` / SSE | plugin-api/events.ts | ✅ |
| 进程执行（ssh/scp） | 插件进程无沙箱（`cmd.Dir`=插件目录，继承环境） | `plugin/runtime.go:413-447`；git-fancy 实证 `exec git` | ✅ |
| 让 agent 也能操作 | `contributes.tools` + `ToolDef` + `ExecuteTool` | 插件协议 | ✅（可选，见 §5.4） |
| **会话级切换 RPC** | — | — | ❌ **R2 补** |
| **runner 发行产物** | — | — | ❌ **R1 补** |
| **归属维度收口** | — | — | ❌ **R3 补** |

> **除 R1–R3 三个与 SSH 无关的通用补齐外，插件完全跑在既有通用接口上。核心无需新增任何 SSH 专有代码。**

---

## 5. 插件设计：`xbot.ssh-runner`

### 5.1 形态选择（关键决策）

| 选项 | 结论 |
|---|---|
| **非 channel 的 stdio 插件**（推荐） | 单进程；可 exec ssh；被动响应前端 RPC。**局限**：不能主动调 server RPC（`SetInboundHandler` 无调用点，`plugin/runtime.go:451`）⇒ 编排由前端负责 |
| channel 插件（genui 型） | 双进程（激活进程 + 专用 channel 进程，`channel_plugin.go:73-89`）；可经 transport 调任意 RPC，但**身份为空**（`transport_channel_plugin.go:611` `context.Background()` → `bizID=""`，`rpc.go:48`）⇒ 会把 runner 建到空用户下。**不用** |
| native（进程内 Go） | 生产零实例（仅测试），且会引入主模块耦合。**不用** |

**⇒ 采用「非 channel 的 stdio 插件 + 前端编排」**：浏览器已持有 operator cookie 身份，核心 RPC 调用天然带正确身份，规避了 channel 路径的空身份陷阱。

### 5.2 manifest（只用通用字段）

```jsonc
{
  "id": "xbot.ssh-runner",
  "name": "Remote Machines (SSH)",
  "version": "1.0.0",
  "runtime": "stdio",
  "entry": "./bin/ssh-runner-plugin",
  "activationEvents": ["onStart"],
  "permissions": ["rpc", "ui", "config", "storage.private"],
  "contributes": {
    "configuration": {
      "title": "Remote Machines",
      "properties": {
        "downloadBase": { "type": "string", "label": "Runner 下载基址",
          "default": "https://github.com/ai-pivot/xbot/releases/latest/download" },
        "installDir":   { "type": "string", "label": "远端安装目录", "default": "/usr/local/bin" },
        "serviceMode":  { "type": "string", "label": "服务方式", "default": "systemd",
          "options": [{"label":"systemd user unit","value":"systemd"},
                      {"label":"nohup 兜底","value":"nohup"}] },
        "targets":      { "type": "string", "label": "已纳管目标(JSON)", "default": "[]" }
      }
    }
  },
  "web": {
    "entry": "index.js",
    "contributes": [
      { "kind": "view", "id": "xbot.ssh-runner.panel", "container": "right_sidebar",
        "title": "Remote Machines", "icon": "server", "entry": "index.js" }
    ]
  }
}
```

> 注意（既有 gotcha）：plugin.json 的 `permissions` 受**后端 25 项白名单**校验，**不能写 `files`**（前端 9 项才含它）。

### 5.3 后端方法（前端经 `ctx.rpc.call('xbot.ssh-runner.<m>')`）

| 方法 | 职责 | 写远端 |
|---|---|---|
| `probe {ssh}` | 只读探测：`uname -sm`、是否 root、出口网络、已装版本、`command -v curl/wget` | 否 |
| `provision {ssh, name, download_base?, install_dir?, dry_run?}` | **仅安装**：下载 → `checksums.txt` sha256 校验 → 杀老 runner → 落盘。**不启动任何服务** | 是（支持 `dry_run`） |
| `connect {ssh, name, connect_cmd, install_dir?, connection_mode?}` | 起一条受管 SSH 会话，runner 在该会话**前台**运行；连接前**先杀老 runner** | 是 |
| `disconnect {ssh, name}` | 停 supervisor + `pkill` 远端 runner | 是 |
| `status {ssh, name}` | 连接状态（`connected`/`reconnecting`/`disconnected`）+ 重启次数 + 隧道端口 + 已装版本 | 否 |
| `logs {ssh, name, lines}` | 优先返回**管道输出**（supervisor 环形缓冲），否则回落到远端日志文件 | 否 |
| `deprovision {ssh, name, uninstall?}` | 拆管道 → 杀进程 →（可选）删二进制 | 是 |

**目标注册表**由前端写入插件自身 config（`targets` JSON，含可选 `connectionMode` / `autoConnect`）—— 宿主托管、跨设备可见、升级不丢。

#### 5.3.1 连接模型：VS Code Remote 式 SSH 管道（2026-09-17 用户要求）

用户原话：「类似 vsc remote 一样，每次连接自动起 ssh 管道然后起 runner 连接我们服务器，且每次重新连接要重新起 runner，杀老 runner。唯一的区别是我们并不是把服务器跑上面，只是 runner 跑上面」。

⇒ **runner 不再是远端常驻服务**（systemd/nohup 已删除），而是：

```
ssh [-R 127.0.0.1:<rport>:127.0.0.1:<sport>] <host> \
    '<pkill 老 runner>; exec <install_dir>/xbot-runner --server ws://127.0.0.1:<rport>/ws --token … --name …'
    ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ runner 在管道前台运行：管道断 = runner 死
```

四条硬契约：

| 契约 | 实现 |
|---|---|
| **连接由我们发起** | supervisor 持有长时 SSH 会话；`ssh` 子进程 = 管道；runner 随管道生死（前台 `exec`，非 `nohup`/`&`） |
| **每次（重）连先杀老 runner** | 远端脚本**开头**就 `pkill -f 'xbot-runner.*--name[= ]<name>([[:space:]]\|$)'`（名字正则转义、锚定，不会误杀 `m10`）；本地则取消旧 supervisor。⇒ 同一 name 永不并存两个 runner |
| **断线自动重连** | supervisor 循环：会话结束（无论原因）→ 退避重试（1s→15s 封顶）→ 重连即再杀一次老 + 起新，`status.restarts` 递增 |
| **server 不用装到远端** | `tunnel`（默认）：`-R` 把远端 `127.0.0.1:<rport>` 转发到我们这里的 server，runner 只 dial `127.0.0.1:<rport>` ⇒ **远端无需任何到 server 的网络可达性**（NAT/内网都行）；<rport> 由 supervisor 在远端探测空闲端口选取（按 name 哈希分窗，避免多次重连撞同一端口）。`direct`：显式选择，runner 直连 `--server` 给定地址（**无静默回退**） |

**为什么不是常驻服务**：常驻 systemd 需要远端能主动连回 server（内网/NAT 场景不成立）、且「改了 token/换机器」会留下孤儿进程与旧连接竞争。管道模型下连接生命周期**与我们一致**，`connect` 即隐式撤销旧连接（对应 R3「单一权威 + 不留孤儿」）。

#### 5.3.3 插件侧自愈（`state.json`）

管道由**插件进程**持有 ⇒ 插件进程重启（server 重启 / 30s RPC 超时被杀）会断开全部管道。为让「每次连接自动起 ssh 管道」在**无人打开浏览器**时也成立：

- `connect` 成功后把监督项（`ssh` / `name` / `connect_cmd` / `install_dir` / `connection_mode` / `auto_connect`）写入插件目录下的 `state.json`（**0600**，tmp+rename 原子写）；`disconnect` 删除该项。
- `Activate` 时加载该文件，对 `auto_connect: true` 的条目**自动重新 `connect`**（即自动重开管道；重连本身又会先杀老 runner）。
- **opt-in**：只有显式勾选 autoConnect 的目标才会自愈；失败仅记日志、条目保留待下次重试（不阻塞插件启动）。
- 凭据权衡：`connect_cmd` 含 runner token（同一台机器的 DB 里本就明文存有该 token），故文件严格 0600 且仅本机可读；不支持文件系统隔离时请勿开启 autoConnect。

#### 5.3.2 ⚠️ 插件 RPC 的硬约束（实测，直接决定接口形态）

| 约束 | 证据 | 设计后果 |
|---|---|---|
| **30s 硬超时**：`StdioPluginProcess.Call` 的 `pluginCallTimeout = 30 * time.Second` | `plugin/runtime.go:457, 486-489` | 装机（下载 10–20MB + 多轮 ssh）**必然超时** ⇒ **`provision` 必须异步**：立即返回 `job_id`，前端轮询 `job_status` |
| **超时会杀进程**：`<-time.After(pluginCallTimeout)` 分支调 `p.stopLocked()` | `plugin/runtime.go:486-489` | 超时不仅失败，还**把插件进程杀掉**（后续调用全挂，且**所有管道随之断开**）⇒ 绝不能同步长任务；`connect` 也必须立即返回（supervisor 在后台持有会话） |
| **单飞**：`stdin` 顺序 + 单个 `pending` ⇒ 同一插件同时只允许一个在途调用 | `plugin/runtime.go:463-470`（注释「only one Call can be in-flight at a time」） | 前端的 `status` 轮询与 `provision` 不能并发；长任务必须在插件内部后台跑（supervisor 即为此） |

⇒ 方法表据此调整为：`provision` / `deprovision` 返回 `{job_id}`；`connect` / `disconnect` / `probe` / `status` / `logs` 同步快速返回；`job_status {job_id}` 轮询。

> **注**：管道由插件进程持有 ⇒ 插件进程重启（含 30s 超时被杀、server 重启）会断开全部管道；前端按 `autoConnect` 重新 `connect` 即恢复（重连语义 = 杀老 + 起新，幂等）。

### 5.4 纳管时序（端到端只用通用接口）

```
① 前端 → 核心:  runner_create {name}                       ⇒ {token, command}
② 前端 → 插件:  probe {ssh}                                ⇒ 环境报告（展示给用户确认）
③ 前端 → 插件:  provision {ssh, name, download_base, install_dir}
   插件后台:     探测平台 → 下载 → sha256 校验 → 杀老 runner → 原子落盘
④ 前端:        轮询 job_status {job_id}                    ⇒ detect/prepare-dir/download/verify/kill-old/install/ready
⑤ 前端 → 插件:  connect {ssh, name, connect_cmd: command, install_dir, connection_mode}
   插件后台:     选空闲远端端口 → ssh -R <隧道> host '<杀老 runner>; exec xbot-runner …'
⑥ 前端:        轮询 status {ssh, name}                     ⇒ connected=true（否则 reconnecting + last_error）
⑦ 前端 → 核心:  runner_session_set {channel, chat_id, name}  ⇒ 本会话切过去
                （前端把 {name, ssh, install_dir, connection_mode, autoConnect} 写进插件 config.targets）
```

**重连**：管道断开 ⇒ supervisor 自动重试（每次都杀老 + 起新）；`status.restarts` 可见。
**断开**：`disconnect`（或 `deprovision`）⇒ 拆管道 + 杀掉远端 runner。

**agent 侧（可选）**：`contributes.tools` 声明 `ssh_runner` 工具。但插件工具是"桥"，**只能回插件进程、不能调核心 RPC** ⇒ 工具只做"SSH + 装机/连接"并把结果（含 connect 命令）返回给模型，注册/切换由模型走既有的 `config action=runner`。**首期可不做**，UI 路径已闭环。

### 5.5 安全

| 面 | 措施 |
|---|---|
| SSH 凭据 | **不落库、不进日志/事件**；只存用户给的 SSH 命令/别名；沿用 server 主机 `~/.ssh` + agent。与 `.gitignore` 拒私钥策略一致（仓库/DB 都不存私钥） |
| 目标校验 | `provision` 前先 `probe` 回显环境；支持 `dry_run` |
| runner token | 一次性、可轮换（同名 `runner_create` 覆盖）、可撤销（`runner_delete`） |
| 传输 | 推荐 wss（`Sandbox.PublicURL` 支持配置）；文档标明内网 http 风险；R7 的协议版本防错配 |
| 审计 | 每一步写插件私有日志（复用 `pluginLogger` 约定 → `~/.xbot/plugins/<id>/logs/`） |
| 幂等 | 同名目标：探针 → 版本比对 → 仅在需要时下载/重装 |

---

## 6. 实施计划（每步可独立验收）

| 阶段 | 内容 | 判据（可数、可验证） |
|---|---|---|
| **P0** | **R3 + R2**（归属收口 + 会话绑定持久化 + RPC） | 生产 DB 形态一致（`active_runner` 不再悬空、双表合一）；重启 server 后 `runner_session_get` 仍返回原绑定；新增单测绿 |
| **P1** | **R1**（发布 `xbot-runner` 产物） | `gh release view` 含 5 个 `xbot-runner-*`；`checksums.txt` 覆盖；`xbot-runner --server/--token` 在本机可连 |
| **P2** | 插件骨架：目录 + `go.mod`(仅 protocol) + manifest + 后端 `probe`/`status` + 前端面板空壳 | `make plugins-install` 后激活成功；面板可开；`probe` 对真实机器返回环境报告 |
| **P3** | 纳管闭环 `provision`/`deprovision` + 前端表单 + 切换按钮 | 对一台测试机：`runner_list` 该 runner `online=true`；切过去后 `Shell hostname` 输出远端主机名（断言） |
| **P4** | R4/R5/R6/R7/R8（清死码、修 token 泄露、修静默回退、协议版本、文档） | `go build ./...`、`go test ./...`、`golangci-lint`；E2E 面板用例；文档 diff |
| **P5** | 发布集成 | `plugins/package.sh` 产物含 `xbot.ssh-runner/{plugin.json,bin/,web/index.js}`；`release.yml` 加 esbuild 块 + 枚举守卫通过 |

> 两个既有 gotcha 必须遵守：① 新增插件**必须**在 `release.yml` 前端 job 加 esbuild 块（genui / iteration-stats 两次漏发事故）；② `plugins/package.sh` 自动遍历 `plugins/*/plugin.json`，但 tarball 内用**插件 ID** 布局（源目录用连字符）。

---

## 7. 风险与取舍

| 风险 | 缓解 |
|---|---|
| 远端环境差异（无 systemd / 非 root / 无 curl） | `probe` 决定路径：systemd → nohup 兜底；非 root → `~/.local/bin` + user unit；下载器 curl → wget → python 降级 |
| 远端回连不可达（NAT/防火墙） | 文档要求 + `connect_cmd` 取 `PublicWSAddr()`（可配置）+ 允许 `--server` 覆盖 |
| 平台覆盖 | release 与 xbot-cli 同矩阵（linux/amd64+arm64、darwin/arm64）；`probe` 报 arch，不匹配明确报错 |
| 删遗留表需迁移存量 | 先 `INSERT OR IGNORE` 到 `runners`，再 `DROP`；`Validate` 过渡兼容 |
| 范围蔓延（R2 涉及前端会话 UI） | P0 只做 RPC + 插件面板内的切换按钮；会话标题栏选择器留 P4 |

---

## 8. 待用户裁决

1. **runner 产物形态**：独立 `xbot-runner`（推荐，轻量独立权限域）／ `xbot-cli runner` 子命令（无新产物但带 TUI 依赖）？
2. **归属层级**：是否**取消 user 级 `active_runner`**、统一为会话级（+ 默认会话兜底）？
3. **离线行为**：硬失败（推荐，修 B11）／回退本机？
4. **SSH 凭据**：确认「只存 SSH 命令/别名、私钥走 server 主机 `~/.ssh`」（推荐）— 还是需要插件托管密钥？
5. **交付范围**：只出方案 ／ 方案 + P0 基建 ／ 方案 + P0–P3 端到端？

---

## 9. 交付清单（实现索引）

| 项 | 落地位置 |
|---|---|
| R1 发布 `xbot-runner` 产物 | `.github/workflows/release.yml` → `build-runner` job（交叉编译 5 平台 → `dist/xbot-runner-*`，进 release + checksums） |
| R2 会话级绑定（权威） | `serverapp/runner_binding.go`（`tenants.runner_id` 适配器）→ `wireRunnerBindingStore` 于 `serverapp/server_core.go` / `server.go`；内存缓存 `tools/sandbox_router.go`；RPC `runner_session_get/set`（`serverapp/rpc_table.go`） |
| R3 归属收口（去用户维度） | `storage/sqlite/schema.go` + `migrations.go` 的 `migrateV68ToV69`（runners 去 user_id 重建 / DROP runner_tokens / 删 user 级 active_runner）；`tools/runner_store.go`（`RunnerStore`，列表不含 token） |
| R4 删死代码 | 删除 `runner/` 包；`tools/registry.go` 的 runner 工具簇 + 会话绑定位；`agent/agent.go` 的 `runnerManager`/`SetLocalTools`/Skill/Agent 声明 |
| R5 token 不外传 | `RunnerInfo` 无 token 字段；`tools/runner_store.go` 的 `Token/RotateToken` 是唯一访问器；无人消费且泄露 token 的 `/api/runners` REST 端点已删除 |
| R6 离线硬失败 | `tools/offline_sandbox.go`（`OfflineRunnerSandbox`）+ `SandboxRouter.SandboxForSession` |
| R7 协议版本 | `internal/runnerproto` 的 `ProtocolVersion`/`RegisterRequest.{RunnerName,Version,ProtocolVersion}`；`tools/remote_sandbox.go` 的版本闸门；端点 `/ws` |
| R8 文档 | 本文件 + `AGENTS.md` + `docs/agent/{architecture,tools}.md` + `docs-site/content/{en,zh-cn}/{architecture.md,guides/sandbox.md}` |
| 插件（控制面） | `plugins/xbot-ssh-runner/`（独立 module，只依赖 `plugin/protocol`）+ `web/src/plugins/ssh-runner/index.tsx`（右侧栏面板）+ esbuild 接线（`release.yml` 前端 job、`Makefile`）。**连接模型 = SSH 管道**：`main.go`（方法/步进脚本）、`supervisor.go`（每目标一个 supervisor：`ssh -R` 隧道、前台跑 runner、**先杀老**、断线退避重连、输出环形缓冲）、`state.go`（`state.json` 0600 原子写：`auto_connect` 目标在插件重启后自动重开管道） |
| 连接模型（用户 2026-09-17 追加要求） | 见 §5.3.1：连接由我们发起、runner 跑在管道**前台**、每次（重）连**先杀老 runner**、`tunnel` 默认让远端无需可达 server；systemd/nohup 常驻路径**已删除** |

**验证**：`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；前端 `tsc --noEmit` 0 错 + `vitest` 全绿；插件 module `go build/vet/test` 通过（含 `supervisor_test.go`：隧道改写/直连不改写/杀老在新进程之前/同名 kill 模式锚定与转义/独占 Connect/Stop 远端 pkill/断线重连/Tail 有界/预检缺失二进制/StopAll；`state_test.go`：往返/自动重连过滤/0600/原子写/损坏报错）。
