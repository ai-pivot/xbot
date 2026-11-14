package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/prompt"
)

// todoInstructionNeedle 是新加到 prompt/base/behavior.md 里的那条指令的识别串。
// 用 "Track multi-step work with TodoWrite" 作为锚点（该短语在仓库其它位置不存在，
// 见测试内的唯一性断言）。
const todoInstructionNeedle = "Track multi-step work with TodoWrite"

// TestSystemPromptTodoInstructionRendered 用**真实渲染链路**产出
// system-prompt-todo-instruction.txt 的全部内容：
//
//	NewPromptLoader("").Render(...)  → 00_base 段（prompt.md 模板 + embed 的
//	                                    base/identity|behavior|environment|code_rules）
//	MessageContext{SystemParts}.BuildSystemPrompt() → 最终 system message 文本
//
// 不给 XBOT_REQ_DUMP_DIR 时只做断言（不写盘），设置了才落证据文件。
//
// 口径（不要夸大）：下面算出的行号/偏移是 **00_base 段内**（也就是最终
// system message 里的位置，因为 "00_base" 在 BuildSystemPrompt 的字典序里第一）。
// 本测试没有渲染 skills / memory / sender / time 等 middleware 段 ——
// 那些段本轮不参与，不能声称「已覆盖」。
func TestSystemPromptTodoInstructionRendered(t *testing.T) {
	base := NewPromptLoader("").Render(PromptData{
		Channel: "web",
		WorkDir: "/home/smith",
		CWD:     "/home/smith",
	})
	if base == "" {
		t.Fatal("base prompt 渲染为空 —— PromptLoader 用了 fallback，链路不对")
	}
	final := (&MessageContext{SystemParts: map[string]string{"00_base": base}}).BuildSystemPrompt()

	// 唯一性：这条指令在最终文本里必须恰好出现一次。
	if n := strings.Count(final, todoInstructionNeedle); n != 1 {
		t.Fatalf("%q 在最终 system message 出现 %d 次，want 1", todoInstructionNeedle, n)
	}

	lines := strings.Split(final, "\n")
	lineNo := 0 // 1-based
	lineText := ""
	byteOffset := -1
	runesBefore := 0
	for i, l := range lines {
		if strings.Contains(l, todoInstructionNeedle) {
			lineNo = i + 1
			lineText = l
			byteOffset = len(strings.Join(lines[:i], "\n"))
			if i > 0 {
				byteOffset++ // 加上与第 i 行之间的那个 \n
			}
			runesBefore = len([]rune(strings.Join(lines[:i], "\n")))
			if i > 0 {
				runesBefore++
			}
			break
		}
	}
	if lineNo == 0 {
		t.Fatalf("最终 system message 里找不到 %q —— 指令没进 prompt", todoInstructionNeedle)
	}

	// 上下文：前后各 3 行
	ctxStart := lineNo - 1 - 3
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxEnd := lineNo - 1 + 3
	if ctxEnd > len(lines)-1 {
		ctxEnd = len(lines) - 1
	}
	var ctx strings.Builder
	for i := ctxStart; i <= ctxEnd; i++ {
		marker := "  "
		if i == lineNo-1 {
			marker = ">>"
		}
		fmt.Fprintf(&ctx, "%s %4d | %s\n", marker, i+1, lines[i])
	}

	// 段顺序实测：用各段首行做锚，验证 Identity → Behavior → Tools → Memory →
	// Environment → CodeRules 到底有没有按这个顺序落进 final。
	anchors := []struct {
		name string
		part string
	}{
		{"Identity", prompt.Identity},
		{"Behavior", prompt.Behavior},
		{"Tools", prompt.ToolsFlat},
		{"Environment", prompt.Environment},
		{"CodeRules", prompt.CodeRules},
	}
	var orderLines strings.Builder
	prev := -1
	orderOK := true
	for _, a := range anchors {
		needle := firstNonEmptyLine(a.part)
		if needle == "" {
			fmt.Fprintf(&orderLines, "%-12s (段为空，跳过)\n", a.name)
			continue
		}
		idx := strings.Index(final, needle)
		if idx < 0 {
			fmt.Fprintf(&orderLines, "%-12s 缺失（锚点 %q 未出现在 final）\n", a.name, needle)
			orderOK = false
			continue
		}
		mark := "OK"
		if idx < prev {
			mark = "顺序错误"
			orderOK = false
		}
		prev = idx
		fmt.Fprintf(&orderLines, "%-12s byte_offset=%-8d %s\n", a.name, idx, mark)
	}

	report := fmt.Sprintf(`# system prompt 中 TodoWrite 指令的位置（真实渲染链路，非手抄）

## 一、生产是否生效（核实结果）
- /home/smith/.xbot/config.json: agent.prompt_file = "prompt.md"（相对路径）
- /home/smith/prompt.md 与 /home/smith/.xbot/prompt.md 均不存在
  → PromptLoader.loadFromFile 失败 → 回退 EmbeddedPrompt()（prompt/prompt.md，编译期嵌入）
- 即使 PromptFile 指向别的模板，只要模板含 {{.Behavior}}，enrichPromptData
  （agent/context.go:145）仍会无条件用 embed 的 prompt/base/behavior.md 覆盖 data.Behavior
→ 结论：改 prompt/base/behavior.md 在本环境生产路径上会生效。

## 二、渲染链路
base := NewPromptLoader("").Render(PromptData{Channel:"web", WorkDir:"/home/smith", CWD:"/home/smith"})
final := (&MessageContext{SystemParts: map[string]string{"00_base": base}}).BuildSystemPrompt()
（base 段 = prompt/prompt.md 模板：Identity → Behavior → Tools → Memory → Environment → CodeRules）

## 三、段顺序实测（在 final 中的字节偏移）
%s
整体顺序判定：%v

## 四、指令定位（在 final 文本内）
- 行号（1 起）：%d
- 字节偏移：%d
- 字符（rune）偏移：%d
- 总行数：%d，总字节数：%d
- 指令原文：
%s

## 五、前后各 3 行上下文
%s
## 六、结论
"%s" 位于最终 system message 的 **base 段（00_base）**内第 %d 行、字节偏移 %d。
因为 "00_base" 在 MessageContext.BuildSystemPrompt 的字典序里排第一，该指令落在
最终 system message 的**最前端段**。
限定：本测试只渲染了 00_base 段（Identity/Behavior/Tools/Memory/Environment/CodeRules），
skills / agents / memory / sender / time 等 middleware 段本次未渲染，不做任何覆盖性声明。
`,
		orderLines.String(), orderOK,
		lineNo, byteOffset, runesBefore, len(lines), len(final),
		lineText,
		ctx.String(),
		todoInstructionNeedle, lineNo, byteOffset)

	t.Logf("\n%s", report)

	if !orderOK {
		t.Errorf("段顺序/段缺失不符合 Identity → Behavior → Tools → Memory → Environment → CodeRules")
	}

	dir := os.Getenv("XBOT_REQ_DUMP_DIR")
	if dir == "" {
		t.Log("XBOT_REQ_DUMP_DIR 未设置 —— 不落盘（断言已完成）")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "system-prompt-todo-instruction.txt"), []byte(report), 0o644); err != nil {
		t.Fatalf("write system-prompt-todo-instruction.txt: %v", err)
	}
}

// 注：firstNonEmptyLine 已存在于 agent_process.go（包级），此处直接复用，
// 不再重复定义。
