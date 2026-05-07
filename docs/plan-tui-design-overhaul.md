# xbot TUI 视觉设计重构方案

> 生成时间：2026-05-07
> 状态：待确认
> 目标：一次改完。超越 Crush，建立 xbot 自己的可配置视觉语言

---

## 一、核心思路

### 1.1 一句话

> xbot 的 TUI 应该像 VS Code 一样——**布局可配、内容可配、宽度自适应**，同时拥有比 Crush 更精致的视觉签名。

### 1.2 三个设计支柱

| 支柱 | 含义 |
|------|------|
| **自适应布局** | 窄屏单列、中屏居中限宽、宽屏侧边栏——自动变形，用户也可手动覆盖 |
| **高度可配置** | 侧边栏放什么、放在哪、多宽、聊天区多宽——全部用户可配，持久化 |
| **统一视觉签名** | `◈` 菱形系统贯穿全局，图标统一、色板升级、面板差异化 |

### 1.3 与 Crush 的差异化定位

| 维度 | Crush | xbot 目标 |
|------|-------|-----------|
| 布局灵活性 | sidebar 固定 30px 右侧，宽度/位置/内容全硬编码 | **完全可配置**：宽屏 sidebar 可左可右、宽度可拖、内容可排序 |
| 宽屏体验 | 有 sidebar 但内容固定不可定制 | 三档自适应 + 居中限宽 + 可配 sidebar |
| 视觉签名 | `╱` 斜纹 + `▌` 焦点 | `◈` 菱形 + `◇→◆` 焦点 + `┊` 虚线引导 |
| 气质 | 冷蓝紫科技、解码美学 | 温金属工坊、精密机械美学 |
| 配置深度 | 3 个 TUI 选项 | 完整的布局配置系统（sidebar/chat/content 三区可配） |

---

## 二、自适应布局系统

### 2.1 三档自动布局

布局根据终端宽度自动选择，用户也可通过设置手动覆盖：

#### 窄屏 <80 列：单列紧凑（保持现状，已经很好）

```
┌──────────────────────────────────────┐
│ ◈ xbot [dir]                         │
├──────────────────────────────────────┤
│ 消息流（全宽）                        │
├──────────────────────────────────────┤
│ ◈ Ready · 5 msgs                     │
├──────────────────────────────────────┤
│ Footer                               │
├──────────────────────────────────────┤
│ ╭ Input ──────────────────────────╮  │
│ ╰─────────────────────────────────╯  │
└──────────────────────────────────────┘
```

- 不显示 sidebar
- 消息不限宽（屏幕本身就是限制）
- 所有面板以 overlay 形式弹出

#### 中屏 80-120 列：单列居中限宽

```
┌──────────────────────────────────────────────────────────────┐
│ ◈ xbot [dir]  ·· ·· ·· ··  gpt-4o              Ctrl+K      │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│          ┊ 15:04  ◆ Assistant                                │
│          ┊ 消息内容（限宽 ~76 字符）                           │
│          ┊                                                   │
│          ┊                               15:05  ◇ You        │
│          ┊                          用户内容右对齐             │
│                                                              │
├ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─┤
│ ◈ Ready · 5 msgs · 12k/128k                     ████░░      │
├──────────────────────────────────────────────────────────────┤
│ Ctrl+K Palette   Tab Complete   Ctrl+E Fold           /help │
├──────────────────────────────────────────────────────────────┤
│ ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈│
│ ╭──────────────────────────────────────────────────────────╮ │
│ ║ ◈ placeholder text...                                    ║ │
│ ╰──────────────────────────────────────────────────────────╯ │
└──────────────────────────────────────────────────────────────┘
```

- 消息内容居中，最大宽度 = `chat_max_width` 设置项（默认 76）
- 两侧留白区域可以放非常淡的装饰（竖线、点阵）
- 不显示 sidebar
- Input Box 和 Status Bar 保持全宽

#### 宽屏 >120 列：双栏（sidebar + 居中聊天）

