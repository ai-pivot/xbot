# Plan: 终端粘贴图片功能

## Summary

实现 CLI 终端的图片粘贴功能。用户通过 `Ctrl+V` 或 `/paste` 命令将系统剪贴板中的图片粘贴到 xbot 对话中，图片自动保存为临时文件、编码为 base64 data URL 发送给 LLM vision 模型。支持本地和远程两种模式，跨 macOS / Linux(X11+Wayland) / Windows 平台。

同时修复现有 Gap：`@path/to/image.png` 文件引用当前只追加为纯文本路径，LLM 无法看到图片内容。

## Background — 当前架构与 Gap

### 数据流（现状）

```
CLI 用户输入
  ├── @path/to/file → parseFileReferences → InboundMsg.Media []string (文件路径)
  └── 普通文本 → textarea → sendMessage → InboundMsg.Content

agent/agent.go:2148-2156:
  if len(msg.Media) > 0 {
      msg.Content += "[Attached files]\n- path1\n- path2"  // ← 纯文本路径，LLM 看不到图片
  }

llm/openai.go:371-392:
  parseEmbeddedImages(content)  // ← 能解析 ![alt](data:image/png;base64,...) 但没人生成它
```

### Gap

1. **Media 图片路径只作为文本追加** — vision 模型看不到图片内容
2. **parseEmbeddedImages 已就绪但无输入** — base64 data URL 编码逻辑缺失
3. **InboundMsg 不支持内联图片内容** — 只有文件路径引用，无 inline content
4. **Ctrl+V 只处理文本粘贴** — BubbleTea 的 `tea.PasteMsg` 不包含图片数据

## Design Decisions（已确认）

| 决策 | 选择 |
|------|------|
| 剪贴板读取 | `golang.design/x/clipboard` 纯 Go 库 |
| 用户交互 | `Ctrl+V` 智能拦截 + `/paste` 命令兼容 |
| 图片编码 | channel 层支持「文件引用」+「文件内容」两种模式 |
| 远程模式 | MVP 支持，base64 随 WS JSON 传输 |
| 图片大小限制 | 5MB（base64 后约 6.7MB） |

## Architecture

### 新增类型

```go
// channel/types.go — 新增到 InboundMsg
type InboundMsg struct {
    // ... existing fields ...
    Media        []string        `json:"media,omitempty"`         // 文件路径引用（@path）
    MediaContent []MediaContent  `json:"media_content,omitempty"`  // 内联媒体内容（粘贴）
}

// MediaContent 携带内联的媒体数据（如剪贴板粘贴的图片）
type MediaContent struct {
    MIMEType string `json:"mime_type"`           // "image/png", "image/jpeg"
    Base64   string `json:"base64"`              // base64 编码的图片数据
    Filename string `json:"filename,omitempty"`  // 显示用文件名（如 "paste_20260614_234326.png"）
}
```

```go
// bus/bus.go — 同步新增到 InboundMessage
type InboundMessage struct {
    // ... existing fields ...
    Media        []string        // 文件路径引用
    MediaContent []MediaContent  // 内联媒体内容
}
```

### 数据流（目标）

```
场景 1: Ctrl+V / /paste 粘贴图片
  剪贴板 → golang.design/x/clipboard.Read(FmtImage)
    → 压缩/限制 (max 5MB)
    → InboundMsg.MediaContent [{MIMEType, Base64, Filename}]
    → agent.go: 编码为 data URL，嵌入 content
    → openai.go: parseEmbeddedImages → vision 模型看到图片

场景 2: @path/to/image.png 文件引用（修复 Gap）
  parseFileReferences → InboundMsg.Media ["path/to/image.png"]
    → agent.go: 检测图片文件 → 读取 → 编码为 data URL，嵌入 content
    → openai.go: parseEmbeddedImages → vision 模型看到图片

场景 3: @path/to/document.pdf 文件引用（不变）
  parseFileReferences → InboundMsg.Media ["path/to/doc.pdf"]
    → agent.go: 非图片文件 → 保持纯文本引用
```

## Changes

### 1. `channel/types.go` — 新增 MediaContent 类型

- **What**: 新增 `MediaContent` struct + `InboundMsg.MediaContent` 字段
- **Why**: 支持 channel 层传递内联图片内容（区别于 Media 的文件路径引用）

### 2. `bus/bus.go` — 同步 InboundMessage

- **What**: 新增 `MediaContent` 类型 + `InboundMessage.MediaContent` 字段
- **Why**: 消息总线需要传递内联媒体内容到 agent 层

### 3. `channel/dispatcher.go` — 透传 MediaContent

- **What**: `bus.InboundMessage` 构造处添加 `MediaContent` 透传
- **Why**: 从 channel.InboundMsg 到 bus.InboundMessage 的转换不能丢失字段

### 4. `agent/agent.go` — 图片编码核心修复

