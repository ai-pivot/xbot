# xbot TUI 优化方案 v2 — 功利版

> 原则：每行代码都问"这值得吗？" 优先改色值/样式，不动架构。
> 砍掉：自建 diff 引擎、LSP 增强、复杂动画系统 → 插件/以后再说。

---

## 总览：改动量 vs 视觉收益矩阵

```
高收益
  │  🟢 主题色板扩展        🟢 消息引导线
  │  🟢 Unicode 图标集      🟢 标题栏渐变
  │                         🟡 Thinking Box
  │                         🟡 紧凑模式
  │  🟢 Glamour 样式映射    🔴 Progress 块重设计
  │  🟢 消息 Focus/Blur
  │
  └──────────────────────────────────────────
     30min         2h          4h        1d+
```

| 标记 | 含义 |
|------|------|
| 🟢 | 几十分钟～2小时，立竿见影 |
| 🟡 | 半天～1天，结构改动但收益大 |
| 🟢→✅ | 已完成 |

---

## 🟢 TIER 1: 零风险快速抛光（6 项，总计 ~6h）

这些改动只涉及**色值替换和样式调整**，不改控制流，零回归风险。

### 1. 扩展 cliTheme 语义色板（30min）

**当前问题**：13 个平色，缺少分层感。crush 用 30+ charmtone 色。

**做法**：给现有 `cliTheme` 结构**加字段**，给每个主题补默认值。不改 buildStyles 的映射方式，只是多了一些可用的色值：

```go
// 新增字段（加在现有字段后面）
type cliTheme struct {
    // ... 现有 20 个字段保持不变 ...

    // ── 新增：4 级前景分层 ──
    FGMostSubtle string // 最弱，比 TextMuted 更淡（分隔符、引导线轨道）
    FGGuide      string // 引导线（│）颜色

    // ── 新增：背景分层 ──
    BGPanel    string // 面板背景（比 Surface 略亮）
    BGSelected string // 选中行背景

    // ── 新增：状态背景色 ──
    ErrorBg   string // 错误背景（diff 删除行）
    SuccessBg string // 成功背景（diff 插入行）
    WarningBg string // 警告背景
    InfoBg    string // 信息背景
}
```

**改动文件**：`channel/cli_theme.go` — 扩展 struct + 各主题补默认值

**视觉收益**：所有主题自动获得更丰富的色彩层次，CSS 里加几个 CSS 变量的效果。

---

### 2. 重写 Glamour Markdown 样式映射（30min）

**当前问题**：`newGlamourRenderer` 只用 `styles.DarkStyleConfig` 硬编码，主题切换时 Markdown 渲染不变。

**做法**：利用上一步新增的色值，让 Glamour 的代码块、引用、标题等跟随主题。改动就是改 `newGlamourRenderer` 里的几行色值引用：

```go
// 之前：硬编码 styles.DarkStyleConfig
style := styles.DarkStyleConfig

// 之后：从 currentTheme 映射
style.CodeBlock.BackgroundColor = c(t.BGPanel)       // 代码块背景跟随面板色
style.CodeBlock.Color = c(t.TextPrimary)              // 代码文本用主文本色
style.BlockQuote.IndentToken = c("│ ")                 // 引用线用引导线色
// ...
```

**改动文件**：`channel/cli_types.go` — `newGlamourRenderer()` 函数

**视觉收益**：/help 输出、Markdown 回复中的代码块和引用，颜色与当前主题一致，不再"出戏"。

---

### 3. Unicode 图标统一与升级（30min）

**当前问题**：图标混用（emoji + ASCII + Unicode），风格不统一。

**做法**：建立统一的图标常量表，替换散落各处的硬编码图标：

```go
const (
    IconCheck      = "✓"   // 替代 ✓ 和 ✅ 混用
    IconCross      = "✗"   // 替代 ✗ 和 ❌ 混用
    IconDot        = "●"   // 替代 ● 和 ⏺ 混用
    IconArrow      = "→"   // 替代 →
    IconBullet     = "•"   // 替代 •
    IconFolder     = "📁"  // 保留
    IconGear       = "⚙"   // 替代 ⚙
    IconSearch     = "🔍"  // 保留
    IconWarning    = "⚠"   // 替代 ⚠
    IconInfo       = "ℹ"   // 替代 ℹ️
    IconRobot      = "🤖"  // agent 保留
    IconRunnerOn   = "🟢"  // 保留
    IconRunnerWait = "🟡"  // 保留
    IconUser       = "👤"  // 保留
)
```

**改动文件**：
- `channel/cli_types.go` — 定义常量
- `channel/cli_view.go`, `cli_message.go`, `cli_panel.go` — 全局替换

**视觉收益**：所有图标风格统一，消除"拼凑感"。

---

### 4. 消息引导线（1h）

**当前问题**：消息没有任何视觉连接，对话轮次分割不明显。

