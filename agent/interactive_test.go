package agent

import (
	"context"
	"testing"

	"xbot/bus"
)

func TestInteractiveKey(t *testing.T) {
	tests := []struct {
		channel, chatID, roleName, instance, want string
	}{
		{"feishu", "oc_xxx", "code-reviewer", "", "feishu:oc_xxx/code-reviewer"},
		{"cli", "direct", "writer", "", "cli:direct/writer"},
		{"", "", "test", "", ":/test"},
		{"feishu", "oc_xxx", "brainstorm", "1", "feishu:oc_xxx/brainstorm:1"},
		{"feishu", "oc_xxx", "brainstorm", "architect", "feishu:oc_xxx/brainstorm:architect"},
		{"cli", "chat-1", "reviewer", "oneshot-reviewer-123", "cli:chat-1/reviewer:oneshot-reviewer-123"},
	}
	for _, tt := range tests {
		got := interactiveKey(tt.channel, tt.chatID, tt.roleName, tt.instance)
		if got != tt.want {
			t.Errorf("interactiveKey(%q, %q, %q, %q) = %q, want %q", tt.channel, tt.chatID, tt.roleName, tt.instance, got, tt.want)
		}
	}
}

func TestWireSubAgentProgressCarriesSessionKey(t *testing.T) {
	const sessionKey = "cli:chat-1/reviewer:review-1"

	var got []SubAgentProgressDetail
	ctx := WithSubAgentProgress(context.Background(), func(detail SubAgentProgressDetail) {
		got = append(got, detail)
	})
	cfg := &RunConfig{}
	subCtx := wireSubAgentProgress(
		ctx,
		context.Background(),
		cfg,
		&CallChain{Chain: []string{"main"}},
		"reviewer",
		"review-1",
		sessionKey,
		false,
	)

	cfg.ProgressNotifier([]string{"working"}, "checking")
	if len(got) != 1 || got[0].SessionKey != sessionKey {
		t.Fatalf("direct progress session key = %#v, want %q", got, sessionKey)
	}

	nestedCB, ok := SubAgentProgressFromContext(subCtx)
	if !ok {
		t.Fatal("nested progress callback not installed")
	}
	const nestedKey = "cli:chat-1/fixer:fix-1"
	nestedCB(SubAgentProgressDetail{
		Path:       []string{"main/reviewer", "main/reviewer/fixer"},
		Instance:   "fix-1",
		SessionKey: nestedKey,
	})
	if len(got) != 2 || got[1].SessionKey != nestedKey {
		t.Fatalf("nested progress session key = %#v, want %q", got, nestedKey)
	}
}

func TestParseInteractiveKeyChatID(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"CLI absolute path", "cli:/home/user/workspace/explore:oneshot-explore-1234", "/home/user/workspace"},
		{"Feishu chatID", "feishu:oc_abc123/explore:session-1", "oc_abc123"},
		{"Web chatID", "web:senderID/explore:inst", "senderID"},
		{"multi-slash path", "cli:/a/b/c/d/explore:oneshot-explore-9876", "/a/b/c/d"},
		{"empty chatID", "cli:/explore:inst", ""},
		{"no slash", "cli:explore:inst", ""},
		{"no colon", "cli/explore:inst", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseInteractiveKeyChatID(tt.key)
			if got != tt.want {
				t.Errorf("parseInteractiveKeyChatID(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestResolveOriginIDs_WithMetadata(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "agent",
		SenderID: "sub_agent",
		ChatID:   "agent_chat",
		Metadata: map[string]string{
			"origin_channel": "feishu",
			"origin_chat_id": "oc_abc123",
			"origin_sender":  "ou_xyz789",
		},
	}
	ch, chatID, sender := resolveOriginIDs(msg)
	if ch != "feishu" {
		t.Errorf("channel = %q, want %q", ch, "feishu")
	}
	if chatID != "oc_abc123" {
		t.Errorf("chatID = %q, want %q", chatID, "oc_abc123")
	}
	if sender != "ou_xyz789" {
		t.Errorf("sender = %q, want %q", sender, "ou_xyz789")
	}
}

func TestResolveOriginIDs_FallbackToTopLevel(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "feishu",
		SenderID: "ou_direct",
		ChatID:   "oc_direct",
		Metadata: nil,
	}
	ch, chatID, sender := resolveOriginIDs(msg)
	if ch != "feishu" {
		t.Errorf("channel = %q, want %q", ch, "feishu")
	}
	if chatID != "oc_direct" {
		t.Errorf("chatID = %q, want %q", chatID, "oc_direct")
	}
	if sender != "ou_direct" {
		t.Errorf("sender = %q, want %q", sender, "ou_direct")
	}
}

func TestResolveOriginIDs_PartialMetadata(t *testing.T) {
	msg := bus.InboundMessage{
		Channel:  "agent",
		SenderID: "sub",
		ChatID:   "sub_chat",
		Metadata: map[string]string{
			"origin_channel": "feishu",
			// origin_chat_id and origin_sender missing → should fallback
		},
	}
	ch, chatID, sender := resolveOriginIDs(msg)
	if ch != "feishu" {
		t.Errorf("channel = %q, want %q", ch, "feishu")
	}
	if chatID != "sub_chat" {
		t.Errorf("chatID = %q, want %q (fallback)", chatID, "sub_chat")
	}
	if sender != "sub" {
		t.Errorf("sender = %q, want %q (fallback)", sender, "sub")
	}
}
