package serverapp

import (
	"encoding/json"
	"strings"
	"testing"

	"xbot/agent"
	"xbot/config"
	"xbot/storage/sqlite"
)

// m2: generate_link_code / list_identities must NOT silently fall back to
// user 1 when the caller's canonical user cannot be resolved (userID==0,
// e.g. an unrecognized CLI sender). The fallback handed out user 1's link
// code (admin credentials) and user 1's identity list to unknown callers.
func TestLinkCodeRPCsRejectUnknownIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ag := &agent.Agent{}
	ag.SetIdentityResolver(agent.NewIdentityResolver(db.Conn()))
	table := BuildRPCTable(&config.Config{}, ag, nil, nil, nil)

	// Unknown identity: canonical user resolution failed (userID=0).
	unknownCtx := identityCtx("unknown-sender", 0, "user")

	// generate_link_code must NOT issue a code under these conditions.
	raw, err := table.Dispatch(unknownCtx, "generate_link_code", json.RawMessage(`{}`))
	if err == nil {
		var res map[string]any
		if jerr := json.Unmarshal(raw, &res); jerr == nil {
			if code, ok := res["code"].(string); ok && code != "" {
				t.Fatalf("generate_link_code issued a link code (%q) for an unresolvable identity — this hands user-1 (admin) credentials to unknown callers", code)
			}
			if msg, _ := res["error"].(string); msg == "" {
				t.Fatalf("generate_link_code should return an error for unknown identity, got: %s", string(raw))
			} else if !strings.Contains(msg, "identity") {
				t.Errorf("generate_link_code error should mention identity resolution, got: %q", msg)
			}
		}
	}

	// list_identities must NOT list user 1's identities for an unknown caller.
	raw2, err2 := table.Dispatch(unknownCtx, "list_identities", json.RawMessage(`{}`))
	if err2 == nil {
		var res map[string]any
		if jerr := json.Unmarshal(raw2, &res); jerr == nil {
			if listedID, _ := res["user_id"].(float64); listedID == 1 {
				t.Fatal("list_identities returned user 1's identities for an unresolvable caller — fallback must be an error, not user 1")
			}
		}
	}

	// Control: a resolved identity (userID>0) still gets its link code —
	// the fix must only affect the unresolvable case. Resolve a real user
	// first (link_codes.user_id has a FK into users).
	resolver := agent.NewIdentityResolver(db.Conn())
	knownUID, _, err := resolver.Resolve("cli", "cli_user")
	if err != nil || knownUID <= 0 {
		t.Fatalf("resolve control identity: uid=%d err=%v", knownUID, err)
	}
	knownCtx := identityCtx("cli_user", knownUID, "user")
	raw3, err3 := table.Dispatch(knownCtx, "generate_link_code", json.RawMessage(`{}`))
	if err3 != nil {
		t.Fatalf("generate_link_code for resolved identity failed: %v", err3)
	}
	var res3 map[string]any
	if err := json.Unmarshal(raw3, &res3); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if code, _ := res3["code"].(string); code == "" {
		t.Errorf("generate_link_code for resolved identity returned no code: %s", string(raw3))
	}
}
