package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/protocol"
	"xbot/tools"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func newTestFeishuChannel() *FeishuChannel {
	return NewFeishuChannel(FeishuConfig{}, bus.NewMessageBus())
}

func getCardElements(card map[string]any) ([]map[string]any, bool) {
	body, ok := card["body"].(map[string]any)
	if !ok {
		return nil, false
	}
	elements, ok := body["elements"].([]map[string]any)
	return elements, ok
}

func cardContainsTag(card map[string]any, tag string) bool {
	elements, ok := getCardElements(card)
	if !ok {
		return false
	}
	return containsTagRecursive(elements, tag)
}

func containsTagRecursive(elements []map[string]any, tag string) bool {
	for _, elem := range elements {
		if elem["tag"] == tag {
			return true
		}
		if columns, ok := elem["columns"].([]map[string]any); ok {
			if containsTagRecursive(columns, tag) {
				return true
			}
		}
		if children, ok := elem["elements"].([]map[string]any); ok {
			if containsTagRecursive(children, tag) {
				return true
			}
		}
	}
	return false
}

func cardJSON(card map[string]any) string {
	data, _ := json.Marshal(card)
	return string(data)
}

// --- Parsing helpers tests ---

func TestParseActionData(t *testing.T) {
	if r := parseActionData(`{"action":"settings_tab","tab":"model"}`); r == nil || r["action"] != "settings_tab" {
		t.Error("expected valid parse")
	}
	if parseActionData("") != nil {
		t.Error("expected nil for empty")
	}
	if parseActionData("{bad") != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestParseActionDataFromMap(t *testing.T) {
	m := map[string]any{"action_data": `{"action":"settings_set_model"}`}
	if r := parseActionDataFromMap(m); r == nil || r["action"] != "settings_set_model" {
		t.Error("expected valid parse")
	}
	if parseActionDataFromMap(map[string]any{}) != nil {
		t.Error("expected nil for missing")
	}
}

func TestMustMapToJSON(t *testing.T) {
	result := mustMapToJSON(map[string]string{"k": "v"})
	var parsed map[string]string
	if err := json.Unmarshal([]byte(result), &parsed); err != nil || parsed["k"] != "v" {
		t.Errorf("unexpected: %s", result)
	}
}

func TestFormStr(t *testing.T) {
	data := map[string]any{"name": "  hello  ", "number": 42}
	if formStr(data, "name") != "hello" {
		t.Error("should trim spaces")
	}
	if formStr(data, "number") != "" {
		t.Error("non-string should return empty")
	}
	if formStr(data, "missing") != "" {
		t.Error("missing key should return empty")
	}
}

// --- General tab ---

func TestBuildSettingsCard_GeneralTab(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RunnerConnectCmdGet: func(senderID string) string {
			return "./xbot-runner --server ws://example.com:8080/" + senderID + " --token secret"
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "general")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card["schema"] != "2.0" {
		t.Errorf("expected schema=2.0")
	}

	if !strings.Contains(cardJSON(card), "远程 Runner") {
		t.Error("general tab should have remote runner section")
	}
	if !strings.Contains(cardJSON(card), "xbot-runner") {
		t.Error("should show runner connect command")
	}
	if !strings.Contains(cardJSON(card), "user1") {
		t.Error("should include senderID in connect command")
	}
}

func TestBuildSettingsCard_DefaultsToGeneral(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RunnerConnectCmdGet: func(senderID string) string {
			return "./xbot-runner --server ws://example.com:8080/" + senderID + " --token secret"
		},
	})

	for _, tab := range []string{"", "unknown", "basic"} {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", tab)
		if err != nil {
			t.Fatalf("tab=%q error: %v", tab, err)
		}
		if !strings.Contains(cardJSON(card), "远程 Runner") {
			t.Errorf("tab=%q should default to general tab", tab)
		}
	}
}

