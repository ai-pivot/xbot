package serverapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"xbot/agent"
	"xbot/channel"
	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

// M1: subscription WRITE-path permission checks must use the canonical
// user_id (v45), not sender_id. A subscription created through one channel
// identity (web-7, user_id=7) must be manageable through any other identity
// linked to the same user (cli or feishu sender with the same user_id).

func newSubWriteAuthTable(t *testing.T) RPCTable {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	return BuildRPCTable(&config.Config{}, ag, nil, nil, nil)
}

// addAuthTestSubscription adds a subscription as the given identity and
// returns its ID (looked up via list_subscriptions under the same identity).
func addAuthTestSubscription(t *testing.T, table RPCTable, ctx context.Context, name string) string {
	t.Helper()
	params, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name":     name,
			"provider": "openai",
			"base_url": "https://api.example/v1",
			"api_key":  "sk-test-key-123",
			"model":    "gpt-4o",
		},
	})
	if _, err := table.Dispatch(ctx, "add_subscription", params); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}
	raw, err := table.Dispatch(ctx, "list_subscriptions", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_subscriptions: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(raw, &subs); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, s := range subs {
		if s.Name == name {
			return s.ID
		}
	}
	t.Fatalf("subscription %q not found after add", name)
	return ""
}

// identityCtx builds an RPC context for a channel identity with the given
// canonical user id (mirrors server.go's WithRPCCtxResolved construction).
func identityCtx(senderID string, userID int64, role string) context.Context {
	return WithRPCCtxResolved(context.Background(), senderID, senderID, userID, role)
}

// TestSubscriptionWriteAuth_CrossChannelIdentity verifies the M1 fix: write
// paths (update/remove/rename/set model/per-model config/set default/export)
// authorize by canonical user_id so linked identities can manage their own
// subscriptions regardless of which channel created them.
func TestSubscriptionWriteAuth_CrossChannelIdentity(t *testing.T) {
	table := newSubWriteAuthTable(t)

	// web-7 owns user_id=7; a linked CLI identity resolves to the same uid.
	webCtx := identityCtx("web-7", 7, "user")
	linkedCliCtx := identityCtx("cli_user_b", 7, "user")
	otherUserCtx := identityCtx("web-8", 8, "user")

	subID := addAuthTestSubscription(t, table, webCtx, "cross-auth-sub")

	t.Run("update_subscription", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{
			"id": subID,
			"sub": map[string]any{
				"name":     "cross-auth-sub-updated",
				"provider": "openai",
				"base_url": "https://api.example/v1",
				"api_key":  "sk-test-key-123",
			},
		})
		// Linked identity (same user_id, different sender_id) must succeed.
		if _, err := table.Dispatch(linkedCliCtx, "update_subscription", params); err != nil {
			t.Errorf("linked identity update_subscription: %v", err)
		}
		// A different user must be denied.
		if _, err := table.Dispatch(otherUserCtx, "update_subscription", params); err == nil ||
			!strings.Contains(err.Error(), "not found") {
			t.Errorf("other user update_subscription: err=%v, want access denied", err)
		}
	})

	t.Run("rename_subscription", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"id": subID, "name": "renamed-sub"})
		if _, err := table.Dispatch(linkedCliCtx, "rename_subscription", params); err != nil {
			t.Errorf("linked identity rename_subscription: %v", err)
		}
		if _, err := table.Dispatch(otherUserCtx, "rename_subscription", params); err == nil ||
			!strings.Contains(err.Error(), "not found") {
			t.Errorf("other user rename_subscription: err=%v, want access denied", err)
		}
	})

	t.Run("update_per_model_config", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{
			"id":    subID,
			"model": "gpt-4o",
			"config": map[string]any{
				"max_context":       128000,
				"max_output_tokens": 4096,
			},
		})
		if _, err := table.Dispatch(linkedCliCtx, "update_per_model_config", params); err != nil {
			t.Errorf("linked identity update_per_model_config: %v", err)
		}
		if _, err := table.Dispatch(otherUserCtx, "update_per_model_config", params); err == nil ||
			!strings.Contains(err.Error(), "not found") {
			t.Errorf("other user update_per_model_config: err=%v, want access denied", err)
		}
	})

	t.Run("set_subscription_model", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"id": subID, "model": "gpt-4o-mini"})
		if _, err := table.Dispatch(linkedCliCtx, "set_subscription_model", params); err != nil {
			t.Errorf("linked identity set_subscription_model: %v", err)
		}
		if _, err := table.Dispatch(otherUserCtx, "set_subscription_model", params); err == nil ||
			!strings.Contains(err.Error(), "not found") {
			t.Errorf("other user set_subscription_model: err=%v, want access denied", err)
		}
	})

	t.Run("set_default_subscription", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"id": subID})
		if _, err := table.Dispatch(linkedCliCtx, "set_default_subscription", params); err != nil {
			t.Errorf("linked identity set_default_subscription: %v", err)
		}
		if _, err := table.Dispatch(otherUserCtx, "set_default_subscription", params); err == nil ||
			!strings.Contains(err.Error(), "not found") {
			t.Errorf("other user set_default_subscription: err=%v, want access denied", err)
		}
	})

	t.Run("export_subscriptions_by_ids", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"ids": []string{subID}})
		raw, err := table.Dispatch(linkedCliCtx, "export_subscriptions", params)
		if err != nil {
			t.Fatalf("linked identity export_subscriptions: %v", err)
		}
		var export struct {
			Subscriptions []map[string]any `json:"subscriptions"`
		}
		if err := json.Unmarshal(raw, &export); err != nil {
			t.Fatalf("unmarshal export result: %v (raw=%s)", err, string(raw))
		}
		if len(export.Subscriptions) != 1 {
			t.Errorf("linked identity export: got %d subs, want 1 (the owned sub)", len(export.Subscriptions))
		}
		raw2, err := table.Dispatch(otherUserCtx, "export_subscriptions", params)
		if err != nil {
			t.Fatalf("other user export_subscriptions dispatch: %v", err)
		}
		var export2 struct {
			Subscriptions []map[string]any `json:"subscriptions"`
		}
		if err := json.Unmarshal(raw2, &export2); err != nil {
			t.Fatalf("unmarshal export result (other user): %v", err)
		}
		if len(export2.Subscriptions) != 0 {
			t.Errorf("other user export: got %d subs, want 0 (access denied skips)", len(export2.Subscriptions))
		}
	})

	t.Run("remove_subscription", func(t *testing.T) {
		params, _ := json.Marshal(map[string]any{"id": subID})
		if _, err := table.Dispatch(otherUserCtx, "remove_subscription", params); err == nil ||
			!strings.Contains(err.Error(), "not found") {
			t.Fatalf("other user remove_subscription: err=%v, want access denied", err)
		}
		if _, err := table.Dispatch(linkedCliCtx, "remove_subscription", params); err != nil {
			t.Fatalf("linked identity remove_subscription: %v", err)
		}
	})
}
