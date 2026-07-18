package agent

import (
	"testing"
	"time"
)

func TestAgentCloseWaitsForLifecycleWorkers(t *testing.T) {
	a := &Agent{lifecycleStopCh: make(chan struct{})}
	stopping := make(chan struct{})
	release := make(chan struct{})
	a.lifecycleWG.Add(1)
	go func() {
		defer a.lifecycleWG.Done()
		<-a.lifecycleStopCh
		close(stopping)
		<-release
	}()

	closed := make(chan struct{})
	go func() {
		_ = a.Close()
		close(closed)
	}()

	<-stopping
	select {
	case <-closed:
		t.Fatal("Close returned before lifecycle worker exited")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after lifecycle worker exited")
	}

	if err := a.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
}
