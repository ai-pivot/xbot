# 计划：安装启动引导流程全面优化

> 生成时间：2026-06-01
> 状态：待确认

## 背景与目标

### 问题

当前 xbot 的首次启动引导对非技术用户极不友好：

1. **技术术语壁垒**：Setup Panel 使用 "LLM Provider"、"API Key"、"Base URL"、"Model"、"沙箱模式"、"记忆模式" 等术语，用户完全看不懂
2. **没有概念解释**：不解释什么是 API Key、去哪里获取、为什么需要
3. **没有交互教学**：Setup 完成后用户不知道怎么和 TUI 交互（输入框在哪、怎么发消息、快捷键是什么）
4. **空字段无助提示**：Model 名称默认为空，用户不知道该填什么
5. **无验证反馈**：填完 API Key 不验证是否正确，直到发第一条消息才报错
6. **英文文档割裂**：`tools.md` 为英文，部分 UI 文案可能包含英文
7. **安装脚本依赖不明确**：需要 jq/python3 但不告诉用户怎么装

### 目标

让**完全不懂技术的用户**（不知道什么是 API、什么是密钥、不懂英文、看不懂技术文档）也能：
- 成功安装 xbot
- 理解为什么需要填 API Key，并成功获取和填写
- 学会 TUI 基本操作（打字、发送、快捷键）
- 在 5 分钟内完成首次对话

### 设计原则

1. **大白话**：所有用户可见文案用最简单的语言，不用术语
2. **渐进式**：先教最必要的，高级功能需要时再引导
3. **带路式**：不仅告诉用户"填这个"，还告诉"去哪找、怎么填"
4. **即时验证**：每一步都给反馈，不让用户走完才发现错了
5. **中文优先**：所有文案和文档以中文为主

---

## 现状分析

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `channel/i18n.go` | Setup Schema 定义、所有 UI 文案、Help 内容 | 修改 |
| `channel/cli_panel.go` | Setup Panel / Settings Panel 渲染 | 修改 |
| `channel/cli.go` | 首次运行检测后打开 Setup | 修改 |
| `channel/cli_view.go` | Splash 渲染、Footer 提示 | 修改 |
| `channel/cli_update_handlers.go` | Splash 结束、Setup 提交处理 | 修改 |
| `channel/cli_model.go` | 模型状态定义 | 修改 |
| `channel/cli_settings.go` | 设置读写逻辑 | 修改 |
| `channel/capability.go` | Provider URL 映射、Provider 列表 | 修改 |
| `cmd/xbot-cli/main.go` | isFirstRun、ApplySettings、订阅更新 | 修改 |
| `config/config.go` | 配置结构、加载逻辑 | 可能修改 |
| `docs-site/content/installation.md` | 安装文档 | 修改 |
| `scripts/install.sh` | 安装脚本 | 修改 |

### 依赖关系

```
isFirstRun() → openSetupPanel() → SetupSchema (i18n.go)
                                    ↓
                              ProviderDefaultURLs (capability.go)
                                    ↓
                              ApplySettings → updateActiveSubscription
```

### 风险点

- Setup Panel 和 Settings Panel 共用 panel 框架，修改需注意不影响日常设置
- i18n.go 是三语言（中/英/日）共文件，修改需同步更新所有语言
- API Key 验证需要实际发起 HTTP 请求，可能增加启动延迟
- Setup Panel 字段增减需要同步 capability.go 中的自动填充逻辑

---

## 详细计划

### 阶段一：Setup Panel 重新设计（核心体验改造）

将技术术语替换为通俗描述，添加解释性文案，增加智能默认值和验证。

#### 步骤 1.1：重写 Setup Schema 文案（`channel/i18n.go`）

**当前**：
```
llm_provider: "LLM 供应商"
llm_api_key: "API Key"
llm_model: "模型"
llm_base_url: "Base URL"
sandbox_mode: "沙箱模式"
memory_provider: "记忆模式"
theme: "配色方案"
```

