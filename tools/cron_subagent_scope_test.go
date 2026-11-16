package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"xbot/storage/sqlite"
)

// openCronToolAt 在指定 DB 文件上打开一个 CronTool，返回关闭函数。
// 用固定路径（而非 t.TempDir() 每次新建）才能验证"进程重启后归属仍正确"：
// 关闭后用同一路径再开一个 CronService 实例，等价于服务重启后的读路径。
func openCronToolAt(t *testing.T, dbPath string) (*CronTool, func()) {
	t.Helper()
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db %s: %v", dbPath, err)
	}
	return NewCronTool(sqlite.NewCronService(db)), func() { _ = db.Close() }
}

// subAgentTestCtx 返回子代理 run 的 ToolContext 形态：Channel/ChatID 仍是
// 父会话 origin（engine_wire.go buildSubAgentRunConfig 的既有语义），
// CallerSessionKey 是它自己的物理会话 key（裸 interactive key）。
func subAgentTestCtx(parentChatID, interactiveKey string) *ToolContext {
	return &ToolContext{
		Channel:          "web",
		ChatID:           parentChatID,
		CallerSessionKey: interactiveKey,
		SenderID:         "main/court.gongbu",
	}
}

// TestCronTool_SubAgentJobIsolation 验证 cron job 归属的会话隔离：
//   - 主会话（Channel=web, ChatID=web-4）与子代理（CallerSessionKey=<interactive key>）
//     各建一个 job；
//   - 子代理的 job 归属 ("agent", <interactive key>)，主会话的 job 归属 ("web", "web-4")；
//   - 两侧 list 互不可见；
//   - 用同一 DB 重建 CronService（等价重启）后归属不变。
//
// 归属口径见 tools/cron.go 的 cronOwnership。
func TestCronTool_SubAgentJobIsolation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cron.db")
	tool, closeDB := openCronToolAt(t, dbPath)

	const interactiveKey = "web:web-4/court.gongbu:inst-1"
	mainCtx := &ToolContext{Channel: "web", ChatID: "web-4", SenderID: "user-1"}
	subCtx := subAgentTestCtx("web-4", interactiveKey)

	resMain, err := tool.Execute(mainCtx, `{"action":"add","message":"main session job","every_seconds":60}`)
	if err != nil {
		t.Fatalf("add from main session: %v", err)
	}
	jobMain := jobIDFromResult(t, resMain)

	resSub, err := tool.Execute(subCtx, `{"action":"add","message":"subagent job","every_seconds":60}`)
	if err != nil {
		t.Fatalf("add from subagent context: %v", err)
	}
	jobSub := jobIDFromResult(t, resSub)

	// (a) 子代理 job 的归属必须是 ("agent", <interactive key>)，
	// 而不是继承来的父会话 ("web", "web-4")。
	gotSub, err := tool.cronSvc.GetJob(jobSub)
	if err != nil {
		t.Fatalf("get subagent job: %v", err)
	}
	if gotSub == nil {
		t.Fatalf("subagent job %s not persisted", jobSub)
	}
	if gotSub.Channel != "agent" || gotSub.ChatID != interactiveKey {
		t.Fatalf("subagent job ownership = (%q, %q), want (\"agent\", %q) — 子代理的 job 必须归它自己的会话",
			gotSub.Channel, gotSub.ChatID, interactiveKey)
	}
	// 主会话 job 的归属保持原样（本次改动不得影响主会话语义）。
	gotMain, err := tool.cronSvc.GetJob(jobMain)
	if err != nil {
		t.Fatalf("get main job: %v", err)
	}
	if gotMain == nil || gotMain.Channel != "web" || gotMain.ChatID != "web-4" {
		t.Fatalf("main session job ownership changed: %+v (want channel=web chat_id=web-4)", gotMain)
	}

	// (b) 主会话 list 只见 A。
	listMain, err := tool.Execute(mainCtx, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("main list: %v", err)
	}
	if !strings.Contains(listMain.Summary, jobMain) {
		t.Fatalf("main session list must show its own job %s, got:\n%s", jobMain, listMain.Summary)
	}
	if strings.Contains(listMain.Summary, jobSub) {
		t.Fatalf("main session list must NOT show the subagent job %s, got:\n%s", jobSub, listMain.Summary)
	}

	// (c) 子代理 list 只见 B。
	listSub, err := tool.Execute(subCtx, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("subagent list: %v", err)
	}
	if !strings.Contains(listSub.Summary, jobSub) {
		t.Fatalf("subagent list must show its own job %s, got:\n%s", jobSub, listSub.Summary)
	}
	if strings.Contains(listSub.Summary, jobMain) {
		t.Fatalf("subagent list must NOT show the main session job %s, got:\n%s", jobMain, listSub.Summary)
	}

	// (d) "重启"：关闭 DB 后用同一路径新建 CronService，归属仍正确。
	closeDB()
	tool2, closeDB2 := openCronToolAt(t, dbPath)
	defer closeDB2()

	listMain2, err := tool2.Execute(mainCtx, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("main list after restart: %v", err)
	}
	listSub2, err := tool2.Execute(subCtx, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("subagent list after restart: %v", err)
	}
	if !strings.Contains(listMain2.Summary, jobMain) || strings.Contains(listMain2.Summary, jobSub) {
		t.Fatalf("after restart main list must show ONLY %s, got:\n%s", jobMain, listMain2.Summary)
	}
	if !strings.Contains(listSub2.Summary, jobSub) || strings.Contains(listSub2.Summary, jobMain) {
		t.Fatalf("after restart subagent list must show ONLY %s, got:\n%s", jobSub, listSub2.Summary)
	}
	gotSub2, err := tool2.cronSvc.GetJob(jobSub)
	if err != nil || gotSub2 == nil {
		t.Fatalf("get subagent job after restart: job=%+v err=%v", gotSub2, err)
	}
	if gotSub2.Channel != "agent" || gotSub2.ChatID != interactiveKey {
		t.Fatalf("after restart subagent job ownership = (%q, %q), want (\"agent\", %q)",
			gotSub2.Channel, gotSub2.ChatID, interactiveKey)
	}
}

