package feishuapp

import (
	"errors"
	"strings"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

// The agent preset is a contract: the CardKit streaming progress card is dead
// without cardkit:card:write, and the bot is dead without the IM scopes/events.
// A regression here is silent (the card just degrades), so guard it explicitly.
func TestAddons_ContainsAgentPreset(t *testing.T) {
	addons := Addons()
	if addons == nil {
		t.Fatal("Addons() returned nil")
	}

	tenant := map[string]bool{}
	for _, s := range addons.Scopes.Tenant {
		tenant[s] = true
	}
	for _, required := range []string{
		"cardkit:card:write", // streaming progress card
		"cardkit:card:read",
		"im:message:send_as_bot",
		"im:message.p2p_msg:readonly",
		"im:message.group_at_msg:readonly",
		"im:chat:read",
	} {
		if !tenant[required] {
			t.Errorf("agent preset is missing required scope %q", required)
		}
	}

	events := map[string]bool{}
	for _, e := range addons.Events.Items.Tenant {
		events[e] = true
	}
	if !events["im.message.receive_v1"] {
		t.Error("agent preset is missing the im.message.receive_v1 event")
	}

	if len(addons.Callbacks.Items) == 0 || addons.Callbacks.Items[0] != "card.action.trigger" {
		t.Errorf("agent preset callbacks: got %v, want [card.action.trigger]", addons.Callbacks.Items)
	}

	// The user-identity scope is what lets the app keep acting after the
	// initial authorization.
	found := false
	for _, s := range addons.Scopes.User {
		if s == "offline_access" {
			found = true
		}
	}
	if !found {
		t.Error("agent preset is missing the offline_access user scope")
	}
}

func TestAddons_ReturnsACopy(t *testing.T) {
	// Mutating the returned addons must not corrupt the package-level preset
	// (it is applied on every bind attempt).
	first := Addons()
	first.Scopes.Tenant[0] = "mutated"
	if Addons().Scopes.Tenant[0] == "mutated" {
		t.Error("Addons() leaks its backing slice — the preset is shared mutable state")
	}
}

func TestDescribeError_RegistrationError(t *testing.T) {
	err := DescribeError(&registration.RegisterAppError{Code: "access_denied", Description: "user denied"})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"access_denied", "user denied"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}

	// Unrelated errors pass through untouched.
	plain := errors.New("boom")
	if DescribeError(plain) != plain {
		t.Error("DescribeError should return unrelated errors unchanged")
	}
}
