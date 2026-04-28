# 计划：TUI 布局全面优化

> 生成时间：2026-04-28
> 状态：待确认

## 背景与目标

用户反馈当前 TUI 布局中，**Info Bar、Status Bar、TODO Bar、Footer 全部堆在输入框上方**，视觉上"非常难看"。需要参考 Apple 设计哲学和流行 TUI 工具进行优化。

### 设计原则（Apple HIG + 流行 TUI）

| 原则 | 含义 | TUI 映射 |
|------|------|----------|
| **内容优先** | 最重要的内容占最大空间 | Viewport（消息）+ Input（输入）是核心，紧密相邻 |
| **信息分层** | 按重要程度分层放置 | L1 核心区（消息+输入）、L2 上下文（标题栏+进度）、L3 辅助（底部条） |
| **简洁** | 少即是多，不需要的不显示 | 空状态时不渲染行，减少视觉噪音 |
| **深度** | 通过位置/层次创造空间感 | 顶部=元信息，底部=辅助信息 |
| **一致性** | 统一设计语言 | 所有辅助信息规整到底部一行 |

### 参考设计

- **iMessage/微信**：消息在上，输入框在下，中间无干扰。状态信息（"正在输入..."）在消息区底部轻量显示
- **lazygit**：底部状态栏，信息密度高但不干扰主内容区
- **Warp/gh-dash**：底部 info bar 整合任务/状态/快捷键
- **macOS Terminal**：标题栏+内容区，状态信息在标题栏中

## 现状分析

### 当前布局（`cli_view.go:212-263`）

```
┌─────────────────────────────────────────────┐
│  Title Bar         (1行)                     │
├─────────────────────────────────────────────┤
│  Viewport          (可变)                    │
├─────────────────────────────────────────────┤
│  Status Bar        (1行, 始终)               │  ← 在输入上方
├─────────────────────────────────────────────┤
│  TODO Bar          (0~N行)                   │  ← 在输入上方
├─────────────────────────────────────────────┤
│  Footer            (1行, 有提示时)            │  ← 在输入上方
├─────────────────────────────────────────────┤
│  Info Bar          (1行, 有任务/代理时)       │  ← 在输入上方
├─────────────────────────────────────────────┤
│  Input Box         (1~10行)                  │
├─────────────────────────────────────────────┤
│  Toast             (0~3行)                   │
└─────────────────────────────────────────────┘
```

**问题**：Input 上方存在多达 **4 层分隔条**（Status + TODO + Footer + Info Bar），严重割裂了消息区与输入区的视觉连贯性。

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `channel/cli_view.go` | 核心布局和渲染函数 | **主要修改** |
| `channel/cli_update.go` | Viewport 高度计算 (`layoutViewportHeight`) | 修改 |
| `channel/cli_test.go` | 布局相关测试 | 更新 |
| `channel/cli_theme.go` | 样式定义 | 可能新增样式 |
| `channel/i18n.go` | 国际化文本 | 可能新增 |

### 当前组件清单

| 组件 | 函数 | 行号 | 当前定位 | 优化后定位 |
|------|------|------|----------|-----------|
| Title Bar | `renderTitleBar()` | :30 | 顶部 | 顶部（增强，整合就绪信息） |
| Ready Status | `renderReadyStatus()` | :106 | input 上方 | → **整合到 Title Bar** |
| Progress Status | `renderProgressStatus()` | :810 | input 上方 | → **保留在 viewport/input 之间**（仅处理中） |
| TODO Bar | `renderTodoBar()` | :395 | input 上方 | → **整合到 Bottom Bar** |
| Footer | `renderFooter()` | :682 | input 上方 | → **整合到 Bottom Bar** |
| Info Bar | `renderInfoBar()` | :350 | input 上方 | → **整合到 Bottom Bar** |
| Input Box | `renderInputArea()` | :61 | 底部 | 底部（位置不变） |
| Toast | `renderToast()` | :769 | 最底部 | 最底部（位置不变） |

## 优化方案

### 目标布局

