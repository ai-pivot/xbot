package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"xbot/channel/cli"
	"xbot/protocol"
)

// C2: calling the ask_user_resolved subscription handler must delete the
// persisted pending_askuser cache (path convention mirrored from
// channel/cli/cli_askuser_persist.go: HOME/.xbot/pending_askuser/sha256hex("ch:"+chatID)+".json").
func TestHandleAskUserResolvedBroadcastDeletesDiskCache(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir() reads $HOME on unix but %USERPROFILE% on Windows, and the
	// production path resolution (channel/cli/cli_askuser_persist.go:
	// pendingAskUserDir) goes through it. Override BOTH so the cache lands in
	// this temp dir on every platform — with only HOME set, this test passed on
	// Linux/macOS but failed the Windows CI job (the handler resolved a
	// different directory and deleted nothing).
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Fatalf("test setup: os.UserHomeDir()=%q (err=%v), want %q — the home override did not take effect on this platform", got, err, home)
	}
	chatID := "asku-chat-1"

	sum := sha256.Sum256([]byte("cli:" + chatID))
	path := filepath.Join(home, ".xbot", "pending_askuser", hex.EncodeToString(sum[:])+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"chat_id":"`+chatID+`"}`), 0600); err != nil {
		t.Fatal(err)
	}

	cliCh := cli.NewCLIChannel(&cli.CLIChannelConfig{ChatID: chatID})
	defer cliCh.Stop()

	payload, err := json.Marshal(protocol.AskUserResolvedEvent{
		Channel: "cli", ChatID: chatID, RequestID: "req-1", Reason: "answered",
	})
	if err != nil {
		t.Fatal(err)
	}
	handleAskUserResolvedBroadcast(cliCh, protocol.EventEnvelope{Payload: payload})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pending ask_user disk cache not deleted after ask_user_resolved (stat err=%v)", err)
	}
}
