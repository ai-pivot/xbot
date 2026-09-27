package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"xbot/storage/sqlite"
)

// fakeBlackboardHub records what the tool published/subscribed, so the tool's
// contract with the runtime (event per accepted change, watches per session)
// is testable without the agent.
type fakeBlackboardHub struct {
	mu      sync.Mutex
	events  []BlackboardEvent
	watches map[string][]BlackboardWatch
}

func newFakeBlackboardHub() *fakeBlackboardHub {
	return &fakeBlackboardHub{watches: map[string][]BlackboardWatch{}}
}

func (h *fakeBlackboardHub) PublishBlackboardChange(ev BlackboardEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
}

func (h *fakeBlackboardHub) WatchBoard(sessionKey, board, prefix string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, w := range h.watches[sessionKey] {
		if w.Board == board {
			return // idempotent
		}
	}
	h.watches[sessionKey] = append(h.watches[sessionKey], BlackboardWatch{Board: board, Prefix: prefix})
}

func (h *fakeBlackboardHub) UnwatchBoard(sessionKey, board string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	kept := h.watches[sessionKey][:0]
	found := false
	for _, w := range h.watches[sessionKey] {
		if w.Board == board {
			found = true
			continue
		}
		kept = append(kept, w)
	}
	h.watches[sessionKey] = kept
	return found
}

func (h *fakeBlackboardHub) ListWatches(sessionKey string) []BlackboardWatch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]BlackboardWatch(nil), h.watches[sessionKey]...)
}

func (h *fakeBlackboardHub) ops() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.events))
	for _, e := range h.events {
		out = append(out, e.Op+":"+e.Key)
	}
	return out
}

func (h *fakeBlackboardHub) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = nil
}

// newBlackboardTool builds a tool over a throwaway DB plus its hub.
func newBlackboardTool(t *testing.T) (*BlackboardTool, *fakeBlackboardHub) {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/bb.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	hub := newFakeBlackboardHub()
	return &BlackboardTool{Board: sqlite.NewBlackboardService(db), Hub: hub}, hub
}

// bbCtx builds a ToolContext. rootKey is the canonical session (shared by a main
// agent and all of its SubAgents); sessionKey may be overridden by a physical
// channel (the web-browsing-a-CLI-session case).
func bbCtx(agentID, sessionKey, rootKey string) *ToolContext {
	return &ToolContext{Ctx: context.Background(), AgentID: agentID, Channel: "cli", ChatID: "s1",
		SessionKey: sessionKey, RootSessionKey: rootKey}
}

func runBB(t *testing.T, tool *BlackboardTool, ctx *ToolContext, args map[string]any) *ToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := tool.Execute(ctx, string(raw))
	if err != nil {
		t.Fatalf("Blackboard(%v) returned a Go error: %v", args["action"], err)
	}
	return res
}

