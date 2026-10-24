// 复现 + 回归：turn-less 命令（/compress、/settings…）在执行完把会话留在 idle，
// 即使会话还有 active goal —— 而 goal 的「干到完为止」续跑只存在于 Run 内部
// （PreTurnEnd 钩子 → maybeContinueTurn）。用户 2026-10-21 亲报：「压缩后会话没
// 自动继续」（/cancel 打断在飞 turn → /compress → 之后再无任何 turn，直到用户手打
// 一条消息）。
//
// 判据分两层：
//  1. 行为：resumeRunAfterCommand 只在 active goal 时注入 resume turn；
//  2. 接线：agent.go 的命令分发分支必须真的调用它（否则就是死代码 —— 函数体存在
//     但没有任何消费方，正是这类「修好了但没接线」的假绿）。
package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"xbot/bus"
)

func newGoalAgent(t *testing.T) *Agent {
	t.Helper()
	// Isolate goal persistence: goalDir() resolves under os.UserHomeDir(), so a
	// temp home keeps the test from writing/reading the operator's real
	// ~/.xbot/goals (and from reading a goal another test just persisted).
	//
	// 平台差异（2026-10-22 CI）：os.UserHomeDir() 在 Windows 上读 **USERPROFILE**
	// （其次 HOMEDRIVE+HOMEPATH），根本不看 HOME —— 只设 HOME 会让
	// TestResumeRunAfterCommand_ActiveGoalInjectsResume 写下的 goal 落进 runner 的
	// 真实家目录，随后 TestResumeRunAfterCommand_NoGoalNoResume 把它读回来
	// （CI 实测：`no goal: resumeRunAfterCommand returned true, want false`）。
	// 因此两个变量都设，并清掉 HOMEDRIVE/HOMEPATH 以免覆盖 USERPROFILE。
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	return &Agent{
		goalManager: NewGoalManager(),
		bus:         bus.NewMessageBus(),
		agentCtx:    context.Background(),
	}
}

func drainResume(bs *bus.MessageBus) *bus.InboundMessage {
	select {
	case m := <-bs.Inbound:
		return &m
	default:
		return nil
	}
}

// TestResumeRunAfterCommand_ActiveGoalInjectsResume — 有 active goal 时必须注入
// resume turn（空 content + resume_turn 元数据，与 /continue 同一机制）。
func TestResumeRunAfterCommand_ActiveGoalInjectsResume(t *testing.T) {
	a := newGoalAgent(t)
	a.goalManager.Set(qualifyChatID("web", "chat_x"), "重构状态机")

	if !a.resumeRunAfterCommand("web", "chat_x", "web-1") {
		t.Fatal("active goal: resumeRunAfterCommand returned false, want true (session would stay idle)")
	}
	m := drainResume(a.bus)
	if m == nil {
		t.Fatal("active goal: no resume turn injected into bus.Inbound")
	}
	if m.Metadata["resume_turn"] != "true" {
		t.Fatalf("injected metadata=%v, want resume_turn=true", m.Metadata)
	}
	if m.Content != "" {
		t.Fatalf("resume content=%q, want empty (user row already in DB)", m.Content)
	}
	if m.Channel != "web" || m.ChatID != "chat_x" {
		t.Fatalf("resume routed to %s:%s, want web:chat_x", m.Channel, m.ChatID)
	}
}

// TestResumeRunAfterCommand_NoGoalNoResume — 无 goal / 已完成的 goal 绝不注入：
// 「命令回复」本身不能凭空拉出一个 turn。
func TestResumeRunAfterCommand_NoGoalNoResume(t *testing.T) {
	a := newGoalAgent(t)
	if a.resumeRunAfterCommand("web", "chat_x", "web-1") {
		t.Fatal("no goal: resumeRunAfterCommand returned true, want false")
	}
	if m := drainResume(a.bus); m != nil {
		t.Fatalf("no goal: unexpected injected message %+v", m)
	}

	a.goalManager.Set(qualifyChatID("web", "chat_x"), "done goal")
	a.goalManager.Complete(qualifyChatID("web", "chat_x"), "完成")
	if a.resumeRunAfterCommand("web", "chat_x", "web-1") {
		t.Fatal("completed goal: resumeRunAfterCommand returned true, want false")
	}
	if m := drainResume(a.bus); m != nil {
		t.Fatalf("completed goal: unexpected injected message %+v", m)
	}
}

// TestResumeRunAfterCommand_IsWiredIntoCommandDispatch — 接线判据。函数体正确但
// 没有调用点 = 用户操作静默无效（本仓已出过 4 次同类事故）。这里钉「命令分发分支
// 里确实有调用」，mutation：删掉 agent.go 的调用点 ⇒ 本条必红。
func TestResumeRunAfterCommand_IsWiredIntoCommandDispatch(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("read agent.go: %v", err)
	}
	text := string(src)
	call := "a.resumeRunAfterCommand(msg.Channel, msg.ChatID, msg.SenderID)"
	if !strings.Contains(text, call) {
		t.Fatalf("agent.go no longer calls %s — turn-less commands park an active-goal session in idle", call)
	}
	// 调用点必须落在「命令回复 → return out」这个分支里（同一函数体内、且在
	// markCommandReply 之后），否则是在别处空喊。
	idxCall := strings.Index(text, call)
	idxMark := strings.Index(text, "out.Metadata = markCommandReply(out.Metadata)")
	if idxMark < 0 || idxCall < idxMark {
		t.Fatalf("call site not inside the command-reply branch (mark=%d call=%d)", idxMark, idxCall)
	}
}
