package serverapp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/agent"
	"xbot/channel/web"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"

	"github.com/google/uuid"
)

// structured_callbacks.go — 结构化消息 WebCallbacks 的真实实现。
//
// 数据流（sendmessage-structured §7/§8/§9）：
//   1. HTTP 请求经 resolveAPISession 鉴权（callbacks 收到的 sel 已通过校验）；
//   2. 解析到会话库（TenantSession.SessionService()，绝不读主库）；
//   3. 幂等落库（AppendStructuredMessage / AnswerAskUser）；
//   4. 投递：answer/decision/command/text 进入 agent 插队优先队列
//      （运行中在 iteration 边界注入，空闲时开启新 turn）；
//   5. 返回可渲染消息，由 web 层广播 structured_message；
//   6. 原文落库**成功之后**，把这条消息的摘要写进主库 activity_index —— 跨会话
//      活动视图（/api/activity）因此只打主库一张表，不必逐会话库打开。索引是
//      派生物：写失败只记日志，绝不回滚已落库的原消息。

// structuredPriority 返回 kind 对应的插队优先级（设计 §9.2）。
func structuredPriority(kind string) int {
	switch kind {
	case "answer", "decision":
		return agent.StructuredPriorityHigh
	case "text", "command":
		return agent.StructuredPriorityNormal
	default:
		return agent.StructuredPriorityBackground
	}
}

// structuredDeliverable 判断该 kind 是否需要进入 agent run 循环。
func structuredDeliverable(kind string) bool {
	switch kind {
	case "answer", "decision", "command", "text", "report":
		// report 也必须唤醒目标会话：用户要求"工作收尾 / 需要批准时立刻上报"，
		// 不能只躺在收件箱里等下一次朝会（2026-10-22）。
		return true
	default:
		return false
	}
}

// reportDeliverCooldown 是同一目标会话的 report 唤醒冷却时间：子代理连发多条
// report 时不必每条都拉起一个新 turn（落库不受影响，一条都不会丢）。
const reportDeliverCooldown = 60 * time.Second

var (
	reportDeliverMu   sync.Mutex
	reportDeliverSeen = map[string]time.Time{}
)

// reportDeliverKey 用 (channel, chatID) 分桶 —— 只用 chatID 会让 web 与 cli 的
// 同名会话互相压制。
func reportDeliverKey(channel, chatID string) string { return channel + "/" + chatID }

// payloadBypassesReportCooldown 判断这条 report 是否必须绕过冷却。
// 上报规范：needs_approval / urgent 必须「立刻」处理，不能被普通报告压掉。
func payloadBypassesReportCooldown(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	for _, k := range []string{"needs_approval", "urgent"} {
		switch v := payload[k].(type) {
		case bool:
			if v {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(v), "true") {
				return true
			}
		}
	}
	return false
}

// reportDeliverDelay 返回本条 report 需要延迟多久再唤醒；0 表示立刻唤醒。
//
// 语义是**延迟**而不是丢弃。2026-10-22 review 发现：原实现冷却期内只把
// deliver 置 false 就返回，而全仓没有任何补投者（ListInbox 只服务 Web 角标），
// 于是被压掉的 report 永远不会自己醒来；而真实拓扑是 6 个子代理上报同一个
// 物理 chatID（分层下真实存在的物理 chatID（如 agent:team.ops/main）），最坏情况只唤醒第一条。
// 现在改由调用方 time.AfterFunc 在窗口结束时补投**原消息**。
func reportDeliverDelay(channel, chatID string, payload map[string]any) time.Duration {
	key := reportDeliverKey(channel, chatID)
	now := time.Now()

	reportDeliverMu.Lock()
	defer reportDeliverMu.Unlock()

	// 顺手淘汰过期条目：key 含 instance（自由字符串，如 oneshot-<role>-<纳秒>），
	// 不清理会单调增长。
	for k, t := range reportDeliverSeen {
		if now.Sub(t) >= reportDeliverCooldown {
			delete(reportDeliverSeen, k)
		}
	}

	if payloadBypassesReportCooldown(payload) {
		reportDeliverSeen[key] = now
		return 0 // 紧急报告永不延迟
	}
	if t, ok := reportDeliverSeen[key]; ok {
		if elapsed := now.Sub(t); elapsed < reportDeliverCooldown {
			return reportDeliverCooldown - elapsed
		}
	}
	reportDeliverSeen[key] = now
	return 0
}

// resetReportDeliverSeen 仅供测试：包级 map 跨测试会互相污染。
func resetReportDeliverSeen() {
	reportDeliverMu.Lock()
	reportDeliverSeen = map[string]time.Time{}
	reportDeliverMu.Unlock()
}