```
┌──────────┬────────────────────────────────────────────────────────┐
│ Sessions │        ◈ xbot [dir]  ·· ·· ··  gpt-4o      Ctrl+K    │
│──────────│────────────────────────────────────────────────────────│
│ ● chat1  │                                                    │
│ ○ chat2  │          ┊ 15:04  ◆ Assistant                      │
│ ○ chat3  │          ┊ 消息内容（限宽 ~76 字符）                 │
│          │          ┊                                         │
│──────────│          ┊                          15:05  ◇ You    │
│ ◈ Status │          ┊                     用户内容右对齐        │
│ · bg: 2   │                                                    │
│ · agent: 1│ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─│
│──────────│ ◈ Ready · 5 msgs · 12k/128k               ████░░   │
│ ⚙ Model  │ ╭──────────────────────────────────────────────────╮ │
│ gpt-4o   │ ║ ◈ placeholder text...                            ║ │
│          │ ╰──────────────────────────────────────────────────╯ │
└──────────┴────────────────────────────────────────────────────┘
```

- Sidebar 宽度 = `sidebar_width` 设置项（默认 20，范围 16-40）
- Sidebar 位置 = `sidebar_position` 设置项（`left` 或 `right`，默认 `left`）
- 聊天区仍然居中限宽（独立于 sidebar）
- `Ctrl+B` 快速 toggle sidebar 显示/隐藏
- sidebar 可通过 `Ctrl+Shift+B` 切换位置（左/右）

### 2.2 布局计算逻辑

```go
// 伪代码：布局决策树
func computeLayout(width, height int, cfg LayoutConfig) Layout {
    sidebarVisible := cfg.SidebarEnabled && width >= cfg.SidebarMinWidth
    
    if sidebarVisible {
        sidebarW := cfg.SidebarWidth  // 默认 20
        chatW := width - sidebarW - 2  // -2 for separator + padding
        contentMaxW := min(chatW, cfg.ChatMaxWidth) // 默认 76
        // sidebar 在左或右
        return DualColumn{Sidebar: sidebarW, Chat: chatW, ContentMax: contentMaxW}
    } else {
        contentMaxW := min(width - 4, cfg.ChatMaxWidth)
        return SingleColumn{Width: width, ContentMax: contentMaxW}
    }
}
```

**关键**：`cfg.ChatMaxWidth` 控制消息内容的最大渲染宽度。默认 76（适合阅读），设为 0 表示不限宽（回到当前行为）。

---

## 三、布局配置系统（VS Code 风格）

### 3.1 配置项一览

所有配置项加入 `AllSettingDefs`，scope = `ScopeUser`，持久化到 user_settings DB：

| Key | 类型 | 默认值 | 说明 |
|-----|------|--------|------|
| `layout_mode` | string | `"auto"` | `"auto"` / `"single"` / `"dual"` — 布局模式，auto 自动按宽度切换 |
| `sidebar_enabled` | bool | `true` | 宽屏时是否显示 sidebar |
| `sidebar_width` | int | `20` | Sidebar 宽度（字符），范围 16-40 |
| `sidebar_position` | string | `"left"` | `"left"` / `"right"` |
| `sidebar_sections` | string | 见下 | 有序的 section 列表（JSON array） |
| `chat_max_width` | int | `76` | 消息内容最大宽度，0=不限宽 |
| `chat_center` | bool | `true` | 中屏时是否居中限宽 |

### 3.2 Sidebar Sections 可配置

`sidebar_sections` 是一个 JSON 数组，定义 sidebar 从上到下显示哪些 section：

**默认值**：
```json
["sessions", "status", "model"]
```

**可选 section**：

| Section ID | 内容 | 说明 |
|-----------|------|------|
| `sessions` | 会话列表 | 当前所有会话，活跃会话高亮 |
| `status` | 工具/Agent 运行状态 | bg tasks、active agents 数量和简要信息 |
| `model` | 当前模型 + 连接状态 | 模型名、provider、连接状态 |
| `tools` | 工具执行摘要 | 最近 N 个工具的执行结果 |
| `context` | 上下文使用量 | token 用量进度条、压缩状态 |
| `agents` | SubAgent 状态 | 活跃 SubAgent 列表、角色、状态 |
| `files` | 相关文件 | 当前对话涉及的文件列表 |
| `shortcuts` | 快捷键参考 | 常用快捷键速查 |

**Section 可折叠**：每个 section 标题行可 `Enter` 折叠/展开，折叠状态持久化。