func bbArgs(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// TestBlackboardTool_MainAndSubAgentShareOneBoard is the flagship invariant: a
// SubAgent's ToolContext carries its own SessionKey but the parent's
// RootSessionKey, and the default board follows the ROOT — so the work a
// SubAgent posts is visible to its main agent and to sibling SubAgents with no
// ceremony.
func TestBlackboardTool_MainAndSubAgentShareOneBoard(t *testing.T) {
	tool, _ := newBlackboardTool(t)
	main := bbCtx("main", "cli:s1", "cli:s1")
	sub := bbCtx("main/explore", "main/explore", "cli:s1")
	sibling := bbCtx("main/reviewer", "main/reviewer", "cli:s1")

	if res := runBB(t, tool, sub, bbArgs("action", "post", "key", "finding-1", "kind", "finding", "title", "发现一个 bug", "body", "详情见 x.y:12")); res.IsError {
		t.Fatalf("subagent post failed: %s", res.Summary)
	}
	got := runBB(t, tool, main, bbArgs("action", "get", "key", "finding-1"))
	if !strings.Contains(got.Detail, "详情见 x.y:12") {
		t.Fatalf("main agent cannot read the SubAgent's entry: %+v", got)
	}
	list := runBB(t, tool, sibling, bbArgs("action", "list"))
	if !strings.Contains(list.Detail, "finding-1") {
		t.Fatalf("sibling SubAgent cannot see the entry: %s", list.Detail)
	}
}

// TestBlackboardTool_PhysicalChannelOverrideKeepsOneBoard: a web client browsing
// a CLI session must not split the board in two (SessionKey says web:*, the
// canonical root says cli:*).
func TestBlackboardTool_PhysicalChannelOverrideKeepsOneBoard(t *testing.T) {
	tool, _ := newBlackboardTool(t)
	canonical := bbCtx("main", "cli:s1", "cli:s1")
	overridden := bbCtx("main", "web:s1", "cli:s1")

	runBB(t, tool, canonical, bbArgs("action", "post", "key", "k", "title", "t"))
	if !strings.Contains(runBB(t, tool, overridden, bbArgs("action", "list")).Detail, "k") {
		t.Fatal("physical-channel override split the board — the default board must follow the canonical root session")
	}
}

// TestBlackboardTool_CrossSessionIsolation: different root sessions never see
// each other's work unless they join a named board on purpose.
func TestBlackboardTool_CrossSessionIsolation(t *testing.T) {
	tool, _ := newBlackboardTool(t)
	a := bbCtx("main", "cli:a", "cli:a")
	b := bbCtx("main", "cli:b", "cli:b")

	runBB(t, tool, a, bbArgs("action", "post", "key", "secret", "title", "A 的私事"))
	if list := runBB(t, tool, b, bbArgs("action", "list")); strings.Contains(list.Detail, "secret") {
		t.Fatal("session B saw session A's board — boards must be isolated by default")
	}

	// Explicit shared board: both sessions name it, both see it.
	runBB(t, tool, a, bbArgs("action", "post", "board", "dev-team", "key", "plan", "title", "联合作战"))
	if list := runBB(t, tool, b, bbArgs("action", "list", "board", "dev-team")); !strings.Contains(list.Detail, "plan") {
		t.Fatal("a named board must be shared across sessions")
	}
}

// TestBlackboardTool_Lifecycle drives the whole collaboration protocol through
// the tool: plan → claim → progress → dependency unblock → close.
func TestBlackboardTool_Lifecycle(t *testing.T) {
	tool, hub := newBlackboardTool(t)
	ctx := bbCtx("main", "cli:s1", "cli:s1")

	runBB(t, tool, ctx, bbArgs("action", "post", "key", "design", "kind", "task", "title", "设计 API", "body", "草案"))
	runBB(t, tool, ctx, bbArgs("action", "post", "key", "impl", "kind", "task", "title", "实现 API", "blocked_by", []string{"design"}))

	// The dependent entry is blocked: not ready, and claiming it is refused.
	list := runBB(t, tool, ctx, bbArgs("action", "list"))
	if !strings.Contains(list.Detail, "blocked by design") {
		t.Fatalf("list must show the blocking dependency: %s", list.Detail)
	}
	// Claiming blocked work is refused (readiness is a gate): the agent must
	// finish the dependency first, and closing it unblocks this entry.
	if res := runBB(t, tool, ctx, bbArgs("action", "claim", "key", "impl")); !res.IsError {
		t.Fatal("claiming a dependency-blocked entry must be refused")
	} else if !strings.Contains(res.Summary, "blocked") {
		t.Fatalf("claim-on-blocked message = %q, want it to explain the dependency", res.Summary)
	}

	// list must not leak bodies (context discipline).
	if strings.Contains(list.Detail, "草案") {
		t.Fatal("list must not include bodies — they cost context; get is the explicit way to read one")
	}

	// Claim, extend with the token, then close the dependency to unblock.
	claimed := runBB(t, tool, ctx, bbArgs("action", "claim", "key", "design", "ttl_seconds", 120))
	token := claimTokenOf(t, claimed)
	if res := runBB(t, tool, ctx, bbArgs("action", "claim", "key", "design", "claim_token", token)); res.IsError {
		t.Fatalf("extending with the lease token must succeed: %s", res.Summary)
	}
	designRev := revisionOf(t, runBB(t, tool, ctx, bbArgs("action", "get", "key", "design")))
	runBB(t, tool, ctx, bbArgs("action", "close", "key", "design", "expected_revision", designRev))

	unblocked := runBB(t, tool, ctx, bbArgs("action", "get", "key", "impl"))
	if !strings.Contains(unblocked.Summary, "ready") {
		t.Fatalf("closing the dependency must make the dependent ready: %s", unblocked.Summary)
	}
	impl := runBB(t, tool, ctx, bbArgs("action", "claim", "key", "impl"))
	if impl.IsError {
		t.Fatalf("claim after unblock failed: %s", impl.Summary)
	}
	implRev := revisionOf(t, impl)

	// Partial update: only the body changes, the title survives.
	updated := runBB(t, tool, ctx, bbArgs("action", "update", "key", "impl", "body", "进度 60%", "expected_revision", implRev))
	if updated.IsError {
		t.Fatalf("update failed: %s", updated.Summary)
	}
	after := runBB(t, tool, ctx, bbArgs("action", "get", "key", "impl"))
	if !strings.Contains(after.Summary+after.Detail, "进度 60%") || !strings.Contains(after.Summary+after.Detail, "实现 API") {
		t.Fatalf("partial update must keep untouched fields: %s / %s", after.Summary, after.Detail)
	}

	// Stale CAS is refused with the live entry (never a silent overwrite).
	stale := runBB(t, tool, ctx, bbArgs("action", "update", "key", "impl", "body", "越权覆盖", "expected_revision", implRev))
	if !stale.IsError || !strings.Contains(stale.Summary, "冲突") {
		t.Fatalf("stale update must be refused as a conflict: %+v", stale)
	}
	if strings.Contains(runBB(t, tool, ctx, bbArgs("action", "get", "key", "impl")).Detail, "越权覆盖") {
		t.Fatal("a conflicting update overwrote the entry")
	}

	// Ops published for the UI/watchers, and no event for read-only actions.
	ops := strings.Join(hub.ops(), ",")
	for _, want := range []string{"post:design", "post:impl", "claim:design", "close:design", "claim:impl", "update:impl"} {
		if !strings.Contains(ops, want) {
			t.Errorf("hub events %q missing %q", ops, want)
		}
	}
	hub.reset()
	runBB(t, tool, ctx, bbArgs("action", "list"))
	runBB(t, tool, ctx, bbArgs("action", "get", "key", "design"))
	if len(hub.ops()) != 0 {
		t.Errorf("read-only actions published events: %v", hub.ops())
	}
	// Releasing a free entry is a no-op: it must not publish (no false wake-ups).
	runBB(t, tool, ctx, bbArgs("action", "release", "key", "design"))
	if len(hub.ops()) != 0 {
		t.Errorf("releasing an already-free entry published %v, want nothing", hub.ops())
	}
}

// TestBlackboardTool_ClaimExclusivityAcrossIdenticalRoles: two SubAgent
// instances of the SAME role share one session key, so identity cannot carry the
// lease — the token does.
func TestBlackboardTool_ClaimExclusivityAcrossIdenticalRoles(t *testing.T) {
	tool, _ := newBlackboardTool(t)
	inst1 := bbCtx("main/explore", "main/explore", "cli:s1")
	inst2 := bbCtx("main/explore", "main/explore", "cli:s1")

	runBB(t, tool, inst1, bbArgs("action", "post", "key", "job", "title", "抢我"))
	first := runBB(t, tool, inst1, bbArgs("action", "claim", "key", "job"))
	if first.IsError {
		t.Fatalf("first claim failed: %s", first.Summary)
	}
	second := runBB(t, tool, inst2, bbArgs("action", "claim", "key", "job"))
	if !second.IsError {
		t.Fatal("a second instance with the same role label must NOT be able to take the lease")
	}
	if !strings.Contains(second.Summary, "冲突") {
		t.Fatalf("conflict summary = %q", second.Summary)
	}
}

// TestBlackboardTool_ErrorsAreActionable: every misuse fails with an explanation
// and a next step — never a Go error (which would abort the tool batch).
func TestBlackboardTool_ErrorsAreActionable(t *testing.T) {
	tool, _ := newBlackboardTool(t)
	ctx := bbCtx("main", "cli:s1", "cli:s1")

	cases := []struct {
		name    string
		args    map[string]any
		wantSub string
	}{
		{"missing key", bbArgs("action", "get"), "key is required"},
		{"unknown action", bbArgs("action", "nope"), "unknown action"},
		{"update without CAS", bbArgs("action", "post", "key", "k", "title", "t"), ""},
	}
	for _, tc := range cases {
		if tc.name == "update without CAS" {
			tool.Execute(ctx, `{"action":"post","key":"k","title":"t"}`) //nolint:errcheck
			// A missing CAS guard is a PARAMETER error (the caller must read the
			// entry first), so it is a Go error, not a tool-result failure.
			if _, err := tool.Execute(ctx, `{"action":"update","key":"k","body":"x"}`); err == nil {
				t.Fatal("update without expected_revision must be refused")
			} else if !strings.Contains(err.Error(), "expected_revision") {
				t.Fatalf("error = %v, want it to mention expected_revision", err)
			}
			continue
		}
		if _, err := tool.Execute(ctx, mustJSON(t, tc.args)); err == nil {
			t.Errorf("%s: expected a Go error (bad parameters), got nil", tc.name)
		} else if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: error = %v, want it to mention %q", tc.name, err, tc.wantSub)
		}
	}

	// A duplicate key is a conflict result (not a Go error) that carries the
	// live entry, so the model can decide between update and a new key.
	runBB(t, tool, ctx, bbArgs("action", "post", "key", "dup", "title", "原始"))
	res := runBB(t, tool, ctx, bbArgs("action", "post", "key", "dup", "title", "覆盖"))
	if !res.IsError || !strings.Contains(res.Summary, "原始") {
		t.Fatalf("duplicate post must report the live entry: %+v", res)
	}
	if !strings.Contains(res.Tips, "update") {
		t.Errorf("duplicate-post tip should point at update: %q", res.Tips)
	}
}

