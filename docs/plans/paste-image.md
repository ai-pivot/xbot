# Plan: 终端粘贴图片功能

## Summary

实现 CLI 终端的图片粘贴功能。用户通过 `/paste` 命令将系统剪贴板中的图片粘贴到 xbot 对话中，图片自动保存为临时文件、编码为 base64 data URL 发送给 LLM vision 模型。支持本地和远程两种模式，跨 macOS / Linux(X11+Wayland) / Windows 平台。

`@path` 文件引用保持现有行为不变（纯文本路径追加），不做特殊图片处理。

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

1. **InboundMsg 不支持内联图片内容** — 只有文件路径引用，无 inline content
2. **parseEmbeddedImages 已就绪但无输入** — base64 data URL 编码逻辑缺失

## Design Decisions（已确认）

| 决策 | 选择 |
|------|------|
| 剪贴板读取 | `golang.design/x/clipboard` 纯 Go 库 |
| 用户交互 | `/paste` 命令（后续可选加 Ctrl+V） |
| 图片编码 | channel 层支持「文件引用」+「文件内容」两种模式 |
| 远程模式 | MVP 支持，base64 随 WS JSON 传输 |
| 图片大小限制 | 5MB（base64 后约 6.7MB） |
| 非 vision 模型 | 乐观发送 + API 报错自动降级（移除图片后重试），无白名单维护 |

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
/paste 粘贴图片
  剪贴板 → golang.design/x/clipboard.Read(FmtImage)
    → 压缩/限制 (max 5MB)
    → InboundMsg.MediaContent [{MIMEType, Base64, Filename}]
    → agent.go: 编码为 data URL，嵌入 content
    → openai.go: parseEmbeddedImages → vision 模型看到图片

@path/to/file 引用（不变）
  parseFileReferences → InboundMsg.Media ["path/to/file"]
    → agent.go: 追加为 "[Attached files]" 纯文本，保持现有行为
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

### 4. `agent/agent.go` — MediaContent 编码为 data URL

- **What**: 处理 `msg.MediaContent`：每个 item 编码为 `data:{mime};base64,{data}` → 嵌入 content 为 `![filename](data:url)`
- **Why**: 将内联图片内容转换为 LLM 可识别的格式
- **关键**: 现有 `parseEmbeddedImages` (openai.go:466) 已能解析 data URL，无需改动 LLM 层
- **注意**: `msg.Media`（@path 引用）保持现有行为不变，不做特殊处理

### 5. `channel/cli/clipboard_image.go` — 新文件：剪贴板图片读取

- **What**: 
  - `ReadClipboardImage() ([]byte, error)` — 调用 `clipboard.Read(clipboard.FmtImage)`
  - `CompressImage(data []byte, maxBytes int) ([]byte, string, error)` — 压缩/缩放
  - `SavePasteImage(data []byte) (string, error)` — 保存到 `~/.xbot/paste/{timestamp}.png`
  - `CleanupOldPasteImages(maxKeep int)` — 清理旧图片（保留最近 N 张）
- **Why**: 封装剪贴板操作，供 /paste 命令使用

### 6. `channel/cli/cli_slash.go` — /paste 命令

- **What**: 新增 `case "/paste"` 分支
  - 读取剪贴板 → 检查图片 → 如果有图片则粘贴，否则提示「剪贴板中没有图片」
  - 如果剪贴板有文本，将文本插入到 textarea（兼容 aider 行为）
- **Why**: 提供可靠的显式图片粘贴入口

### 7. `channel/cli/cli_types.go` — 注册 /paste 命令

- **What**: 在 `cliCommands` 数组中添加 `"/paste"`
- **Why**: Tab 补全支持

### 8. `channel/cli/cli_inbound.go` — 新增发送图片消息方法

- **What**: 新增 `sendImageMessage(mediaContent MediaContent, displayText string)` 方法
  - `displayText`（如 "📎 已粘贴图片 (paste_xxx.png, 234KB)"）存入 cliMessage 用于终端显示
  - `mediaContent` 存入 InboundMsg.MediaContent 用于发送给 agent
  - 终端不显示 base64 内容
- **Why**: 终端显示内容 ≠ 发送给 agent 的内容

### 9. `agent/client.go` — 远程模式 RPC 扩展

- **What**: `SendInbound` RPC 参数结构添加 `MediaContent` 字段
- **Why**: 远程 CLI 需要通过 WS 将 base64 图片传输到服务器

### 10. `serverapp/rpc_table.go` — 服务端 handler 同步

- **What**: `send_inbound` handler 接收 `MediaContent` 并透传到 `bus.InboundMessage`
- **Why**: 远程模式闭环

### 11. `llm/anthropic.go` — Anthropic 图片 content block 支持

- **What**: `toAnthropicMessages` 中 user 消息添加 `parseEmbeddedImages` 调用，将 `data:image/...;base64,...` 转为 Anthropic 的 image content block：
  ```go
  {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
  ```
- **Why**: 否则只有 OpenAI 兼容模型能看到图片，Anthropic（Claude）用户粘贴图片无效
- **复用**: 解析逻辑可直接调用 `openai.go` 中已有的 `parseEmbeddedImages`（提取到公共位置或复制）

### 12. `llm/openai.go` + `llm/anthropic.go` — 不支持 vision 的模型自动降级

- **What**: 当 LLM API 返回图片相关的 4xx 错误时，自动移除 content 中的 image parts 后重试一次
  - OpenAI: 检测 `parseEmbeddedImages` 产生的 image content part → 移除 → 只保留文本
  - Anthropic: 检测 image content block → 移除 → 只保留文本
  - 降级成功后通过 callback 通知 CLI 显示 `⚠️ 当前模型不支持图片，已发送文本部分`
- **Why**: 没有可靠的 API 接口能查询模型是否支持 vision（OpenAI `/v1/models` 只返回 ID，无能力字段）。乐观发送 + 错误降级是唯一零维护方案
- **错误识别**: 匹配错误消息中的关键词（`image`, `vision`, `multimodal`, `not supported`, `invalid content`）
- **降级范围**: 仅在本轮请求中移除 image parts，不修改 session history 中存储的原始消息

### 13. `go.mod` — 新增依赖

- **What**: `golang.design/x/clipboard`
- **Why**: 跨平台剪贴板读取

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
| WS 大消息被截断 | CLI 侧 5MB 硬限制；server 侧确认 WS max message size |
| agent.go 图片编码增加首消息处理延迟 | base64 编码 5MB 图片 < 50ms，仅在 MediaContent 非空时触发 |
| 旧版本 InboundMsg JSON 缺少 MediaContent | `omitempty` + nil slice 处理，向后兼容 |
| 模型不支持 vision 导致 API 4xx | 乐观发送 + 错误降级：自动移除 image parts 重试一次，CLI 提示降级 |

## Definition of Done

- [ ] `go build ./...` 通过（含 `golang.design/x/clipboard` 新依赖）
- [ ] macOS/Linux/Windows 下 `clipboard.Init()` 不 panic（无 GUI 环境优雅降级）
- [ ] `/paste` 命令：剪贴板有图片时保存 + 发送，无图片时提示
- [ ] 终端显示 `📎 已粘贴图片 (xxx.png, 234KB)`，不显示 base64
- [ ] 图片 > 5MB 自动压缩；压缩后仍 > 5MB 则拒绝并提示
- [ ] 远程模式下图片通过 WS 传输成功
- [ ] 不支持 vision 的模型：API 报错后自动移除图片重试，CLI 提示降级
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