- **What**: 
  1. 处理 `msg.MediaContent`：每个 item 编码为 `data:{mime};base64,{data}` → 嵌入 content 为 `![filename](data:url)`
  2. 处理 `msg.Media` 中的图片文件：检测扩展名 → 读取文件 → base64 编码 → 同样嵌入 content
  3. 非图片 Media 文件保持纯文本路径引用
- **Why**: 修复现有 gap，让 vision 模型能看到图片；同时支持 inline content
- **关键**: 现有 `parseEmbeddedImages` (openai.go:466) 已能解析 data URL，无需改动 LLM 层

### 5. `channel/cli/clipboard_image.go` — 新文件：剪贴板图片读取

- **What**: 
  - `ReadClipboardImage() ([]byte, error)` — 调用 `clipboard.Read(clipboard.FmtImage)`
  - `CompressImage(data []byte, maxBytes int) ([]byte, string, error)` — 压缩/缩放
  - `SavePasteImage(data []byte) (string, error)` — 保存到 `~/.xbot/paste/{timestamp}.png`
  - `CleanupOldPasteImages(maxKeep int)` — 清理旧图片（保留最近 N 张）
- **Why**: 封装剪贴板操作，供 Ctrl+V 和 /paste 共用

### 6. `channel/cli/cli_update.go` — Ctrl+V 智能拦截

- **What**: 
  - 在 Update() 的 switch 块中（line ~234），新增 `case tea.PasteMsg` 处理
  - 当 `PasteMsg.Content` 为空时，异步检查剪贴板是否有图片
  - 有图片 → 处理为图片粘贴（保存、编码、发送）
  - 无图片 → 正常 fallthrough 到 textarea 文本粘贴
- **Why**: 让 Ctrl+V 在剪贴板有图片时自动粘贴图片
- **注意**: 
  - 剪贴板读取是同步阻塞操作（~10-50ms），用 `tea.Cmd` 异步执行避免卡 UI
  - `PasteMsg.Content` 非空时一定是文本粘贴，跳过图片检查（避免延迟）
  - 某些终端在剪贴板只有图片时不发送 PasteMsg，需 /paste 作为 fallback

### 7. `channel/cli/cli_slash.go` — /paste 命令

- **What**: 新增 `case "/paste"` 分支
  - 读取剪贴板 → 检查图片 → 如果有图片则粘贴，否则提示「剪贴板中没有图片」
  - 如果剪贴板有文本，将文本插入到 textarea（兼容 aider 行为）
- **Why**: 提供可靠的显式图片粘贴入口

### 8. `channel/cli/cli_types.go` — 注册 /paste 命令

- **What**: 在 `cliCommands` 数组中添加 `"/paste"`
- **Why**: Tab 补全支持

### 9. `channel/cli/cli_inbound.go` — 新增发送图片消息方法

- **What**: 新增 `sendImageMessage(mediaContent MediaContent, displayText string)` 方法
  - `displayText`（如 "📎 已粘贴图片 (paste_xxx.png, 234KB)"）存入 cliMessage 用于终端显示
  - `mediaContent` 存入 InboundMsg.MediaContent 用于发送给 agent
  - 终端不显示 base64 内容
- **Why**: 终端显示内容 ≠ 发送给 agent 的内容

### 10. `agent/client.go` — 远程模式 RPC 扩展

- **What**: `SendInbound` RPC 参数结构添加 `MediaContent` 字段
- **Why**: 远程 CLI 需要通过 WS 将 base64 图片传输到服务器

### 11. `serverapp/rpc_table.go` — 服务端 handler 同步

- **What**: `send_inbound` handler 接收 `MediaContent` 并透传到 `bus.InboundMessage`
- **Why**: 远程模式闭环

### 12. `llm/anthropic.go` — Anthropic 图片 content block 支持

- **What**: `toAnthropicMessages` 中 user 消息添加 `parseEmbeddedImages` 调用，将 `data:image/...;base64,...` 转为 Anthropic 的 image content block：
  ```go
  {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
  ```
- **Why**: 否则只有 OpenAI 兼容模型能看到图片，Anthropic（Claude）用户粘贴图片无效
- **复用**: 解析逻辑可直接调用 `openai.go` 中已有的 `parseEmbeddedImages`（提取到公共位置或复制）

### 13. `go.mod` — 新增依赖

- **What**: `golang.design/x/clipboard`
- **Why**: 跨平台剪贴板读取

## Ctrl+V 交互流程（详细）

```
用户按 Ctrl+V
  ├── 终端激活 bracketed paste mode
  │   ├── 剪贴板有文本 → 发送 bracketed paste(text) → PasteMsg{Content: "text"}
  │   │   → Content 非空，跳过图片检查 → 正常文本粘贴
  │   └── 剪贴板只有图片 → 发送 empty paste 或无事件
  │       → PasteMsg{Content: ""} 或无 PasteMsg
  │       → 异步检查剪贴板图片 → 有图 → 图片粘贴
  │
  └── 某些终端（不拦截 Ctrl+V）
      → KeyPressMsg{Code: 0x16} (Ctrl+V)
      → 拦截 → 异步检查剪贴板 → 有图 → 图片粘贴 / 无图 → 插入文本
```

