# xbot TUI 优化计划

> 目标：又简单，功能不退化，性能保持，且优雅

---

## 核心判断

当前 TUI 共 82 个文件、~45k 行（含测试），其中 ~27k 行是源码。复杂度来源分两类：

- **本质复杂度**（不可简化）：三异步源交汇（progressCh buffer=1 / asyncCh buffer=256 / tick 100ms）、远程模式断线重连、多会话历史 stale 防护——每一个机制都有对应的真实 bug，去掉就退化。
- **偶然复杂度**（可以简化）：渲染缓存层数过多、面板状态混在一个 struct 里、Settings 渲染和 zone 追踪双重计算、QuickSwitch 导航路径不统一、AGENTS.md 中已移除机制的 gotcha 残留。

**优化原则**：只动偶然复杂度，不碰本质复杂度。每一步都要"删代码不减功能"。

---

## Phase 1: 清理已死代码（低风险，高收益）

### 1.1 删除 AGENTS.md 中已移除机制的 gotcha

subagent 分析发现以下 gotcha 指向的代码**已不存在**：

| Gotcha | 现状 | 操作 |
|---|---|---|
| `padProgressLines` / `progressBlockCompositeFP` | `renderProgressBlock()` 已是 no-op，返回 `""` | 删除 gotcha + 删除 no-op 函数 + 删除 `rc.progressBlock` 字段 |
| `fastTickActive` / tick 链断裂 | tick 由全局 goroutine 驱动，`handleTickMsg` never returns tickCmd | 删除 gotcha |
| `cachedHistoryLines`/`cachedProgressBlockLines`/`cachedDynamicLines` 命名 | 当前代码命名为 `histLines`/`progressBlock`(空)/`dynamicLines` | 更新 gotcha 命名 |

**收益**：减少 AGENTS.md 噪声，避免下一位维护者在不存在的函数上浪费时间。

**风险**：零——删除的是已经不执行的代码和不再准确的文档。

### 1.2 移除 `renderProgressBlock` no-op 及关联字段

```go
// cli_render_turn.go:539-546 — 当前代码
func (m *cliModel) renderProgressBlock() string {
    return ""  // no-op
}
```

- 删除函数
- 删除 `renderCache.progressBlock` 字段
- 删除所有调用点（如果有）

**收益**：少一个"看起来在做事实什么都没做"的函数。

---

## Phase 2: 渲染缓存层合并（中风险，高收益）

### 2.1 现状：5 层缓存

| 层 | 字段 | 状态 |
|---|---|---|
| L1 单消息缓存 | `msg.rendered/wrappedLines/renderWidth/dirty` | ✅ 必须保留 |
| L2 历史拼接缓存 | `rc.history/histLines/histMaxW` | ✅ 必须保留 |
| L3 allLines 拼装 | `rc.allLines/allLinesGen/histGen` | 🔍 可简化 |
| L4 Dynamic 后缀 | `rc.dynamicRaw/dynamicLines/dynamicWidth` | 🔍 可合并到 L2 |
| L5 流式增量 | `streamCompleted*` + `streamAllBuf` + `streamPrefix*` | 🔍 可简化命名 |

### 2.2 优化：合并 L3+L4，简化 L5 命名

**L3 allLines 的作用**：tick 快速路径直接复用拼装好的 `[]string`，避免每 tick 重新 `append(histLines, dynamicLines...)`。

**问题**：`allLines` 是 `histLines + dynamicLines` 的拼装结果，但它引入了 `allLinesGen`/`histGen` 代际计数器——6 个 `bumpHistGen()` 调用点，遗漏任何一个就渲染错误。

**优化方案**：去掉 `allLines` 层。tick 快速路径改为直接 `append(rc.histLines, rc.dynamicLines...)` ——这是一个 O(len(dynamic)) 的 append（通常 0-5 行），比 O(N) glamour 渲染快几个数量级，不需要缓存。

```go
// 之前:
if m.rc.histGen == m.rc.allLinesGen {
    // 快速路径：直接用 allLines
    viewportSetLinesBypassMaxWidth(m.viewport, m.rc.allLines, m.rc.allLinesMaxW)
    return
}

// 之后:
// 去掉 allLines，直接 append（dynamicLines 通常 0-5 行）
lines := append(append([]string{}, m.rc.histLines...), m.rc.dynamicLines...)
viewportSetLinesBypassMaxWidth(m.viewport, lines, m.rc.histMaxW)
```