// structuredContent 生成人读文本（LLM 上下文/UI 缺省内容）。
func structuredContent(kind string, payload map[string]any, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if payload == nil {
		return ""
	}
	if s, ok := payload["text"].(string); ok && s != "" {
		return s
	}
	switch kind {
	case "ask_user":
		var parts []string
		if qs, ok := payload["questions"].([]any); ok {
			for _, q := range qs {
				if m, ok := q.(map[string]any); ok {
					if s, ok := m["question"].(string); ok && s != "" {
						parts = append(parts, s)
					}
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	case "report":
		if s, _ := payload["summary"].(string); s != "" {
			return s
		}
	case "decision":
		verdict, _ := payload["verdict"].(string)
		// 兼容两种拼写：工具描述与文档用 "comment"，历史实现读 "comments"。
		comments, _ := payload["comment"].(string)
		if comments == "" {
			comments, _ = payload["comments"].(string)
		}
		label := map[string]string{"approve": "准", "reject": "驳", "modify": "改"}[verdict]
		if label == "" {
			label = verdict
		}
		if comments != "" {
			return label + "：" + comments
		}
		return label
	}
	return ""
}

func structuredRole(kind string) string {
	switch kind {
	case "ask_user", "report":
		return "assistant"
	default:
		return "user"
	}
}

// newAskCorrelationID 生成 ask_user 的关联键（工单钥匙）。
//
// 形状 ask-<uuid>：前缀让日志/前端一眼分开「回答钥匙」与 message_id；用 UUID 而不是
// 自增 id，是因为 correlation_id 会跨会话、跨渠道出现，必须全局唯一且不可猜。
func newAskCorrelationID() string {
	return "ask-" + uuid.NewString()
}

// ensureAskCorrelationID 只给「缺钥匙的 ask_user」补发钥匙，其余输入原样返回。
//
// 三条边界（都有单测）：
//   - 客户端显式给了 correlation_id ⇒ 尊重（幂等重投、导回填都靠它对齐）；
//   - 非 ask_user ⇒ 不碰（report/decision/text 的 correlation_id 语义不同，
//     凭空生成会让 reply_to 闭环对不上）；
//   - 只补一次：入参已是最终值，落库与 envelope 共用同一个 input，天然同值。
func ensureAskCorrelationID(input protocol.StructuredMessageInput) protocol.StructuredMessageInput {
	if input.Kind != "ask_user" || strings.TrimSpace(input.CorrelationID) != "" {
		return input
	}
	input.CorrelationID = newAskCorrelationID()
	return input
}

// newUserCardCorrelationID 生成用户看板卡片的关联键（待办钥匙）。
//
// 形状 card-<uuid>：与 ask_user 的 ask-<uuid> 同源同风格（全局唯一、不可猜），
// 但前缀区分「用户看板待办」与「工单回答钥匙」，日志/前端一眼能认出。
func newUserCardCorrelationID() string {
	return "card-" + uuid.NewString()
}

// ensureUserCardCorrelationID 只给「缺钥匙的 to="user" 卡片」补发钥匙。
//
// 为什么卡片必须有钥匙：用户处理后回流给发起 agent 的消息要带同一把钥匙，
// 否则「我提的这条待办」和「用户对它的处理」在存储/审计里对不上（旧实现回流
// 自造 notice-<id>，两把钥匙永远配不上对）。
//
// 三条边界：
//   - 只碰 Silent（to="user" 卡片）：普通 report/decision 的 correlation_id 语义不同；
//   - 已有钥匙 ⇒ 原样保留（ask_user 已由 ensureAskCorrelationID 发过 ask-<uuid>，
//     幂等重投/导入回填也要能带上自己那把）；
//   - 只补一次：落库与信封共用同一个 input，天然同值。
func ensureUserCardCorrelationID(input protocol.StructuredMessageInput) protocol.StructuredMessageInput {
	if !input.Silent || strings.TrimSpace(input.CorrelationID) != "" {
		return input
	}
	input.CorrelationID = newUserCardCorrelationID()
	return input
}

// buildStructuredEnvelope 生成完整信封 JSON（服务端填充 from/created_at）。
//
// origin 只在 to="user" 卡片上出现：它记录**发起方**（哪个 agent、哪个物理会话），
// 用户在看板处理这张卡之后，回流消息投递到 origin.session（见 resolveFn）。
func buildStructuredEnvelope(senderID string, sel web.SessionSelector, input protocol.StructuredMessageInput) map[string]any {
	env := map[string]any{
		"schema":          "xbot.message/v1",
		"kind":            input.Kind,
		"from":            map[string]any{"type": "user", "id": senderID, "session": sel.Channel + ":" + sel.ChatID},
		"to":              input.To,
		"correlation_id":  input.CorrelationID,
		"reply_to":        input.ReplyTo,
		"idempotency_key": input.IdempotencyKey,
		"payload":         input.Payload,
		"text":            structuredContent(input.Kind, input.Payload, input.Text),
		"created_at":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if input.OriginSession != "" || input.OriginAgent != "" {
		env["origin"] = map[string]any{
			"session": input.OriginSession,
			"agent":   input.OriginAgent,
		}
	}
	if input.Silent {
		env["silent"] = true
	}
	return env
}

// toProtocolStructured 把存储行转换为前端可渲染形态。
func toProtocolStructured(m sqlite.StructuredMessage) *protocol.StructuredMessage {
	payload := map[string]any{}
	envelope := map[string]any{}
	if m.StructuredJSON != "" {
		_ = json.Unmarshal([]byte(m.StructuredJSON), &envelope)
		if p, ok := envelope["payload"].(map[string]any); ok {
			payload = p
		}
	}
	out := &protocol.StructuredMessage{
		MessageID:     m.ID,
		Kind:          m.Kind,
		CorrelationID: m.CorrelationID,
		ReplyTo:       m.ReplyTo,
		Direction:     m.Direction,
		Answered:      m.Answered,
		Cancelled:     m.Cancelled,
		Text:          m.Content,
		Payload:       payload,
		Sender:        protocol.SenderInfo{Type: m.SenderType, ID: m.SenderID, Name: m.SenderName},
		CreatedAt:     m.CreatedAt,
		Envelope:      envelope,
	}
	if out.Text == "" {
		out.Text = structuredContent(m.Kind, payload, "")
	}
	return out
}

// refuseSelfTarget 是 D-07 的**自激守卫**：结构化消息若投给调用方自己的会话，
// 会立刻把调用方再唤醒一次（自激 / 死循环），必须拒绝。
//
// 分两支，且**禁止从 SessionKey 做字符串切割**（那不是可靠前提，2026-10-22 审计）：
//   - agent 目标：目标是子代理会话，只有 interactive 子代理才有物理会话 key
//     （CallerScope.SessionKey 非空），拿它与解析后的目标物理 key 比。
//     主会话（SessionKey 为空）发往 agent 目标天然不是"自己"，直接放行。
//   - 非 agent 目标：调用方 (Channel, ChatID) 与目标同形即拒。
//
// 旧实现把"逻辑地址"和"物理 key"直接比，永不相等 ⇒ 等于没拦；只删不补又让
// "主会话向自己会话发 report"从被拒变成放行 = 净回归。现在是单点实现 + 表驱动测试。
func refuseSelfTarget(kind string, caller tools.CallerScope, channel, realChatID string) error {
	if channel == "agent" {
		if caller.SessionKey != "" && realChatID == caller.SessionKey {
			return fmt.Errorf("refusing to post kind=%s to the caller's own session (%s) — it would re-trigger the caller; target another session", kind, realChatID)
		}
		return nil
	}
	if caller.Channel == channel && caller.ChatID == realChatID {
		return fmt.Errorf("refusing to post kind=%s to the caller's own session (%s:%s) — it would re-trigger the caller; target the department session instead", kind, channel, realChatID)
	}
	return nil
}

// structuredCallbacks 构造 WebCallbacks 的结构化消息五件套。
func structuredCallbacks(ag *agent.Agent) (appendFn func(string, web.SessionSelector, protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error), answerFn func(string, web.SessionSelector, protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error), inboxFn func(web.SessionSelector, int64, int) ([]protocol.StructuredMessage, bool, int64, error), countFn func(web.SessionSelector) (int, error), listFn func(web.SessionSelector, string, int64, int64, int) ([]protocol.StructuredMessage, bool, int64, error), resolveFn func(string, web.SessionSelector, protocol.StructuredResolveInput) (*protocol.StructuredResolveResult, error)) {
	sessionOf := func(sel web.SessionSelector) (*sqlite.SessionService, int64, error) {
		sess, err := ag.MultiSession().GetOrCreateSession(sel.Channel, sel.ChatID)
		if err != nil {
			return nil, 0, err
		}
		// master #444 起 SessionService() 返回存储抽象的 storage.SessionStore：
		// 结构化消息（report/ask_user/decision/inbox）目前只有 SQLite 后端实现，
		// PG 后端尚未落地该特性（**未覆盖项**，不是静默降级）—— 显式断言并给出
		// 可读错误，避免批复写进一张不存在的表、消息凭空蒸发。
		svc, ok := sess.SessionService().(*sqlite.SessionService)
		if !ok {
			return nil, 0, fmt.Errorf("structured messages are not supported by this storage backend (channel=%s)", sel.Channel)
		}
		return svc, sess.TenantID(), nil
	}

	// activityIdx 是主库活动索引（跨会话活动流的唯一写入口）。PG 后端没有这张
	// 表 ⇒ nil，Insert 静默跳过（视图退化为空列表，而不是 500）。
	var activityIdx *sqlite.ActivityIndex
	if ms := ag.MultiSession(); ms != nil && ms.DB() != nil {
		activityIdx = sqlite.NewActivityIndex(ms.DB().Conn())
	}

	// indexActivity 把一条**已落库**的结构化消息摘要写进主库索引。
	//
	// 时序铁律：只在原文写成功之后调用（索引可以有延迟，但绝不能出现「索引里有、
	// 原文没有」—— 用户点进去会 404）。普通 text 不入索引（见 ActivityKindIndexed）。
	indexActivity := func(tenantID int64, sel web.SessionSelector, row *sqlite.StructuredMessage, summary string) {
		if activityIdx == nil || row == nil {
			return
		}
		kind := row.Kind
		if kind == "" {
			kind = sqlite.StructuredKindText
		}
		if !sqlite.ActivityKindIndexed(kind) {
			return
		}
		if strings.TrimSpace(summary) == "" {
			summary = row.Content
		}
		err := activityIdx.Insert(sqlite.ActivityItem{
			TenantID:   tenantID,
			ChatID:     sel.ChatID,
			Channel:    sel.Channel,
			MessageID:  row.ID,
			Kind:       kind,
			SenderID:   row.SenderID,
			SenderName: row.SenderName,
			Summary:    summary,
			CreatedAt:  sqlite.FormatActivityTime(row.CreatedAt),
		})
		if err != nil {
			// 派生物失败不影响主流程：丢掉一行索引只是少一条活动，原消息仍在会话库。
			log.WithError(err).WithFields(log.Fields{
				"chat_id": sel.ChatID, "message_id": row.ID, "kind": kind,
			}).Warn("Failed to index structured message into activity_index")
		}
	}

	appendFn = func(senderID string, sel web.SessionSelector, input protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, err
		}
		// kind=answer 与 /api/answer 共用同一套 CAS 闭环（见 answerViaMessage）：
		// 否则 /api/message 写一行 answer 却不会把 ask_user 置 answered，收件箱永远不收敛。
		if input.Kind == "answer" {
			return answerViaMessage(senderID, sel, svc, tenantID, ag, activityIdx, input)
		}
		// D-07：ask_user 是「可回答的工单」，correlation_id 是回答它的唯一钥匙。
		// 创建方是服务端，所以钥匙必须由服务端发——客户端（Web 表单 / SendMessage
		// 工具 / 飞书卡片）都可能不带。此前落库 correlation_id="" 时，/api/answer
		// 对空值一律 400（correlation_id is required），提问在结构上不可回答：
		// 用户看得见、点回答必失败。生成点放在落库之前，envelope 与 message 同值。
		input = ensureAskCorrelationID(input)
		// to="user" 卡片必须有稳定的 correlation_id：用户处理后的回流要带同一把钥匙
		// （见 ensureUserCardCorrelationID 与 persistUserNoticeFeedback）。
		input = ensureUserCardCorrelationID(input)
		createdAt, err := parseStructuredCreatedAt(input.CreatedAt)
		if err != nil {
			return nil, false, err
		}
		envelope := buildStructuredEnvelope(senderID, sel, input)
		raw, _ := json.Marshal(envelope)
		senderName := senderID
		senderType := "user"
		if input.OriginSession != "" || input.OriginAgent != "" {
			// 卡片是 agent 发给用户的：发起方不是"用户"，看板要能显示是谁发的。
			senderType = "agent"
		}
		recordType := sqlite.HistoryRecordType("")
		if input.Silent {
			// to="user" 卡片：**出流** —— 只进看板，不进聊天流/历史回放/LLM 上下文。
			// 存储仍是同一张 session_messages（record_type='notice'），不新造第二套存储。
			recordType = sqlite.HistoryRecordNotice
		}
		stored := sqlite.StructuredMessage{
			ChatID:         sel.ChatID,
			RecordType:     recordType,
			Kind:           input.Kind,
			CorrelationID:  input.CorrelationID,
			ReplyTo:        input.ReplyTo,
			IdempotencyKey: input.IdempotencyKey,
			StructuredJSON: string(raw),
			Direction:      "in",
			Role:           structuredRole(input.Kind),
			Content:        structuredContent(input.Kind, input.Payload, input.Text),
			SenderType:     senderType,
			SenderID:       senderID,
			SenderName:     senderName,
			CreatedAt:      createdAt,
		}
		var id int64
		var created bool
		if input.Kind == "decision" {
			// 批复闭环：目标行必须存在且未批复/未取消（CAS）。
			// reply_to 是权威目标；兼容老客户端只用 payload.target_message_id。
			stored.ReplyTo = structuredFirstNonEmpty(input.ReplyTo, structuredPayloadString(input.Payload, "target_message_id"))
			id, created, err = svc.AppendDecision(tenantID, stored)
		} else {
			id, created, err = svc.AppendStructuredMessage(tenantID, stored)
		}
		if err != nil {
			return nil, false, err
		}
		row, err := svc.GetStructuredMessageByID(tenantID, id)
		if err != nil {
			return nil, false, err
		}
		// payload.summary 优先（发送方自己给的一句话结论），否则退回正文前 200 字符。
		indexActivity(tenantID, sel, row, structuredPayloadString(input.Payload, "summary"))
		// Silent（to="user" 卡片）：落库但不唤醒目标会话 —— 卡片是给用户看的待办，
		// 不是给用户会话的新一轮输入，否则会话会被自己的卡片自激。
		deliver := created && structuredDeliverable(input.Kind) && !input.Silent
		item := &agent.StructuredQueueItem{
			ID:            fmt.Sprintf("structured:%d", id),
			Channel:       sel.Channel,
			ChatID:        sel.ChatID,
			SenderID:      senderID,
			SenderName:    senderName,
			Content:       row.Content,
			Kind:          input.Kind,
			CorrelationID: input.CorrelationID,
			Priority:      structuredPriority(input.Kind),
		}
		if deliver && input.Kind == "report" {
			if delay := reportDeliverDelay(sel.Channel, sel.ChatID, input.Payload); delay > 0 {
				// 冷却窗口内：延迟补投原消息（不是丢弃）。urgent/needs_approval
				// 的 delay 恒为 0，走下面的立即投递分支。
				time.AfterFunc(delay, func() { ag.DeliverStructuredMessage(item) })
				deliver = false
			}
		}
		if deliver {
			ag.DeliverStructuredMessage(item)
		}
		return toProtocolStructured(*row), !created, nil
	}

	answerFn = func(senderID string, sel web.SessionSelector, input protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, err
		}
		return applyStructuredAnswer(senderID, sel, svc, tenantID, ag, activityIdx, input)
	}

	inboxFn = func(sel web.SessionSelector, cursor int64, limit int) ([]protocol.StructuredMessage, bool, int64, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, 0, err
		}
		rows, more, next, err := svc.ListInbox(tenantID, cursor, limit)
		if err != nil {
			return nil, false, 0, err
		}
		out := make([]protocol.StructuredMessage, 0, len(rows))
		for _, r := range rows {
			out = append(out, *toProtocolStructured(r))
		}
		return out, more, next, nil
	}

	countFn = func(sel web.SessionSelector) (int, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return 0, err
		}
		return svc.CountInbox(tenantID)
	}

	listFn = func(sel web.SessionSelector, kind string, cursor, afterID int64, limit int) ([]protocol.StructuredMessage, bool, int64, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, false, 0, err
		}
		// afterID > 0 ⇒ 增量补齐（SSE 重连），按 id 升序；否则走分页（id DESC）。
		var rows []sqlite.StructuredMessage
		var more bool
		var next int64
		if afterID > 0 {
			rows, more, next, err = svc.ListStructuredMessagesAfter(tenantID, kind, afterID, limit)
		} else {
			rows, more, next, err = svc.ListStructuredMessages(tenantID, kind, cursor, limit)
		}
		if err != nil {
			return nil, false, 0, err
		}
		out := make([]protocol.StructuredMessage, 0, len(rows))
		for _, r := range rows {
			out = append(out, *toProtocolStructured(r))
		}
		return out, more, next, nil
	}

	// ── 用户看板卡片处理（to="user" 闭环的交互端）──────────────────────────
	//
	// 状态机：pending（agent 发卡）→ 用户在看板上 approve/reject/dismiss →
	// resolved（CAS 置 answered/cancelled）→ 回流发起 agent。
	//
	// 幂等靠 storage 的 CAS + (chat_id, idempotency_key)：只有 Created==true 才投递回流。
	resolveFn = func(senderID string, sel web.SessionSelector, input protocol.StructuredResolveInput) (*protocol.StructuredResolveResult, error) {
		svc, tenantID, err := sessionOf(sel)
		if err != nil {
			return nil, err
		}
		action := strings.ToLower(strings.TrimSpace(input.Action))
		status, ok := sqlite.NoticeStatusOfAction(action)
		if !ok {
			return nil, fmt.Errorf("invalid action %q (want approve|reject|answer|dismiss)", input.Action)
		}
		if input.MessageID <= 0 {
			return nil, fmt.Errorf("message_id is required")
		}
		row, err := svc.GetStructuredMessageByID(tenantID, input.MessageID)
		if err != nil {
			return nil, err // ErrStructuredNotFound → 404
		}
		if row.Kind != sqlite.StructuredKindReport && row.Kind != sqlite.StructuredKindAskUser {
			return nil, fmt.Errorf("invalid: message_id %d is kind=%q, not a user board card (want report|ask_user)", input.MessageID, row.Kind)
		}
		originSession, originAgent := userNoticeOrigin(row)
		comment := strings.TrimSpace(input.Comment)
		content := userNoticeResolutionText(row.ID, row.Kind, status, comment)
		resolutionJSON, _ := json.Marshal(map[string]any{
			"status":         status,
			"action":         action,
			"comment":        comment,
			"answered_by":    senderID,
			"origin_session": originSession,
			"origin_agent":   originAgent,
		})
		idem := strings.TrimSpace(input.IdempotencyKey)
		if idem == "" {
			// 默认幂等键：同一张卡的同一个动作只生效一次（双击/刷新重点都安全）。
			idem = fmt.Sprintf("notice:%d:%s", row.ID, action)
		}
		res, rerr := svc.ResolveNotice(tenantID, sel.ChatID, input.MessageID, action, content, string(resolutionJSON), idem, senderID, senderID)
		if rerr != nil {
			return nil, rerr // 409（已处理）/ 404（不存在）
		}
		if res.Created && originSession != "" {
			// 回流只在这里发生一次；第二次调用 Created=false，不会重复打扰发起 agent。
			//
			// 两步都是既有语义（与 decision 回流一致）：
			//  1. 在**发起方会话**里留一行 decision/answer —— 它被唤醒后从历史里读到；
			//  2. 唤醒该会话（busy → 优先队列 / idle → 新 turn）。
			// 只投递不落行的话，agent 醒来看到的是一条空消息（DeliverStructuredMessage
			// 的空 content 契约），用户说了什么全丢。
			if perr := persistUserNoticeFeedback(ag, originSession, input.MessageID, row.Kind, status, content, senderID, row.CorrelationID); perr != nil {
				log.WithError(perr).WithFields(log.Fields{
					"message_id": input.MessageID, "origin_session": originSession,
				}).Warn("Failed to persist user notice feedback into the originating session")
			}
			if !deliverUserNoticeFeedback(ag, originSession, input.MessageID, row.Kind, status, content, senderID, row.CorrelationID) {
				log.WithFields(log.Fields{
					"message_id": input.MessageID, "origin_session": originSession,
				}).Warn("Failed to deliver user notice feedback to the originating agent")
			}
		}
		if res.Written != nil {
			indexActivity(tenantID, sel, res.Written, content)
		}
		return &protocol.StructuredResolveResult{
			MessageID:    input.MessageID,
			ResolutionID: res.WrittenID,
			Status:       status,
			Duplicate:    !res.Created,
			Origin:       originSession,
		}, nil
	}

	// ── 把同一份落地实现注入给工具层 ─────────────────────────────────────
	// SendMessage(kind = report/ask_user/answer/decision) 走的就是这个 appendFn：
	// agent 在自己会话里说"批了"→ 工具层调它 → 落到目标（子代理）会话 + CAS。
	// 共用闭包而不是另写一份，是为了让"web 点按钮"和"自然语言批复"永远同一语义。
	tools.SetStructuredAppend(func(senderID string, caller tools.CallerScope, channel, chatID string, in protocol.StructuredMessageInput) (int64, bool, error) {
		// 逻辑地址（agent:role/instance）→ 物理会话 key（web:web-4/role:instance）。
		// 不做这一步就会把批复写进一个新建的空壳会话：2026-10-22 实测事故。
		//
		// D-10：解析必须带**调用方作用域**（caller.Channel/caller.ChatID）——
		// 目标必须在调用方作用域内（同一顶层会话的子树：子代理组 ↔ 调度方可互发，
		// 跨顶层会话一律拒），否则任意会话都能凭逻辑地址把 report/decision 投进
		// 别人的子代理会话。解析不出来/超出作用域/多命中时
		// ResolveStructuredTarget 返回 ("", err)，这里直接失败，不建野会话。
		// 注意它**不校验层级**：地址里没有组织关系，「只向上报、不许平级串」
		// 靠 CHARTCHECK 纪律 + 审计抽查。
		realChatID, rerr := ag.ResolveStructuredTarget(caller.Channel, caller.ChatID, channel, chatID)
		if rerr != nil {
			return 0, false, rerr
		}
		// D-07 自激守卫（判据见 refuseSelfTarget，单点实现 + 独立测试）。
		if gerr := refuseSelfTarget(in.Kind, caller, channel, realChatID); gerr != nil {
			return 0, false, gerr
		}
		msg, dup, err := appendFn(senderID, web.SessionSelector{Channel: channel, ChatID: realChatID}, in)
		if err != nil || msg == nil {
			return 0, dup, err
		}
		return msg.MessageID, dup, nil
	})

	// to="user"（用户看板）的落地实现：与上面**同一份** append 闭包，只是落点已经
	// 由工具层解析成用户可见的根会话，并带上 origin/silent（HTTP 客户端伪造不了：
	// 那三个字段是 protocol.StructuredMessageInput 上的 json:"-"）。
	//
	// 不做 ResolveStructuredTarget：目标是普通渠道会话（web:web-4），不是 agent 地址，
	// 且调用方（子代理）本来就允许给自己的根会话投递 —— 那是"向上报给用户"，不是越权。
	tools.SetUserNoticeAppend(func(senderID string, caller tools.CallerScope, channel, chatID string, in protocol.StructuredMessageInput) (int64, bool, error) {
		if !in.Silent {
			// 防御：这条路径只服务用户看板，必须既不唤醒目标会话又带回流目标。
			return 0, false, fmt.Errorf("user board append requires silent=true and an origin session")
		}
		msg, dup, err := appendFn(senderID, web.SessionSelector{Channel: channel, ChatID: chatID}, in)
		if err != nil || msg == nil {
			return 0, dup, err
		}
		return msg.MessageID, dup, nil
	})
	return
}

