package main

// 入站日志与正文护栏。
//
// 为什么单独一个文件：2026-10-26 的现场排查里，`inbound accepted` 老格式是
//
//	qq-plugin: inbound accepted: <sender> in <chat> admin=false msg=<msg_id>
//
// 一行里 sender 与 chat 用 "in" 拼在一起，而且**不打印 chat_type / event / body**。
// 结果是两条极重要的信息被埋掉：
//
//  1. 一条 C2C 消息（sender == chat）看起来和"群消息的 chat_id 解析错了"
//     一模一样，排查时把人往错误方向带；
//  2. 正文根本没打印，`msg=` 里那个 ROBOT1.0_ 前缀是**消息 ID**（官方事件体的
//     id 字段，见 group_at_message_create.html 的事件示例），不是正文，
//     也不是任何密文 —— 只看日志会误判成"正文被加密了"。
//
// 所以这里把三者分开、各自带前缀，并把正文截断打印，用于和用户实际输入比对。
//
// ⚠️ 关于"正文加密"：QQ 官方事件体的 content 就是**明文**文本
// （群 @ 事件页原文："content 字段已自动去除@机器人的前缀"），官方没有对事件
// 正文做加密的流程。ROBOT1.0_ 前缀是 id 字段的格式，不需要也无法用
// app_id/app_secret 解密。本文件不实现任何"解密"，只做一件事：如果正文
// 长得像平台令牌（而不是用户打的字），明确报错并丢弃，绝不喂给 agent。

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// logBodyLimit 是日志里正文的最大字符数（按 rune 计）。
const logBodyLimit = 80

// chatLogTarget 返回日志用的会话标识，带场景前缀，避免与 sender 混淆：
//
//	群聊 → "group:<group_openid>"
//	私聊 → "c2c:<sender_openid>"
//
// 注意：这只是**日志**表示。发给 host 的 chat_id 仍是 Identity.ChatID 的结果
// （私聊是裸 openid，群聊是 group:<group_openid>），两者不能混用。
func chatLogTarget(in Inbound) string {
	if in.IsGroup() {
		return GroupChatID(in.GroupOpenID)
	}
	if in.SenderOpenID == "" {
		return "c2c:"
	}
	return "c2c:" + in.SenderOpenID
}

// logPreview 把正文压成单行、截断到 logBodyLimit 个字符。
//
// 换行/回车/制表一律转义，保证一条入站永远只占一行日志（否则日志被多行正文
// 撕开，grep 会漏）。截断按 rune 而不是 byte，避免切坏 UTF-8。
func logPreview(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	if utf8.RuneCountInString(s) <= logBodyLimit {
		return s
	}
	r := []rune(s)
	return string(r[:logBodyLimit]) + "…"
}

// bodyRuneLen 返回正文的字符数（按 rune 计，不是字节数）。
func bodyRuneLen(s string) int { return utf8.RuneCountInString(s) }

// platformTokenBody 匹配"正文其实是平台令牌"的形态：
// ROBOT1.0_ 开头的长串（消息 ID 的格式）。
//
// 阈值 20 是为了不误伤：用户真的手打 "ROBOT1.0_" 这四个字符也不会被丢。
var platformTokenBody = regexp.MustCompile(`^ROBOT1\.0_[A-Za-z0-9._!~-]{20,}$`)

// looksLikePlatformToken 报告正文是否其实是平台令牌而非用户文本。
func looksLikePlatformToken(body string) bool {
	t := strings.TrimSpace(body)
	if t == "" {
		return false
	}
	if !platformTokenBody.MatchString(t) {
		return false
	}
	// 防御：含空白或任何非 ASCII 可打印字符的，认为是真人输入（宁可放过）。
	for _, r := range t {
		if r > unicode.MaxASCII || r < 0x21 {
			return false
		}
	}
	return true
}