// TestBlackboardTool_WatchIsPerSessionAndScoped: watch is an explicit,
// per-session subscription (no implicit notifications), and it is scoped to the
// routable root session so a SubAgent's watch wakes the conversation it belongs to.
func TestBlackboardTool_WatchIsPerSessionAndScoped(t *testing.T) {
	tool, hub := newBlackboardTool(t)
	main := bbCtx("main", "cli:s1", "cli:s1")
	sub := bbCtx("main/explore", "main/explore", "cli:s1")

	res := runBB(t, tool, sub, bbArgs("action", "watch", "prefix", "api-"))
	if res.IsError {
		t.Fatalf("watch failed: %s", res.Summary)
	}
	if got := hub.ListWatches("cli:s1"); len(got) != 1 || got[0].Prefix != "api-" {
		t.Fatalf("watch registered on %v, want the root session cli:s1 with the prefix filter", hub.watches)
	}
	// Idempotent: a second watch does not duplicate.
	runBB(t, tool, sub, bbArgs("action", "watch"))
	if got := hub.ListWatches("cli:s1"); len(got) != 1 {
		t.Fatalf("watch must be idempotent, got %v", got)
	}
	runBB(t, tool, main, bbArgs("action", "unwatch"))
	if got := hub.ListWatches("cli:s1"); len(got) != 0 {
		t.Fatalf("unwatch must clear the subscription, got %v", got)
	}
	if res := runBB(t, tool, main, bbArgs("action", "unwatch")); !strings.Contains(res.Summary, "本来就没有") {
		t.Fatalf("unwatching an unknown subscription must say so: %s", res.Summary)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// claimTokenOf digs the lease handle out of the claim result's tips (the tool
// surfaces it there so the model can copy it).
func claimTokenOf(t *testing.T, res *ToolResult) string {
	t.Helper()
	const marker = "claim_token="
	i := strings.Index(res.Tips, marker)
	if i < 0 {
		t.Fatalf("claim result carries no claim_token: %+v", res)
	}
	rest := res.Tips[i+len(marker):]
	if j := strings.IndexAny(rest, " \n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// revisionOf reads "revision N" out of a rendered entry.
func revisionOf(t *testing.T, res *ToolResult) float64 {
	t.Helper()
	const marker = "revision "
	i := strings.Index(res.Summary+res.Detail, marker)
	if i < 0 {
		t.Fatalf("no revision in result: %+v", res)
	}
	rest := (res.Summary + res.Detail)[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	var n float64
	if end == 0 {
		t.Fatalf("malformed revision in %q", rest)
	}
	for _, c := range rest[:end] {
		n = n*10 + float64(c-'0')
	}
	return n
}