```
┌─────────────────────────────────────────────┐
│  Title Bar                                    │
│  ⌂ xbot [src]   ● 就绪 · 24 msgs · gpt-4o   /help │
│  （整合：模式+路径 + 就绪状态+模型+消息数 + 提示）│
├─────────────────────────────────────────────┤
│                                             │
│  Viewport（消息滚动区域 — 最大化空间）          │
│                                             │
├─────────────────────────────────────────────┤
│  ◐ thinking #3 · 2.3s    ← 仅处理中 (0或1行)  │
├─────────────────────────────────────────────┤
│  Input Box（含 context 使用率进度条顶边）       │
│  ████████░░░░ 42%                             │
│  │ 用户输入...                         │       │
├─────────────────────────────────────────────┤
│  Ctrl+K Del  / Cmds  Tab ↹  Ctrl+E Fold  │ ⚡3任务 · 🧠2代理│
│  （快捷键提示 + 后台状态，智能截断适配窄终端）    │
├─────────────────────────────────────────────┤
│  Toast                                       │
└─────────────────────────────────────────────┘
```

### 关键设计决策

1. **Title Bar 承担"就绪态"信息展示**：模型名、消息计数、代理标识从 Status Bar 迁移到 Title Bar，利用已有空间
2. **进度行只在处理中显示**：`typing || progress != nil` 时在 viewport 和 input 之间显示紧凑1行，就绪时消失（零行）
3. **Bottom Bar 统一管理所有辅助信息**：快捷键（左）+ 后台状态（右），一条线解决
4. **TODO 进度压缩为 Bottom Bar 中的紧凑指示器**：如 `☐ 3/7`，按 `^` 展开详情面板
5. **空状态零噪音**：没有后台任务、没有 TODO、没有队列时，Bottom Bar 只显示快捷键（不占额外行）

### 空间收益分析

| 场景 | 当前占用（input上方） | 优化后占用（input上方） | 节省 |
|------|---------------------|----------------------|------|
| 就绪，无任务 | Status(1) + Footer(1) = 2行 | 0行 | **2行** |
| 就绪，有任务 | Status(1) + Footer(1) + Info(1) = 3行 | 0行 | **3行** |
| 处理中，有TODO | Status(1) + TODO(N+1) + Footer(1) + Info(1) = N+4行 | Progress(1行) | **N+3行** |

Viewport 空间在典型场景下增加 25%~50%。

## 详细计划

### 阶段一：增强 Title Bar（整合就绪状态信息）

**目标**：将 `renderReadyStatus()` 的信息整合到 Title Bar 中。

**步骤**：
1. 修改 `renderTitleBar()` → 新增 `renderEnhancedTitleBar()`
   - 左侧：模式标签 + 路径（保持不变）
   - 右侧增强：
     - 就绪态显示：`● 就绪 · N msgs · model [Ctrl+N]`（从 `renderReadyStatus` 迁移）
     - 处理中隐去模型信息，显示 `◐ thinking...`（简略版）
     - 保留原 `titleRight` 的更新提示、Runner 状态
   - 智能截断：窄终端时优先保留路径，右侧信息渐进式省略
   - 文件：`channel/cli_view.go`

2. 需要新增的 model 字段：无（已有 `cachedModelName`、`modelCount`、`typing` 等）

3. **注意**：Title Bar 宽度有限（单行），需要精心设计截断策略：
   ```
   优先保留：模式标签 > 路径 > 模型名 > 消息计数 > 状态指示
   ```

### 阶段二：重构 `layoutMain` 布局组装

**目标**：将 input 上方的多层 bar 压缩为最多 1 行进度。

**步骤**：
1. 修改 `layoutMain()` 的组装逻辑（`cli_view.go:212-263`）：
   ```go
   // 旧逻辑：
   lines = titleBar, viewport, status, todoBar, footer, infoBar, input, toast
   
   // 新逻辑：
   lines = titleBar, viewport, progressLine, input, bottomBar, toast
   ```
2. 就绪态时 `progressLine` 为空（不占行）
3. 移除 `renderReadyStatus()` 的独立渲染调用
4. 调整 `status` hint 合并逻辑：`tempStatus` 和 `newContentHint` 整合到 Bottom Bar 或 progress line

### 阶段三：创建 `renderBottomBar()`

**目标**：整合 Footer（快捷键）+ Info Bar（任务/代理/队列）+ TODO 进度到一行。

**步骤**：
1. 新建 `renderBottomBar()` 函数（`cli_view.go`）
   - 左部分：快捷键提示（从 `renderFooter` 迁移逻辑）
   - 右部分：后台状态指示（从 `renderInfoBar` 迁移逻辑）+ TODO 进度（如 `☐ 3/7`）
   - 中间用空格填充到终端宽度（`padBetween` 模式）
   
