package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// m4: SaveToFile must be safe under concurrent writers. The old fixed tmp
// path (path + ".tmp") let two concurrent SaveToFile calls race: A writes the
// tmp file, B overwrites it, A renames B's content (A's update silently lost)
// and B's rename then fails with ENOENT.
func TestSaveToFileConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Seed with a valid base config so the merge path is exercised too.
	if err := SaveToFile(path, &Config{Server: ServerConfig{Host: "base", Port: 1}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const writers = 64
	const rounds = 8
	ports := make(chan int, writers*rounds)

	// Barrier-synchronized start to maximize the race window overlap.
	start := make(chan struct{})
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for r := 0; r < rounds; r++ {
				port := w*rounds + r + 100
				if err := SaveToFile(path, &Config{Server: ServerConfig{Host: "concurrent", Port: port}}); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					return
				}
				ports <- port
			}
		}()
	}
	close(start)
	wg.Wait()
	close(ports)

	if firstErr != nil {
		t.Fatalf("concurrent SaveToFile returned an error: %v (fixed tmp name race: rename ENOENT or clobbered write)", firstErr)
	}

	// The final file must be valid JSON whose Port is one of the values a
	// successful writer actually wrote (a merged/clobbered tmp write loses
	// this invariant and yields stale or torn content).
	attempted := make(map[int]bool)
	for p := range ports {
		attempted[p] = true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final config: %v", err)
	}
	var finalCfg Config
	if err := json.Unmarshal(data, &finalCfg); err != nil {
		t.Fatalf("final config is not valid JSON (torn write): %v\n%s", err, string(data))
	}
	if !attempted[finalCfg.Server.Port] {
		t.Fatalf("final Port %d was never written by any successful SaveToFile call — concurrent tmp clobbering lost an update", finalCfg.Server.Port)
	}

	// No temp-file residue may survive a successful rename (any writer's).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file residue left behind: %s", e.Name())
		}
	}
}