func TestBuildApprovalCard_ContainsApproveDenyControls(t *testing.T) {
	f := newTestFeishuChannel()
	pending := &feishuPendingApproval{
		Request: tools.ApprovalRequest{
			ToolName: "Shell",
			RunAs:    "root",
			Reason:   "install package",
			Command:  "apt install nginx",
		},
		ApproveAction:    "perm_approve_test",
		DenyAction:       "perm_deny_test",
		DenySubmitAction: "perm_deny_submit_test",
	}

	card := f.buildApprovalCard(pending)
	s := cardJSON(card)
	if !strings.Contains(s, "Permission Approval") {
		t.Fatalf("expected approval card header, got %s", s)
	}
	if !strings.Contains(s, "perm_approve_test") || !strings.Contains(s, "perm_deny_test") {
		t.Fatalf("expected approve/deny action ids in card: %s", s)
	}
	if strings.Contains(s, "deny_reason") {
		t.Fatalf("did not expect deny_reason field in initial approval card: %s", s)
	}
	if !strings.Contains(s, "Deny") {
		t.Fatalf("expected deny button in card: %s", s)
	}
	if strings.Contains(s, "perm_deny_submit_test") {
		t.Fatalf("did not expect deny submit action in initial approval card: %s", s)
	}
}

func TestHandleApprovalCardAction_DenyReasonPropagates(t *testing.T) {
	f := newTestFeishuChannel()
	pending := &feishuPendingApproval{
		Request:          tools.ApprovalRequest{ToolName: "Shell", RunAs: "root", Command: "rm -rf /tmp/x"},
		SenderID:         "user_open_id",
		ResultCh:         make(chan tools.ApprovalResult, 1),
		CreatedAt:        time.Now(),
		ApproveAction:    "perm_approve_test",
		DenyAction:       "perm_deny_test",
		DenySubmitAction: "perm_deny_submit_test",
	}
	f.approvals[pending.ApproveAction] = pending
	f.approvals[pending.DenyAction] = pending
	f.approvals[pending.DenySubmitAction] = pending

	resp, handled := f.handleApprovalCardAction(
		map[string]any{"action": pending.DenyAction},
		&callback.CallBackAction{},
		"user_open_id",
	)
	if !handled {
		t.Fatal("expected action to be handled")
	}
	if resp == nil || resp.Toast == nil || resp.Card == nil {
		t.Fatal("expected toast and updated card response")
	}
	if got := <-func() chan string {
		ch := make(chan string, 1)
		select {
		case result := <-pending.ResultCh:
			ch <- result.DenyReason
		default:
			ch <- "__pending__"
		}
		return ch
	}(); got != "__pending__" {
		t.Fatalf("deny button should open deny-reason card first, got immediate result %q", got)
	}

	resp, handled = f.handleApprovalCardAction(
		map[string]any{"action": pending.DenySubmitAction},
		&callback.CallBackAction{FormValue: map[string]any{"deny_reason": "unsafe"}},
		"user_open_id",
	)
	if !handled {
		t.Fatal("expected deny submit action to be handled")
	}
	if resp == nil || resp.Toast == nil || resp.Card == nil {
		t.Fatal("expected toast and updated card response after deny submit")
	}
	select {
	case result := <-pending.ResultCh:
		if result.Approved {
			t.Fatal("expected denied result")
		}
		if result.DenyReason != "unsafe" {
			t.Fatalf("expected deny reason propagation, got %q", result.DenyReason)
		}
	default:
		t.Fatal("expected approval result to be delivered after deny submit")
	}
}

func TestBuildApprovalResultCard_TimeoutClosedMessage(t *testing.T) {
	f := newTestFeishuChannel()
	pending := &feishuPendingApproval{
		Request:   tools.ApprovalRequest{ToolName: "Shell", RunAs: "root", Command: "ls -la /root"},
		MessageID: "msg_timeout_test",
	}
	card := f.buildApprovalResultCard(pending, tools.ApprovalResult{Approved: false, DenyReason: "approval request timed out"})
	s := cardJSON(card)
	if !strings.Contains(s, "Timed Out") {
		t.Fatalf("expected timeout status in card: %s", s)
	}
	if !strings.Contains(s, "This card is now closed") {
		t.Fatalf("expected closed-card message in timeout card: %s", s)
	}
}