**改为**（仅展示中文版）：
```
llm_provider: "AI 服务商"
  Description: "选择你使用的 AI 服务，就像选择用微信还是 QQ 一样"
  选项增加描述：
    - openai: "OpenAI（ChatGPT 的公司）— 最常用"
    - anthropic: "Anthropic（Claude 的公司）"
    - deepseek: "DeepSeek（深度求索）— 国内直连"
    - moonshot: "Moonshot（月之暗面/Kimi）— 国内直连"
    - zhipu: "智谱（GLM/ChatGLM）— 国内直连"
    - siliconflow: "SiliconFlow（硅基流动）— 国内聚合平台"
    - ollama: "Ollama（本地运行，不需要联网）"
    - openai_compatible: "其他兼容 OpenAI 的服务"
    - custom: "自定义（高级选项）"

llm_api_key: "密钥（也叫 API Key）"
  Description: "这是使用 AI 服务的通行证，就像门禁卡一样。
                👉 还没有？选完服务商后下面会告诉你怎么获取。"
  Placeholder: "sk-..."

llm_model: "AI 模型"
  Description: "可以理解为 AI 的「大脑版本」，不同版本擅长不同的事"
  Placeholder: "选完服务商后自动推荐"
  **根据 provider 动态设置推荐默认值**

llm_base_url: "服务器地址"
  Description: "通常不用改，会自动填好。只有服务商特殊要求时才需要修改"
  **默认隐藏此字段**，只有选择 custom/ollama/openai_compatible 时才显示

sandbox_mode: → **从 Setup 中移除**，使用默认值 none
  （非技术人员不需要理解沙箱概念，首次使用不应暴露）

memory_provider: → **从 Setup 中移除**，使用默认值 flat
  （非技术人员不需要理解记忆模式，首次使用不应暴露）

theme: "界面风格"
  Description: "选一个你喜欢的颜色风格"
  保留
```

**涉及文件**：
- `channel/i18n.go` — SetupSchema 中文定义（~427-485 行）、英文定义（~818-950 行）、日文定义
- `channel/capability.go` — ProviderDefaultURLs 扩充更多国内服务商

#### 步骤 1.2：Provider 选项增加国内服务商（`channel/capability.go`）

当前 `ProviderDefaultURLs` 只有 openai/anthropic/ollama 三个。需要增加国内常用服务商：

```go
// channel/capability.go ProviderDefaultURLs 扩充
"deepseek":     "https://api.deepseek.com",
"moonshot":     "https://api.moonshot.cn/v1",
"zhipu":        "https://open.bigmodel.cn/api/paas/v4",
"siliconflow":  "https://api.siliconflow.cn/v1",
```

同时增加 `ProviderRecommendedModels` map，根据 provider 推荐默认模型：

```go
var ProviderRecommendedModels = map[string]string{
    "openai":            "gpt-4o",
    "anthropic":         "claude-sonnet-4-20250514",
    "deepseek":          "deepseek-chat",
    "moonshot":          "moonshot-v1-auto",
    "zhipu":             "glm-4-flash",
    "siliconflow":       "deepseek-ai/DeepSeek-V3",
    "ollama":            "qwen3:8b",
}
```

#### 步骤 1.3：Provider 选择后动态联动（`channel/cli_panel.go`）

在 Setup Panel 中，用户选择 provider 后自动联动：

1. **自动填充 Base URL**（已有逻辑，扩充新 provider）
2. **自动填充推荐模型**（新增：从 `ProviderRecommendedModels` 获取）
3. **显示 API Key 获取指引**（新增：在 API Key 字段下方显示动态提示）

API Key 获取指引内容（根据 provider 动态显示）：

```
OpenAI:     "👉 获取方式：打开 platform.openai.com → 右上角登录 → API Keys → Create"
Anthropic:  "👉 获取方式：打开 console.anthropic.com → 登录 → API Keys → Create Key"
DeepSeek:   "👉 获取方式：打开 platform.deepseek.com → 登录 → API Keys → 创建"
Moonshot:   "👉 获取方式：打开 platform.moonshot.cn → 登录 → API Key 管理 → 创建"
智谱:        "👉 获取方式：打开 open.bigmodel.cn → 登录 → API Keys → 添加"
硅基流动:    "👉 获取方式：打开 siliconflow.cn → 登录 → API 密钥 → 创建"
Ollama:     "✅ 不需要密钥！只需先安装 Ollama（ollama.com）并运行模型"
```

