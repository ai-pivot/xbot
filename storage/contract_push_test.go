package storage

import (
	"testing"
	"time"
)

// testPushStoreContract exercises the push_targets half of the Storage
// contract. Like testConfigStoreContract, rows are sender-scoped and
// list-observable, so every subtest uses a unique sender derived from t.Name().
func testPushStoreContract(t *testing.T, factory StorageFactory) {
	t.Run("UpsertListDelete", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ps := s.Config()
		sender := "web-push-" + t.Name()

		first := &PushTarget{
			UserID:     7,
			SenderID:   sender,
			Provider:   "fcm",
			Platform:   "android",
			Address:    "token-a",
			Metadata:   map[string]string{"device_name": "phone", "app_version": "0.3.5"},
			DeviceName: "phone",
			AppVersion: "0.3.5",
		}
		if err := ps.UpsertPushTarget(first); err != nil {
			t.Fatalf("UpsertPushTarget(first): %v", err)
		}

		// Upsert is idempotent by (provider, address) and updates mutable fields.
		first.DeviceName = "phone-renamed"
		first.Metadata = map[string]string{"device_name": "phone-renamed"}
		if err := ps.UpsertPushTarget(first); err != nil {
			t.Fatalf("UpsertPushTarget(update): %v", err)
		}

		second := &PushTarget{
			UserID:     7,
			SenderID:   sender,
			Provider:   "webpush",
			Platform:   "web",
			Address:    "https://push.example/ep-1",
			Metadata:   map[string]string{"p256dh": "key", "auth": "secret"},
			DeviceName: "browser",
		}
		if err := ps.UpsertPushTarget(second); err != nil {
			t.Fatalf("UpsertPushTarget(second): %v", err)
		}

		list, err := ps.ListPushTargetsBySender(sender)
		if err != nil {
			t.Fatalf("ListPushTargetsBySender: %v", err)
		}
		if len(list) != 2 {
			t.Fatalf("ListPushTargetsBySender returned %d targets, want 2", len(list))
		}
		var fcm, web *PushTarget
		for i := range list {
			switch list[i].Provider {
			case "fcm":
				fcm = &list[i]
			case "webpush":
				web = &list[i]
			}
		}
		if fcm == nil || web == nil {
			t.Fatalf("list is missing providers: %+v", list)
		}
		if fcm.DeviceName != "phone-renamed" {
			t.Errorf("fcm.DeviceName = %q, want phone-renamed", fcm.DeviceName)
		}
		if fcm.Metadata["device_name"] != "phone-renamed" {
			t.Errorf("fcm.Metadata not round-tripped: %+v", fcm.Metadata)
		}
		if web.Metadata["p256dh"] != "key" || web.Metadata["auth"] != "secret" {
			t.Errorf("webpush.Metadata not round-tripped: %+v", web.Metadata)
		}

		// Deleting another user's target is a no-op.
		if err := ps.DeletePushTarget(999, "fcm", "token-a"); err != nil {
			t.Fatalf("DeletePushTarget(wrong user): %v", err)
		}
		if list, _ := ps.ListPushTargetsBySender(sender); len(list) != 2 {
			t.Fatalf("wrong-user delete removed a target: %+v", list)
		}

		if err := ps.DeletePushTarget(7, "fcm", "token-a"); err != nil {
			t.Fatalf("DeletePushTarget: %v", err)
		}
		if list, _ := ps.ListPushTargetsBySender(sender); len(list) != 1 {
			t.Fatalf("after delete, got %d targets, want 1", len(list))
		}
	})

	t.Run("TouchAndDeleteByID", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ps := s.Config()
		sender := "web-push-touch-" + t.Name()

		target := &PushTarget{
			UserID:   8,
			SenderID: sender,
			Provider: "fcm",
			Platform: "android",
			Address:  "token-touch",
		}
		if err := ps.UpsertPushTarget(target); err != nil {
			t.Fatalf("UpsertPushTarget: %v", err)
		}
		list, err := ps.ListPushTargetsBySender(sender)
		if err != nil || len(list) != 1 {
			t.Fatalf("ListPushTargetsBySender: %v, %d", err, len(list))
		}
		id := list[0].ID

		at := time.Now().Add(10 * time.Minute)
		if err := ps.TouchPushTargets([]int64{id}, at); err != nil {
			t.Fatalf("TouchPushTargets: %v", err)
		}
		list, err = ps.ListPushTargetsBySender(sender)
		if err != nil || len(list) != 1 {
			t.Fatalf("ListPushTargetsBySender after touch: %v, %d", err, len(list))
		}
		if diff := list[0].LastSeenAt.Sub(at); diff < -2*time.Second || diff > 2*time.Second {
			t.Errorf("LastSeenAt = %v, want ~%v", list[0].LastSeenAt, at)
		}

		if err := ps.DeletePushTargetByID(id); err != nil {
			t.Fatalf("DeletePushTargetByID: %v", err)
		}
		if list, _ := ps.ListPushTargetsBySender(sender); len(list) != 0 {
			t.Fatalf("after DeletePushTargetByID, got %d targets, want 0", len(list))
		}
	})

	t.Run("SettingsRoundTrip", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ps := s.Config()

		// Fresh DB: no settings row yet (the push service seeds it from config).
		if raw, err := ps.GetPushSettings(); err != nil || raw != "" {
			t.Fatalf("fresh GetPushSettings = %q, %v; want empty, nil", raw, err)
		}
		first := `{"enable":true,"fcm":{"enable":true,"project_id":"p"}}`
		if err := ps.SavePushSettings(first); err != nil {
			t.Fatalf("SavePushSettings: %v", err)
		}
		if got, err := ps.GetPushSettings(); err != nil || got != first {
			t.Fatalf("GetPushSettings = %q, %v; want %q", got, err, first)
		}
		// Upsert, not duplicate row.
		second := `{"enable":false}`
		if err := ps.SavePushSettings(second); err != nil {
			t.Fatalf("SavePushSettings(update): %v", err)
		}
		if got, err := ps.GetPushSettings(); err != nil || got != second {
			t.Fatalf("GetPushSettings after update = %q, %v; want %q", got, err, second)
		}
	})
}