// userNoticeOrigin 从卡片信封里取出回流目标（origin.session / origin.agent）。
// 老行（没有 origin）返回空串：卡片仍可在看板上处理，只是没有 agent 可回流。
func userNoticeOrigin(row *sqlite.StructuredMessage) (session, agentAddr string) {
	if row == nil || strings.TrimSpace(row.StructuredJSON) == "" {
		return "", ""
	}
	var env struct {
		Origin struct {
			Session string `json:"session"`
			Agent   string `json:"agent"`
		} `json:"origin"`
	}
	if json.Unmarshal([]byte(row.StructuredJSON), &env) != nil {
		return "", ""
	}
	return strings.TrimSpace(env.Origin.Session), strings.TrimSpace(env.Origin.Agent)
}

// userNoticeResolutionText 生成处理记录行的正文（人读）。
func userNoticeResolutionText(messageID int64, kind, status, comment string) string {
	label := map[string]string{
		"approved":  "已批准",
		"rejected":  "已否决",
		"answered":  "已回答",
		"dismissed": "已忽略",
	}[status]
	if label == "" {
		label = status
	}
	out := fmt.Sprintf("用户处理了卡片 #%d（kind=%s）：%s", messageID, kind, label)
	if comment != "" {
		out += "。" + comment
	}
	return out
}

