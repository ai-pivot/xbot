package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// BlackboardEvent is one accepted change on a board. It is the single signal
// both consumers need: the live UI (web clients redraw the board) and the
// watcher notification path (agents that asked to be told get a digest).
type BlackboardEvent struct {
	Board     string `json:"board"`
	Key       string `json:"key"`
	Op        string `json:"op"`
	Revision  int64  `json:"revision"`
	Actor     string `json:"actor"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Closed    bool   `json:"closed"`
	ClaimedBy string `json:"claimed_by,omitempty"`
	At        int64  `json:"at"`
}

// BlackboardWatch is a session's subscription to one board.
type BlackboardWatch struct {
	Board  string `json:"board"`
	Prefix string `json:"prefix,omitempty"`
}

// BlackboardHub carries board changes outward. It is an interface (not a
// concrete type) so the tools package stays independent of the agent runtime
// that owns sessions, notifications and web clients — the same seam
// OffloadRecallStore/BgTaskManager use.
//
// Implementations MUST NOT block the writer: publish is fire-and-forget, and
// delivery is coalesced by the implementation (a busy board must not spawn a
// turn per change).
type BlackboardHub interface {
	// PublishBlackboardChange reports an accepted change. The implementation
	// decides who hears about it: every web client (live UI) and the sessions
	// that watch this board (via the background notification pipeline).
	PublishBlackboardChange(ev BlackboardEvent)
	// WatchBoard subscribes a routable session key to a board. prefix (optional)
	// narrows delivery to keys starting with it. Idempotent.
	WatchBoard(sessionKey, board, prefix string)
	// UnwatchBoard removes the subscription; reports whether one existed.
	UnwatchBoard(sessionKey, board string) bool
	// ListWatches returns the session's current subscriptions.
	ListWatches(sessionKey string) []BlackboardWatch
}

// BlackboardTool is the agent-facing surface of the shared blackboard: the
// workspace that lets a main agent, its SubAgents and independent peer sessions
// cooperate on one plan without stepping on each other.
//
// The tool is deliberately thin — validation and atomicity live in
// sqlite.BlackboardService, delivery lives in the hub — and it owns exactly two
// things: (a) resolving which board and which identity the calling agent means
// (the default board is the ROOT session, so a SubAgent shares it with its
// main agent automatically) and (b) rendering results an LLM can act on.
type BlackboardTool struct {
	Board *sqlite.BlackboardService
	Hub   BlackboardHub
}

func (t *BlackboardTool) Name() string { return "Blackboard" }

func (t *BlackboardTool) Description() string {
	return `共享黑板：与其它 agent（主 agent、子代理、其它会话）共享的工作面。默认黑板 = 你所在会话的根会话，因此主 agent 与它的全部 SubAgent 自动共享同一块黑板 —— 用来分工、认领、汇报进展与结论。

## 为什么用它
并行 agent 的三大失败模式，黑板都有结构性答案：
1. 重复劳动 → claim 原子认领（带租约，过期自动可被接管；持有者崩溃不留死锁）
2. 互相覆盖 → 每次写入都带 revision（compare-and-swap），落后者收到冲突而不是覆盖
3. 顺序错乱 → blocked_by 依赖边：依赖未 closed 的条目 blocked=true 且 ready=false

## Actions
- post   新建条目。key 板内唯一（重复会报冲突并返回现有条目）。title 必填。
- get    读一条（含 body）。
- list   列条目（默认不含 body、不含已 closed；支持 prefix / include_closed / limit）。
- update 改内容（只改你传入的字段）。必须带 expected_revision（CAS）。
- claim  原子认领：空闲或租约过期时成功，返回 claim_token。续租传同一个 claim_token。默认租约 10 分钟。
- release 释放认领。带 claim_token 只释放自己那条租约；不带 = 强制释放（帮卡住的同伴解锁）。已空闲时是幂等空操作。
- close / reopen  置/清 closed（依赖门控的唯一开关）。close 会同时释放租约。
- delete 删除条目（带 expected_revision）。
- watch / unwatch  订阅本会话对某板的变更：别人改动该板时你会收到通知（忙则注入当前迭代，闲则开启新一轮）；不订阅就不会被打扰。

## key / title / body 约定（host 不解释这些字段）
- kind：内容类型，由你命名，惯例 task / finding / decision / note。
- status：自由标签，惯例 open / working / done；它是给人看的标签，不参与依赖判定。
- body：不透明载荷（markdown/JSON 均可，≤64KB）。列表不会带出 body —— 需要正文就用 get。

## Examples
- 把一个计划拆成可并行认领的条目：
  Blackboard(action="post", key="api-design", kind="task", title="设计 /v2 API", body="...")
  Blackboard(action="post", key="api-impl", kind="task", title="实现 /v2 API", blocked_by=["api-design"])
- 找活干（只挑 ready 的）：
  Blackboard(action="list", prefix="api-")
- 认领并汇报：
  Blackboard(action="claim", key="api-impl")   → 拿到 claim_token
  Blackboard(action="update", key="api-impl", expected_revision=<上一步返回的 revision>, body="进度 60%")
  Blackboard(action="close", key="api-impl", expected_revision=<最新 revision>)
- 让别人知道你改了：Blackboard(action="watch")（默认黑板的全部变更都会通知你）`
}

// blackboardArgs is the tool's parameter object. Content fields are pointers so
// that "absent" and "empty" stay distinguishable on update (a partial update
// must not wipe fields the caller never mentioned).
type blackboardArgs struct {
	Action           string    `json:"action"`
	Board            string    `json:"board,omitempty"`
	Key              string    `json:"key,omitempty"`
	Kind             string    `json:"kind,omitempty"`
	Title            *string   `json:"title,omitempty"`
	Body             *string   `json:"body,omitempty"`
	Status           *string   `json:"status,omitempty"`
	BlockedBy        *[]string `json:"blocked_by,omitempty"`
	ExpectedRevision int64     `json:"expected_revision,omitempty"`
	ClaimToken       string    `json:"claim_token,omitempty"`
	TTLSeconds       int       `json:"ttl_seconds,omitempty"`
	Prefix           string    `json:"prefix,omitempty"`
	IncludeClosed    bool      `json:"include_closed,omitempty"`
	Limit            int       `json:"limit,omitempty"`
}

func (t *BlackboardTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Required: true, Description: "post | get | list | update | claim | release | close | reopen | delete | watch | unwatch"},
		{Name: "board", Type: "string", Description: "Shared board name (letters/digits/._-, ≤64) or an explicit session key (channel:chatID) to join another session's board. Omit = your own session's board."},
		{Name: "key", Type: "string", Description: "Entry key, unique within the board (letters/digits/._:-). Required for every action except list/watch/unwatch."},
		{Name: "kind", Type: "string", Description: "Producer-declared content type, ≤32 chars (e.g. task / finding / decision / note)."},
		{Name: "title", Type: "string", Description: "One-line summary (required on post, must stay non-empty on update)."},
		{Name: "body", Type: "string", Description: "Opaque payload ≤64KB (markdown or JSON). Never returned by list — read it with get."},
		{Name: "status", Type: "string", Description: "Free-form label ≤32 chars (convention: open / working / done). Informational only — dependencies gate on close, not status."},
		{Name: "blocked_by", Type: "array", Items: &llm.ToolParamItems{Type: "string"}, Description: "Dependency keys. The entry is blocked (ready=false) until every listed key is closed."},
		{Name: "expected_revision", Type: "integer", Description: "Compare-and-swap guard for update/close/reopen/delete: the revision you last read. A stale value is rejected with the current entry."},
		{Name: "claim_token", Type: "string", Description: "Lease handle returned by claim. Pass it to extend (claim) or to release exactly your own lease."},
		{Name: "ttl_seconds", Type: "integer", Description: "Lease length for claim, 30…86400 (default 600). A crashed holder's lease simply expires."},
		{Name: "prefix", Type: "string", Description: "list: only keys starting with this. watch: only notify for such keys."},
		{Name: "include_closed", Type: "boolean", Description: "list: include closed entries (default false)."},
		{Name: "limit", Type: "integer", Description: "list: max entries to return (default 50, max 200)."},
	}
}

func (t *BlackboardTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	if t.Board == nil {
		return nil, fmt.Errorf("blackboard storage is not available")
	}
	var a blackboardArgs
	if err := json.Unmarshal([]byte(input), &a); err != nil {
		return nil, fmt.Errorf("parse Blackboard arguments: %w", err)
	}
	board, err := blackboardBoardKey(ctx, a.Board)
	if err != nil {
		return nil, err
	}
	actor := blackboardActor(ctx)

	switch strings.ToLower(strings.TrimSpace(a.Action)) {
	case "post":
		return t.post(board, actor, &a)
	case "get":
		return t.get(board, &a)
	case "list":
		return t.list(board, &a)
	case "update":
		return t.update(board, actor, &a)
	case "claim":
		return t.claim(board, actor, &a)
	case "release":
		return t.release(board, actor, &a)
	case "close", "reopen":
		return t.setClosed(board, actor, &a)
	case "delete":
		return t.delete(board, actor, &a)
	case "watch":
		return t.watch(ctx, board, actor, &a)
	case "unwatch":
		return t.unwatch(ctx, board, &a)
	case "":
		return nil, fmt.Errorf("blackboard: action is required (post | get | list | update | claim | release | close | reopen | delete | watch | unwatch)")
	default:
		return nil, fmt.Errorf("blackboard: unknown action %q", a.Action)
	}
}

// blackboardBoardKey resolves the board for this call: an explicit board wins;
// otherwise the ROOT session key (so a SubAgent lands on its main agent's board
// — and a web client browsing a CLI session lands on the CLI session's board,
// not a second board keyed by the overridden physical channel).
func blackboardBoardKey(ctx *ToolContext, explicit string) (string, error) {
	if e := strings.TrimSpace(explicit); e != "" {
		if strings.HasPrefix(e, "@") {
			return e, nil
		}
		// A bare name is a named board (host-owned namespace); anything with a
		// ':' is treated as an explicit session key to join.
		if strings.Contains(e, ":") {
			return e, nil
		}
		return "@" + e, nil
	}
	if ctx.RootSessionKey != "" {
		return ctx.RootSessionKey, nil
	}
	if ctx.SessionKey != "" {
		return ctx.SessionKey, nil
	}
	if ctx.Channel != "" && ctx.ChatID != "" {
		return ctx.Channel + ":" + ctx.ChatID, nil
	}
	return "", fmt.Errorf("blackboard: cannot determine a board (no session identity in this context)")
}

// blackboardActor names the writer/holder for audit and the UI. A SubAgent is
// named by its own session key ("main/reviewer"); everything else uses the
// canonical session key. It is a LABEL only: lease exclusivity rides on the
// claim token, never on this string.
func blackboardActor(ctx *ToolContext) string {
	if strings.Contains(ctx.AgentID, "/") && ctx.SessionKey != "" {
		return ctx.SessionKey
	}
	if ctx.RootSessionKey != "" {
		return ctx.RootSessionKey
	}
	if ctx.SessionKey != "" {
		return ctx.SessionKey
	}
	return ctx.Channel + ":" + ctx.ChatID
}

func (t *BlackboardTool) post(board, actor string, a *blackboardArgs) (*ToolResult, error) {
	title := ""
	if a.Title != nil {
		title = *a.Title
	}
	body, status := "", ""
	if a.Body != nil {
		body = *a.Body
	}
	if a.Status != nil {
		status = *a.Status
	}
	var blockedBy []string
	if a.BlockedBy != nil {
		blockedBy = *a.BlockedBy
	}
	entry, err := t.Board.Post(&sqlite.BlackboardEntry{
		Board: board, Key: a.Key, Kind: a.Kind, Title: title, Body: body,
		Status: status, BlockedBy: blockedBy, CreatedBy: actor,
	})
	if err != nil {
		return blackboardFailure(err)
	}
	t.publish("post", entry, actor)
	return NewResultWithTips(
		fmt.Sprintf("已在黑板 %s 新建 %s\n\n%s", board, a.Key, renderBlackboardEntry(entry)),
		"用 action=\"list\" 看整块板；需要正文用 action=\"get\"。若要别人据此开工，先确认依赖（blocked_by）指向的条目存在。",
	), nil
}

func (t *BlackboardTool) get(board string, a *blackboardArgs) (*ToolResult, error) {
	if a.Key == "" {
		return nil, fmt.Errorf("blackboard: key is required for get")
	}
	entry, err := t.Board.Get(board, a.Key)
	if err != nil {
		return blackboardFailure(err)
	}
	return NewResultWithDetail(renderBlackboardEntry(entry), entry.Body), nil
}

func (t *BlackboardTool) list(board string, a *blackboardArgs) (*ToolResult, error) {
	entries, err := t.Board.List(sqlite.BlackboardListOptions{
		Board: board, Prefix: a.Prefix, IncludeClosed: a.IncludeClosed, Limit: a.Limit,
	})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return NewResultWithTips(
			fmt.Sprintf("黑板 %s 没有匹配的条目", board),
			"用 action=\"post\" 创建条目（title 必填），或去掉 prefix/include_closed 重试。",
		), nil
	}
	var ready, blocked, claimed int
	var b strings.Builder
	fmt.Fprintf(&b, "黑板 %s（%d 条）\n", board, len(entries))
	now := time.Now().UnixMilli()
	for i := range entries {
		e := &entries[i]
		switch {
		case e.Closed:
		case e.Blocked:
			blocked++
		case e.ClaimedBy != "":
			claimed++
		default:
			ready++
		}
		b.WriteString(renderBlackboardRow(e, now))
	}
	tips := "claim 一个 ready 的条目来接手（返回 claim_token，续租/释放都靠它）。"
	if ready == 0 && blocked > 0 {
		tips = "当前没有 ready 的条目：先用 get 看被依赖的条目，把依赖 close 掉就会自动解锁。"
	}
	summary := fmt.Sprintf("黑板 %s：%d 条（ready %d / 认领中 %d / 阻塞 %d）", board, len(entries), ready, claimed, blocked)
	return NewResultWithDetail(summary, b.String()).WithTips(tips), nil
}

func (t *BlackboardTool) update(board, actor string, a *blackboardArgs) (*ToolResult, error) {
	if a.Key == "" {
		return nil, fmt.Errorf("blackboard: key is required for update")
	}
	if a.ExpectedRevision <= 0 {
		return nil, fmt.Errorf("blackboard: update requires expected_revision (read the entry first — it is the CAS guard against overwriting a teammate)")
	}
	cur, err := t.Board.Get(board, a.Key)
	if err != nil {
		return blackboardFailure(err)
	}
	// Partial update: absent fields keep their current value.
	kind, title, body, status := cur.Kind, cur.Title, cur.Body, cur.Status
	blockedBy := cur.BlockedBy
	if a.Kind != "" {
		kind = a.Kind
	}
	if a.Title != nil {
		title = *a.Title
	}
	if a.Body != nil {
		body = *a.Body
	}
	if a.Status != nil {
		status = *a.Status
	}
	if a.BlockedBy != nil {
		blockedBy = *a.BlockedBy
	}
	entry, err := t.Board.Update(board, a.Key, kind, title, body, status, blockedBy, a.ExpectedRevision)
	if err != nil {
		return blackboardFailure(err)
	}
	t.publish("update", entry, actor)
	return NewResultWithTips(
		fmt.Sprintf("已更新 %s\n\n%s", a.Key, renderBlackboardEntry(entry)),
		fmt.Sprintf("当前 revision = %d；下次写入（update/close/delete）请带 expected_revision=%d。", entry.Revision, entry.Revision),
	), nil
}

func (t *BlackboardTool) claim(board, actor string, a *blackboardArgs) (*ToolResult, error) {
	if a.Key == "" {
		return nil, fmt.Errorf("blackboard: key is required for claim")
	}
	ttl := sqlite.BlackboardClaimTTL
	if a.TTLSeconds > 0 {
		ttl = time.Duration(a.TTLSeconds) * time.Second
	}
	entry, err := t.Board.Claim(board, a.Key, actor, a.ClaimToken, ttl)
	if err != nil {
		return blackboardFailure(err)
	}
	// Claim is the change the whole board cares about (work just got taken), so
	// it must reach the UI and the watchers like any other write.
	t.publish("claim", entry, actor)
	verb := "已认领"
	if a.ClaimToken != "" {
		verb = "已续租"
	}
	return NewResultWithTips(
		fmt.Sprintf("%s %s（租约到 %s）\n\n%s", verb, a.Key, formatBlackboardTime(entry.ClaimExpiresAt), renderBlackboardEntry(entry)),
		fmt.Sprintf("claim_token=%s —— 续租（claim）或释放（release）时带上它。做完就 close（会自动释放租约），别让同伴一直等。", entry.ClaimToken),
	), nil
}

func (t *BlackboardTool) release(board, actor string, a *blackboardArgs) (*ToolResult, error) {
	if a.Key == "" {
		return nil, fmt.Errorf("blackboard: key is required for release")
	}
	entry, changed, err := t.Board.Release(board, a.Key, a.ClaimToken)
	if err != nil {
		return blackboardFailure(err)
	}
	if !changed {
		return NewResultWithTips(
			fmt.Sprintf("%s 本来就是空闲的（无需释放）\n\n%s", a.Key, renderBlackboardEntry(entry)),
			"空闲条目可以直接 claim 接手。",
		), nil
	}
	t.publish("release", entry, actor)
	return NewResultWithTips(
		fmt.Sprintf("已释放 %s\n\n%s", a.Key, renderBlackboardEntry(entry)),
		"条目回到 ready，其它 agent 可以接手。",
	), nil
}

func (t *BlackboardTool) setClosed(board, actor string, a *blackboardArgs) (*ToolResult, error) {
	if a.Key == "" {
		return nil, fmt.Errorf("blackboard: key is required for %s", a.Action)
	}
	if a.ExpectedRevision <= 0 {
		return nil, fmt.Errorf("blackboard: %s requires expected_revision (read the entry first)", a.Action)
	}
	closed := strings.EqualFold(a.Action, "close")
	entry, err := t.Board.SetClosed(board, a.Key, closed, a.ExpectedRevision)
	if err != nil {
		return blackboardFailure(err)
	}
	op := "close"
	if !closed {
		op = "reopen"
	}
	t.publish(op, entry, actor)
	what := "已关闭"
	tips := fmt.Sprintf("依赖它的条目现在会解锁。当前 revision = %d。", entry.Revision)
	if !closed {
		what = "已重新打开"
		tips = fmt.Sprintf("该条目重新参与依赖门控。当前 revision = %d。", entry.Revision)
	}
	return NewResultWithTips(
		fmt.Sprintf("%s %s\n\n%s", what, a.Key, renderBlackboardEntry(entry)),
		tips,
	), nil
}

func (t *BlackboardTool) delete(board, actor string, a *blackboardArgs) (*ToolResult, error) {
	if a.Key == "" {
		return nil, fmt.Errorf("blackboard: key is required for delete")
	}
	entry, err := t.Board.Delete(board, a.Key, a.ExpectedRevision)
	if err != nil {
		return blackboardFailure(err)
	}
	t.publish("delete", entry, actor)
	return NewResult(fmt.Sprintf("已删除 %s（%s）", entry.Key, entry.Title)), nil
}

func (t *BlackboardTool) watch(ctx *ToolContext, board, actor string, a *blackboardArgs) (*ToolResult, error) {
	if t.Hub == nil {
		return nil, fmt.Errorf("blackboard: watch is not available in this runtime")
	}
	session := blackboardNotifySession(ctx)
	if session == "" {
		return nil, fmt.Errorf("blackboard: cannot determine a routable session for watch")
	}
	t.Hub.WatchBoard(session, board, a.Prefix)
	scope := "整块板"
	if a.Prefix != "" {
		scope = fmt.Sprintf("key 以 %q 开头", a.Prefix)
	}
	return NewResultWithTips(
		fmt.Sprintf("已订阅 %s 的变更（%s）", board, scope),
		"同伴的改动会作为通知送到你这里（忙时注入当前迭代，闲时开启新一轮）；不想被打扰就 unwatch。",
	), nil
}

func (t *BlackboardTool) unwatch(ctx *ToolContext, board string, a *blackboardArgs) (*ToolResult, error) {
	if t.Hub == nil {
		return nil, fmt.Errorf("blackboard: unwatch is not available in this runtime")
	}
	session := blackboardNotifySession(ctx)
	existed := t.Hub.UnwatchBoard(session, board)
	if !existed {
		return NewResult(fmt.Sprintf("你本来就没有订阅 %s", board)), nil
	}
	return NewResult(fmt.Sprintf("已退订 %s 的变更通知", board)), nil
}

// blackboardNotifySession resolves the session key that notifications should be
// delivered to. Only a REAL session (channel:chatID) can be woken into a turn,
// so a SubAgent's watch notifies its root conversation. The tool documents this
// as "the board of your session".
func blackboardNotifySession(ctx *ToolContext) string {
	if ctx.RootSessionKey != "" {
		return ctx.RootSessionKey
	}
	if ctx.SessionKey != "" {
		return ctx.SessionKey
	}
	if ctx.Channel != "" && ctx.ChatID != "" {
		return ctx.Channel + ":" + ctx.ChatID
	}
	return ""
}

// publish reports a change to the hub (nil hub = no live push, e.g. in tests).
func (t *BlackboardTool) publish(op string, e *sqlite.BlackboardEntry, actor string) {
	if t.Hub == nil || e == nil {
		return
	}
	t.Hub.PublishBlackboardChange(BlackboardEvent{
		Board: e.Board, Key: e.Key, Op: op, Revision: e.Revision, Actor: actor,
		Kind: e.Kind, Title: e.Title, Closed: e.Closed, ClaimedBy: e.ClaimedBy, At: time.Now().UnixMilli(),
	})
}

// blackboardFailure renders an operation failure as a tool result the model can
// act on: conflicts carry the authoritative entry, so the next step is always
// visible (retry with the fresh revision, wait for the lease, use another key).
func blackboardFailure(err error) (*ToolResult, error) {
	var conflict *sqlite.BlackboardConflictError
	if errors.As(err, &conflict) {
		msg := fmt.Sprintf("⛔ 冲突：%s", conflict.Reason)
		if conflict.Entry != nil {
			msg += "\n\n" + renderBlackboardEntry(conflict.Entry)
		}
		return NewErrorResult(msg).WithTips(blackboardConflictTip(conflict.Op)), nil
	}
	if errors.Is(err, sqlite.ErrBlackboardNotFound) {
		return NewErrorResult(fmt.Sprintf("⛔ %s", err.Error())).WithTips("用 action=\"list\" 看板上现有条目（key 可能拼错或已被删除）。"), nil
	}
	if errors.Is(err, sqlite.ErrBlackboardInvalid) {
		return NewErrorResult(fmt.Sprintf("⛔ 参数不合法：%s", err.Error())), nil
	}
	return nil, err
}

func blackboardConflictTip(op string) string {
	switch op {
	case "claim":
		return "别人持有有效租约：换一条 ready 的做，或等租约过期（list 里能看到剩余时间）。你自己那条要续租/释放就带上 claim_token。"
	case "release":
		return "你的 claim_token 已失效（租约被接管或已释放）：重新 claim 一次即可。"
	case "post":
		return "key 已被占用：用 action=\"get\" 读现有条目再决定是 update 它还是换一个 key（绝不覆盖同伴的工作）。"
	default:
		return "用最新 revision 重试（先 get 读一次），或先解决冲突再写。"
	}
}

// renderBlackboardEntry renders one entry as a markdown card.
func renderBlackboardEntry(e *sqlite.BlackboardEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** (%s) · `%s`\n", e.Title, boardKind(e), e.Key)
	fmt.Fprintf(&b, "- 状态: %s · revision %d", boardState(e), e.Revision)
	if e.ClaimedBy != "" {
		fmt.Fprintf(&b, " · 认领者 %s（剩 %s）", e.ClaimedBy, formatBlackboardDuration(e.ClaimExpiresAt-time.Now().UnixMilli()))
	}
	b.WriteString("\n")
	if len(e.BlockedBy) > 0 {
		fmt.Fprintf(&b, "- 依赖: %s%s\n", strings.Join(e.BlockedBy, ", "), boardBlockedNote(e))
	}
	if e.Body != "" {
		b.WriteString("\n" + e.Body + "\n")
	}
	return b.String()
}

// renderBlackboardRow renders one entry as a single list line: the whole point
// of a list is that an agent can see, at a glance, what it may pick up.
func renderBlackboardRow(e *sqlite.BlackboardEntry, now int64) string {
	marker := "○"
	switch {
	case e.Closed:
		marker = "✔"
	case e.Blocked:
		marker = "⛔"
	case e.ClaimedBy != "":
		marker = "◐"
	}
	line := fmt.Sprintf("  %s %-6s %-28s", marker, boardKind(e), e.Key)
	switch {
	case e.Closed:
		line += "closed"
	case e.Blocked:
		line += fmt.Sprintf("blocked by %s", strings.Join(unsatisfiedDeps(e), ", "))
	case e.ClaimedBy != "":
		line += fmt.Sprintf("claimed by %s (%s left)", e.ClaimedBy, formatBlackboardDuration(e.ClaimExpiresAt-now))
	default:
		line += "READY"
	}
	line += fmt.Sprintf(" · %s · rev %d", boardStatus(e), e.Revision)
	if e.Title != "" {
		line += " — " + e.Title
	}
	return line + "\n"
}

func boardKind(e *sqlite.BlackboardEntry) string {
	if e.Kind == "" {
		return "note"
	}
	return e.Kind
}

func boardStatus(e *sqlite.BlackboardEntry) string {
	if e.Status == "" {
		return "open"
	}
	return e.Status
}

func boardState(e *sqlite.BlackboardEntry) string {
	switch {
	case e.Closed:
		return "closed"
	case e.Blocked:
		return "blocked"
	case e.ClaimedBy != "":
		return "claimed by " + e.ClaimedBy
	default:
		return "ready"
	}
}

func boardBlockedNote(e *sqlite.BlackboardEntry) string {
	if e.Blocked {
		return "（未满足 → 该条目不可认领）"
	}
	return "（已满足）"
}

// unsatisfiedDeps lists the dependencies still open (best effort: the entry
// only carries its edges, so the caller sees the full list).
func unsatisfiedDeps(e *sqlite.BlackboardEntry) []string {
	if len(e.BlockedBy) == 0 {
		return []string{"?"}
	}
	return e.BlockedBy
}

func formatBlackboardTime(unixMs int64) string {
	if unixMs <= 0 {
		return "-"
	}
	return time.UnixMilli(unixMs).Format("15:04:05")
}

func formatBlackboardDuration(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	d := time.Duration(ms) * time.Millisecond
	if d >= time.Minute {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
