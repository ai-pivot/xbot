# xbot 安装与插件分发重设计方案

> 状态：提案（2026-08-30）
> 目标读者：xbot 维护者
> 关联代码：`plugin/`（manager/registry/manifest）、`scripts/install.*`、`.github/workflows/release.yml`、`web/src/plugins/manager/`、`serverapp/rpc_table.go`

---

## 0. 问题诊断（现状探索结论）

三个用户痛点，全部定位到具体根因：

### 痛点 1：「自带插件装了也不会安装」

| 根因 | 证据 |
|---|---|
| **release 不构建任何插件** | `release.yml` 只出 5 平台 `xbot-cli` + `xbot-web-dist.tar.gz`，`plugins/` 目录零 CI 任务 |
| 官方插件二进制手工提交、仅 Linux | `plugins/xbot-genui/bin/genui-plugin` 手工 commit 进仓库 |
| Makefile 覆盖不全 | `make plugins-install` 只管 genui + git-fancy，不含 ambience |
| 前端产物无构建部署入口 | git-fancy Makefile 只装 `bin/` + `plugin.json`，不装 `web/`（仓库里还有 `web/~/.xbot/plugins/` 这个 `~` 未展开的意外目录） |
| channel 隐性开关 | genui 装完必须手改 config `channels.genui.enabled=true` 才生效（AGENTS.md 有踩坑实录） |
| `make install` 产物平台绑定 | 用户在 macOS/Windows 上根本编译不出 Linux 二进制之外的东西 |
| `PluginRegistry` 是 MVP 壳 | `RegistrySourceGitHub/URL` 常量 + `DownloadURL/Checksum` 字段是死代码，`Install()` 只取第一个 local source |

### 痛点 2：「web 没直接安装渠道」

| 根因 | 证据 |
|---|---|
| 无 registry/市场 | grep `marketplace` 零匹配；无远程下载代码 |
| PluginManagerPanel 只有 zip 上传 | `installPluginFile` → `/api/plugin/install-file`（admin-only）是唯一安装入口 |
| 卸载无按钮 | `plugin_uninstall` RPC 存在，前端零调用 |
| 面板不显示前端内置插件 | refresh 只调 `plugin_status`，`activateBuiltins` 的 4 个（plugin-manager/skill-manager/ambience/session-stats）不可见 |
| `WatchConfig` 死代码 | 改 config.json 的 `disabled_plugins` 不热生效 |

### 痛点 3：「安装太原始」

| 根因 | 证据 |
|---|---|
| Windows 装完 Web 404 | `install.ps1` 不下载 `xbot-web-dist.tar.gz` |
| Docker 装完 Web 404 | Dockerfile 不 COPY dist，`resolveStaticDir` 第 2 优先级 `/app/web/dist` 为空 |
| 前端 dist 与二进制分离 | 三级路径解析（StaticDir → binary-relative → XBOT_HOME）依赖 install.sh 恰好命中第 3 级；换 INSTALL_PATH 即断 |
| Web 注册后零引导 | 首注册即 admin，LLM/channel 全靠自己找 |
| 泄漏与遗留 | `xbot-runner.service` 硬编码真实 token 在仓库里；根目录 3 个构建产物二进制；runner 无安装路径 |

**关键洞察：安装管线 90% 已存在** —— `InstallPluginFromZip`（zip-slip/zip-bomb 防护）、`plugin_uninstall`/`plugin_set_enabled` RPC、`web_plugin_list` rescan 热加载、`VerifyChecksum`（未接线）、权限白名单、`resolvePluginRPCMethod` 路由、`version` 包（GitHub release 检查）、`install-cn.sh` 的镜像模式——全部现成。**缺的只是：产物（CI 构建）、远程源（registry 下载）、渠道（marketplace UI + CLI 命令）。**

---

## 1. 北极星与设计原则

**北极星**：`curl | bash` 一行 → 30 秒后浏览器打开 localhost:8082 即可用（Web UI 内嵌、官方插件已激活）；插件市场一键安装/升级/卸载社区插件；`xbot-cli plugin install <id>` 同等能力。