**收益**：
- 删除 `allLines`/`allLinesGen`/`histGen`/`allLinesHistLen` 四个字段
- 删除 6 个 `bumpHistGen()` 调用点
- 减少一个整层的缓存失效逻辑
- append `[]string` 几乎零成本（dynamic 通常 0-5 行）

**风险**：低——`append` 每帧分配一个新 slice，但 dynamic 行数极少。如果 profiling 显示 GC 压力，可用 `sync.Pool` 复用。但大概率不需要。

### 2.3 L4 Dynamic 合并到 L2

`dynamicLines`（rewind block）和历史行在渲染时只是"放在最后的前缀行"。与其单独维护一层缓存，不如在 rewind 内容变化时直接追加到 `histLines`，并在下次 rewind 变化时替换尾部。

**但这一步可以不做**——如果 2.2 已经消除了 allLines 层，dynamic 作为 `histLines` 的"后缀 append"就足够简单了。保持当前 `dynamicLines` 独立存在但去掉了上层的 allLines 拼装，复杂度已大幅降低。

### 2.4 L5 流式缓存命名整理

当前 `streamCompletedLines/Count/Width` + `streamAllBuf` + `streamPrefixStart/End/Len` 命名分散。不改变逻辑，只做命名整理：

```go
// 之前:
streamCompletedLines []string
streamCompletedCount int
streamCompletedWidth int
streamAllBuf         strings.Builder
streamPrefixStart    int
streamPrefixEnd      int
streamPrefixLen      int

// 之后:
streamDoneLines  []string      // 已完成 iteration 的渲染行
streamDoneWidth  int           // 上述行的最大宽度
streamLiveBuf    strings.Builder // 活跃 iteration 的增量 buffer
streamStableLen  int           // buffer 中稳定前缀的长度
```

**收益**：纯可读性提升，零行为变更。

---

## Phase 3: 面板状态拆分（中风险，中收益）

### 3.1 现状：panelState 61 字段

10 种面板模式共用一个 struct，`closePanel()` 必须清理所有 10 种模式的残留状态。字段交叉污染风险高。

### 3.2 优化：按模式分组

```go
// 之前:
type panelState struct {
    mode     string
    cursor   int
    scrollY  int
    // Settings 字段 (~20)
    schema     []ch.SettingDefinition
    values     map[string]string
    editing    bool
    combo      bool
    editTA     textinput.Model
    // QuickSwitch 字段 (~15)
    quickSwitchMode     string
    quickSwitchRows     []qsRow
    quickSwitchCursor   int
    quickSwitchScrollY  int
    // AskUser 字段 (~10)
    askItems    []AskUserItem
    askCursor   int
    askScrollY  int
    // Runner/Danger/BgTasks/... 字段 (~15)
    runnerTIs      []runnerTI
    dangerItems    []dangerItem
    // ... 共 61 字段
}

// 之后:
type panelState struct {
    mode    string
    cursor  int
    scrollY int
    stack   []panelStackEntry

    settings settingsPanel
    llm      quickSwitchPanel
    askUser  askUserPanel
    runner   runnerPanel
    misc     miscPanel  // danger/bgtasks/approval/channel
}

type settingsPanel struct {
    schema  []ch.SettingDefinition
    values  map[string]string
    editing bool
    combo   bool
    editTA  textinput.Model
}

type quickSwitchPanel struct {
    mode     string
    rows     []qsRow
    cursor   int
    scrollY  int
    filter   string
    // ...
}
```

**收益**：
- `closePanel()` 只清 `mode/cursor/scrollY/stack` + 对应子结构，不再扫 61 字段
- 编译器帮助发现跨模式字段误用
- 新增面板模式不用动其他模式的字段

**风险**：中——需要改所有引用 `m.panelState.xxx` 的地方（~200 处），但都是机械替换。

### 3.3 统一 cursor 可见性

当前每个面板各写一套 `ensureXxxCursorVisible`：

| 面板 | 函数 | 行数 |
|---|---|---|
| Settings | `ensurePanelCursorVisible(extra)` | ~20 |
| QuickSwitch | `ensureQuickSwitchCursorVisible(maxVisible)` | ~15 |
| AskUser | `ensureAskUserCursorVisible()` | ~40 |

**优化**：抽象为通用函数：

```go
// 通用 cursor-in-view 算法
func ensureCursorVisible(cursorLine, scrollY *int, visibleHeight int) {
    if *cursorLine < *scrollY {
        *scrollY = *cursorLine
    }
    if *cursorLine >= *scrollY+visibleHeight {
        *scrollY = *cursorLine - visibleHeight + 1
    }
    if *scrollY < 0 {
        *scrollY = 0
    }
}
```

