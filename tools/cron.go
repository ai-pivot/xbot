package tools

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"xbot/cron"
	"xbot/llm"
	log "xbot/logger"
	"xbot/storage"
	"xbot/storage/common"

	"github.com/google/uuid"
)

// CronTool 定时任务工具（无状态）
type CronTool struct {
	cronSvc storage.CronStore
}

// NewCronTool 创建 CronTool 实例
func NewCronTool(cronSvc storage.CronStore) *CronTool {
	return &CronTool{
		cronSvc: cronSvc,
	}
}

func (t *CronTool) Name() string { return "Cron" }

func (t *CronTool) Description() string {
	return `Schedule tasks that trigger the agent at specified times. Actions: add, list, remove.
- add: create a job with message + one of (cron_expr, every_seconds, delay_seconds, at). When triggered, the message is sent to the agent as a user message, initiating a full processing loop (LLM reasoning + tool calls + reply).
- list: show scheduled jobs for the CURRENT session only (session-scoped — each session manages only its own jobs)
- remove: delete a job by job_id (only jobs created by the current session)`
}

func (t *CronTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: add, list, remove", Required: true},
		{Name: "message", Type: "string", Description: "Prompt sent to the agent when the job triggers. Write it as a user instruction, e.g. 'Check server status and report any issues' or 'Remind me to start the standup meeting'. The agent will process this as a normal user message.", Required: false},
		{Name: "every_seconds", Type: "integer", Description: "Interval in seconds for recurring tasks", Required: false},
		{Name: "delay_seconds", Type: "integer", Description: "Execute once after this many seconds (one-shot delay)", Required: false},
		{Name: "cron_expr", Type: "string", Description: "Cron expression like '0 9 * * *' (5-field, Local timezone)", Required: false},
		{Name: "at", Type: "string", Description: "ISO datetime for one-time execution, e.g. '2026-02-12T10:30:00'", Required: false},
		{Name: "job_id", Type: "string", Description: "Job ID (for remove)", Required: false},
	}
}

type cronParams struct {
	Action       string `json:"action"`
	Message      string `json:"message"`
	EverySeconds int    `json:"every_seconds"`
	DelaySeconds int    `json:"delay_seconds"`
	CronExpr     string `json:"cron_expr"`
	At           string `json:"at"`
	JobID        string `json:"job_id"`
}

// cronSubAgentChannel 是子代理会话的 channel 名。
//
// interactive / one-shot 子代理的物理会话是 multiSession 里的
// GetOrCreateSession("agent", <interactive key>) —— 见
// agent/interactive.go（`cfg.CallerSessionKey = key` + `GetOrCreateSession("agent", key)`）
// 与 agent/engine_wire.go 的 one-shot 路径。所以子代理创建的 cron job 用
// Channel="agent" + ChatID=<interactive key> 才与它的物理会话一致，
// 到期通知（"agent:" + key）才可能落回创建它的那个会话。
//
// ⚠️ 能力边界（2026-11-04 工部验收打回项 2）：**只有持久 interactive 子代理
// 会话还活着时**，到期通知才真的以子代理身份投进该会话。one-shot 子代理的
// 会话在其 Run 结束时就 destroyInteractiveSession 了（cron 到期通常在那之后），
// 于是 agent/agent.go 的 deliverCronFiredToSubAgentSession 返回 false，
// **由主 Agent 在 "agent:"+key 这个 tenant 上执行**（回落点打 INFO 日志，
// 含 session_key + reason）。别把这条注释读成"一定能按子代理身份触发"。
const cronSubAgentChannel = "agent"