// splitPhysicalSessionKey 把物理会话 key 还原成存储层的 (channel, chatID)。
//
// interactive 子代理的会话在存储层是 channel="agent"、chatID=key
// （形如 "web:web-4/court.shoufu:main"）；主会话是 "web:web-4"。
func splitPhysicalSessionKey(key string) (channel, chatID string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ""
	}
	slash := strings.LastIndex(key, "/")
	colon := strings.Index(key, ":")
	if slash > 0 && colon > 0 && colon < slash {
		return "agent", key
	}
	if colon <= 0 || colon == len(key)-1 {
		return "", ""
	}
	return key[:colon], key[colon+1:]
}

// persistUserNoticeFeedback 在**发起方会话**里留一行 decision/answer 回流记录。
//
// 为什么要单独落行（而不是把内容塞进投递）：agent.DeliverStructuredMessage 对
// 空闲会话投的是**空 content + structured_delivery**（约定：结构化行已经先落库，
// 避免重复写一条 user 行）。既有 decision 回流靠"decision 行就写在目标会话里"满足
// 这个约定；用户看板的卡片行却在**用户的**会话里，所以回流必须在这里补一行，
// 否则 agent 被唤醒后读不到用户到底说了什么。
//
// 幂等：idempotency_key = "notice-feedback:<card id>"，重复提交不会产生第二行。
//
// 钥匙对齐（N1 断言③）：cardCorrelationID 是**卡片创建时**落库的 correlation_id，
// 回流必须逐字复用它 —— 前端/审计才能把「我提的这条待办」与「用户对它的处理」
// 配成一对。旧实现自造 notice-<id>，等于两把互不相认的钥匙。
// 老卡片（升级前落库、没有钥匙）退化为 notice-<id>：不阻断回流，只是无键可对。
func persistUserNoticeFeedback(ag *agent.Agent, originSession string, messageID int64, cardKind, status, content, senderID, cardCorrelationID string) error {
	if ag == nil || ag.MultiSession() == nil {
		return fmt.Errorf("no session manager")
	}
	channel, chatID := splitPhysicalSessionKey(originSession)
	if channel == "" || chatID == "" {
		return fmt.Errorf("cannot parse origin session %q", originSession)
	}
	sess, err := ag.MultiSession().GetOrCreateSession(channel, chatID)
	if err != nil {
		return err
	}
	svc, ok := sess.SessionService().(*sqlite.SessionService)
	if !ok {
		return fmt.Errorf("session backend does not support structured messages")
	}
	kind := sqlite.StructuredKindDecision
	if status == "dismissed" || status == "answered" {
		kind = sqlite.StructuredKindAnswer
	}
	correlationID := strings.TrimSpace(cardCorrelationID)
	if correlationID == "" {
		correlationID = fmt.Sprintf("notice-%d", messageID)
	}
	envelope, _ := json.Marshal(map[string]any{
		"schema":         "xbot.message/v1",
		"kind":           kind,
		"to":             originSession,
		"correlation_id": correlationID,
		"payload":        map[string]any{"status": status, "card_kind": cardKind, "card_id": messageID, "text": content},
		"text":           content,
	})
	_, _, err = svc.AppendStructuredMessage(sess.TenantID(), sqlite.StructuredMessage{
		ChatID:         chatID,
		Kind:           kind,
		CorrelationID:  correlationID,
		StructuredJSON: string(envelope),
		Direction:      "in",
		Role:           "user",
		Content:        content,
		SenderType:     "user",
		SenderID:       senderID,
		SenderName:     senderID,
		IdempotencyKey: fmt.Sprintf("notice-feedback:%d", messageID),
		CreatedAt:      time.Now(),
	})
	return err
}