**原则**：
1. **单二进制优先**（go:embed 前端）——消灭「dist 与二进制分离」整类问题
2. **GitHub 即 registry**（官方 repo + release assets），不自建商店服务端
3. **复用现有管线**——远程安装 = 下载 zip + 现有 `InstallPluginFromZip`，不造新安装机制
4. **官方插件是一等公民**——随主程序 CI 构建发布，默认 bundle 安装
5. **信任根最小**——默认仅官方源；社区源需显式添加 + 风险提示；安装/更新 admin-only + 权限确认弹窗

---

## 2. 总体架构

```
┌─ 构建/发布（CI）──────────────────────────────────────────┐
│  frontend job ──► web/dist ──┬───────────────────────────┐  │
│                              │ embed 进二进制            │  │
│  go job ──► xbot-cli (5 平台, 内嵌 Web UI) ◄────────────┤  │
│                              │                           │  │
│  plugin job ──► 官方插件 ×(插件,平台) zip ──► release assets
│  bundle job ──► xbot-official-plugins-<os>-<arch>.tar.gz   │
│                              │                           │  │
│  xbot-plugins repo ◄── registry.json 索引(PR 自动更新) ◄────┘  │
└──────────────────────────────────────────────────────────────┘
                │                          │
        install.sh / ps1              marketplace UI / CLI
                │                          │
                ▼                          ▼
┌─ 运行时（用户机器）───────────────────────────────────────┐
│  ~/.xbot/                                                  │
│  ├── config.json  (admin token, registries, channels)      │
│  ├── web/dist (static_dir override, 开发用)                │
│  └── plugins/                                              │
│      ├── xbot.genui/     (bin/ + web/ + plugin.json)       │
│      ├── xbot.git-fancy/                                   │
│      └── <community>/     ← 从 registry 一键装             │
│                                                            │
│  安装管线（全部现有 + 3 个新函数）:                          │
│  registry.json 解析 → 下载(镜像fallback) → sha256 校验       │
│  → InstallPluginFromZip(现有) → channel 自动开启(新)         │
│  → web_plugin_list rescan(现有) → 前端热加载(现有)           │
└────────────────────────────────────────────────────────────┘
```

---

## 3. 分层设计

### L1 分发层：官方插件进 CI，registry repo 做索引

#### 3.1 release.yml 新增 plugin job

```yaml
plugin-build:
  needs: [frontend]          # 前端 esbuild 产物进 zip
  strategy:
    matrix: {os: [linux, darwin, windows], arch: [amd64, arm64]}  # 按 xbot-cli 5 平台收敛
  steps:
    - 每个 Go 插件（xbot-genui, xbot-git-fancy）: CGO_ENABLED=0 go build → bin/
    - script 插件（xbot-ambience）: 平台无关
    - esbuild web/src/plugins/git-fancy → <plugin>/web/；genui 前端同
    - zip 每插件 → xbot-plugin-<id>-<os>-<arch>.zip
bundle:
  needs: [plugin-build]
  - tar 全部插件 → xbot-official-plugins-<os>-<arch>.tar.gz
```

产物清单（每 release）：`xbot-cli-*`（5）+ `xbot-plugin-*`（插件×平台）+ `xbot-official-plugins-*.tar.gz`（5）+ `xbot-web-dist.tar.gz`（保留，static_dir 用户用）+ `checksums.txt`。

**Go 插件跨平台**：manifest 已有 `entry_windows/entry_darwin/entry_linux` 字段，CI 矩阵直接产平台二进制。

#### 3.2 registry repo（`ai-pivot/xbot-plugins`）

```jsonc
// registry.json — raw.githubusercontent.com 直读 + jsDelivr/ghfast 镜像
{
  "version": 1,                      // schema 版本
  "updated_at": "2026-08-30T00:00:00Z",
  "plugins": [
    {
      "id": "xbot.git-fancy",
      "name": "Git Fancy",
      "description": "Git 历史面板 / diff 查看器",
      "author": "ai-pivot",
      "homepage": "https://github.com/ai-pivot/xbot",
      "icon": "icons/git-fancy.svg",          // repo 内相对路径
      "channel": "official",                     // official | community
      "latest": "1.2.0",
      "versions": {
        "1.2.0": {
          "min_xbot_version": "0.9.0",
          "notes": "…",
          "assets": {
            "linux-amd64":  { "url": "…/xbot-plugin-git-fancy-1.2.0-linux-amd64.zip", "sha256": "…" },
            "darwin-arm64":  { "url": "…", "sha256": "…" }
            // script 插件可省 platform 维度，用 "any"
          }
        },
        "1.1.0": { … }   // 保留最近 N=5 个版本，旧的 GitHub release 本身就是归档
      }
    }
  ]
}
```