**Section 配置示例**：
```json
["sessions", "context", "model", "status", "shortcuts"]
```

用户把 `context` 放第二位、`status` 放第四位——完全自由。

### 3.3 配置入口

1. **Settings Panel**（`Ctrl+,`）— 新增 "Layout" 分类，包含所有布局配置项
2. **命令面板**（`Ctrl+K`）— 新增命令：
   - `View: Toggle Sidebar` (`Ctrl+B`)
   - `View: Flip Sidebar Position` (`Ctrl+Shift+B`)
   - `View: Reset Layout`（恢复默认布局）
3. **CLI 参数**：
   - `xbot-cli --sidebar-width 25`
   - `xbot-cli --no-sidebar`
   - `xbot-cli --chat-width 100`

---

## 四、视觉签名系统

### 4.1 xbot 的签名元素：`◈` 菱形

Crush 有 `╱` 斜纹，xbot 用菱形家族建立自己的视觉语言。

| 符号 | 用途 | 场景 |
|------|------|------|
| `◈` | 品牌标记 | Logo、面板标题前缀、状态标记、分隔线装饰 |
| `◆` | 焦点/活跃 | 当前发言人指示器、选中项标记 |
| `◇` | 非焦点/默认 | 非活跃发言人、未选中项 |
| `┊` | 虚线引导 | 消息引导线（活跃） |
| `┆` | 暗淡引导 | 消息引导线（完成/历史） |
| `┈` | 点线分隔 | 区域分隔、进度条轨道、Title Bar 填充 |

### 4.2 签名应用矩阵

| 位置 | 当前 | 目标 |
|------|------|------|
| Logo wordmark | `xbot` + `╱` 斜纹填充 | `◈` + "xbot" 渐变文字 + `··` 点阵填充 |
| 消息焦点指示 | 无 | 消息左侧 `◇→◆`（空心=非焦点，实心=焦点） |
| 消息引导线 | `│` 实线 | `┊` 虚线（活跃）/ `┆` 暗淡虚线（完成） |
| 状态标记 | `●` | `◈` |
| 区域分隔 | 空行 | `┈` 点线 |
| Context 进度条 | `─`/`┊` 混用 | `┈` 点线统一 |
| 面板标题 | 纯文字 | `◈ Title ┈┈┈┈┈┈` |

---

## 五、配色方案升级

### 5.1 色板扩展：3 级 → 5 级前景 + 5 级背景

在所有 9 套主题中增加以下槽位：

**新增前景**：
```
FGBright    ← 最亮，用于焦点高亮和交互反馈（midnight: #ffffff）
```

**新增背景**：
```
BGHover     ← 选中/悬停行背景（midnight: #2d2f3e）
BGInset     ← 代码块、思考框（比主背景更深）（midnight: #171827）
BGOverlay   ← 全屏覆盖层（最深）（midnight: #0d0e1a）
```

### 5.2 语义色增强：4 色 → 8 色

```
当前 4 色:               新增 4 色弱化版（用于次要信息）：
Success       #81c995    SuccessMuted   #4a7a5a
Warning       #fdd663    WarningMuted   #8a7a3a
Error         #f28b82    ErrorMuted     #7a4a42
Info          #8ab4f8    InfoMuted      #4a5a7a
```

### 5.3 Accent 渐变对

每个主题新增 `AccentStart` + `AccentEnd` 两个色值，用于 Logo 渐变、Spinner 渐变、Title Bar 渐变：

```
midnight:   #8c9eff → #667eea
ocean:      #22d3ee → #0ea5e9
forest:     #4ade80 → #22c55e
sunset:     #fb923c → #ea580c
rose:       #f472b6 → #db2777
mono:       #f0f6fc → #484f58
nord:       #88c0d0 → #5e81ac
dracula:    #bd93f9 → #6272a4
catppuccin: #cba6f7 → #89b4fa
```

### 5.4 Context 进度条主题化

消除 `ctxBarStyles` 的硬编码颜色，改为主题派生：

```
BarSafe    = 主题 Success 色（<50%）
BarCaution = 主题 Warning 色（50-80%）
BarDanger  = 主题 Error 色（>80%）
BarTrack   = 主题 FGSubtle 色（空轨道）
```

---

## 六、图标系统统一