// deliverUserNoticeFeedback 把"用户处理了卡片"回流给发起 agent（唤醒）。
//
// 复用既有 kind=decision 的结构化回流通道（agent.DeliverStructuredMessage）：
// 目标会话忙则进优先队列、空闲则起一轮 —— 不另造消息通道。
func deliverUserNoticeFeedback(ag *agent.Agent, originSession string, messageID int64, kind, status, content, senderID, cardCorrelationID string) bool {
	if ag == nil {
		return false
	}
	channel, chatID := splitPhysicalSessionKey(originSession)
	if channel == "" || chatID == "" {
		return false
	}
	correlationID := strings.TrimSpace(cardCorrelationID)
	if correlationID == "" {
		correlationID = fmt.Sprintf("notice-%d", messageID)
	}
	return ag.DeliverStructuredMessage(&agent.StructuredQueueItem{
		ID:            fmt.Sprintf("notice-resolve:%d", messageID),
		Channel:       channel,
		ChatID:        chatID,
		SenderID:      senderID,
		SenderName:    senderID,
		Content:       content,
		Kind:          "decision",
		CorrelationID: correlationID,
		Priority:      agent.StructuredPriorityHigh,
	})
}

// structuredFirstNonEmpty 返回第一个非空字符串（reply_to 与旧字段兼容用）。
func structuredFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// structuredPayloadString 读取 payload 的字符串字段（缺省空串）。
func structuredPayloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