2. 布局格式：
   ```
   Ctrl+K Del  / Cmds  Tab ↹  Ctrl+E Fold          ⚡3任务 · 🧠2代理 · ☐ 3/7
   ```
   
3. TODO 进度压缩为单图标+数字：`☐ done/total`，原有详细视图移到 `^` 面板（已有功能）
4. 智能截断：窄终端时从左到右保留：
   - 优先保留：TODO > 任务计数 > 代理计数 > 快捷键（最低优先级）
   
5. 空状态处理：
   - 无任务/代理/队列/TODO 时，右部分为空，只显示左部分快捷键
   - 完全无内容时返回空字符串

### 阶段四：调整 `layoutViewportHeight()` 高度计算

**目标**：更新 viewport 高度计算，反映新的布局结构。

**步骤**：
1. 修改 `channel/cli_update.go:383-442` 的 `layoutViewportHeight()`
2. 新的 `fixedLines`：
   ```go
   // 旧：fixedLines = 3 (titleBar + status + footer)
   // 新：fixedLines = 2 (titleBar + bottomBar)
   ```
3. 移除 `todoLines` 和 `infoBarLines` 的计算（它们现在在 input 下方，不影响 viewport）
4. 进度行仅在 `typing || progress != nil` 时计入 `reservedLines`
5. 验证小终端适配（height < 12/8/5）仍然正确

### 阶段五：清理和测试

**步骤**：
1. 移除或标记为 deprecated 的旧函数（保留用于参考，但不调用）：
   - `renderReadyStatus()` — 逻辑迁移到 Title Bar
   - `renderInfoBar()` — 逻辑迁移到 Bottom Bar
   - `renderTodoBar()` — TODO 详情迁移到 Bottom Bar 简化版
   - `renderFooter()` — 逻辑迁移到 Bottom Bar

2. 更新 `cli_test.go` 中相关测试：
   - `renderProgressStatus` 测试保持不变（函数仍存在）
   - 新增 Bottom Bar 渲染测试
   - 更新 viewport 高度计算测试

3. 更新 `cli_helpers.go` 中引用：
   - `showTempStatus` 不再追加到 status bar，改为追加到 Bottom Bar 或 progress line

4. 检查所有布局相关回调：
   - `cli_update_handlers.go:929-956` 中关于 status bar 和 info bar 的逻辑
   - `cli_helpers.go:233,314` 的缓存更新

5. 运行完整测试套件

## 验证方案

- **视觉验证**：运行 `go run ./cmd/xbot-cli`，检查主聊天布局：
  - 就绪态：input 上方无多余 bar，消息区直连输入框
  - 处理中：input 上方仅 1 行进度
  - 有后台任务时：Bottom Bar 正确显示
- **高度计算验证**：不同终端尺寸下 viewport 高度正确，不溢出
- **测试验证**：`go test ./channel/... -run "TestRenderProgress|TestLayout|TestView" -v`
- **构建验证**：`go build ./...`

## 回滚策略

- 所有修改集中在 `cli_view.go` 和 `cli_update.go`
- 旧函数保留（不删除），仅不再被调用
- 如有问题，恢复 `layoutMain` 的旧组装逻辑即可

## 注意事项

1. **Title Bar 宽度限制**：单行 title bar 承载了更多信息，窄终端（<80列）时右侧信息可能被截断。需要实现渐进式截断策略
2. **Bottom Bar 与 Toast 的层级**：Bottom Bar 在 input 下方、Toast 上方。Toast 出现时正确推上去
3. **进度行的临时状态提示**：原先 `tempStatus` 和 `newContentHint` 的黄色/蓝色 hint 需要找到新位置（整合到 Bottom Bar 右部分或进度行）
4. **TODO 详情**：用户仍然可以通过 `^` 快捷键查看完整的 TODO 面板（`panelMode="bgtasks"` 或类似）
5. **search layout**：`layoutSearch` 不受影响（它有自己的布局逻辑）
6. **panel layout**：`layoutPanel` 和 `layoutAskUser` 不受影响（它们有独立的布局函数）
7. **`padBetween`**：已在 Footer 中使用，可直接复用到 Bottom Bar

✅ 自审通过