func TestHandleApprovalCardAction_RejectsWrongUser(t *testing.T) {
	f := newTestFeishuChannel()
	pending := &feishuPendingApproval{
		Request:       tools.ApprovalRequest{ToolName: "Shell", RunAs: "root"},
		SenderID:      "owner_user",
		ResultCh:      make(chan tools.ApprovalResult, 1),
		CreatedAt:     time.Now(),
		ApproveAction: "perm_approve_test2",
		DenyAction:    "perm_deny_test2",
	}
	f.approvals[pending.ApproveAction] = pending
	f.approvals[pending.DenyAction] = pending

	resp, handled := f.handleApprovalCardAction(map[string]any{"action": pending.ApproveAction}, &callback.CallBackAction{}, "other_user")
	if !handled {
		t.Fatal("expected action to be handled")
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "error" {
		t.Fatal("expected error toast for wrong user")
	}
	select {
	case <-pending.ResultCh:
		t.Fatal("should not resolve pending approval for wrong user")
	default:
	}
}

func TestHandleSettingsAction_SetModel(t *testing.T) {
	f := newTestFeishuChannel()
	var setSubID, setModel string
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMSet: func(senderID, subID, model string) error { setSubID = subID; setModel = model; return nil },
		LLMList: func(senderID string) ([]protocol.ModelEntry, protocol.ModelEntry) {
			return []protocol.ModelEntry{
				{SubID: "sub1", SubName: "test", Model: "gpt-4"},
				{SubID: "sub1", SubName: "test", Model: "claude-3"},
			}, protocol.ModelEntry{SubID: "sub1", SubName: "test", Model: "claude-3"}
		},
	})

	actionData := map[string]any{
		"action_data":     `{"action":"settings_set_model"}`,
		"selected_option": "sub1|claude-3",
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
		return
	}
	if setModel != "claude-3" {
		t.Errorf("expected model=claude-3, got %q", setModel)
	}
	if setSubID != "sub1" {
		t.Errorf("expected subID=sub1, got %q", setSubID)
	}
}

func TestHandleSettingsAction_UnknownAction(t *testing.T) {
	f := newTestFeishuChannel()
	_, err := f.HandleSettingsAction(context.Background(), map[string]any{
		"action_data": `{"action":"unknown"}`,
	}, "user1", "chat1", "msg1")
	if err == nil {
		t.Error("expected error")
	}
}

func TestHandleSettingsAction_MissingActionData(t *testing.T) {
	f := newTestFeishuChannel()
	_, err := f.HandleSettingsAction(context.Background(), map[string]any{}, "u", "c", "m")
	if err == nil {
		t.Error("expected error")
	}
}

// --- V2 compatibility ---

func TestSettingsCard_NoUnsupportedV2Tags(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		ContextModeGet: func() string { return "phase1" },
		LLMList: func(senderID string) ([]protocol.ModelEntry, protocol.ModelEntry) {
			return []protocol.ModelEntry{{SubID: "sub1", Model: "gpt-4"}}, protocol.ModelEntry{SubID: "sub1", Model: "gpt-4"}
		},
	})

	for _, tab := range []string{"general", "model"} {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", tab)
		if err != nil {
			t.Fatalf("tab %s: %v", tab, err)
		}
		if cardContainsTag(card, "note") {
			t.Errorf("tab %s: 'note' tag not supported in V2", tab)
		}
		if cardContainsTag(card, "action") {
			t.Errorf("tab %s: 'action' tag not supported in V2", tab)
		}
	}
}

func TestSettingsCard_NoCommandReferences(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		ContextModeGet: func() string { return "phase1" },
	})

	for _, tab := range []string{"general", "model"} {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", tab)
		if err != nil {
			t.Fatalf("tab %s: %v", tab, err)
		}
		s := cardJSON(card)
		for _, cmd := range []string{"/set-llm", "/unset-llm", "/llm"} {
			if strings.Contains(s, cmd) {
				t.Errorf("tab %s: should not reference command %q", tab, cmd)
			}
		}
	}
}

func TestBuildSettingsCard_NilCallbacks(t *testing.T) {
	f := newTestFeishuChannel()
	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "general")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil || card["schema"] != "2.0" {
		t.Error("should produce valid card even without callbacks")
	}
}

// --- Concurrency settings (removed: legacy llm_max_concurrent_personal) ---

func TestBuildSettingsCard_ModelTab_NoConcurrencySection(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMList: func(senderID string) ([]protocol.ModelEntry, protocol.ModelEntry) {
			return []protocol.ModelEntry{{SubID: "sub1", Model: "gpt-4"}, {SubID: "sub1", Model: "gpt-4o"}}, protocol.ModelEntry{SubID: "sub1", Model: "gpt-4"}
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "model")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	s := cardJSON(card)
	if strings.Contains(s, "个人 LLM 并发限制") {
		t.Error("model tab should NOT contain personal concurrency section header (removed)")
	}
	if strings.Contains(s, "并发上限") {
		t.Error("model tab should NOT contain concurrency label (removed)")
	}
}