// TestCronTool_RemoveAcrossSessions 验证跨会话 remove 被拒（报 "job not found"，
// 不泄漏存在性）：主会话删不动子代理的 job，子代理也删不动主会话的 job。
func TestCronTool_RemoveAcrossSessions(t *testing.T) {
	tool := newCronTestTool(t)

	const interactiveKey = "web:web-4/court.gongbu:inst-1"
	mainCtx := &ToolContext{Channel: "web", ChatID: "web-4", SenderID: "user-1"}
	subCtx := subAgentTestCtx("web-4", interactiveKey)

	resMain, err := tool.Execute(mainCtx, `{"action":"add","message":"main job","every_seconds":60}`)
	if err != nil {
		t.Fatalf("add from main session: %v", err)
	}
	jobMain := jobIDFromResult(t, resMain)

	resSub, err := tool.Execute(subCtx, `{"action":"add","message":"sub job","every_seconds":60}`)
	if err != nil {
		t.Fatalf("add from subagent context: %v", err)
	}
	jobSub := jobIDFromResult(t, resSub)

	// 主会话删子代理的 job → not found。
	_, err = tool.Execute(mainCtx, `{"action":"remove","job_id":"`+jobSub+`"}`)
	if err == nil {
		t.Fatalf("main session must NOT remove the subagent job %s", jobSub)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("cross-session remove must report 'not found' (no existence leak), got: %v", err)
	}

	// 子代理删主会话的 job → not found。
	_, err = tool.Execute(subCtx, `{"action":"remove","job_id":"`+jobMain+`"}`)
	if err == nil {
		t.Fatalf("subagent must NOT remove the main session job %s", jobMain)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("cross-session remove must report 'not found' (no existence leak), got: %v", err)
	}

	// 两条 job 都还在（remove 均被拒）。
	for _, id := range []string{jobMain, jobSub} {
		job, err := tool.cronSvc.GetJob(id)
		if err != nil {
			t.Fatalf("get job %s: %v", id, err)
		}
		if job == nil {
			t.Fatalf("job %s must survive the rejected cross-session removes", id)
		}
	}

	// 各自删自己的 → 成功。
	if _, err := tool.Execute(mainCtx, `{"action":"remove","job_id":"`+jobMain+`"}`); err != nil {
		t.Fatalf("main session removing its own job: %v", err)
	}
	if _, err := tool.Execute(subCtx, `{"action":"remove","job_id":"`+jobSub+`"}`); err != nil {
		t.Fatalf("subagent removing its own job: %v", err)
	}
}