#### 步骤 1.4：数据结构扩展 — 支持条件字段和选项描述

**扩展 `SettingDefinition`**（`channel/capability.go`）：
```go
type SettingDefinition struct {
    // ... 现有字段 ...
    DependsOnKey    string `json:"depends_on_key,omitempty"`    // 条件依赖的字段 key
    DependsOnValues string `json:"depends_on_values,omitempty"` // 逗号分隔的触发值（满足时显示）
}
```

**扩展 `SettingOption`**（`channel/capability.go`）：
```go
type SettingOption struct {
    Label       string `json:"label"`
    Value       string `json:"value"`
    Description string `json:"description,omitempty"` // 选项说明文字（灰色小字）
}
```

**面板渲染适配**（`channel/cli_panel.go`）：
- `renderSettingsBody` / `trackSettingsZones` 中根据 `DependsOn` 条件过滤字段
- Provider combo 选项渲染时显示 Description

#### 步骤 1.5：API Key 即时验证（`channel/cli_panel.go`）

在 Setup Panel 提交前验证 API Key 有效性（非 Ollama 时）：

1. 提交时，如果填了 API Key 且有 LLM 字段变更，先异步调用一次轻量请求（用 `client.ListModels()` 或简单 chat completion `{"model":"x","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`）
2. 成功 → 正常保存
3. 失败 → 显示友好错误并阻止提交（提供"仍然保存"选项）：
   - 401/403: "❌ 密钥不正确，请检查是否复制完整（注意前后不要有空格）"
   - 网络错误: "⚠️ 无法连接到服务商，请检查网络。如果你在国内，可能需要设置代理"
   - 其他: "❌ 验证失败：{错误信息}"

**涉及文件**：
- `channel/capability.go` — 数据结构扩展
- `channel/cli_panel.go` — 提交逻辑 + 条件渲染 + 验证步骤
- `channel/cli_update_handlers.go` — 验证结果的 UI 反馈

#### 步骤 1.6：Setup Panel 必填校验 + 欢迎标题

- API Key 字段：非 Ollama 时**必须**填写才能提交
- 提交时如果 API Key 为空，显示： "⚠️ 还没有填写密钥。AI 服务需要密钥才能使用。如果你选择了 Ollama，则不需要密钥"
- 在面板顶部添加说明文字（仅 Setup 模式显示）：

```
╭─────────────────────────────────────────╮
│  👋 欢迎使用 xbot！                      │
│                                         │
│  要开始使用，需要做 2 件事：              │
│  1. 选择一个 AI 服务商                   │
│  2. 填入你的密钥（选 Ollama 可跳过）      │
│                                         │
│  其他选项可以先不填，之后随时可以改       │
╰─────────────────────────────────────────╯
```

#### 步骤 1.7：条件隐藏/显示字段

根据用户选择动态显示/隐藏字段：
- **Base URL**：选择 custom/ollama/openai_compatible 时显示，其他 provider 时隐藏
- **Sandbox Mode**：从 Setup 中移除（首次不需要）
- **Memory Provider**：从 Setup 中移除（首次不需要）
- **Model**：有推荐值时预填，允许用户修改

最终 Setup 只显示：
1. AI 服务商（必选）
2. 密钥（Ollama 外必填）
3. AI 模型（自动推荐，可改）
4. 服务器地址（默认隐藏，仅部分 provider 显示）
5. 界面风格（可选）

#### 步骤 1.8：无 LLM 配置时的友好提示（`channel/cli.go`）

如果用户关闭 Setup 但未配置 API Key，发送第一条消息时检测到无可用 LLM 配置，在对话区显示：

```
⚠️ 还没有配置 AI 服务密钥，暂时无法对话。

按 /setup 重新配置，或按 /settings 打开完整设置。

📖 如果你不确定怎么做，输入 /help 查看入门指南。
```