### 6.1 原则：全部 Unicode，零 Emoji

消除所有 Emoji 图标（`🤖🟢🟡👤🔍`），替换为 Unicode 字符。

### 6.2 完整替换表

| 当前 | 替换为 | 用途 |
|------|-------|------|
| `🤖` | `◆` | Agent/SubAgent |
| `🟢` | `◉` | Runner 在线 |
| `🟡` | `◎` | Runner 连接中 |
| `👤` | `▣` | 用户 |
| `🔍` | `◈` | 搜索 |
| `●` | `◉` | 进行中/活跃（更精致） |
| `✓` | `✓` | 保持不变 |
| `✗` | `✗` | 保持不变 |
| `⚙` | `⚙` | 保持不变 |
| `☁` | `◈` | 已连接 |
| `⊘` | `◘` | 已断开 |
| `◌` | `◌` | 连接中 |

---

## 七、面板视觉差异化

### 7.1 当前问题

所有面板（Settings/Sessions/Palette/Rewind/Approval）共用 `PanelBox`（RoundedBorder + Accent），零辨识度。

### 7.2 面板视觉身份表

| 面板 | 边框风格 | 标题装饰 | 色调 |
|------|---------|---------|------|
| **Command Palette** | `RoundedBorder` + Accent | `◈ Commands` 渐变标题 | 品牌色 |
| **Settings** | 左粗线 `▎` + 无上下右边框 | `◈ Category ┈┈┈` | Accent + 分组色 |
| **Sessions** | `RoundedBorder` + Info 色 | `◈ Sessions ┈┈┈` | Info 蓝 |
| **Rewind** | 虚线边框 `┌┄┄┐` | `◈ History ┈┈┈` | FGMuted |
| **Approval/Danger** | `RoundedBorder` + Error 色粗边 | `⚠ Approval Required` | Error 红 |
| **Quick Switch** | 无边框 + 色块对比 | 直接色块 | 对比色 |
| **Help** | `RoundedBorder` + FGSecondary | `◈ Shortcuts ┈┈┈` | 中性 |
| **Ask User** | `RoundedBorder` + Warning | `◈ Question ┈┈┈` | Warning 黄 |

---

## 八、组件级设计规范

### 8.1 Title Bar

```
当前: xbot [workdir] ╱╱╱╱╱╱╱╱╱╱ Ctrl+K Palette
目标: ◈ xbot [dir]  ·· ·· ·· ··  gpt-4o │ Ctrl+K
```

- `╱` 斜纹 → `··` 点阵填充（间隔 2 字符），配合 Accent 渐变
- 新增模型名显示（在点阵右侧、快捷键左侧）
- 窄屏隐藏模型名和点阵

### 8.2 Status Bar

```
当前: ● Ready · 5 msgs · gpt-4o
目标: ◈ Ready · 5 msgs · 12k/128k tokens
```

- `●` → `◈`
- 新增 token 用量概览
- 右侧显示 TODO 进度条 `◈━━━━━░░░░`

### 8.3 Input Box

```
┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈  ← Context 进度条（点线风格）
╭──────────────────────────────────────────────────────────╮
║ ◈ placeholder text...                                     ║
╰──────────────────────────────────────────────────────────╯
```

- Context 进度条统一使用 `┈` 点线
- Placeholder 前加 `◈`
- 焦点时边框色 = Accent，失焦时 = FGMuted

### 8.4 Thinking Box

```
当前: ╭─ Thinking ──────────╮
      │  推理内容            │
      ╰─────────────────────╯

目标: ┆ ◇ Thinking ┈┈┈┈┈┈┈┈┈┈┈┈┈┈
      ┆ 推理内容（BGInset 深色背景）
      ┆ 推理内容...
      ┆···  （折叠提示）
```

- 去掉边框，改为虚线引导线 `┆`
- 背景使用 `BGInset`（比主背景更深，反向层次）
- 默认折叠 6 行

### 8.5 Sidebar Section 样式

每个 sidebar section 格式统一：

```
◈ Section Title ┈┈┈┈┈
  ● item 1（活跃）
  ○ item 2（非活跃）
  ○ item 3
```