// parseStructuredCreatedAt 解析可选的 created_at（导入/回填/演示用）。// 空串 ⇒ 零值（由存储层取 now）；非法格式 ⇒ 错误（web 层映射 400）。
func parseStructuredCreatedAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid created_at %q (want RFC3339)", raw)
}

// answerViaMessage 让 POST /api/message 的 kind=answer 与 POST /api/answer 共用
// 同一套 CAS 闭环（方案 a）：目标不存在 → 404；已答/已取消 → 409；成功后目标
// ask_user 置 answered=1，收件箱立即收敛。
//
// 与 /api/answer 的唯一区别是寻址方式：这里用 reply_to（message_id）定位问题行，
// correlation_id 从目标行派生（客户端显式传了就必须一致）。
func answerViaMessage(senderID string, sel web.SessionSelector, svc *sqlite.SessionService, tenantID int64, ag *agent.Agent, activityIdx *sqlite.ActivityIndex, input protocol.StructuredMessageInput) (*protocol.StructuredMessage, bool, error) {
	targetID, perr := strconv.ParseInt(strings.TrimSpace(input.ReplyTo), 10, 64)
	if perr != nil || targetID <= 0 {
		return nil, false, fmt.Errorf("invalid: reply_to must be the ask_user message_id for kind=answer")
	}
	target, err := svc.GetStructuredMessageByID(tenantID, targetID)
	if err != nil {
		return nil, false, err // ErrStructuredNotFound → 404
	}
	if target.Kind != "ask_user" {
		return nil, false, fmt.Errorf("invalid: reply_to %d points to kind=%s, want ask_user", targetID, target.Kind)
	}
	if strings.TrimSpace(target.CorrelationID) == "" {
		return nil, false, fmt.Errorf("invalid: ask_user %d has no correlation_id", targetID)
	}
	if input.CorrelationID != "" && input.CorrelationID != target.CorrelationID {
		return nil, false, fmt.Errorf("invalid: correlation_id %q does not match target %q", input.CorrelationID, target.CorrelationID)
	}
	answers, cancelled, err := answersFromPayload(input.Payload)
	if err != nil {
		return nil, false, err
	}
	return applyStructuredAnswer(senderID, sel, svc, tenantID, ag, activityIdx, protocol.StructuredAnswerInput{
		CorrelationID:  target.CorrelationID,
		Answers:        answers,
		Cancelled:      cancelled,
		IdempotencyKey: input.IdempotencyKey,
		Channel:        sel.Channel,
		ChatID:         sel.ChatID,
	})
}