- **索引更新自动化**：官方插件仓库（本 repo）打 `plugin-<id>-v<version>` tag → GitHub Action 构建上传 release asset → 自动 PR 更新 registry.json（版本 + sha256 由 CI 计算，人只点 merge）。
- **国内镜像**：下载 URL 支持 `GH_MIRROR` 前缀重写（复用 install-cn.sh 的 ghfast/gh-proxy 探测逻辑），registry URL 本身走 jsdelivr CDN 备选。

#### 3.3 官方插件 bundle 随 install 装

`install.sh` / `install.ps1` 增加一步：检测平台 → 下载 `xbot-official-plugins-<os>-<arch>.tar.gz` → 解压 `~/.xbot/plugins/` → **自动写 `channels.genui.enabled=true`**（见 3.5）。

默认 bundle 成员：`xbot.genui`（web GenUI 渲染的核心依赖，display_html 工具）、`xbot.git-fancy`、`xbot.ambience`。

### L2 安装层：单二进制 + 修洞

#### 3.4 前端 go:embed（消灭「dist 分离」问题类）

```
serverapp/webembed/
├── embed.go        // //go:embed all:dist → fs.FS；dist 为空 placeholder 时返回 nil
└── dist/
    ├── .gitkeep    // 提交占位（"前端未构建，请配置 web.static_dir 或安装含前端的发行版"）
    └── (CI/Makefile 从 web/dist 同步)
```

`resolveStaticDir` 优先级变为：`cfg.Web.StaticDir`（显式覆盖，开发模式）→ **embedded FS（非空时）** → `<exe_dir>/web/dist/` → `$XBOT_HOME/web/dist`（保留后两级兼容存量安装）。

CI 编排：frontend job → upload artifact → go job download 到 `serverapp/webembed/dist/` → `go build`。本地 `make build` 同样先跑 `make web`（Makefile 串联）。

效果：
- Windows（install.ps1 不用再下载 dist）、Docker（单阶段 COPY 二进制）、任意裸二进制场景 → Web UI 开箱即用
- 前端与后端版本永远配套（同一 release tag 构造）
- 升级 = 换二进制，`~/.xbot/web/dist` 老文件不再阴魂不散（SW 缓存版本错配问题同步消失）

体积代价：dist ~8MB → 二进制 +8MB（对比 xbot-cli 99MB 可忽略）。

#### 3.5 channel 自动开启（修复 genui 隐性坑）

`InstallPlugin`（plugin/manager.go）激活成功后检查 `manifest.Contributes.ChannelProvider`：
- config 无 `channels.<name>` 条目 → 自动写 `channels.<name>.enabled=true` → 调 `SetChannelConfig`（现有 reconfigureFn 热重启 channel）
- 已有条目但 disabled → 不动（尊重用户选择）
- Web 安装弹窗列出「此插件将开启 genui channel」让 admin 确认（CLI 场景直接自动开 + 日志说明）

#### 3.6 其余安装层修复（Phase 0 速赢）

| 项 | 改动 |
|---|---|
| install.ps1 | 补 `download_web_dist`（对齐 install.sh:352-367）；L2 embed 落地后此步可删 |
| Dockerfile | frontend stage build dist → COPY 进 `serverapp/webembed/dist` → go build（embed 后一步到位） |
| `xbot-runner.service` | **删除**（仓库泄漏真实 token + ws URL） |
| `xbot.service` | 删除（`go run /home/user/...` 开发遗留） |
| `make plugins-install` | 补 ambience + 前端产物同步（`web/~/.xbot/` 意外目录一并清理） |
| runner 分发 | release.yml 加 `cmd/runner` target（backlog，Phase 3） |
| `WatchConfig` | 死代码处理：删除（`plugin_set_enabled` RPC 已持久化 config，轮询方案淘汰） |
| Web onboarding | 首注册用户无 LLM 订阅时显示引导卡（步骤：配 LLM → 浏览插件市场 → 完成）；纯前端实现（`hasSubscription` 判断已有），不动后端 |