**做法**：在每条消息左侧加一条竖线（`│`），颜色根据角色变化：

```
│ You · 15:45:03
│ 帮我看看这个
│
│ ┌ Thinking (1.2s) ──────────┐     ← 折叠的思考块
│ │ 需要先读代码...             │
│ └────────────────────────────┘
│ 好的，让我来分析...
│ ● Read · main.go · 234ms          ← 工具调用
│    1  package main
│    2  ...
│ ✓ Read · main.go                   ← 工具完成
│ 问题在于第 42 行...
│
│ You · 15:45:12
│ 谢谢！
```

**实现**：在 `renderMessage()` 开头加一个前缀样式，改动极小：

```go
// 引导线颜色 = 角色色
guideColor := RoleColor(msg.role)  // 已有此函数
guideStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(guideColor))
guide := guideStyle.Render("│ ")
```

**改动文件**：`channel/cli_message.go` — `renderMessage()` 函数

**视觉收益**：最大的单点视觉提升。crush 看起来"精致"很大程度上就是这条线。

---

### 5. 消息 Focus/Blur 状态（1h）

**当前问题**：所有消息一样"亮"，没有视觉焦点。

**做法**：当前助手消息用 AssistLabel 样式（绿色粗体），现在改为：
- **Focused**（正在流式输出的消息）：亮色 bold + 更亮的引导线
- **Blurred**（已完成的旧消息）：半透明/暗淡

```go
// 之前：所有 assistant 消息一样
assistantLabelStyle := s.AssistLabel  // 绿色 bold

// 之后：区分聚焦/非聚焦
if msg.isStreaming {
    labelStyle = s.AssistLabel  // 亮绿色 bold
    guideStyle = s.FGGuide      // 亮色引导线
} else {
    labelStyle = s.TextSecondary  // 暗色
    guideStyle = s.FGMostSubtle   // 暗色引导线（几乎不可见）
}
```

**改动文件**：`channel/cli_message.go` — `renderMessage()`

**视觉收益**：流式消息"跳"出来，历史消息安静退后。不用滚动就能定位当前内容。

---

### 6. 标题栏渐变字标（1.5h）

**当前问题**：`xbot` 字标没有渐变，crush 的 `CRUSH` 有。

**做法**：给标题左侧的品牌字标加渐变。利用已有的 `hslToHex` 和 `RoleColor` 类似逻辑。

```go
// 在 renderTitleBar() 开头替换 m.titleText()
func gradientWordmark(text string, from, to string) string {
    // text 每个字符从 from 色渐变到 to 色
    fromR, fromG, fromB := hexToRGB(from)
    toR, toG, toB := hexToRGB(to)
    n := len([]rune(text))
    var sb strings.Builder
    for i, ch := range text {
        t := float64(i) / float64(max(n-1, 1))
        r := uint8(fromR + int(float64(toR-fromR)*t))
        g := uint8(fromG + int(float64(toG-fromG)*t))
        b := uint8(fromB + int(float64(toB-fromB)*t))
        sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))).Render(string(ch)))
    }
    return sb.String()
}
```

配合对角线填充（`╱` 字符重复填充剩余空间）：

```
╭─ xbot v0.0.33 ╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱╱  ~/src/xbot  gpt-4o  42% ─╮
```

**改动文件**：`channel/cli_view.go` — `renderTitleBar()`

**视觉收益**：标题栏从"功能条"变成"品牌元素"。

---

## 🟡 TIER 2: 半天级结构优化（3 项，总计 ~10h）

### 7. Thinking Box 折叠面板（3h）

**当前问题**：助理的推理/思考内容平铺显示，占大量空间且与回复混在一起。

**做法**：当 assistant 消息包含 reasoning/thinking 时，用带边框的面板包裹，默认显示尾部几行 + "Thought for Xs" 脚注，可点击展开。

```
│ ┌ Thinking (0.8s) ────────────────────────────┐
│ │ ...需要先理解代码结构，找到对应的函数...     │
│ │ … (8 lines hidden) [click or space]         │
│ └──────────────────────────────────────────────┘
│
│ 好的，我看到问题了。在第 42 行...
```

**实现要点**：
- 检测消息中 `reasoning_content` 或 `thinking` 字段
- 折叠时只显示最后 10 行 + 省略提示
- 用 `lipgloss.Border()` 包裹

**改动文件**：`channel/cli_message.go` — `renderMessage()` 新增分支

**视觉收益**：长推理不再淹没实际回复内容，信息密度提升。

---

### 8. Progress 块重设计：去除 markdown 冲突（3h）

**当前问题**：`renderToolHint` 输出（glamour 渲染的 markdown）放在带圆角边框的 progress 块内，glamour 的背景色/边距与边框冲突。

**方案**：**把 tool hints（diff 等）从带边框的块中拿出来**，放在独立的无边框区域内联渲染。