// answersFromPayload 把 /api/message 的 payload.answers 转成协议结构。
func answersFromPayload(payload map[string]any) ([]protocol.StructuredAnswer, bool, error) {
	if payload == nil {
		return nil, false, fmt.Errorf("invalid: payload.answers is required for kind=answer")
	}
	cancelled, _ := payload["cancelled"].(bool)
	rawAnswers, ok := payload["answers"].([]any)
	if !ok {
		if cancelled && payload["answers"] == nil {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("invalid: payload.answers must be an array")
	}
	out := make([]protocol.StructuredAnswer, 0, len(rawAnswers))
	for i, raw := range rawAnswers {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("invalid: payload.answers[%d] must be an object", i)
		}
		a := protocol.StructuredAnswer{}
		a.QuestionID, _ = m["question_id"].(string)
		a.Text, _ = m["text"].(string)
		if opts, ok := m["option_ids"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok {
					a.OptionIDs = append(a.OptionIDs, s)
				}
			}
		}
		out = append(out, a)
	}
	return out, cancelled, nil
}

// applyStructuredAnswer 是 /api/answer 与 /api/message(kind=answer) 的共同闭环：
// 校验答案形状 → CAS 置位目标 ask_user → 写 answer 行 → 投递 agent。
func applyStructuredAnswer(senderID string, sel web.SessionSelector, svc *sqlite.SessionService, tenantID int64, ag *agent.Agent, activityIdx *sqlite.ActivityIndex, input protocol.StructuredAnswerInput) (*protocol.StructuredMessage, bool, error) {
	// 校验答案形状（multi_select=false 时 option_ids ≤ 1 等）。
	if err := validateAskUserAnswers(svc, tenantID, input); err != nil {
		return nil, false, err
	}
	payload := map[string]any{
		"answers":     input.Answers,
		"cancelled":   input.Cancelled,
		"answered_by": senderID,
	}
	raw, _ := json.Marshal(payload)
	text := answerText(input)
	id, _, created, err := svc.AnswerAskUser(tenantID, sel.ChatID, input.CorrelationID,
		text, string(raw), input.IdempotencyKey, senderID, senderID, input.Cancelled)
	if err != nil {
		return nil, false, err
	}
	stored, err := svc.GetStructuredMessageByID(tenantID, id)
	if err != nil {
		return nil, false, err
	}
	// 答案行同样是结构化消息：落库成功后进活动索引（summary 用回答正文）。
	indexAnswer := func() {
		if activityIdx == nil || stored == nil {
			return
		}
		if err := activityIdx.Insert(sqlite.ActivityItem{
			TenantID:   tenantID,
			ChatID:     sel.ChatID,
			Channel:    sel.Channel,
			MessageID:  stored.ID,
			Kind:       sqlite.StructuredKindAnswer,
			SenderID:   stored.SenderID,
			SenderName: stored.SenderName,
			Summary:    text,
			CreatedAt:  sqlite.FormatActivityTime(stored.CreatedAt),
		}); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"chat_id": sel.ChatID, "message_id": stored.ID, "kind": "answer",
			}).Warn("Failed to index answer into activity_index")
		}
	}
	indexAnswer()
	if created {
		ag.DeliverStructuredMessage(&agent.StructuredQueueItem{
			ID:            fmt.Sprintf("answer:%d", id),
			Channel:       sel.Channel,
			ChatID:        sel.ChatID,
			SenderID:      senderID,
			SenderName:    senderID,
			Content:       text,
			Kind:          "answer",
			CorrelationID: input.CorrelationID,
			Priority:      agent.StructuredPriorityHigh,
		})
	}
	return toProtocolStructured(*stored), !created, nil
}