### L3 渠道层：Marketplace UI + CLI + RPC

#### 3.7 新 RPC（命名无点号——`web_plugin_rpc` 路由 gotcha）

| RPC | 行为 |
|---|---|
| `plugin_list_available` `{query?}` | 拉取 registry.json（10min 内存 cache + ETag）+ 合并本地安装状态 → `[{id, name, description, icon, author, channel, installed_version?, latest, updatable, permissions}]` |
| `plugin_install_remote` `{id, version?}` | registry 解析 → 平台资产 URL（GH_MIRROR 重写）→ 下载（32MB cap 同现有）→ **sha256 校验（接线现有 `VerifyChecksum`）** → `InstallPluginFromZip` → channel 自动开启 → 返回安装结果 |
| `plugin_update` `{id}` / `plugin_update_all` | 版本比较（semver，registry `latest` vs 本地 manifest `version`）→ 同安装管线原子替换（旧目录 rename 备份 → 装新 → 失败回滚） |
| `plugin_check_updates` | 批量 updatable 计算 |

REST：`/api/plugin/install-remote`（POST，admin 鉴权，复用现有 `handlePluginInstallFile` 的模式）。CLI：`/plugin install <id|path>`（无路径 → registry 查找）、`/plugin search <q>`、`/plugin update [id]`、`/plugin available`。

#### 3.8 config 变更

```jsonc
"plugins": {
  "enabled": true,
  "registries": [
    { "name": "official", "url": "https://raw.githubusercontent.com/ai-pivot/xbot-plugins/main/registry.json", "trusted": true }
  ],
  "auto_update": false
}
```

社区源：Web UI「添加源」入口（红色风险提示「插件执行任意代码」）→ config `registries` 追加 `trusted: false` → 市场列表聚合多源（`channel: community` 标签）。

#### 3.9 Web Marketplace UI（改造 PluginManagerPanel）

```
┌─ 插件 ────────────────────────────────────────┐
│ [已安装] [市场]                     🔍 搜索    │
├───────────────────────────────────────────────┤
│ 已安装 tab（现有列表增强）:                     │
│  · 补卸载按钮（plugin_uninstall RPC 接线）      │
│  · 补「前端内置」分区（activateBuiltins 4 个，  │
│    runtime.listPluginStates 合并——恢复曾被     │
│    实现后被删的合并逻辑）                        │
│  · 每行显示版本 + 「可更新 v1.3.0」badge        │
│                                                │
│ 市场 tab:                                       │
│  ┌──────────────────────────────────────┐     │
│  │ [icon] Git Fancy        official v1.2 │ [安装]│
│  │ Git 历史面板 / diff 查看器   ⭐ 已装 v1.1 │ [更新]│
│  │ by ai-pivot                          │      │
│  └──────────────────────────────────────┘     │
│  安装确认弹窗:                                  │
│   · 权限列表（permissions 逐条中文说明）         │
│   · 「将开启 channel: genui」（channelProvider）│
│   · 「来源: official（受信任）/ community」      │
│  已安装不可用项: 灰色 [已安装 v1.2.0]           │
└───────────────────────────────────────────────┘
```

安装进度：下载（MB 进度）→ 校验 → 解压 → 激活，复用 `web_plugin_init` WS 广播热加载（零刷新）。**卸载**：`plugin_uninstall` + 前端 `web_plugin_deactivate`（均现有）。

### L4 版本与安全

