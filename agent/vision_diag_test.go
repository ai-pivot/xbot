package agent

// Diagnostic: verify the storage layer reads the per-model vision switch from
// a REAL database copy. This isolates "vision not reaching the model" between
// (a) the storage read (GetModel / loadPerModelConfigs) and (b) the factory
// client cache. Skipped unless VISION_DIAG_DB points at a DB path (use a copy
// of the live DB, never the live file).
//
// Usage:
//
//	cp ~/.xbot/xbot.db /tmp/vision_diag.db
//	VISION_DIAG_DB=/tmp/vision_diag.db go test ./agent/ -run TestVisionDiagRealDBStorage -v

import (
	"os"
	"testing"

	"xbot/storage/sqlite"
)

func TestVisionDiagRealDBStorage(t *testing.T) {
	dbPath := os.Getenv("VISION_DIAG_DB")
	if dbPath == "" {
		t.Skip("set VISION_DIAG_DB=<db path> to run this diagnostic")
	}
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()
	svc := sqlite.NewLLMSubscriptionService(db)

	subs, err := svc.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	visionRows := 0
	for _, sub := range subs {
		rows, gerr := svc.GetModels(sub.ID)
		if gerr != nil {
			t.Logf("sub %s: GetModels error: %v", sub.ID, gerr)
			continue
		}
		for _, r := range rows {
			if r.Vision {
				visionRows++
				// Path 1: GetModel (used by resolveModelConfig → client build).
				sm, err := svc.GetModel(sub.ID, r.Model)
				if err != nil || sm == nil {
					t.Errorf("GetModel(%s, %s) failed: err=%v sm=%v", sub.ID, r.Model, err, sm)
					continue
				}
				t.Logf("GetModel: sub=%s model=%s vision=%v detail=%q enabled=%v",
					sub.ID, sm.Model, sm.Vision, sm.VisionDetail, sm.Enabled)
				if !sm.Vision {
					t.Errorf("GetModel returned vision=false for a vision row: %+v", sm)
				}
				// Path 2: the subscription projection (listSubscriptions / UI).
				if cfg, ok := sub.PerModelConfigs[r.Model]; ok {
					t.Logf("PerModelConfigs projection: model=%s vision=%v detail=%q",
						r.Model, cfg.Vision, cfg.VisionDetail)
					if !cfg.Vision {
						t.Errorf("PerModelConfigs projection lost vision for %s: %+v", r.Model, cfg)
					}
				} else {
					t.Errorf("PerModelConfigs missing row for %s (sub %s)", r.Model, sub.ID)
				}
			}
		}
	}
	t.Logf("scanned %d subscriptions, %d vision-enabled model rows", len(subs), visionRows)
	if visionRows == 0 {
		t.Log("NOTE: no vision rows in this DB — nothing to verify")
	}
}
