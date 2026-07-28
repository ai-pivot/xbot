package cli

import (
	"testing"
	"time"

	ch "xbot/channel"
	"xbot/protocol"

	tea "charm.land/bubbletea/v2"
)

func TestCLIDestructiveEventsWaitForAsyncCapacity(t *testing.T) {
	cli := &CLIChannel{
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}
	cli.asyncCh <- cliToastMsg{text: "busy"}
	done := make(chan struct{})
	go func() {
		cli.SendSessionState(protocol.SessionEvent{
			Channel: "cli", ChatID: "chat", Action: "history_rewound", TargetHistoryID: 11,
		})
		cli.sendCritical(cliToastMsg{text: "after gate"})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("destructive event returned before it entered the full async channel")
	case <-time.After(20 * time.Millisecond):
	}
	<-cli.asyncCh
	select {
	case raw := <-cli.asyncCh:
		msg, ok := raw.(cliSessionStateMsg)
		if !ok || msg.event.Action != "history_rewound" {
			t.Fatalf("reliable TUI session event = %#v", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("history rewind was not delivered after asyncCh freed")
	}
	select {
	case raw := <-cli.asyncCh:
		msg, ok := raw.(cliToastMsg)
		if !ok || msg.text != "after gate" {
			t.Fatalf("event after gate = %#v", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("event after gate was not queued after history rewind")
	}
	<-done

	cli.asyncCh <- cliToastMsg{text: "busy-again"}
	done = make(chan struct{})
	go func() {
		defer close(done)
		if _, err := cli.Send(ch.OutboundMsg{
			Channel: "cli", ChatID: "chat", Metadata: map[string]string{"session_reset": "true"},
		}); err != nil {
			t.Errorf("Send session reset: %v", err)
		}
	}()
	select {
	case <-done:
		t.Fatal("session reset returned before it entered the full async channel")
	case <-time.After(20 * time.Millisecond):
	}
	<-cli.asyncCh
	select {
	case raw := <-cli.asyncCh:
		msg, ok := raw.(cliOutboundMsg)
		if !ok || msg.msg.Metadata["session_reset"] != "true" {
			t.Fatalf("reliable TUI session reset = %#v", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("session reset was not delivered after asyncCh freed")
	}
	<-done

	cli.asyncCh <- cliToastMsg{text: "busy-resync"}
	done = make(chan struct{})
	go func() {
		cli.SendSessionState(protocol.SessionEvent{
			Channel: "cli", ChatID: "chat", Action: "resync_required",
		})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("resync returned before it entered the full async channel")
	case <-time.After(20 * time.Millisecond):
	}
	<-cli.asyncCh
	select {
	case raw := <-cli.asyncCh:
		msg, ok := raw.(cliSessionStateMsg)
		if !ok || msg.event.Action != "resync_required" {
			t.Fatalf("reliable TUI resync event = %#v", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("resync was not delivered after asyncCh freed")
	}
	<-done
}

func TestCLIStopSignalCancelsBlockedDestructiveEvent(t *testing.T) {
	cli := &CLIChannel{
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}
	cli.asyncCh <- cliToastMsg{text: "busy"}
	delivered := make(chan struct{})
	go func() {
		cli.SendSessionState(protocol.SessionEvent{Action: "history_rewound"})
		close(delivered)
	}()

	close(cli.stopCh)
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("stop signal did not cancel blocked destructive event")
	}
}

func TestCLIQueuedAskUserCannotOvertakeSessionReset(t *testing.T) {
	cli := &CLIChannel{
		program: &tea.Program{},
		msgChan: make(chan queuedCLIOutbound, 2),
		asyncCh: make(chan tea.Msg, 2),
		stopCh:  make(chan struct{}),
	}
	if _, err := cli.Send(ch.OutboundMsg{Channel: "cli", ChatID: "chat", WaitingUser: true, Content: "stale question"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Send(ch.OutboundMsg{
		Channel: "cli", ChatID: "chat", Metadata: map[string]string{"session_reset": "true"},
	}); err != nil {
		t.Fatal(err)
	}

	cli.wg.Add(1)
	go cli.handleOutbound()
	defer func() {
		close(cli.stopCh)
		cli.wg.Wait()
	}()

	raw := <-cli.asyncCh
	reset, ok := raw.(cliOutboundMsg)
	if !ok || reset.msg.Metadata["session_reset"] != "true" {
		t.Fatalf("first delivered message = %#v", raw)
	}
	time.Sleep(20 * time.Millisecond)
	select {
	case stale := <-cli.asyncCh:
		t.Fatalf("stale AskUser crossed the reset barrier: %#v", stale)
	default:
	}
}

func TestCLIQueuedProgressCannotCrossDestructiveBarrier(t *testing.T) {
	for _, action := range []string{"history_rewound", "resync_required"} {
		t.Run(action, func(t *testing.T) {
			cli := &CLIChannel{
				program:        &tea.Program{},
				asyncCh:        make(chan tea.Msg, 2),
				progressSignal: make(chan struct{}, 1),
				stopCh:         make(chan struct{}),
			}
			cli.SendProgress("chat", &protocol.ProgressEvent{ChatID: "chat", Phase: "thinking"})
			cli.SendSessionState(protocol.SessionEvent{Channel: "cli", ChatID: "chat", Action: action})

			cli.wg.Add(1)
			go cli.handleProgressDrain()
			defer func() {
				close(cli.stopCh)
				cli.wg.Wait()
			}()

			raw := <-cli.asyncCh
			barrier, ok := raw.(cliSessionStateMsg)
			if !ok || barrier.event.Action != action {
				t.Fatalf("first delivered message = %#v", raw)
			}
			time.Sleep(20 * time.Millisecond)
			select {
			case stale := <-cli.asyncCh:
				t.Fatalf("stale progress crossed %s barrier: %#v", action, stale)
			default:
			}
		})
	}
}

func TestCLISetConnStateUsesBubbleTeaMessage(t *testing.T) {
	model := initTestModel()
	model.connState = "connected"
	cli := &CLIChannel{
		program:         &tea.Program{},
		model:           model,
		asyncCh:         make(chan tea.Msg, 1),
		connStateSignal: make(chan struct{}, 1),
		stopCh:          make(chan struct{}),
	}
	cli.wg.Add(1)
	go cli.handleConnStateDrain()
	defer func() {
		close(cli.stopCh)
		cli.wg.Wait()
	}()

	cli.SetConnState("disconnected")
	if model.connState != "connected" {
		t.Fatal("SetConnState wrote cliModel outside Bubble Tea Update")
	}
	select {
	case raw := <-cli.asyncCh:
		msg, ok := raw.(cliConnStateMsg)
		if !ok || msg.state != "disconnected" {
			t.Fatalf("connection state message = %#v", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("connection state was not queued")
	}
}

func TestCLISetConnStateBeforeProgramStartCoalescesLatest(t *testing.T) {
	model := initTestModel()
	model.connState = "connected"
	cli := &CLIChannel{
		model:           model,
		asyncCh:         make(chan tea.Msg, 1),
		connStateSignal: make(chan struct{}, 1),
		stopCh:          make(chan struct{}),
	}

	cli.SetConnState("disconnected")
	cli.SetConnState("reconnecting")
	if model.connState != "connected" {
		t.Fatal("SetConnState wrote cliModel before the Bubble Tea event loop started")
	}
	if len(cli.connStateSignal) != 1 {
		t.Fatalf("connection state signals = %d, want one coalesced wakeup", len(cli.connStateSignal))
	}

	cli.program = &tea.Program{}
	cli.wg.Add(1)
	go cli.handleConnStateDrain()
	defer func() {
		close(cli.stopCh)
		cli.wg.Wait()
	}()

	select {
	case raw := <-cli.asyncCh:
		msg, ok := raw.(cliConnStateMsg)
		if !ok || msg.state != "reconnecting" {
			t.Fatalf("connection state after startup = %#v", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-start connection state was not drained after startup")
	}
}
