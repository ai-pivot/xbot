package agent

import (
	"testing"

	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

// newBindingTestFactory builds a DB-backed factory with subscription +
// tenant + settings services, plus helper shortcuts for the session-binding
// tests (drift / empty-model / balance-tier binding).
func newBindingTestFactory(t *testing.T) (*LLMFactory, *sqlite.LLMSubscriptionService, *sqlite.TenantService, *SettingsService) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	subSvc := sqlite.NewLLMSubscriptionService(db)
	tenantSvc := sqlite.NewTenantService(db)
	settingsSvc := NewSettingsService(sqlite.NewUserSettingsService(db))
	f := NewLLMFactory(&llm.MockLLM{}, "fallback-default-model")
	f.SetSubscriptionSvc(subSvc)
	f.SetTenantSvc(tenantSvc)
	f.SetSettingsService(settingsSvc)
	return f, subSvc, tenantSvc, settingsSvc
}

// addBindingSub registers a subscription with a per-model max_context and
// returns it.
func addBindingSub(t *testing.T, subSvc *sqlite.LLMSubscriptionService, id, model string, maxContext int) *sqlite.LLMSubscription {
	t.Helper()
	sub := &sqlite.LLMSubscription{
		ID: id, SenderID: "cli_user", Name: id, Provider: "openai",
		BaseURL: "https://api." + id + ".example/v1", APIKey: "sk-" + id, Model: model,
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add sub: %v", err)
	}
	if err := subSvc.UpsertModel(id, model, maxContext, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	return sub
}

// ── Reproduction 1: empty-model binding drifts (user report: "模型在用户没
// 切换的情况下会莫名其妙变化") ────────────────────────────────────────────
//
// tenants(subID, model="") + user_default_model pointing at model A: every
// ResolveLLM call falls through to the USER-LEVEL default (GetLLM), so the
// session follows user_default_model changes instead of its own binding —
// switching the user default in ANOTHER session changes this session's model.
// The fix: an empty-model binding must be REPAIRED (written back with a
// concrete model) instead of left in the drift state forever.
func TestResolveLLM_EmptyModelBindingDrifts(t *testing.T) {
	f, subSvc, tenantSvc, _ := newBindingTestFactory(t)
	subA := addBindingSub(t, subSvc, "sub-a", "model-a", 1_000_000)
	addBindingSub(t, subSvc, "sub-b", "model-b", 200_000)

	chatID := "/w/proj:Agent-drift"
	// The poisoned state: subscription bound, model EMPTY (e.g. written by
	// SetSessionLLM when user_default_model had no model yet — real-world rows
	// exist in tenants with model='').
	if err := tenantSvc.SetTenantSubscription("cli", chatID, subA.ID, ""); err != nil {
		t.Fatalf("SetTenantSubscription: %v", err)
	}
	// User-level default initially points at sub-a/model-a.
	if err := subSvc.SetUserDefaultModel("cli_user", subA.ID, "model-a"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}

	// First resolve: session has no model of its own → user default (model-a).
	_, m1, _, _, _ := f.ResolveLLM("cli_user", chatID, "cli")
	if m1 != "model-a" {
		t.Fatalf("first resolve model = %q, want model-a (user default)", m1)
	}

	// Another session switches the user default to model-b.
	if err := subSvc.SetUserDefaultModel("cli_user", "sub-b", "model-b"); err != nil {
		t.Fatalf("SetUserDefaultModel(b): %v", err)
	}

	// The session WITHOUT its own binding now resolves to model-b — drift.
	_, m2, _, _, _ := f.ResolveLLM("cli_user", chatID, "cli")
	if m2 == "model-b" && m1 == "model-a" {
		t.Fatalf("REPRODUCED: session model drifted %q → %q without any switch on this session", m1, m2)
	}
	// The repair contract: the first resolve must have REPAIRED the empty
	// binding so the session keeps a stable model of its own.
	gotSub, gotModel, _ := tenantSvc.GetTenantSubscription("cli", chatID)
	if gotSub == "" || gotModel == "" {
		t.Fatalf("binding after resolve: sub=%q model=%q — must be non-empty (session binding repaired)", gotSub, gotModel)
	}
	if gotModel != m2 || gotModel != m1 {
		t.Fatalf("binding after resolve = (%s,%q) but resolve returns %q — binding must be stable across user-default changes", gotSub, gotModel, m2)
	}
}

// ── Reproduction 2: empty-model binding → 200k compression vs 1M display
// (user report: "web前端显示模型上下文1M，但是200k就触发压缩") ────────────────
//
// Display chain (ResolveContextConfig) reads tenants (sub, model="") → falls
// back to the user default (model-a, 1M) — while the compression chain
// (ResolveLLM → applyUserContextLimits) returns maxContext from the SAME
// fallback. They must agree. The drift scenario (repro 1) breaks this: the
// display shows the tenants-pair model's context while the agent loop uses
// the drifted user-default model's context (0 → config 200k fallback).
func TestResolveLLM_EmptyModelBinding_MaxContextAgreesWithDisplay(t *testing.T) {
	f, subSvc, tenantSvc, _ := newBindingTestFactory(t)
	subA := addBindingSub(t, subSvc, "sub-a", "model-a", 1_000_000)
	addBindingSub(t, subSvc, "sub-b", "model-b", 0) // no per-model context → 0

	chatID := "/w/proj:Agent-maxctx"
	if err := tenantSvc.SetTenantSubscription("cli", chatID, subA.ID, ""); err != nil {
		t.Fatalf("SetTenantSubscription: %v", err)
	}
	// User default points at model-b (NO per-model max context → 0).
	if err := subSvc.SetUserDefaultModel("cli_user", "sub-b", "model-b"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}

	// Display chain: tenants (sub-a, "") → user default (sub-b/model-b) → 0 →
	// falls back to defaultMaxContext (the caller's config value).
	_, _, displayModel, displayMax := f.ResolveContextConfig("cli_user", chatID, "cli", 200000)
	if displayModel != "model-b" {
		t.Fatalf("display model = %q, want model-b (user default fallback)", displayModel)
	}

	// Compression chain: same session, same fallback → same (model, context).
	_, compModel, compMax, _, _ := f.ResolveLLM("cli_user", chatID, "cli")
	if compModel != displayModel {
		t.Fatalf("model split: compression uses %q but display shows %q", compModel, displayModel)
	}
	if compMax != displayMax && compMax != 0 {
		t.Fatalf("maxContext split: compression=%d display=%d (must agree when the model is the same)", compMax, displayMax)
	}
	// After the repair (first resolve binds a concrete model), the binding must
	// no longer be empty and both chains read the SAME pair.
	gotSub, gotModel, _ := tenantSvc.GetTenantSubscription("cli", chatID)
	if gotSub == "" || gotModel == "" {
		t.Fatalf("binding still empty after resolve: (%q, %q)", gotSub, gotModel)
	}
}

// ── Reproduction 3: user_default_model sender residue (multi-user era row
// under 'web-4' invisible to the collapsed operator 'cli_user') ───────────
//
// v63 collapsed every sender to the single operator, but user_default_model
// rows written by pre-v63 web senders survive under their original sender ids.
// GetUserDefaultModel('cli_user') then misses and the whole GetLLM fallback
// chain lands on the deployment defaultModel (config llm.model) — a model the
// user never chose — while the UI keeps showing the tenants-bound model.
func TestGetUserDefaultModel_SingleOperatorFallback(t *testing.T) {
	_, subSvc, _, _ := newBindingTestFactory(t)
	subA := addBindingSub(t, subSvc, "sub-a", "model-a", 0)

	// Legacy residue: the only default-model row is owned by a pre-v63 sender.
	if err := subSvc.SetUserDefaultModel("web-4", subA.ID, "model-a"); err != nil {
		t.Fatalf("SetUserDefaultModel(web-4): %v", err)
	}

	got, err := subSvc.GetUserDefaultModel("cli_user")
	if err != nil {
		t.Fatalf("GetUserDefaultModel(cli_user): %v", err)
	}
	if got == nil || got.Model != "model-a" {
		t.Fatalf("GetUserDefaultModel(cli_user) = %+v — the single operator must inherit the legacy row (web-4), got nil/miss", got)
	}
}

// ── Reproduction 4: EnsureSessionModelBinding must repair empty-model
// bindings, not skip them ("会话必须创建就绑定模型…任何时候禁止会话绑定的
// 模型为空") ────────────────────────────────────────────────────────────────
//
// ensureSessionModel treats a (subID!="" && model=="") binding as "already
// bound" and skips — the empty model survives forever and repro 1's drift
// replays on every resolve. It must treat an empty model as UNBOUND and
// rebind (balance tier → user default → subscription's own model).
func TestEnsureSessionModel_RepairsEmptyModelBinding(t *testing.T) {
	f, subSvc, tenantSvc, _ := newBindingTestFactory(t)
	subA := addBindingSub(t, subSvc, "sub-a", "model-a", 0)

	chatID := "/w/proj:Agent-repair"
	if err := tenantSvc.SetTenantSubscription("cli", chatID, subA.ID, ""); err != nil {
		t.Fatalf("SetTenantSubscription: %v", err)
	}

	// No balance tier configured; user default points at model-a.
	if err := subSvc.SetUserDefaultModel("cli_user", subA.ID, "model-a"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}

	f.EnsureSessionModelBinding("cli_user", chatID, "cli")

	gotSub, gotModel, _ := tenantSvc.GetTenantSubscription("cli", chatID)
	if gotSub != subA.ID || gotModel != "model-a" {
		t.Fatalf("after EnsureSessionModelBinding: (%q, %q) — empty-model binding must be repaired to (sub-a, model-a)", gotSub, gotModel)
	}
}

// ── Reproduction 5: binding must fall back beyond the balance tier
// ("没指定情况下优先 balance tier" — but NEVER stay empty when a concrete
// model is derivable) ────────────────────────────────────────────────────────
//
// With no balance tier configured, the binding must fall back to the user
// default model; with neither, to the subscription's own model; only when
// NOTHING is derivable may the session stay unbound (deployment has no
// subscriptions at all).
func TestEnsureSessionModel_FallbackBeyondBalanceTier(t *testing.T) {
	f, subSvc, tenantSvc, _ := newBindingTestFactory(t)

	// No balance tier, user default points at sub-b/model-b.
	subA := addBindingSub(t, subSvc, "sub-a", "model-a", 0)
	subB := addBindingSub(t, subSvc, "sub-b", "model-b", 0)
	if err := subSvc.SetUserDefaultModel("cli_user", subB.ID, "model-b"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}

	chatID := "/w/proj:Agent-tierfall"
	f.EnsureSessionModelBinding("cli_user", chatID, "cli")

	gotSub, gotModel, _ := tenantSvc.GetTenantSubscription("cli", chatID)
	if gotSub == "" || gotModel == "" {
		t.Fatalf("binding = (%q, %q) — must bind the user default (sub-b/model-b), not stay empty", gotSub, gotModel)
	}
	if gotSub != subB.ID || gotModel != "model-b" {
		t.Fatalf("binding = (%q, %q), want (sub-b, model-b) from user default", gotSub, gotModel)
	}
	_ = subA
}

// ── Reproduction 6: SetSessionLLM must never write an empty model
// ("任何时候禁止会话绑定的模型为空") ────────────────────────────────────────
//
// SetSessionLLM resolves the model from user_default_model; when that row is
// absent it wrote model="" — producing exactly the poisoned rows repro 1-4
// operate on. It must fall back to the subscription's own model instead.
func TestSetSessionLLM_NeverWritesEmptyModel(t *testing.T) {
	f, subSvc, tenantSvc, _ := newBindingTestFactory(t)
	// Subscription with a concrete Model column; NO user_default_model row.
	subA := &sqlite.LLMSubscription{
		ID: "sub-a", SenderID: "cli_user", Name: "sub-a", Provider: "openai",
		BaseURL: "https://api.sub-a.example/v1", APIKey: "sk-a", Model: "model-a",
	}
	if err := subSvc.Add(subA); err != nil {
		t.Fatalf("Add: %v", err)
	}

	chatID := "/w/proj:Agent-setllm"
	if err := f.SetSessionLLM("cli_user", chatID, "cli", subA); err != nil {
		t.Fatalf("SetSessionLLM: %v", err)
	}
	gotSub, gotModel, _ := tenantSvc.GetTenantSubscription("cli", chatID)
	if gotSub != subA.ID {
		t.Fatalf("binding sub = %q, want sub-a", gotSub)
	}
	if gotModel == "" {
		t.Fatalf("REPRODUCED: SetSessionLLM wrote model=\"\" (user_default_model absent, sub.Model=%q ignored)", subA.Model)
	}
}
