package web

// sse_secret_filter.go — SSE 出站密文过滤（2026-11-07 用户裁定）。
//
// 边界（必须严格遵守，违反任何一条都是 bug）：
//  1. **只过滤 tool 的输出物**（ToolProgress.Summary / ToolProgress.Detail），
//     防止工具结果里的真实凭据被意外展示到前端。
//  2. **不过滤模型自己说的话**（Content / Reasoning / StreamContent /
//     StreamDelta / ReasoningStream* / GenUIContent）——模型上下文里本来就有
//     真值，它需要真值才能工作；打码它只会让它看不到自己的数据。
//  3. **不过滤工具参数 Args**——那是模型写的调用参数，属于模型侧。
//  4. **只匹配固定格式的高置信凭据 token**（有精确长度/字符集），
//     不做 key=value、不做 --flag、不做任何上下文推断——
//     上下文推断全是误杀（pwd=***/signature:/session_id= 都曾被打码）。
//  5. **深拷贝后再改**：不得污染引擎侧的 ProgressEvent（它会落库进历史、
//     也会参与后续 LLM 上下文），否则会重演「记忆里的值被打码」事故。
//
// 用户原话：「GHP 下划线开头这种固定的格式，严格匹配这种格式你才打一下；
// 主要作用于工具的输出物，防止意外泄露；模型自己写的不要打码；
// 也就是 SSE 事件返回给前端的时候过滤，而且过滤的必须是 tool 的返回值。」

import (
	"xbot/protocol"
)

// redactFixedFormats 委托 protocol：只替换固定格式凭据；其余文本原样返回。
func redactFixedFormats(s string) string {
	return protocol.RedactFixedFormats(s)
}

// filterProgressSecrets 返回过滤后的副本；入参不被修改（深拷贝 ToolProgress 切片）。
func filterProgressSecrets(p *protocol.ProgressEvent) *protocol.ProgressEvent {
	if p == nil {
		return nil
	}
	cp := *p
	cp.ActiveTools = filterToolProgressSlice(p.ActiveTools)
	cp.CompletedTools = filterToolProgressSlice(p.CompletedTools)
	return &cp
}

func filterToolProgressSlice(in []protocol.ToolProgress) []protocol.ToolProgress {
	if len(in) == 0 {
		return in
	}
	out := make([]protocol.ToolProgress, len(in))
	copy(out, in)
	for i := range out {
		// 只碰工具输出物；Args / Label / Name 保持原样（模型侧语义不破坏）。
		out[i].Summary = redactFixedFormats(out[i].Summary)
		out[i].Detail = redactFixedFormats(out[i].Detail)
	}
	return out
}