**涉及文件**：
- `channel/cli.go` — 消息发送前检测 `activeSubscription.APIKey == ""`
- `channel/i18n.go` — 新增 `NoLLMConfig` i18n key

---

### 阶段二：Setup 完成后的 TUI 交互引导

#### 步骤 2.1：首次启动欢迎消息（`channel/cli.go` + `channel/i18n.go`）

Setup 完成后，在对话区显示一条欢迎消息（system message），用通俗语言教用户如何操作：

```
╭──────────────────────────────────────────────────╮
│  🎉 设置完成！你可以开始和 AI 对话了               │
│                                                  │
│  📝 怎么用：                                      │
│  • 在底部输入框打字，按 Enter 发送                 │
│  • 想换行？按 Ctrl+J                              │
│  • 输入 /help 查看更多操作                         │
│                                                  │
│  ⌨️ 常用快捷键：                                   │
│  • Ctrl+K — 命令面板（可以执行各种操作）            │
│  • Ctrl+T — 查看和切换对话                         │
│  • Ctrl+P — 切换 AI 模型                          │
│  • Ctrl+C — 取消 AI 正在生成的回复                 │
│                                                  │
│  💡 小提示：直接用大白话和 AI 说话就行，             │
│  比如"帮我写一个 Python 脚本"或"解释一下这段代码"     │
╰──────────────────────────────────────────────────╯
```

**涉及文件**：
- `channel/cli.go` — Setup 完成回调中调用 `showSystemMsg(welcomeContent)`
- `channel/i18n.go` — 新增 `WelcomeMessage` / `WelcomeTitle` i18n key

#### 步骤 2.2：增强 IdlePlaceholder（`channel/i18n.go`）

当前只有 6 条旋转提示，增加更多教学性提示：

```go
// 新增 idle placeholder（中文）
"在下面输入你的问题，按 Enter 发送",           // 最基础的
"试试问：帮我写一个贪吃蛇游戏",                 // 示例引导
"Ctrl+K 打开命令面板，发现更多功能",              // 功能发现
"Ctrl+T 管理你的对话",                          // 会话管理
"/help 查看所有快捷键和命令",                    // 帮助入口
"输入 @文件名 可以让 AI 读取你的文件",            // 文件功能
```

#### 步骤 2.3：首条消息发送后的额外提示

用户发送第一条消息后，在 Footer 区域短暂显示一条鼓励性提示：

```
"✅ 发送成功！AI 正在思考，稍等片刻..."
```

AI 回复完成后：
```
"💬 收到回复了！继续提问，或用 Ctrl+J 换行写多行内容"
```

这些提示在显示 5 秒后自动消失，不干扰正常使用。

**涉及文件**：
- `channel/cli_update_handlers.go` — 首条消息检测 + 提示渲染
- `channel/cli_model.go` — 增加 `firstMessageSent` 状态标记

---

### 阶段三：Splash 启动画面优化

#### 步骤 3.1：Splash 增加进度提示（`channel/cli_view.go`）

当前 Splash 只有 spinner 动画 + "初始化中..."。改为显示具体阶段：

```
         xbot
        AI 驱动的终端助手

   ⣻  正在准备...         ← 变化
       ↓
   ⣻  正在连接 AI 服务...  ← 变化
       ↓
   ⣻  就绪！
```

**涉及文件**：
- `channel/cli_view.go` — `renderSplash()` 增加阶段文案
- `channel/cli_update_handlers.go` — 更新阶段状态

#### 步骤 3.2：首次运行时 Splash 添加欢迎语

如果是首次运行（`isFirstRun`），Splash 描述改为：

```
"👋 欢迎使用 xbot！正在为你准备初次设置..."
```

非首次运行保持原来的 "AI 驱动的终端助手"。

**涉及文件**：
- `channel/cli_view.go` — 根据 `isFirstRun` 条件渲染
- `channel/i18n.go` — 新增 `SplashFirstRun` i18n key

---

### 阶段四：Help 面板内容优化

#### 步骤 4.1：重写 /help 内容（`channel/i18n.go`）