**PasteMsg 为空时的处理**：
1. 返回 `tea.Cmd` 异步读取剪贴板图片
2. 图片读取成功 → 发送 `cliPasteImageMsg{data}` 消息回 event loop
3. 图片读取失败/无图片 → 发送 `cliPasteImageMsg{data: nil}` → fallthrough 为空操作
4. 避免阻塞 event loop（剪贴板读取 ~10-50ms）

## 图片压缩策略

```
原始图片 bytes
  → 检查 size
    → ≤ 5MB → 直接使用
    → > 5MB → image.Decode → resize(max 2048×2048) → jpeg.Encode(q85)
      → 检查 size
        → ≤ 5MB → 使用压缩后
        → > 5MB → 拒绝，提示用户「图片过大（XMB），请裁剪后重试」
```

- 截图通常是 PNG，压缩为 JPEG 可减少 5-10 倍
- 2048×2048 是主流 LLM vision 的推荐分辨率上限
- 压缩使用标准库 `image`、`image/jpeg`、`golang.org/x/image/draw`（已有间接依赖）

## 临时文件管理

- 存储路径：`~/.xbot/paste/paste_{timestamp}.png`
- 自动清理：每次粘贴后检查，保留最近 20 张，删除更旧的
- 保留文件的原因：
  - 用户可能需要引用文件路径（如让 agent 处理后保存）
  - debug 排查
  - `@~/.xbot/paste/xxx.png` 可再次引用

## 远程模式

```
CLI Client (本地)
  → 剪贴板图片 → base64 → MediaContent
  → WebSocket JSON RPC: send_inbound {content, media_content: [{base64, mime}]}
  → Server: rpc_table.go handler → bus.InboundMessage.MediaContent
  → agent.go: 处理 MediaContent → data URL → LLM
```

- base64 图片直接放入 JSON RPC payload，通过 WS 传输
- 大小限制 5MB（base64 后 ~6.7MB），在 CLI 侧检查后再发送
- WS 消息大小需要在 server 端确认 ws handler 的 max message size（默认通常 ≥ 10MB）

## Risks

| 风险 | 缓解 |
|------|------|
| `golang.design/x/clipboard` 依赖链拉入 `golang.org/x/mobile` | 仅 darwin 平台需要，Linux/Windows 不受影响；`x/mobile` 是纯 Go 无 CGO |
| 剪贴板读取阻塞 event loop | 使用 `tea.Cmd` 异步执行，通过消息回传结果 |
| 某些终端不发送空 PasteMsg | `/paste` 命令作为可靠 fallback |
| WS 大消息被截断 | CLI 侧 5MB 硬限制；server 侧确认 WS max message size |
| agent.go 图片编码增加首消息处理延迟 | 仅在 Media 包含图片文件时触发；base64 编码 5MB 图片 < 50ms |
| 旧版本 InboundMsg JSON 缺少 MediaContent | `omitempty` + nil slice 处理，向后兼容 |

## Definition of Done

- [ ] `go build ./...` 通过（含 `golang.design/x/clipboard` 新依赖）
- [ ] macOS/Linux/Windows 下 `clipboard.Init()` 不 panic（无 GUI 环境优雅降级）
- [ ] `/paste` 命令：剪贴板有图片时保存 + 发送，无图片时提示
- [ ] Ctrl+V：剪贴板有图片时粘贴图片，有文本时正常粘贴文本
- [ ] 终端显示 `📎 已粘贴图片 (xxx.png, 234KB)`，不显示 base64
- [ ] `@path/to/image.png` 引用也能让 vision 模型看到图片（Gap 修复）
- [ ] 图片 > 5MB 自动压缩；压缩后仍 > 5MB 则拒绝并提示
- [ ] 远程模式下图片通过 WS 传输成功
- [ ] 现有所有测试通过（`go test ./...`）
- [ ] 新增单元测试覆盖：图片编码、压缩、文件类型检测

## 已确认的额外改动

### Anthropic API 图片支持（必须）

`llm/anthropic.go:207` — `toAnthropicMessages` 中 user 消息直接用纯文本：
```go
msgs = append(msgs, anthropicMessage{Role: "user", Content: msg.Content})
```
**不支持 data URL 图片**。需要添加类似 `parseEmbeddedImages` 的逻辑，将 `![alt](data:image/png;base64,...)` 解析为 Anthropic 格式的 content blocks：
```json
{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
```
否则只有 OpenAI 兼容模型能看到粘贴的图片。

### WS max message size（已确认）

- `channel/web/web.go:840` — `SetReadLimit(10 << 20)` (10MB)，5MB 图片足够
- CLI remote WS 需要在连接时同样设置足够的 read limit

### Ctrl+V 在 panel/edit 模式下的行为

- 编辑 settings 或回答 askuser 时，Ctrl+V 保持现有文本粘贴行为（已在 `cli_update.go:169` 拦截）
- 图片粘贴仅在主输入区域（非 panel 模式）触发
