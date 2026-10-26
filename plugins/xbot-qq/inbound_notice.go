package main

// 入站旁路动作：台账记录 + 插件直接应答（不经 host、不进 LLM）。
//
// 这两个动作都是"零 LLM"路径：它们只碰本地状态和 QQ 发送接口，绝不产生
// send_inbound。为什么单独一个文件：main.go 的 handleGatewayEvent 已经很长，
// 而这些动作被三条路径共用（!whoami / allow 未配置的自动应答 / 被拒留痕）。

import (
	"context"
	"time"
)

// senderNameOf 返回展示用昵称（官方经常不给昵称，空则占位）。
func senderNameOf(in Inbound) string {
	if n := in.SenderName; n != "" {
		return n
	}
	return unknownSenderName
}

// chatTypeOf 返回台账里的 chat_type：c2c 或 group。
func chatTypeOf(in Inbound) string {
	if in.IsGroup() {
		return "group"
	}
	return "c2c"
}

// chatDescription 返回日志里的会话描述：c2c 或 group:<group_openid>。
func chatDescription(in Inbound) string {
	if in.IsGroup() {
		return "group:" + in.GroupOpenID
	}
	return "c2c"
}

// recordSeen 把发送者写进台账（失败只记日志，不影响消息处理）。
func (a *app) recordSeen(in Inbound, name string, handled bool) {
	if a.seen == nil {
		return
	}
	e := SeenSender{
		TS:           time.Now().UTC().Format(time.RFC3339),
		ChatType:     chatTypeOf(in),
		GroupOpenID:  in.GroupOpenID,
		SenderOpenID: in.SenderOpenID,
		SenderName:   name,
		Handled:      handled,
	}
	if err := a.seen.Record(e); err != nil {
		a.logf("seen_senders: record sender=%s failed: %v (消息处理不受影响)", in.SenderOpenID, err)
	}
}

// replyNotice 用被动回复把一段固定文案发回原会话（零 LLM）。
//
// 记录锚点后再发：这是对用户消息的被动回复，按场景有效期带 msg_id。
// 发送失败只记日志 —— 台账已经记下了 openid，用户还能靠日志排查。
func (a *app) replyNotice(in Inbound, text string) {
	rt := a.runtime()
	if rt == nil {
		a.logf("notice not sent (%s): channel not connected", chatDescription(in))
		return
	}
	rt.RememberInbound(in.ChatID(), in.MessageID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := rt.Send(ctx, in.ChatID(), text); err != nil {
		a.logf("notice send failed (%s): %v", chatDescription(in), err)
	}
}