当前 `/help` 只有命令列表和快捷键列表，缺少功能说明。改为分层结构：

```
╭───────────────────────────────────────────────────╮
│  📖 使用指南                                       │
│                                                   │
│  ✏️ 基本操作                                       │
│  • 在输入框打字 → Enter 发送                       │
│  • Ctrl+J 换行                                     │
│  • Tab 自动补全文件名                               │
│  • Ctrl+C 取消 AI 正在生成的回复                    │
│                                                   │
│  📁 文件操作                                       │
│  • 输入 @文件路径 可以把文件内容发给 AI              │
│  • AI 可以直接帮你读取、编辑、创建文件               │
│                                                   │
│  🔄 会话管理                                       │
│  • Ctrl+T 查看所有对话                              │
│  • /new 新建对话                                   │
│  • /su 切换到其他对话                               │
│                                                   │
│  ⚙️ 设置                                          │
│  • /settings 打开设置面板                           │
│  • /setup 重新运行初始设置向导                      │
│  • Ctrl+P 快速切换 AI 模型                          │
│  • Ctrl+K 命令面板（所有操作的入口）                 │
│                                                   │
│  ⌨️ 所有快捷键                                     │
│  Ctrl+K 命令面板    Ctrl+T 会话管理                 │
│  Ctrl+P 切换模型    Ctrl+N 下一个模型               │
│  Ctrl+J 换行        Tab   补全文件名                │
│  Ctrl+E 折叠回复    Ctrl+C 取消                    │
│  Ctrl+O 工具详情    ↑/↓   历史消息                  │
│                                                   │
│  📋 所有命令                                       │
│  /help   帮助       /new   新建对话                 │
│  /clear  清空对话   /compress 压缩历史              │
│  /setup  初始设置   /settings 完整设置              │
│  /models 查看模型   /set-model 切换模型             │
│  /usage  查看用量   /cancel 取消                    │
│  /rewind 回退消息   /update 更新                    │
╰───────────────────────────────────────────────────╯
```

**涉及文件**：
- `channel/i18n.go` — `HelpContent` / `HelpCmds` / `HelpKeys` 重写
- `channel/cli_message.go` — `renderHelpPanel()` 调整布局

---

### 阶段五：安装文档优化

#### 步骤 5.1：新增「新手入门」文档（`docs-site/content/getting-started.md`）

创建一篇面向纯新手的入门指南，**假设用户完全不懂技术**：

```markdown
# 新手入门指南

## 你需要准备什么

只需要一样东西：一个 AI 服务的「密钥」。

密钥是什么？可以理解为一把钥匙，让你能使用 AI 服务。
就像你需要微信密码才能用微信一样。

## 第一步：获取密钥

### 如果你在中国大陆（推荐）

推荐以下服务，访问速度快，价格便宜：

#### DeepSeek（深度求索）— 最推荐
1. 打开 platform.deepseek.com
2. 注册并登录（手机号即可）
3. 点击左侧「API Keys」
4. 点击「创建 API Key」
5. 复制生成的密钥（以 sk- 开头）
6. 💰 新用户通常有免费额度，可以先免费体验

#### 智谱（GLM）
1. 打开 open.bigmodel.cn
2. 注册并登录
3. ...类似步骤

#### 硅基流动（SiliconFlow）
...类似步骤

### 如果你能访问国外网站

#### OpenAI（ChatGPT 的公司）
1. 打开 platform.openai.com
2. 注册并登录
3. 点击右上角头像 → View API Keys
4. 点击 Create new secret key
5. 复制密钥
6. 💰 需要绑定信用卡

## 第二步：安装 xbot

### macOS / Linux

打开「终端」应用，复制粘贴下面这行命令，按回车：

    curl -fsSL https://raw.githubusercontent.com/.../install.sh | bash

按照提示选择即可，不知道选什么就一直按回车用默认选项。

### Windows

...类似

## 第三步：首次设置

安装完成后，在终端输入 `xbot-cli` 按回车。

会弹出一个设置界面：
1. **AI 服务商**：选择你刚才注册的服务（比如 DeepSeek）
2. **密钥**：粘贴刚才复制的密钥
3. **AI 模型**：一般会自动填好，不用改
4. **界面风格**：选一个喜欢的颜色

按 Ctrl+S 保存设置。

## 第四步：开始对话！

设置完成后，在底部的输入框打字，按 Enter 发送。

试试问：
- "你好，请介绍一下你自己"
- "帮我写一个 Python 脚本，计算 1 到 100 的和"
- "今天天气怎么样？"

## 常见问题

### ❌ 提示"密钥不正确"
- 检查密钥是否复制完整（前后不要有空格）
- 确认密钥没有过期

### ❌ 提示"无法连接"
- 如果在国内使用 OpenAI/Anthropic，需要配置网络代理
- 推荐使用国内服务商（DeepSeek、智谱等）

### ❌ 没有反应
- 按 Ctrl+C 取消当前操作
- 按 /help 查看帮助
```

