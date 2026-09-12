package serverapp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"xbot/agent"
	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

const exportRealKey = "sk-real-secret-abcd1234"

// newSubsTable 建一个只挂 LLMFactory（订阅服务）的 RPC 表，供导出/导入测试用。
func newSubsTable(t *testing.T) (RPCTable, *sqlite.LLMSubscriptionService) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(filepath.Join(dir, "xbot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	return BuildRPCTable(&config.Config{}, ag, nil, nil, nil), subSvc
}

// REPRO（用户报告）："llm 导出功能导出的 api_key 是 mask 后的，无法做到快速配置"。
//
// 修复前双红：
//  1. 导出把 key 打成 `前4位+****`（mask）；
//  2. 导出文档用 `subscriptions` 键、导入端却读 `subs` —— 形状不匹配，
//     把导出文件喂给导入端必然 unmarshal 失败（快速配置闭环断裂）。
//
// 修复后：导出文档 = 导入入参的同一份契约（`{version, subs:[...]}`），
// 且带真实 key，导出→导入原样落库。
func TestExportSubscriptionsRoundTripCarriesRealKey(t *testing.T) {
	table, subSvc := newSubsTable(t)
	if err := subSvc.Add(&sqlite.LLMSubscription{
		ID: "sub-src", SenderID: "cli_user", Name: "src", Provider: "openai",
		BaseURL: "https://api.example/v1", APIKey: exportRealKey, Model: "gpt-5",
	}); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	raw, err := HandleCLIRPC(table, "export_subscriptions", json.RawMessage(`{"ids":[]}`), "admin")
	if err != nil {
		t.Fatalf("export_subscriptions: %v", err)
	}
	if strings.Contains(string(raw), "****") {
		t.Fatalf("export returned a mask placeholder instead of the real key: %s", raw)
	}
	if !strings.Contains(string(raw), exportRealKey) {
		t.Fatalf("export must carry the real api_key; got %s", raw)
	}

	var doc struct {
		Subs    json.RawMessage `json:"subs"`
		Version int             `json:"version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal export doc: %v", err)
	}
	if len(doc.Subs) == 0 {
		t.Fatalf("export doc must carry subscriptions under `subs` (the import wire key); got: %s", raw)
	}

	// 快速配置闭环：把导出文档原样喂给导入端（UI 传的就是这份 subs）。
	params, _ := json.Marshal(map[string]any{"subs": doc.Subs, "overwrite": true})
	res, err := HandleCLIRPC(table, "import_subscriptions", params, "admin")
	if err != nil {
		t.Fatalf("round-trip import of the exported doc failed: %v", err)
	}
	var out struct {
		Imported int `json:"imported"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("unmarshal import result: %v", err)
	}
	if out.Imported != 1 {
		t.Fatalf("imported=%d, want 1 (the exported subscription)", out.Imported)
	}

	all, err := subSvc.List("cli_user")
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	kept := false
	for _, s := range all {
		if s.ID != "sub-src" && s.APIKey == exportRealKey {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("imported subscription must keep the real api_key %q (quick-setup loop); got %+v", exportRealKey, all)
	}

	// 占位符守卫：手改/老版本文件里的 "****" 不落库为真 key（否则是个调用时才炸的坏订阅）。
	legacy, _ := json.Marshal(map[string]any{
		"subs": []map[string]any{{
			"name": "legacy", "provider": "openai",
			"base_url": "https://api.example/v1", "api_key": "sk-old****", "model": "m",
		}},
		"overwrite": false,
	})
	if _, err := HandleCLIRPC(table, "import_subscriptions", legacy, "admin"); err != nil {
		t.Fatalf("legacy import: %v", err)
	}
	all, err = subSvc.List("cli_user")
	if err != nil {
		t.Fatalf("list after legacy import: %v", err)
	}
	found := false
	for _, s := range all {
		if s.Name == "legacy" {
			found = true
			if s.APIKey != "" {
				t.Fatalf("masked placeholder must never be stored as a key, got %q", s.APIKey)
			}
		}
	}
	if !found {
		t.Fatalf("legacy subscription was not imported: %+v", all)
	}
}