- 标题行：`◈` + 标题 + `┈` 填充线
- 内容缩进 2 字符
- 活跃项 `●`，非活跃项 `○`
- 选中行 `BGHover` 背景高亮
- 折叠状态：`▸ Section Title ┈┈┈┈┈`（▸ 表示折叠）

### 8.6 Toast 通知（新增）

```
┌──────────────────────────────────────┐
│ ◈ Session saved                    × │  ← 右上角浮动
└──────────────────────────────────────┘
```

- 独立于聊天流的浮动通知，不注入消息流
- 3 秒自动消失
- 位于 viewport 右上角固定位置
- 成功=Success 色，警告=Warning 色，错误=Error 色

---

## 九、动效设计

### 9.1 Spinner：脉冲菱形阵列

```
帧1: ◇ ◇ ◇ ◇ ◇     帧3: ◇ ◆ ◆ ◇ ◇     帧5: ◇ ◇ ◆ ◆ ◇
帧2: ◇ ◆ ◇ ◇ ◇     帧4: ◇ ◆ ◆ ◆ ◇     帧6: ◇ ◇ ◇ ◆ ◇
```

菱形从左到右脉冲亮起再暗下去，配合 Accent→Gradient 渐变色。替换当前的 Braille 点旋转。

### 9.2 面板渐入

面板首次渲染时，内容前景色从 `FGSubtle` 用 3-4 帧（~150ms）渐变到正常颜色。模拟 fade-in。

### 9.3 流式光标

当前 `▋` 替换为脉冲菱形 `◇◆◇`，配合 Accent 色闪烁。

### 9.4 Sidebar 展开/折叠

`Ctrl+B` toggle 时 sidebar 宽度从 0 → 目标宽度用 4-6 帧动画展开（每帧 +3~4 字符），不是瞬间出现。

---

## 十、排版升级

### 10.1 标题层级

| 级别 | 当前 | 目标 |
|------|------|------|
| H1 | 加粗 + Accent 色 | `BGCard` 背景色块 Banner + `FGBright` Bold，全宽 |
| H2 | 加粗 + Accent 色 | `◇` 前缀 + `FGPrimary` Bold + 底部 `┈` 线 |
| H3 | 加粗 + Accent 色 | `▸` 前缀 + `FGSecondary` Bold |
| H4-H6 | Glamour 默认 | `FGSecondary` + 递减强调度 |

### 10.2 字重策略（更积极使用 Italic/Underline）

| 字重 | 用途 | 示例 |
|------|------|------|
| **Bold** | 标题、焦点、警告标签、按钮 | 状态 Tag `[ERROR]` |
| *Italic* | 引用、注释、次要说明、占位 | Thinking Box 内容、时间戳 |
| <u>Underline</u> | 链接、可交互元素、补全匹配 | URL、快捷键提示 |
| Normal | 正文、代码 | — |

### 10.3 间距

```
面板内边距:      Padding(0, 2)  ← 从 (0,1) 增加到 (0,2)
消息左右间距:    2 字符
面板之间间距:    1 行空行
输入框左右内距:  2 字符
Sidebar 内容缩进: 2 字符
```

---

## 十一、响应式策略

### 11.1 四档断点

| 档位 | 宽度 | 行为 |
|------|------|------|
| **Wide** | ≥ 120 | 双栏（sidebar + chat），sidebar 可 toggle |
| **Normal** | 80-119 | 单列居中限宽，chat_max_width 生效 |
| **Compact** | 60-79 | 单列，隐藏 Footer 详细提示、模型名、Runner 状态 |
| **Narrow** | < 60 | 极简模式，只保留消息流和输入框 |

### 11.2 渐进降级（Wide → Narrow 依次隐藏）

1. Sidebar 自动隐藏（<120）
2. Title Bar 模型名和点阵装饰
3. Footer 快捷键详细提示（只保留 `/help`）
4. 消息时间戳
5. 菱形焦点指示器和引导线装饰
6. Status Bar token 用量

---

## 十二、硬编码颜色清理

以下位置的硬编码颜色全部改为主题派生：

| 文件 | 当前 | 目标 |
|------|------|------|
| `cli_view.go` `ctxBarStyles` | `#6bcb77/#ffd93d/#ff6b6b/#333333` | 主题 Success/Warning/Error/FGSubtle |
| `cli_approval.go` | `#ffffff/#22c55e/#ef4444` | 主题 FGBright/Success/Error |
| `cli_message.go` ~20 处内联 `NewStyle()` | 各种硬编码 hex | 提取到 `cliStyles` 缓存 |