#### 步骤 5.2：更新 README 的快速开始部分

在 README 的"快速开始"中加入获取 API Key 的简要说明和链接到入门文档。

#### 歃骤 5.3：翻译 tools.md 为中文

将 `docs-site/content/tools.md` 从英文翻译为中文。

**涉及文件**：
- `docs-site/content/getting-started.md` — 新建
- `README.md` — 修改快速开始部分
- `docs-site/content/tools.md` — 翻译
- `docs-site/content/installation.md` — 修改

---

### 阶段六：首次运行后的智能引导消息

#### 步骤 6.1：System Prompt 中的新手引导增强

当检测到用户是第一次使用（消息数 < 3），在 system guide 中注入额外的引导提示：

```
[System Guide - 新手模式]
这是一个新用户，可能不熟悉技术术语。请：
- 用简单的语言回答
- 避免使用专业术语，如果必须使用请解释
- 主动提供操作建议
- TUI 操作提示：Ctrl+K 命令面板, /help 帮助, Ctrl+T 会话管理
```

**涉及文件**：
- `agent/middleware_builtin.go` — `buildSystemGuideText()` 增加新手模式判断

#### 步骤 6.2：AI 首条回复模板

如果系统检测到这是用户的首次对话，可以在 AI 的第一条回复中包含操作小贴士：

这通过 system prompt 实现，不需要额外的代码改动（步骤 6.1 的 system guide 已包含）。

---

### 阶段七：安装脚本优化

#### 步骤 7.1：安装脚本增加友好提示（`scripts/install.sh`）

在安装脚本中：
1. 缺少 jq/python3 时，给出具体的安装命令（如 `apt install jq`、`brew install jq`）
2. 安装完成后显示下一步操作提示：

```
✅ xbot 安装完成！

接下来：
1. 在终端运行 xbot-cli
2. 选择一个 AI 服务商
3. 填入密钥

📖 详细图文教程：https://xbot.dev/getting-started
```

3. 国内安装脚本 `install-cn.sh` 的注释改为中文

**涉及文件**：
- `scripts/install.sh` — 错误提示优化
- `scripts/install-cn.sh` — 注释中文化

---

## 验证方案

### 功能验证

1. **首次启动流程**：删除 `~/.xbot/config.json` 和 `~/.xbot/xbot.db`，运行 `xbot-cli`，验证：
   - Splash 显示 "欢迎使用"
   - Setup Panel 显示通俗文案
   - 选择 provider 后模型名自动填充
   - Ollama 时 API Key 不再必填
   - Base URL 默认隐藏
   - 沙箱/记忆选项不再显示
   - 提交时验证 API Key
   - 完成后显示欢迎消息

2. **API Key 验证**：
   - 填入错误 key → 显示友好错误提示
   - 填入正确 key → 正常保存
   - 不填 key + 非 Ollama → 阻止提交并提示
   - 不填 key + Ollama → 允许提交

3. **日常使用不受影响**：
   - `/settings` 打开完整设置面板，包含所有高级选项
   - `/setup` 重新运行简化版向导
   - 已有配置用户启动不触发 Setup

4. **文档验证**：
   - `getting-started.md` 内容完整、语言通俗
   - README 链接到新文档
   - tools.md 已翻译为中文