- **版本**：manifest `version` semver（格式校验已有，新写 `Compare` ~50 行）+ registry `min_xbot_version`（`version` 包已有版本比较基础）
- **兼容窗口**：安装/更新时 `xbotVersion < min_xbot_version` → 拒装并提示；主版本不匹配 → 警告
- **完整性**：registry `sha256` 必填 → 下载后校验失败即删（激活现有 `VerifyChecksum` 于 `InstallPluginFromZip` 前）；zip 内 `plugin.sha256` 可选二次校验
- **信任链**：HTTPS + GitHub release 为根；官方 registry `trusted: true`；社区源 UI 红标 + admin-only
- **执行风险**：安装本就是执行任意代码（现有 zip 上传已 admin-only）——远程安装维持 admin-only + 权限弹窗，不额外造沙箱（stdio 插件进程模型未来可加 seccomp，out of scope）

---

## 4. 文件级改动清单

| 层 | 文件 | 改动 |
|---|---|---|
| CI | `.github/workflows/release.yml` | +plugin job（矩阵构建+zip）、+bundle job、go job 串联 frontend artifact（embed） |
| CI | `plugins/*/Makefile`、根 `Makefile` | `make web-build` 前端产物进包；`plugins-install` 补 ambience |
| embed | `serverapp/webembed/embed.go`（新） | `//go:embed all:dist` + `StaticFS() fs.FS` |
| embed | `serverapp/server.go` `resolveStaticDir` | 优先级插入 embedded FS；`web/static_dir` 保持最高 |
| registry | `plugin/registry.go` | 激活死代码：`fetchRegistry`（HTTP+ETag cache+镜像重写）、`InstallFromSource`（下载→校验→zip 管线）、semver `Compare` |
| registry | `config/config.go` | `plugins.registries`、`auto_update` 字段 |
| RPC | `serverapp/rpc_table.go` | 4 个新 handler（无点号） |
| REST | `channel/web/web_api.go` | `/api/plugin/install-remote`（admin gate 复用） |
| channel 自动开 | `plugin/manager.go` `InstallPlugin` | `Contributes.ChannelProvider` → `SetChannelConfig` 回调（serverapp 注入，避免 plugin→serverapp 反向依赖） |
| UI | `web/src/plugins/manager/` | 市场 tab、卸载按钮、安装确认弹窗、更新 badge、内置插件分区 |
| UI | `web/src/plugins/manager/api.ts` | `installRemote/listAvailable/update` API 封装 |
| CLI | `channel/cli/cli_plugin_cmd.go` | `/plugin install <id>` registry 分支、`search/available/update` |
| install | `scripts/install.sh`、`install.ps1`、`install-cn.sh` | +bundle 下载步骤、+channels.genui 预写 |
| install | `Dockerfile` | embed 后简化为两 stage（build+run） |
| onboarding | `web/src/components/`（新） | 首注册引导卡（无 LLM 订阅检测 → 步骤条） |
| 清理 | 根目录 | 删 `xbot-runner.service`（**泄漏 token**）、`xbot.service`；`.gitignore` 确认 `xbot`/`xbot-cli`/`xbot-cli-static` |
| 清理 | `plugin/manager.go` | 删 `WatchConfig` 死代码 |
| 清理 | `web/~/.xbot/` | 删仓库意外目录（`~` 未展开 bug） |

---

## 5. 分期路线与验收标准

### Phase 0 — 修分发地基（~2-3 天，立竿见影）
1. release.yml 插件矩阵构建 + bundle 产物
2. install.sh/ps1 下载 bundle + `channels.genui.enabled=true` 预写
3. `InstallPlugin` channelProvider 自动开启（代码层，不依赖 install 脚本）
4. Dockerfile COPY dist（embed 前的过渡）+ install.ps1 补 web dist
5. 删泄漏 service 文件 + `WatchConfig` + `make plugins-install` 补全
6. PluginManagerPanel 补卸载按钮 + 前端内置插件分区（纯 UI，RPC 现成）

**验收**：install.sh 装完 → Web 打开即有 GenUI（display_html 可用）；插件面板显示 3 官方插件（含版本）+ 可卸载；Docker run 后 Web UI 可用；Windows install.ps1 后 Web UI 可用；`grep -r "Nm391" .github/ xbot-runner.service` 零结果。

### Phase 1 — 单二进制 + Web 引导（~2-3 天）
1. `serverapp/webembed` + `resolveStaticDir` 优先级 + CI 串联
2. Makefile `make build` 自动 `make web`
3. Web onboarding 引导卡
4. `xbot-plugins` repo 建立 + registry.json schema 定稿 + 官方 3 插件首发流程

