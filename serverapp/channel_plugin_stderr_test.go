package serverapp

import (
	"testing"
	"time"

	"xbot/plugin"
)

// M3: replaceChannelProcess must close the channel plugin process's stderr
// writer fd after killing the old process. spawnChannelProcess opens a log
// file fd per spawn (openPluginStderrWriter) and cmd.Stderr only gives the
// CHILD a dup — the parent's fd stays open forever without an explicit
// Close, leaking one fd on every plugin reload/restart.
func TestReplaceChannelProcessClosesStderrWriter(t *testing.T) {
	tmp := t.TempDir()
	p := &stdioChannelPluginProvider{
		decl: &plugin.ChannelProviderDecl{
			Name:  "stderr-fd-test",
			Entry: "cat", // blocks reading stdin; killed by replace
			Dir:   tmp,
		},
		xbotHome: tmp,
	}

	proc, err := spawnChannelProcess(p.decl, tmp)
	if err != nil {
		t.Fatalf("spawnChannelProcess: %v", err)
	}
	if proc.stderrWriter == nil {
		t.Fatal("spawnChannelProcess must track the stderr writer fd")
	}

	p.replaceChannelProcess(proc, nil)

	// Kill + reap + Close run asynchronously; poll for the writer to close.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := proc.stderrWriter.Write([]byte("x")); err != nil {
			return // fd closed — no leak
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stderrWriter fd still open after replaceChannelProcess — fd leak on plugin reload")
}