---

## 十三、Settings Panel 升级

### 13.1 新增 "Layout" 分类

Settings Panel 新增分组：

```
▸ Layout
    layout_mode         auto
    sidebar_enabled     true
    sidebar_width       20
    sidebar_position    left
    sidebar_sections    ["sessions","status","model"]
    chat_max_width      76
    chat_center         true

▸ Appearance
    theme               midnight

▸ LLM
    ...
```

### 13.2 Settings 布局升级

从纯文本 `▸ key: value` 列表升级为分组卡片：

```
◈ Layout ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
  layout_mode         auto       ◁ ▸ 可选: auto/single/dual
  sidebar_enabled     true       ◁ ▸ 
  sidebar_width       20         ◁ ▸ 范围: 16-40
  sidebar_position    left       ◁ ▸ 可选: left/right
  chat_max_width      76         ◁ ▸ 范围: 0-200, 0=不限
  chat_center         true       ◁ ▸

◈ Appearance ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
  theme               midnight   ◁ ▸ 可选: midnight/ocean/forest/...
```

- 分类标题用 `◈ Category ┈┈┈` 格式
- 选中行 `BGHover` 背景
- 可编辑值右侧显示 `◁ ▸` 提示可操作

---

## 十四、验证方案

### 视觉验证

- [ ] 9 套主题 × 3 种宽度（60/100/140）= 27 种组合截图
- [ ] 消息流含中文/英文/代码/Diff 混合内容截图
- [ ] Sidebar 展开/折叠/toggle 过程截图
- [ ] 与 Crush 并排对比截图

### 兼容性验证

- [ ] iTerm2 / Terminal.app / Kitty / Windows Terminal
- [ ] 确认 `◇◆◈┊┆┈┎┒┎┒` 在所有终端正确渲染和等宽对齐
- [ ] 测试字体：JetBrains Mono / Fira Code / Cascadia Code / Menlo

### 配置验证

- [ ] 修改 sidebar_width 后重启是否保持
- [ ] 切换 layout_mode 后布局立即变化
- [ ] sidebar_sections 重排后 section 顺序正确
- [ ] chat_max_width=0 时不限宽（回到旧行为）

### 性能验证

- [ ] `buildStyles()` 缓存扩展后的内存影响
- [ ] 100+ 消息滚动帧率
- [ ] Sidebar 展开/折叠动画流畅度

---

## 十五、风险与注意事项

1. **终端兼容性**：新增 Unicode 符号需确认主流终端支持。选择 BMP 内字符（菱形家族全部在 BMP），2024+ 终端基本都支持
2. **字体等宽对齐**：测试 JetBrains Mono、Fira Code 等主流等宽字体中 `◇◆◈┊┆┈` 的对齐
3. **主题迁移**：新增颜色字段（FGBright/BGHover/BGInset/BGOverlay/Muted 系列 + AccentStart/AccentEnd）需在所有 9 套主题中定义
4. **样式缓存**：新增 ~30 个样式实例到 `cliStyles`，注意 resize 重建性能
5. **Sidebar 布局引擎**：当前布局是纯垂直字符串拼接，sidebar 需要引入水平分割逻辑。建议用 lipgloss 的 `Place`/`JoinHorizontal` 而非引入新依赖
6. **配置向后兼容**：旧版没有布局配置项，启动时应自动填充默认值，不报错
7. **绝不照搬 Crush**：`╱` 斜纹是 Crush 签名，xbot 用 `·` 点阵 + `◈` 菱形

---

> ✅ 自审通过
>
> 检查项：
> - 目标一致性 ✅ — 每一项都服务于"可配置布局 + 视觉签名"
> - 完整性 ✅ — 布局/配色/图标/面板/动效/配置/验证全部覆盖
> - 不分阶段 ✅ — 所有改动是一次性完整方案
> - 与 Crush 差异化 ✅ — 菱形签名 vs 斜纹签名，可配置布局 vs 固定布局
> - 风险评估 ✅ — 7 个风险点均有说明
