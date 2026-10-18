package web

// web_ws_canonical_test —— P0-1 的**可判别**判据（wire + 纯函数两层）。
//
// 病根（2026-10-17 用户亲报「有网但发消息没有任何反应 / send 与 cancel 打不同会话」）：
// canonicalChatID 曾在 **REST(dispatchInbound)/SSE/出站发布** 三处生效，但唯一真正
// 发消息的通道 —— WS readPump —— 每个分支都直接把协议里的 chat_id 原样透传 ⇒
//   - agent 以 `web:web:chat_X` 建 worker（`msg.Channel + ":" + msg.ChatID`）；
//   - 出站事件在 SendSessionState 被规范化回 `web:chat_X` ⇒ 发布路由与客户端订阅
//     路由（`sessionRouteKey(msgChannel, msgChatID)`，用的是原始 key）不一致；
//   - REST cancel/ask/history 走 resolveInboundSession（已规范化）⇒ 打到裸 id。
// 结果：发送静默无效 + 取消/答复打到另一个会话。
//
// 两层判据：
//  1. 纯函数：normalizeWSInboundChatID 的行为契约（含 nil / 空 channel / 跨 channel）；
//  2. wire：readPump 必须在**唯一一次反序列化之后、`switch msg.Type` 之前**调用它
//     —— 只有这样才能覆盖 message/cancel/ask_user_response/sync/subscribe 全部分支。
//
// 判别力自证：`TestReadPumpCanonicalizer_RejectsUncanonicalizedVariant` 把同一份源码
// 删掉调用后喂给检查器，必须报错（否则本文件对回归毫无防守力）。

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"xbot/protocol"
)

// readPumpBody 截取 readPump 的函数体（函数签名 → 行首 `}` 边界，避免定长窗口在
// "中间加长一行"时把锚点推出窗口 —— 定长窗判据在别处已踩过假红）。
func readPumpBody(t *testing.T, src string) string {
	t.Helper()
	const sig = "func (wc *WebChannel) readPump("
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("wire: %s 不存在（函数被改名/删除 ⇒ 判据失效，请同步更新本测试）", sig)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		return rest[:j]
	}
	t.Fatalf("wire: 无法定位 readPump 函数体末尾")
	return ""
}

// checkReadPumpCanonical 是 wire 判据本体（纯函数，可对"变异源码"复算）。
func checkReadPumpCanonical(body string) error {
	const unmarshal = "json.Unmarshal(raw, &msg)"
	const normalize = "normalizeWSInboundChatID(&msg)"
	const sw = "switch msg.Type {"

	if n := strings.Count(body, unmarshal); n != 1 {
		return fmt.Errorf("readPump 内应恰好 1 处反序列化（实际 %d）—— 多解析点会绕过规范化", n)
	}
	iUnmarshal := strings.Index(body, unmarshal)
	iSwitch := strings.Index(body, sw)
	if iSwitch < 0 {
		return errors.New("readPump 内找不到 `switch msg.Type {`（dispatch 结构变了，请同步本判据）")
	}
	iNormalize := strings.Index(body, normalize)
	if iNormalize < 0 {
		return fmt.Errorf("readPump 未在传输边界规范化 chat_id（缺少 `%s`）—— 全键会生成幽灵会话 web:web:chat_X", normalize)
	}
	if !(iUnmarshal < iNormalize && iNormalize < iSwitch) {
		return fmt.Errorf("规范化必须在反序列化之后、`switch msg.Type` 之前（unmarshal=%d normalize=%d switch=%d）—— 否则 cancel/ask/sync/subscribe 分支仍是原始 key", iUnmarshal, iNormalize, iSwitch)
	}
	return nil
}

func TestReadPumpCanonicalizesInboundChatIDBeforeDispatch(t *testing.T) {
	src, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatalf("read web.go: %v", err)
	}
	if err := checkReadPumpCanonical(readPumpBody(t, string(src))); err != nil {
		t.Fatalf("⛔ %v", err)
	}
}

// 判别力自证：删掉规范化调用后检查器必须报错（同副本不删必须通过）。
func TestReadPumpCanonicalizer_RejectsUncanonicalizedVariant(t *testing.T) {
	src, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatalf("read web.go: %v", err)
	}
	body := readPumpBody(t, string(src))
	if err := checkReadPumpCanonical(body); err != nil {
		t.Fatalf("前置：当前源码必须已合规，否则下面的变异自证无意义：%v", err)
	}
	mutated := strings.Replace(body, "normalizeWSInboundChatID(&msg)", "// canonicalization removed (mutation)", 1)
	if mutated == body {
		t.Fatal("变异未生效（锚点串变了？请同步更新本测试）")
	}
	if err := checkReadPumpCanonical(mutated); err == nil {
		t.Fatal("⛔ 删掉传输边界规范化后判据仍然通过 —— 本判据没有判别力")
	}
}

// readPump 里不得再出现"手写剥前缀"的第二套逻辑（唯一出口 = canonicalChatID）。
func TestReadPumpHasNoAdHocPrefixStripping(t *testing.T) {
	src, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatalf("read web.go: %v", err)
	}
	body := readPumpBody(t, string(src))
	if strings.Contains(body, "strings.TrimPrefix(msg.ChatID") || strings.Contains(body, `strings.HasPrefix(msg.ChatID, "web:`) {
		t.Fatal("⛔ readPump 里出现了手写前缀剥离 —— 规范化必须只有 canonicalChatID 一个出口")
	}
}

// ── 纯函数契约（P0-1 单测）────────────────────────────────────────────────────
//
// normalizeWSInboundChatID 是"唯一一次"的入站规范化入口，因此它自己必须：
//   - 剥**同 channel 的任意层**前缀（`web:web:chat_X` → `chat_X`）；
//   - 不动别的 channel（`cli:` / `agent:` 形态的 subagent id 必须原样）；
//   - 空 channel / 空 chat_id 原样（调用方靠"缺失 ⇒ 当前会话"语义）；
//   - nil 绝不 panic（readPump 之外也可能被调用/被复用）。
func TestNormalizeWSInboundChatID(t *testing.T) {
	for _, c := range []struct {
		name    string
		channel string
		in      string
		want    string
	}{
		{name: "bare id untouched", channel: "web", in: "chat_X", want: "chat_X"},
		{name: "one prefix", channel: "web", in: "web:chat_X", want: "chat_X"},
		{name: "repeated prefix", channel: "web", in: "web:web:chat_X", want: "chat_X"},
		{name: "other channel untouched", channel: "web", in: "cli:/home/u/repo", want: "cli:/home/u/repo"},
		{name: "subagent id untouched", channel: "agent", in: "web:chat_X/review:1", want: "web:chat_X/review:1"},
		{name: "empty channel", channel: "", in: "web:chat_X", want: "web:chat_X"},
		{name: "whitespace", channel: "web", in: "  web:chat_X  ", want: "chat_X"},
		{name: "empty id", channel: "web", in: "", want: ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := &protocol.WSClientMessage{Channel: c.channel, ChatID: c.in}
			normalizeWSInboundChatID(msg)
			if msg.ChatID != c.want {
				t.Fatalf("normalizeWSInboundChatID(channel=%q, chat_id=%q) = %q, want %q", c.channel, c.in, msg.ChatID, c.want)
			}
		})
	}
	// nil 安全
	normalizeWSInboundChatID(nil)
}