**改动前**（当前结构）：
```
╭─ Progress ──────────────────────────────────────╮
│ #0                                               │
│   │ Thinking about the problem...                │
│   ✓ Read · main.go · 234ms                      │
│   │  + func main() {                             │  ← ANSI 着色 diff
│   │  - func oldMain() {                          │     在边框内，冲突
│   │    return nil                                │
│ #1                                               │
│   ● Grep · "error" · running                    │
╰──────────────────────────────────────────────────╯
```

**改动后**（新结构）：
```
#0                                      ← 迭代号无边框
  │ Thinking about the problem...       ← 思考文本
  ✓ Read · main.go · 234ms             ← 工具完成
                                        ← 工具输出（glamour markdown）
  ┌─ main.go ─────────────────────────┐ ← 独立的无边框代码块
  │ + func main() {                    │
  │ - func oldMain() {                 │
  │     return nil                     │
  └───────────────────────────────────┘

#1                                      ← 下一个迭代
  ● Grep · "error" · running
```

**核心改动**：
1. 去掉 progress 块的外层圆角边框（`s.ProgressBlock` 的 Border 去掉）
2. 渲染 tool hints 时，把 glamour 输出放在不参与 progress 块 border 的独立区域
3. 或者更简单：`renderToolHint` 输出时 strip 掉 glamour 的背景色属性，只保留前景色

**最简单实现**：新增一个选项——progress 块内的 tool hints 做 "flat" 渲染（去除 glamour 背景色），progress 块外（如 tool_summary 消息已完成的工具列表）做完整渲染。

```go
func (m *cliModel) renderToolHint(md string, flat bool) (string, error) {
    if flat {
        // 简单前景着色，不产生背景色
        return renderDiffANSI(md, m.width-4-6), nil
    }
    // 完整 glamour 渲染
    return m.renderer.Render(md)
}
```

**改动文件**：`channel/cli_message.go` — `renderProgressBlock()` + `renderToolHint()`

**视觉收益**：diff 渲染不再"破框"，且 future 消息历史中的 diff（tool_summary）可以用完整 glamour 渲染。

---

### 9. 紧凑/响应式布局（3h）

**当前问题**：终端宽度变化时没有自适应，小终端信息过载。

**做法**：加 3 个断点：

| 宽度 | 模式 | 变化 |
|------|------|------|
| >120 | Full | 标题栏全量 + 完整工具参数 |
| 80-120 | Normal | 当前行为 |
| <80 | Compact | 标题栏精简 + 工具参数省略 |

```go
func (m *cliModel) isCompact() bool {
    return m.width < 80
}

func (m *cliModel) isWide() bool {
    return m.width > 120
}
```

compact 模式下：
- 标题栏只显示 `xbot · ~/src/xbot`（去掉 Runner、user、hints）
- 工具行只显示 `✓ Bash`（去掉参数和耗时）

**改动文件**：
- `channel/cli_view.go` — `renderTitleBar()`, `renderFooter()`
- `channel/cli_message.go` — `renderProgressBlock()`

**视觉收益**：小窗口/分屏时可用，不会"糊成一团"。

---

## 🔴 TIER 3: 延后/砍掉

| 原计划项 | 处理 | 理由 |
|----------|------|------|
| 自建 Diff 引擎 (Chroma) | ❌ 砍掉 | Markdown diff 够了，样式问题通过 §8 解决 |
| LSP Diff 增强 | ⏸ 延后到插件 | 等 plugin 系统成熟后作为 plugin 实现 |
| 复杂动画系统 (渐变 spinner) | ⏸ 延后 | 当前 spinner 可接受；费效比低 |
| 滚动条增强 | ⏸ 延后 | viewport 自带够用 |
| Landing 仪表盘 | ⏸ 延后 | 启动 1s 就过了，ROI 极低 |
| 专用工具渲染器 (15种) | ⏸ 延后 | 当前统一渲染可用；需要时再拆 |
| Side-by-Side Diff | ❌ 砍掉 | 实现复杂，极少终端够宽 |

---

## 实施顺序

```
Day 1 (4h):  #1 语义色板 + #2 Glamour 映射 + #3 图标统一
Day 2 (3h):  #4 消息引导线 + #5 Focus/Blur + #6 标题栏渐变
             → 至此视觉效果已有质的提升
Day 3-4:     #7 Thinking Box + #8 Progress 重设计
Day 5:       #9 紧凑模式
```

**结果**：5 天内，xbot TUI 视觉精炼度达到 crush 同等水平，代码改动集中在一个文件（`cli_theme.go`、`cli_message.go`、`cli_view.go`），回归风险可控。

---

## 不做的事（明确边界）

- ❌ 不新建包/文件（除非确实需要）
- ❌ 不引入新依赖
- ❌ 不重构渲染管线（保持 viewport + glamour 方案）
- ❌ 不写自建 diff 引擎
- ❌ 不碰 LSP 集成
- ❌ 不改变消息数据结构（cliMessage 保持不变）