// cronOwnership 解析本次 Cron 工具调用所属的会话身份（channel, chatID）。
//
// add / list / remove 三条路径共用本函数，禁止各写一遍 —— 归属口径必须唯一，
// 否则会出现"能建不能删"或"list 看不见自己 job"的错位。
//
// 归属规则：
//   - 子代理发起（CallerSessionKey 非空）：CallerSessionKey 是该子代理的
//     物理会话 key（interactive key，形如 "web:web-4/court.gongbu:inst-1"，
//     裸 key —— 不带 "agent:" 前缀，见 agent/interactive.go 的
//     `cfg.CallerSessionKey = key`）。归属为 channel="agent"、chatID=<该 key>。
//   - 主会话发起（CallerSessionKey 为空）：沿用 ctx.Channel/ctx.ChatID，
//     与本次改动前逐字节一致；历史 job 行（channel=web/chatID=web-4 等）
//     语义不变，不做数据迁移。
//
// 为什么不能用 ctx.Channel/ctx.ChatID 认子代理：子代理 run 的 Channel/ChatID
// 继承自父会话（engine_wire.go buildSubAgentRunConfig: parentCtx.Channel/ChatID），
// 因此子代理建的 job 会全落父会话名下、且到期后回到父会话执行。
func cronOwnership(ctx *ToolContext) (channel, chatID string) {
	if ctx == nil {
		return "", ""
	}
	if key := strings.TrimSpace(ctx.CallerSessionKey); key != "" {
		return cronSubAgentChannel, key
	}
	return ctx.Channel, ctx.ChatID
}

func (t *CronTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	p, err := parseToolArgs[cronParams](input)
	if err != nil {
		return nil, err
	}

	channel, chatID := cronOwnership(ctx)

	switch p.Action {
	case "add":
		return t.addJob(ctx, *p)
	case "list":
		return t.listJobs(channel, chatID)
	case "remove":
		return t.removeJob(p.JobID, channel, chatID)
	default:
		return nil, fmt.Errorf("unknown action: %s (use add, list, remove)", p.Action)
	}
}

func (t *CronTool) addJob(ctx *ToolContext, p cronParams) (*ToolResult, error) {
	if p.Message == "" {
		return nil, fmt.Errorf("message is required for add")
	}

	// Must specify exactly one scheduling method
	hasCron := p.CronExpr != ""
	hasInterval := p.EverySeconds > 0
	hasDelay := p.DelaySeconds > 0
	hasAt := p.At != ""
	count := 0
	if hasCron {
		if err := cron.ValidateCronExpr(p.CronExpr); err != nil {
			return nil, fmt.Errorf("invalid cron expression: %w", err)
		}
		count++
	}
	if hasInterval {
		count++
	}
	if hasDelay {
		count++
	}
	if hasAt {
		// Validate ISO datetime format (e.g., "2026-02-12T10:30:00" or "2026-02-12T10:30:00Z")
		_, parseErr := time.Parse(time.RFC3339, p.At)
		if parseErr != nil {
			// Also try without timezone for common LLM outputs like "2026-02-12T10:30:00"
			_, parseErr2 := time.Parse("2006-01-02T15:04:05", p.At)
			if parseErr2 != nil {
				return nil, fmt.Errorf("invalid 'at' datetime format %q: must be ISO 8601 (e.g. '2026-02-12T10:30:00' or '2026-02-12T10:30:00Z'): %w", p.At, parseErr)
			}
		}
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("must specify one of: cron_expr, every_seconds, delay_seconds, at")
	}
	if count > 1 {
		return nil, fmt.Errorf("specify only one of: cron_expr, every_seconds, delay_seconds, at")
	}

	now := time.Now()
	job := &common.CronJob{
		ID:           fmt.Sprintf("job_%s", uuid.New().String()[:8]),
		Message:      p.Message,
		CronExpr:     p.CronExpr,
		EverySeconds: p.EverySeconds,
		DelaySeconds: p.DelaySeconds,
		At:           p.At,
		CreatedAt:    now,
	}

	// Set channel and sender info — 归属口径统一由 cronOwnership 解析：
	// 子代理 → ("agent", <interactive key>)，主会话 → (ctx.Channel, ctx.ChatID)。
	channel, chatID := cronOwnership(ctx)
	job.Channel = channel
	job.ChatID = chatID
	if ctx != nil {
		job.SenderID = ctx.SenderID
	}

	// Calculate next run and one_shot flag
	job.OneShot = job.At != "" || job.DelaySeconds > 0

	nextRun, err := cron.CalculateNextRun(job, now)
	if err != nil {
		return nil, err
	}

	// Check if one-shot job is in the past
	if job.OneShot && nextRun.Before(now) {
		return nil, fmt.Errorf("datetime %s is in the past", job.At)
	}

	job.NextRun = nextRun

	// Save to database
	if err := t.cronSvc.AddJob(job); err != nil {
		log.WithError(err).Error("Failed to save cron job")
		return nil, fmt.Errorf("failed to save cron job: %w", err)
	}

	schedDesc := t.scheduleDescription(job)
	return NewResult(fmt.Sprintf("Job created: %s\nSchedule: %s\nMessage: %s\nNext run: %s",
		job.ID, schedDesc, job.Message, job.NextRun.Format("2006-01-02 15:04:05 MST"))), nil
}