**验收**：单拷 `xbot-cli` 二进制到空机器 → Web UI 可用；`web/dist` 目录删除后仍可用；首注册用户看到 LLM 引导。

### Phase 2 — Marketplace（~4-5 天）
1. `plugin/registry.go`：fetchRegistry（ETag + 镜像）+ InstallFromSource + semver 比较
2. 4 个新 RPC + REST + admin gate + sha256 接线
3. 市场 UI（浏览/搜索/安装/更新/卸载/权限确认/channel 确认）
4. CLI `/plugin install <id>` 等命令
5. `plugin_update_all` 一键更新（含官方插件升级路径）

**验收**：全新安装的 Web 里市场可见官方插件（已装标灰）；安装社区测试插件 → 重启仍在 → 卸载干净；权限弹窗出现；`min_xbot_version` 拦截旧版本 xbot 装新插件；断网时市场显示缓存 + 明确错误。

### Phase 3 — 生态 DX（~3-4 天，可延后）
1. `xbot-cli plugin init/dev/package/validate` 脚手架（模板 repo）
2. 发布 Action（打 tag → CI 构建 → asset 上传 → registry PR）
3. 社区源 UI 添加（红标警告）+ `registries` config
4. runner 分发（release target + install 支持）
5. 前端插件 runtime API 版本化（`web.runtime_api` 字段，宿主 React 注入协议文档化）

**验收**：第三方按脚手架 30 分钟内产出可发布插件 zip。

---

## 6. 风险与对策

| 风险 | 对策 |
|---|---|
| embed 后二进制体积 +8MB | 可接受（当前 99MB）；`make build-noweb` tag 保留极端场景 |
| CI 前后端 job 顺序耦合 | Makefile/CI 显式串联 + frontend artifact 命名固化 |
| registry 供应链 | 默认仅官方源；sha256 必填；admin-only；权限弹窗；社区源红标 |
| 插件平台资产缺失（如插件只出 linux） | registry `assets` 缺平台 → UI 显示「当前平台不可用」而非装一半 |
| 前端插件与宿主 React 版本耦合 | 沿用 iteration-stats 已验证的 `window.React` 注入模式（runtime 不打包 React）；Phase 3 加 `runtime_api` 版本声明 + 不匹配警告 |
| GitHub 直连不可用（国内） | GH_MIRROR 探测（install-cn 模式）+ jsdelivr registry CDN + 资产 URL 前缀重写 |
| `plugin_set_enabled` 持久化 config 与 install 自动开 channel 的写入竞争 | 均走 `saveServerConfig`（现有单一入口），serverapp 层串行化 |
| 市场 UI 拉 registry 卡顿 | 10min cache + ETag 304 + 前端骨架屏；失败降级为「仅显示已安装」 |

## 7. 明确不做（Out of Scope）

- 自建 registry 服务端/商店后端（GitHub repo 即 registry）
- 插件评分/评论/下载量统计（无后端，无数据源）
- 插件自动更新（仅手动一键 `update_all`；`auto_update` config 字段预留但默认 false 不实现调度）
- MCP server 分发（mcp.json 体系独立，未来可挂市场 tab 第二分类）
- 插件进程沙箱/seccomp（信任模型 = admin 安装即信任，与现状一致）

---

## 附：与 AGENTS.md 既有 gotcha 的呼应

- 新 RPC 命名无点号（`web_plugin_rpc` 最长前缀匹配路由 gotcha）
- 前端插件静态 import vs 动态 import 的 React #311 教训 → 市场 UI 本身是内置静态组件，不受影响；第三方前端插件走既有 `versionedUrl` 动态加载
- `plugin_config` 无点号直传（iteration-stats 关 TTFT 无效事故）→ 市场 UI 的配置跳转复用现有 `plugin_config`
- channels.<name>.enabled 隐性坑 → 本方案 3.5 从安装管线根除
- `web_plugin_list rescan` 增量激活（不用 ReloadAll——DeactivateAll 超时）→ 远程安装完成后调用同一 RPC