// validateAskUserAnswers 按 ask_user 信封校验回答。
func validateAskUserAnswers(svc *sqlite.SessionService, tenantID int64, input protocol.StructuredAnswerInput) error {
	if input.Cancelled {
		return nil
	}
	rows, _, _, err := svc.ListStructuredMessages(tenantID, "ask_user", 0, 200)
	if err != nil {
		return err
	}
	var target *sqlite.StructuredMessage
	for i := range rows {
		if rows[i].CorrelationID == input.CorrelationID {
			target = &rows[i]
			break
		}
	}
	if target == nil {
		// 不存在时交给 AnswerAskUser 返回 NOT_FOUND（保持单一判定点）。
		return nil
	}
	questions := askQuestions(target.StructuredJSON)
	byID := make(map[string]askQuestionSpec, len(questions))
	for _, q := range questions {
		byID[q.ID] = q
	}
	for _, ans := range input.Answers {
		q, ok := byID[ans.QuestionID]
		if !ok {
			return fmt.Errorf("invalid answer: unknown question_id %q", ans.QuestionID)
		}
		if !q.MultiSelect && len(ans.OptionIDs) > 1 {
			return fmt.Errorf("invalid answer: question %q is single-select", ans.QuestionID)
		}
		if ans.Text != "" && !q.AllowOther {
			return fmt.Errorf("invalid answer: question %q does not allow custom text", ans.QuestionID)
		}
	}
	return nil
}

type askQuestionSpec struct {
	ID          string
	MultiSelect bool
	AllowOther  bool
	Required    bool
}

func askQuestions(structuredJSON string) []askQuestionSpec {
	var env struct {
		Payload struct {
			Questions []struct {
				ID          string `json:"id"`
				MultiSelect bool   `json:"multi_select"`
				AllowOther  bool   `json:"allow_other"`
				Required    bool   `json:"required"`
			} `json:"questions"`
		} `json:"payload"`
	}
	if json.Unmarshal([]byte(structuredJSON), &env) != nil {
		return nil
	}
	out := make([]askQuestionSpec, 0, len(env.Payload.Questions))
	for i, q := range env.Payload.Questions {
		id := q.ID
		if id == "" {
			id = fmt.Sprintf("q%d", i+1)
		}
		out = append(out, askQuestionSpec{ID: id, MultiSelect: q.MultiSelect, AllowOther: q.AllowOther, Required: q.Required})
	}
	return out
}

func answerText(input protocol.StructuredAnswerInput) string {
	if input.Cancelled {
		return "已取消"
	}
	var parts []string
	for _, a := range input.Answers {
		piece := a.QuestionID + ":"
		if len(a.OptionIDs) > 0 {
			piece += strings.Join(a.OptionIDs, ",")
		}
		if a.Text != "" {
			if len(a.OptionIDs) > 0 {
				piece += " "
			}
			piece += a.Text
		}
		parts = append(parts, piece)
	}
	return strings.Join(parts, "; ")
}
