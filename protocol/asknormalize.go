package protocol

// asknormalize.go — ask_user 的 options 归一化。
//
// 背景（2026-11-11 用户报告的严重 bug）：
//
//	agent 按 protocol.AskUserQuestion（events.go:112）把 options 写成**字符串数组**
//	["选项A","选项B"]，而前端 StructuredCards 读的是**对象**的 opt.label
//	（types.ts 的 AskOption {id,label,description?}）。两边对不上 ⇒ 卡片渲染出
//	N 个空复选框，用户完全看不到选项文字。
//
// 归一化必须在**写入前**做（而不是只在前端兜），否则库里会持续积累两种形态；
// 但前端也保留一层兼容，用于修复已经落库的历史行。
//
// 形状规则：
//
//	"选项A"                          → {id:"选项A", label:"选项A"}
//	{"id":"a","label":"A","description":"说明"} → 原样保留
//	{"label":"A"}（缺 id）           → {id:"opt-<i>", label:"A"}
//	{"id":"a"}（缺 label）           → {id:"a", label:"a"}
//	其它类型                          → {id:"opt-<i>", label:"选项 <i+1>"}

import "fmt"

// NormalizeAskPayload 原地把 payload["questions"] 归一化成前端可渲染的形态。
// payload 为 nil / 无 questions / 结构不符时静默返回（校验交给调用方）。
func NormalizeAskPayload(payload map[string]any) {
	if payload == nil {
		return
	}
	qs, ok := payload["questions"].([]any)
	if !ok {
		// 也可能是 []map[string]any（Go 侧直接构造）—— 统一转成 []any 再处理。
		if typed, ok2 := payload["questions"].([]map[string]any); ok2 {
			qs = make([]any, len(typed))
			for i, q := range typed {
				qs[i] = q
			}
			payload["questions"] = qs
		} else {
			return
		}
	}

	for _, rawQ := range qs {
		q, ok := rawQ.(map[string]any)
		if !ok {
			continue
		}
		// question 是必填文本；缺 id 时用序号补（前端 toggle/分组都靠 id）。
		if _, has := q["question"]; !has {
			if s, ok := q["text"].(string); ok && s != "" {
				q["question"] = s
			}
		}
		if id, _ := q["id"].(string); id == "" {
			// 用问题文本前 24 个字符做稳定 id，避免每次刷新都变。
			if text, _ := q["question"].(string); text != "" {
				r := []rune(text)
				if len(r) > 24 {
					r = r[:24]
				}
				q["id"] = string(r)
			}
		}

		opts, hasOpts := q["options"]
		if !hasOpts {
			continue
		}
		list, ok := opts.([]any)
		if !ok {
			// 单个字符串/对象也容错成一项
			list = []any{opts}
		}
		normalized := make([]any, 0, len(list))
		for i, rawOpt := range list {
			normalized = append(normalized, normalizeAskOption(rawOpt, i))
		}
		q["options"] = normalized
	}
}

func normalizeAskOption(raw any, idx int) map[string]any {
	switch v := raw.(type) {
	case string:
		return map[string]any{"id": v, "label": v}
	case map[string]any:
		id, _ := v["id"].(string)
		label, _ := v["label"].(string)
		if label == "" {
			// 有的调用方只给 name/value
			if s, ok := v["name"].(string); ok {
				label = s
			} else if s, ok := v["value"].(string); ok {
				label = s
			}
		}
		if id == "" {
			id = label
		}
		if id == "" {
			id = fmt.Sprintf("opt-%d", idx)
		}
		if label == "" {
			label = id
		}
		out := map[string]any{"id": id, "label": label}
		if d, ok := v["description"].(string); ok && d != "" {
			out["description"] = d
		}
		return out
	default:
		return map[string]any{
			"id":    fmt.Sprintf("opt-%d", idx),
			"label": fmt.Sprintf("选项 %d", idx+1),
		}
	}
}