### 回归验证

- `go build ./...` 编译通过
- `go test ./channel/...` 通过
- 非首次运行时（已有 config.json + API key）正常启动，不弹出 Setup
- `/settings` 完整设置面板包含所有原有字段
- 英文/日文 locale 正常显示（无遗漏的 i18n key）

## 回滚策略

- 所有文案修改集中在 `i18n.go`，可 git revert 单文件回滚
- Setup Panel 逻辑修改在 `cli_panel.go`，与 Settings Panel 共用框架但不影响 Settings 的 schema
- API Key 验证为新增逻辑，可通过配置开关控制是否启用
- 新增文档为独立文件，不影响现有功能

### 自审发现

1. **⚠️ 条件字段隐藏需要扩展数据结构** — `SettingDefinition` 没有 `DependsOn` / `ShowCondition` 字段。需要在 `channel/capability.go` 中扩展 `SettingDefinition` struct，增加 `DependsOnKey string` 和 `DependsOnValue string`（如 `DependsOnKey:"llm_provider", DependsOnValue:"ollama,custom,openai_compatible"` 表示只有选这些值时才显示）。面板渲染逻辑 `cli_panel.go` 中需要过滤不满足条件的字段。

2. **⚠️ Provider 选项需要描述文字** — `SettingOption` 只有 `Label` 和 `Value`。需要在 `channel/capability.go` 中增加 `Description string` 字段，用于显示"OpenAI（ChatGPT 的公司）— 最常用"这样的说明。

3. **✅ Combo 类型适合模型选择** — `SettingTypeCombo` 支持下拉选项 + 自由输入，完美适合模型选择。

4. **⚠️ 需要处理"跳过 Setup"路径** — 如果用户关闭 Setup 但没填 API Key，后续发消息应给出友好提示而不是技术报错。需要在 engine 或 channel 层检测"无可用 LLM 配置"时显示引导消息。

5. **✅ 欢迎消息框** — 可以在 Setup Panel 渲染前（`openSettingsPanel` 调用前）通过 `showSystemMsg` 插入一段欢迎文本，不需要修改 panel 框架。

6. **✅ 步骤具体性** — 每个步骤都指向具体文件和函数，可直接执行。

7. **✅ 文件路径准确** — 与 explore agent 的发现一致。

8. **✅ 向后兼容** — Setup Schema 和 Settings Schema 独立，互不影响。

---

## ✅ 自审通过（已修正）

已在上述自审中发现的问题补充到对应步骤中。

- **三语言同步**：`i18n.go` 中 zh/en/ja 三套文案都需要更新，不能只改中文
- **Settings Panel 不受影响**：Setup Schema 是精简版（`SetupSchema`），Settings Schema 是完整版（`SettingsSchema`），两者独立
- **Provider 列表可扩展**：新增 provider 只需在 `ProviderDefaultURLs` + `ProviderRecommendedModels` + i18n 的 combo options 三处添加
- **向后兼容**：已有的 config.json / DB 订阅不受影响，`isFirstRun()` 检测逻辑不变
- **渐进式发布**：可以先发布阶段一（Setup 改造），验证无问题后再发布后续阶段

---

## 实施优先级

| 优先级 | 阶段 | 预计工作量 | 用户价值 |
|--------|------|-----------|---------|
| P0 🔴 | 阶段一：Setup Panel 重新设计 | 3-4 天 | 直接解决"不会配置"的核心问题 |
| P0 🔴 | 阶段二：TUI 交互引导 | 1-2 天 | 解决"不会用"的问题 |
| P1 🟡 | 阶段三：Splash 优化 | 0.5 天 | 提升第一印象 |
| P1 🟡 | 阶段四：Help 面板优化 | 1 天 | 持续参考 |
| P1 🟡 | 阶段五：文档优化 | 2-3 天 | 安装前置引导 |
| P2 🟢 | 阶段六：智能引导 | 1 天 | 降低对话门槛 |
| P2 🟢 | 阶段七：安装脚本 | 0.5 天 | 安装体验 |