各面板只需计算 `cursorLine`（cursor 的绝对行号），调通用函数。AskUser 的 `hardWrapRunes` 行数计算仍需各自处理，但 scroll-to-cursor 逻辑统一。

**收益**：~75 行 → ~30 行，消除三份重复的 scroll 逻辑。

---

## Phase 4: Settings 双重布局消除（中风险，高收益）

### 4.1 现状

`viewSettingsPanel`（渲染）和 `trackSettingsZones`（鼠标 zone）各自独立计算行布局，必须手动保持同步。这是 "goose chase" 的根源——改一个忘了改另一个，鼠标点击错位。

### 4.2 优化：单一布局引擎

```go
type layoutRow struct {
    text     string
    zoneType string  // "item" / "toggle" / "combo" / "button" / "header" / "divider"
    key      string  // setting key (for item/toggle/combo)
    action   string  // button action ("openURL" / "save" / "cancel")
}

// 唯一的布局函数，输出渲染行 + zone 信息
func (m *cliModel) buildSettingsLayout() []layoutRow {
    rows := []layoutRow{}
    rows = append(rows, layoutRow{text: "Settings", zoneType: "header"})
    rows = append(rows, layoutRow{text: divider, zoneType: "divider"})
    for cat, items := range m.groupedSchema() {
        rows = append(rows, layoutRow{text: cat, zoneType: "category"})
        for _, item := range items {
            rows = append(rows, layoutRow{text: m.renderSettingRow(item), zoneType: "item", key: item.Key})
            if m.panelState.cursor == item.idx && m.panelState.editing {
                rows = append(rows, layoutRow{text: m.renderEditOverlay(item), zoneType: "editOverlay"})
            }
            if m.panelState.cursor == item.idx && m.panelState.combo {
                for _, opt := range item.Options {
                    rows = append(rows, layoutRow{text: opt, zoneType: "comboOption", key: item.Key})
                }
            }
        }
    }
    return rows
}

// view 调用:
func (m *cliModel) viewSettingsPanel() string {
    rows := m.buildSettingsLayout()
    var sb strings.Builder
    for _, r := range rows {
        sb.WriteString(r.text)
        sb.WriteString("\n")
    }
    return sb.String()
}

// mouse 调用:
func (m *cliModel) trackSettingsZones() {
    rows := m.buildSettingsLayout()
    y := m.panelBoxY
    for _, r := range rows {
        if r.zoneType != "header" && r.zoneType != "divider" && r.zoneType != "category" {
            m.zones.Register(y, r.zoneType, r.key, r.action)
        }
        y++
    }
}
```

**收益**：
- 消除双重行计算（~80 行 trackSettingsZones + viewSettingsPanel 中的布局逻辑）
- 布局变更只需改一处
- cursor 行号计算也可从 `layoutRow` 索引自动得出

**风险**：中——需要确保 `buildSettingsLayout` 的输出与当前渲染完全一致（包括 scroll 偏移）。建议先加测试验证。

---

## Phase 5: QuickSwitch 导航统一（低风险，中收益）

### 5.1 现状

QuickSwitch ↔ Settings 导航用 `valuesBackup/cursorBackup/onSubmitBackup + quickSwitchReturnToPanel`，而非 pushPanel/popPanel。这是一个历史遗留——QuickSwitch 是 overlay 层（`quickSwitchMode="llm"`），不走 `panelState.mode`。

### 5.2 优化：统一到面板栈

让 QuickSwitch 也走 `panelState.mode = "quickswitch"`，通过 pushPanel/popPanel 导航：

```go
// Settings → QuickSwitch:
m.pushPanel()  // 保存 Settings 状态
m.panelState.mode = "quickswitch"

// QuickSwitch → EditModel:
m.pushPanel()  // 保存 QuickSwitch 状态
m.panelState.mode = "settings"  // 复用 settings panel 渲染 edit model
// ... 设置 schema 为 model params

// Esc → popPanel() 自动恢复
```

**收益**：
- 删除 `valuesBackup/cursorBackup/onSubmitBackup/quickSwitchReturnToPanel` 四个字段
- 统一所有面板导航为 pushPanel/popPanel
- `closePanel()` 不再需要特殊处理 QuickSwitch

**风险**：低——QuickSwitch 的 `quickSwitchMode` 字段仍需保留（区分 "llm" 和其他可能的未来 QuickSwitch），但导航不再特殊。

---

## Phase 6: 通用清理（低风险，低收益）

### 6.1 `closePanel()` 去 duplicate

`cli_panel.go:130` 和 `:144` 都设了 `scrollY = 0`——无害但冗余，删一个。