// listJobs lists the CURRENT session's cron jobs only (session-scoped: each
// session sees and manages only its own jobs — the same policy as the web
// task panel's CronTasks callback, ListJobsByChannelChatID).
//
// 归属由 cronOwnership 决定：子代理创建的任务归属它自己的物理会话
// （channel="agent"、chatID=<interactive key>），因此子代理的 list 只看得到
// 自己建的 job，父会话看不到也管不到 —— 与"谁建的谁管"一致。
func (t *CronTool) listJobs(channel, chatID string) (*ToolResult, error) {
	jobs, err := t.cronSvc.ListJobsByChannelChatID(channel, chatID)
	if err != nil {
		log.WithError(err).Error("Failed to list cron jobs")
		return nil, fmt.Errorf("failed to list cron jobs: %w", err)
	}

	if len(jobs) == 0 {
		return NewResult("No scheduled jobs."), nil
	}

	// Sort by created time
	slices.SortFunc(jobs, func(a, b *common.CronJob) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Scheduled jobs for this session (%d):\n\n", len(jobs))
	for _, j := range jobs {
		fmt.Fprintf(&sb, "- **%s**\n  Schedule: %s\n  Message: %s\n  Next: %s\n\n",
			j.ID, t.scheduleDescription(j), j.Message,
			j.NextRun.Format("2006-01-02 15:04:05 MST"))
	}
	return NewResult(sb.String()), nil
}

// removeJob deletes a job by ID, verifying it belongs to the CURRENT session
// (session-scoped ownership: a session can only cancel its own jobs). A job
// from another session returns "not found" — no existence leak.
//
// 归属口径与 add/list 同一套（cronOwnership）：子代理只能删自己会话的 job，
// 父会话删不动子代理的 job（反之亦然）。
func (t *CronTool) removeJob(jobID, channel, chatID string) (*ToolResult, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required for remove")
	}

	// Verify job exists and belongs to the calling session (channel+chatID).
	job, err := t.cronSvc.GetJob(jobID)
	if err != nil {
		log.WithError(err).Error("Failed to get cron job")
		return nil, fmt.Errorf("failed to get cron job: %w", err)
	}
	if job == nil {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}
	if job.Channel != channel || job.ChatID != chatID {
		return nil, fmt.Errorf("job not found: %s (jobs can only be removed from the session that created them)", jobID)
	}

	if err := t.cronSvc.RemoveJob(jobID); err != nil {
		log.WithError(err).Error("Failed to remove cron job")
		return nil, fmt.Errorf("failed to remove cron job: %w", err)
	}
	return NewResult(fmt.Sprintf("Job removed: %s", jobID)), nil
}

func (t *CronTool) scheduleDescription(job *common.CronJob) string {
	if job.At != "" {
		return fmt.Sprintf("once at %s", job.At)
	}
	if job.DelaySeconds > 0 {
		if job.DelaySeconds >= 3600 {
			return fmt.Sprintf("once after %dh%dm", job.DelaySeconds/3600, (job.DelaySeconds%3600)/60)
		}
		if job.DelaySeconds >= 60 {
			return fmt.Sprintf("once after %dm%ds", job.DelaySeconds/60, job.DelaySeconds%60)
		}
		return fmt.Sprintf("once after %ds", job.DelaySeconds)
	}
	if job.EverySeconds > 0 {
		if job.EverySeconds >= 3600 {
			return fmt.Sprintf("every %dh%dm", job.EverySeconds/3600, (job.EverySeconds%3600)/60)
		}
		if job.EverySeconds >= 60 {
			return fmt.Sprintf("every %dm%ds", job.EverySeconds/60, job.EverySeconds%60)
		}
		return fmt.Sprintf("every %ds", job.EverySeconds)
	}
	if job.CronExpr != "" {
		return fmt.Sprintf("cron(%s)", job.CronExpr)
	}
	return "unknown"
}