### 6.2 统一历史去重键

当前三个 handler 用三种不同的 identity key：

| Handler | Key | 场景 |
|---|---|---|
| `handleSuHistoryLoad` | `role + "\|" + timestamp` | /su 切会话 |
| `handleHistoryReload` | 整体替换（不追加） | 压缩后重载 |
| `handleHistoryLoad` | `role + ":" + turnID + ":" + content` | DB 历史加载 |

**优化**：统一为一个 `messageIdentity` 类型和 `dedupMessages(messages, newMsgs)` 函数。`handleHistoryReload` 仍用整体替换（语义不同），但另外两个统一。

```go
type msgIdentity struct {
    role      string
    turnID    uint64
    timestamp time.Time
}

func dedupAppend(existing []cliMessage, incoming []historyMsg) []cliMessage {
    seen := make(map[msgIdentity]bool, len(existing))
    for _, m := range existing {
        seen[msgIdentity{m.role, m.turnID, m.timestamp}] = true
    }
    for _, hm := range incoming {
        id := msgIdentity{hm.Role, hm.TurnID, hm.Timestamp}
        if !seen[id] {
            existing = append(existing, toCLIMessage(hm))
            seen[id] = true
        }
    }
    return existing
}
```

### 6.3 filename.go 加 lint guard

`ansi.Truncate` 的 panics 来自 `runes[:maxW-N]` 在 maxW < N 时 slice bounds out of range。grep 确认所有 render body 函数都用 `ansi.Truncate` 后，加一个 lint 规则禁止在 render body 中手动切片。

---

## 实施顺序与优先级

| 阶段 | 估行数变化 | 风险 | 优先级 | 前置条件 |
|---|---|---|---|---|
| P1: 死代码清理 | -50 到 -100 行 | 零 | 🔴 立即 | 无 |
| P2: allLines 缓存层去掉 | -100 到 -150 行 | 低 | 🔴 立即 | P1 |
| P3: 面板状态拆分 | +50 行（子结构定义）-100 行（closePanel 简化） | 中 | 🟡 第二 | P1 |
| P4: Settings 单一布局引擎 | -80 行 | 中 | 🟡 第二 | P3 |
| P5: QuickSwitch 导航统一 | -40 行 | 低 | 🟢 第三 | P3 |
| P6: 通用清理 | -30 行 | 零 | 🟢 第三 | 无 |

**总计预估**：净减 ~350-400 行，消除 4 个缓存字段 + 6 个 bumpHistGen 调用 + 4 个 backup 字段 + ~80 行双重布局。

---

## 不做什么

以下机制虽然复杂，但**不可简化**（每个都有对应的真实 bug）：

| 机制 | 为什么不碰 |
|---|---|
| `dedupMessagesGuard` | 三异步源交汇是本质复杂度 |
| `upsertMessageByTurn` | WS 重连重复投递是本质 |
| `turnDoneFlags` + 2s 超时 | 两阶段完成协议是服务端设计 |
| `streamingMsgIdx` 保留策略 | 消除会引入双闪 |
| `viewportSetLinesBypassMaxWidth` | 49% CPU 瓶颈的唯一解 |
| `relayoutViewport` 宽度/高度区分 | 高度变化不失效缓存是关键优化 |
| `connState` 直写 model | 断线时 channel 不可靠 |
| `readPump t.conn == conn` 检查 | 防无限重连 |
| `sendInboundFn` 同步返回 | 否则永久 spinner |
| `syncProgressTodos` change detection | 34% CPU 优化 |
| `handleSuHistoryLoad` stale 检查 | 跨会话 async 安全 |
| `postRestoreSessionSetup` 标志重置 | 跨会话泄露防护 |

---

## 成功标准

1. **功能不退化**：所有现有测试通过（`cli_sim_test.go` / `cli_progress_test.go` / `cli_test.go`）
2. **性能保持**：idle CPU ~0%、busy 渲染 < 16ms/tick、glamour 渲染只在 fullRebuild 路径触发
3. **更简单**：
   - 渲染缓存从 5 层降到 3 层（L1 单消息 + L2 历史 + L5 流式）
   - panelState 从 61 字段分组到 5 个子结构
   - Settings 渲染/zone 从双份计算降到单布局引擎
   - AGENTS.md 移除已死 gotcha
4. **更优雅**：每个简化都能用一句话解释"删了什么、为什么功能不退化"

---

*生成时间：2026-07-02*
*基于 4 个 explore subagent 对 TUI 渲染系统 + 面板系统的深度代码分析*
