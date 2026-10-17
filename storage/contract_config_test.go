package storage

import (
	"fmt"
	"testing"
	"time"

	"xbot/event"
	"xbot/storage/sqlite"
)

// testConfigStoreContract exercises the ConfigStore half of the Storage
// contract: LLM subscriptions + per-model config, user profile/settings, token
// usage, web-UI auth, cron jobs, event triggers and shared artifacts.
//
// Isolation strategy is deliberately different from testSessionStoreContract:
// ConfigStore rows are NOT tenant-scoped — they are keyed by sender_id or a
// global id, and queries like List, ListAllSubscriptions, ListAllCronJobs,
// ListAllUsage and CountWebUsers observe every row in the backend. A shared
// backend would let one subtest observe another's rows (e.g. "List returns 1"
// would break). Therefore every subtest builds its OWN Storage via factory(t)
// and derives unique identifiers from t.Name(), so even a backend that shares
// state between factory calls stays disjoint.
func testConfigStoreContract(t *testing.T, factory StorageFactory) {
	// ── P0 ──

	t.Run("Subscriptions_CRUD", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		sub := addTestSubscription(t, cs, sender)
		if sub.ID == "" {
			t.Fatalf("Add did not assign a subscription ID")
		}

		list, err := cs.List(sender)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("List returned %d subscriptions, want 1", len(list))
		}

		got, err := cs.Get(sub.ID)
		if err != nil {
			t.Fatalf("Get(%q): %v", sub.ID, err)
		}
		if got == nil {
			t.Fatalf("Get(%q) = nil after Add", sub.ID)
		}
		if got.Name != "Contract Sub" {
			t.Errorf("Get.Name = %q, want %q", got.Name, "Contract Sub")
		}

		got.Name = "Renamed Sub"
		if err := cs.Update(got); err != nil {
			t.Fatalf("Update: %v", err)
		}
		refetched, err := cs.Get(sub.ID)
		if err != nil {
			t.Fatalf("Get after Update: %v", err)
		}
		if refetched == nil || refetched.Name != "Renamed Sub" {
			t.Errorf("Get after Update = %+v, want Name=Renamed Sub", refetched)
		}

		if err := cs.SetDefault(sub.ID); err != nil {
			t.Fatalf("SetDefault: %v", err)
		}
		def, err := cs.GetDefault(sender)
		if err != nil {
			t.Fatalf("GetDefault: %v", err)
		}
		if def == nil {
			t.Fatalf("GetDefault = nil after SetDefault")
		}
		if def.ID != sub.ID {
			t.Errorf("GetDefault.ID = %q, want %q", def.ID, sub.ID)
		}

		if err := cs.SetSubscriptionEnabled(sub.ID, false); err != nil {
			t.Fatalf("SetSubscriptionEnabled(false): %v", err)
		}
		disabled, err := cs.Get(sub.ID)
		if err != nil {
			t.Fatalf("Get after SetSubscriptionEnabled: %v", err)
		}
		if disabled == nil {
			t.Fatalf("Get(%q) = nil after SetSubscriptionEnabled", sub.ID)
		}
		if disabled.Enabled {
			t.Errorf("Get.Enabled = true, want false")
		}

		if err := cs.Remove(sub.ID); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		requireNilSubscription(t, cs, sub.ID)
	})

	t.Run("UpsertModel", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.UpsertModel(sub.ID, "gpt-4o", 128000, 4096, "", ""); err != nil {
			t.Fatalf("UpsertModel: %v", err)
		}
		m, err := cs.GetModel(sub.ID, "gpt-4o")
		if err != nil {
			t.Fatalf("GetModel: %v", err)
		}
		if m == nil {
			t.Fatalf("GetModel(%q, gpt-4o) = nil after UpsertModel", sub.ID)
		}
		if m.MaxContext != 128000 {
			t.Errorf("GetModel.MaxContext = %d, want 128000", m.MaxContext)
		}

		// Re-upsert with a different context window must update the existing row.
		if err := cs.UpsertModel(sub.ID, "gpt-4o", 256000, 8192, "", ""); err != nil {
			t.Fatalf("UpsertModel (second): %v", err)
		}
		m2, err := cs.GetModel(sub.ID, "gpt-4o")
		if err != nil {
			t.Fatalf("GetModel (second): %v", err)
		}
		if m2 == nil {
			t.Fatalf("GetModel(%q, gpt-4o) = nil after second UpsertModel", sub.ID)
		}
		if m2.MaxContext != 256000 {
			t.Errorf("GetModel.MaxContext after re-upsert = %d, want 256000", m2.MaxContext)
		}
	})

	t.Run("GetModels", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.UpsertModel(sub.ID, "model-a", 1000, 100, "", ""); err != nil {
			t.Fatalf("UpsertModel(model-a): %v", err)
		}
		if err := cs.UpsertModel(sub.ID, "model-b", 2000, 200, "", ""); err != nil {
			t.Fatalf("UpsertModel(model-b): %v", err)
		}
		models, err := cs.GetModels(sub.ID)
		if err != nil {
			t.Fatalf("GetModels: %v", err)
		}
		if len(models) < 2 {
			t.Fatalf("GetModels returned %d models, want >= 2", len(models))
		}
	})

	t.Run("EnsureModel", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.EnsureModel(sub.ID, "claude-3"); err != nil {
			t.Fatalf("EnsureModel: %v", err)
		}
		m, err := cs.GetModel(sub.ID, "claude-3")
		if err != nil {
			t.Fatalf("GetModel: %v", err)
		}
		if m == nil {
			t.Fatalf("GetModel(%q, claude-3) = nil after EnsureModel", sub.ID)
		}
	})

	t.Run("SetModelEnabled", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.UpsertModel(sub.ID, "gpt-4o-mini", 128000, 4096, "", ""); err != nil {
			t.Fatalf("UpsertModel: %v", err)
		}
		if err := cs.SetModelEnabled(sub.ID, "gpt-4o-mini", false); err != nil {
			t.Fatalf("SetModelEnabled(false): %v", err)
		}
		m, err := cs.GetModel(sub.ID, "gpt-4o-mini")
		if err != nil {
			t.Fatalf("GetModel: %v", err)
		}
		if m == nil {
			t.Fatalf("GetModel = nil after SetModelEnabled")
		}
		if m.Enabled {
			t.Errorf("GetModel.Enabled = true, want false")
		}
	})

	t.Run("GetUserDefaultModel_SetUserDefaultModel", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)
		sub := addTestSubscription(t, cs, sender)

		if err := cs.SetUserDefaultModel(sender, sub.ID, "gpt-4o"); err != nil {
			t.Fatalf("SetUserDefaultModel: %v", err)
		}
		udm, err := cs.GetUserDefaultModel(sender)
		if err != nil {
			t.Fatalf("GetUserDefaultModel: %v", err)
		}
		if udm == nil {
			t.Fatalf("GetUserDefaultModel = nil after SetUserDefaultModel")
		}
		if udm.SubscriptionID != sub.ID {
			t.Errorf("GetUserDefaultModel.SubscriptionID = %q, want %q", udm.SubscriptionID, sub.ID)
		}
		if udm.Model != "gpt-4o" {
			t.Errorf("GetUserDefaultModel.Model = %q, want %q", udm.Model, "gpt-4o")
		}
	})

	t.Run("ListAllSubscriptions", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()

		addTestSubscription(t, cs, configTestID(t))
		addTestSubscription(t, cs, configTestID(t))

		all, err := cs.ListAllSubscriptions()
		if err != nil {
			t.Fatalf("ListAllSubscriptions: %v", err)
		}
		if len(all) < 2 {
			t.Fatalf("ListAllSubscriptions returned %d subscriptions, want >= 2", len(all))
		}
	})

	t.Run("RenameSubscription", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.RenameSubscription(sub.ID, "NewName"); err != nil {
			t.Fatalf("RenameSubscription: %v", err)
		}
		got, err := cs.Get(sub.ID)
		if err != nil {
			t.Fatalf("Get after RenameSubscription: %v", err)
		}
		if got == nil {
			t.Fatalf("Get(%q) = nil after RenameSubscription", sub.ID)
		}
		if got.Name != "NewName" {
			t.Errorf("Get.Name = %q, want %q", got.Name, "NewName")
		}
	})

	// ── P1 ──

	t.Run("UserSettings_GetSetDelete", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		channel := "web"
		sender := configTestID(t)

		if err := cs.SetUserSetting(channel, sender, "theme", "dark"); err != nil {
			t.Fatalf("SetUserSetting: %v", err)
		}
		settings, err := cs.GetUserSettings(channel, sender)
		if err != nil {
			t.Fatalf("GetUserSettings: %v", err)
		}
		if settings["theme"] != "dark" {
			t.Errorf("settings[theme] = %q, want %q", settings["theme"], "dark")
		}

		if err := cs.DeleteUserSetting(channel, sender, "theme"); err != nil {
			t.Fatalf("DeleteUserSetting: %v", err)
		}
		after, err := cs.GetUserSettings(channel, sender)
		if err != nil {
			t.Fatalf("GetUserSettings after Delete: %v", err)
		}
		if _, ok := after["theme"]; ok {
			t.Errorf("settings still contains theme after Delete: %v", after)
		}
	})

	t.Run("UserProfile_GetSave", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		if err := cs.SaveUserProfile(sender, "Contract Name", "Contract Profile"); err != nil {
			t.Fatalf("SaveUserProfile: %v", err)
		}
		name, profile, err := cs.GetUserProfile(sender)
		if err != nil {
			t.Fatalf("GetUserProfile: %v", err)
		}
		if name != "Contract Name" {
			t.Errorf("GetUserProfile name = %q, want %q", name, "Contract Name")
		}
		if profile != "Contract Profile" {
			t.Errorf("GetUserProfile profile = %q, want %q", profile, "Contract Profile")
		}
	})

	t.Run("TokenUsage_RecordGet", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		if err := cs.RecordUsage(sender, "gpt-4o", 100, 50, 10, 1, 1); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
		u, err := cs.GetUsage(sender)
		if err != nil {
			t.Fatalf("GetUsage: %v", err)
		}
		if u == nil {
			t.Fatalf("GetUsage = nil after RecordUsage")
		}
		if u.InputTokens < 100 {
			t.Errorf("GetUsage.InputTokens = %d, want >= 100", u.InputTokens)
		}
	})

	t.Run("GetDailyUsage", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		if err := cs.RecordUsage(sender, "gpt-4o", 100, 50, 10, 1, 1); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
		days, err := cs.GetDailyUsage(sender, 1)
		if err != nil {
			t.Fatalf("GetDailyUsage: %v", err)
		}
		if len(days) < 1 {
			t.Fatalf("GetDailyUsage returned %d rows, want >= 1", len(days))
		}
	})

	t.Run("GetDailyUsageSummary", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		if err := cs.RecordUsage(sender, "gpt-4o", 100, 50, 10, 1, 1); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
		summary, err := cs.GetDailyUsageSummary(sender, 1)
		if err != nil {
			t.Fatalf("GetDailyUsageSummary: %v", err)
		}
		if len(summary) < 1 {
			t.Fatalf("GetDailyUsageSummary returned %d rows, want >= 1", len(summary))
		}
	})

	t.Run("GetAllUsage", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		if err := cs.RecordUsage(sender, "gpt-4o", 100, 50, 10, 1, 1); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
		all, err := cs.GetAllUsage()
		if err != nil {
			t.Fatalf("GetAllUsage: %v", err)
		}
		if len(all) < 1 {
			t.Fatalf("GetAllUsage returned %d rows, want >= 1", len(all))
		}
	})

	t.Run("WebAuth_CreateGetListDeleteCount", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		username := configTestID(t)

		n, err := cs.CountWebUsers()
		if err != nil {
			t.Fatalf("CountWebUsers: %v", err)
		}
		if n != 0 {
			t.Fatalf("CountWebUsers = %d, want 0 on a fresh store", n)
		}

		id, err := cs.CreateWebUser(username, "hashed-password")
		if err != nil {
			t.Fatalf("CreateWebUser: %v", err)
		}
		if id <= 0 {
			t.Errorf("CreateWebUser id = %d, want > 0", id)
		}

		n, err = cs.CountWebUsers()
		if err != nil {
			t.Fatalf("CountWebUsers after create: %v", err)
		}
		if n != 1 {
			t.Fatalf("CountWebUsers = %d, want 1 after create", n)
		}

		gotID, gotHash, err := cs.GetWebUserForLogin(username)
		if err != nil {
			t.Fatalf("GetWebUserForLogin: %v", err)
		}
		if gotID != id {
			t.Errorf("GetWebUserForLogin id = %d, want %d", gotID, id)
		}
		if gotHash != "hashed-password" {
			t.Errorf("GetWebUserForLogin hash = %q, want %q", gotHash, "hashed-password")
		}

		users, err := cs.ListWebUsers()
		if err != nil {
			t.Fatalf("ListWebUsers: %v", err)
		}
		if len(users) != 1 {
			t.Fatalf("ListWebUsers returned %d users, want 1", len(users))
		}
		if users[0].Username != username {
			t.Errorf("ListWebUsers[0].Username = %q, want %q", users[0].Username, username)
		}

		if err := cs.DeleteWebUser(username); err != nil {
			t.Fatalf("DeleteWebUser: %v", err)
		}
		n, err = cs.CountWebUsers()
		if err != nil {
			t.Fatalf("CountWebUsers after delete: %v", err)
		}
		if n != 0 {
			t.Fatalf("CountWebUsers = %d, want 0 after delete", n)
		}
	})

	t.Run("CronJobs_AddGetRemoveList", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)
		chatID := configTestID(t)
		jobID := configTestID(t)

		job := &sqlite.CronJob{
			ID:        jobID,
			Message:   "contract ping",
			Channel:   "web",
			ChatID:    chatID,
			SenderID:  sender,
			CronExpr:  "*/5 * * * *",
			CreatedAt: time.Now(),
			NextRun:   time.Now().Add(time.Minute).Truncate(time.Second),
			OneShot:   false,
		}
		if err := cs.AddCronJob(job); err != nil {
			t.Fatalf("AddCronJob: %v", err)
		}

		got, err := cs.GetCronJob(jobID)
		if err != nil {
			t.Fatalf("GetCronJob: %v", err)
		}
		if got == nil {
			t.Fatalf("GetCronJob(%q) = nil after AddCronJob", jobID)
		}

		bySender, err := cs.ListCronJobsBySender(sender)
		if err != nil {
			t.Fatalf("ListCronJobsBySender: %v", err)
		}
		if len(bySender) < 1 {
			t.Fatalf("ListCronJobsBySender returned %d jobs, want >= 1", len(bySender))
		}

		all, err := cs.ListAllCronJobs()
		if err != nil {
			t.Fatalf("ListAllCronJobs: %v", err)
		}
		if len(all) < 1 {
			t.Fatalf("ListAllCronJobs returned %d jobs, want >= 1", len(all))
		}

		byChat, err := cs.ListCronJobsByChannelChatID("web", chatID)
		if err != nil {
			t.Fatalf("ListCronJobsByChannelChatID: %v", err)
		}
		if len(byChat) < 1 {
			t.Fatalf("ListCronJobsByChannelChatID returned %d jobs, want >= 1", len(byChat))
		}

		next := time.Now().Add(2 * time.Hour).Truncate(time.Second)
		if err := cs.UpdateCronNextRun(jobID, next); err != nil {
			t.Fatalf("UpdateCronNextRun: %v", err)
		}
		updated, err := cs.GetCronJob(jobID)
		if err != nil {
			t.Fatalf("GetCronJob after UpdateCronNextRun: %v", err)
		}
		if updated == nil {
			t.Fatalf("GetCronJob(%q) = nil after UpdateCronNextRun", jobID)
		}
		if updated.NextRun.Unix() != next.Unix() {
			t.Errorf("NextRun = %v, want %v", updated.NextRun, next)
		}

		if err := cs.RemoveCronJob(jobID); err != nil {
			t.Fatalf("RemoveCronJob: %v", err)
		}
		gone, err := cs.GetCronJob(jobID)
		if err != nil {
			t.Fatalf("GetCronJob after Remove: %v", err)
		}
		if gone != nil {
			t.Errorf("GetCronJob(%q) after Remove = %+v, want nil", jobID, gone)
		}
	})

	t.Run("Triggers_AddGetRemoveList", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)
		triggerID := configTestID(t)
		eventType := configTestID(t)

		trig := &event.Trigger{
			ID:         triggerID,
			Name:       "contract trigger",
			EventType:  eventType,
			Channel:    "web",
			ChatID:     configTestID(t),
			SenderID:   sender,
			MessageTpl: "payload={{.payload}}",
			Secret:     "contract-secret",
			Enabled:    true,
			OneShot:    false,
			CreatedAt:  time.Now(),
		}
		if err := cs.AddTrigger(trig); err != nil {
			t.Fatalf("AddTrigger: %v", err)
		}

		got, err := cs.GetTrigger(triggerID)
		if err != nil {
			t.Fatalf("GetTrigger: %v", err)
		}
		if got == nil {
			t.Fatalf("GetTrigger(%q) = nil after AddTrigger", triggerID)
		}

		byType, err := cs.ListTriggersByEventType(eventType)
		if err != nil {
			t.Fatalf("ListTriggersByEventType: %v", err)
		}
		if len(byType) < 1 {
			t.Fatalf("ListTriggersByEventType returned %d triggers, want >= 1", len(byType))
		}

		bySender, err := cs.ListTriggersBySender(sender)
		if err != nil {
			t.Fatalf("ListTriggersBySender: %v", err)
		}
		if len(bySender) < 1 {
			t.Fatalf("ListTriggersBySender returned %d triggers, want >= 1", len(bySender))
		}

		if err := cs.UpdateTriggerEnabled(triggerID, false); err != nil {
			t.Fatalf("UpdateTriggerEnabled(false): %v", err)
		}
		disabled, err := cs.GetTrigger(triggerID)
		if err != nil {
			t.Fatalf("GetTrigger after UpdateTriggerEnabled: %v", err)
		}
		if disabled == nil {
			t.Fatalf("GetTrigger(%q) = nil after UpdateTriggerEnabled", triggerID)
		}
		if disabled.Enabled {
			t.Errorf("GetTrigger.Enabled = true, want false")
		}

		if err := cs.RecordTriggerFire(triggerID, time.Now()); err != nil {
			t.Fatalf("RecordTriggerFire: %v", err)
		}
		fired, err := cs.GetTrigger(triggerID)
		if err != nil {
			t.Fatalf("GetTrigger after RecordTriggerFire: %v", err)
		}
		if fired == nil {
			t.Fatalf("GetTrigger(%q) = nil after RecordTriggerFire", triggerID)
		}
		if fired.FireCount < 1 {
			t.Errorf("GetTrigger.FireCount = %d, want >= 1", fired.FireCount)
		}

		if err := cs.RemoveTrigger(triggerID); err != nil {
			t.Fatalf("RemoveTrigger: %v", err)
		}
		gone, err := cs.GetTrigger(triggerID)
		if err != nil {
			t.Fatalf("GetTrigger after Remove: %v", err)
		}
		if gone != nil {
			t.Errorf("GetTrigger(%q) after Remove = %+v, want nil", triggerID, gone)
		}
	})

	t.Run("SharedArtifacts_CreateGetRevokeList", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		creator := time.Now().UnixNano()
		token := configTestID(t)

		artifact := &sqlite.SharedArtifact{
			Token:       token,
			PluginID:    "xbot.contract",
			ContentType: "contract/artifact",
			Payload:     `{"hello":"world"}`,
			Title:       "Contract Artifact",
			CreatedBy:   creator,
		}
		if err := cs.CreateSharedArtifact(artifact); err != nil {
			t.Fatalf("CreateSharedArtifact: %v", err)
		}

		got, err := cs.GetSharedArtifact(token)
		if err != nil {
			t.Fatalf("GetSharedArtifact: %v", err)
		}
		if got == nil {
			t.Fatalf("GetSharedArtifact(%q) = nil after CreateSharedArtifact", token)
		}
		if got.PluginID != "xbot.contract" {
			t.Errorf("GetSharedArtifact.PluginID = %q, want %q", got.PluginID, "xbot.contract")
		}

		list, err := cs.ListSharedArtifactsByCreator(creator)
		if err != nil {
			t.Fatalf("ListSharedArtifactsByCreator: %v", err)
		}
		if len(list) < 1 {
			t.Fatalf("ListSharedArtifactsByCreator returned %d artifacts, want >= 1", len(list))
		}

		if err := cs.RevokeSharedArtifact(token, creator); err != nil {
			t.Fatalf("RevokeSharedArtifact: %v", err)
		}
		// A revoked token must no longer resolve. The two backends report the
		// "gone" case differently (SQLite: ErrSharedArtifactNotFound; PG: a nil
		// artifact with no error), so the contract only asserts the artifact is nil.
		afterRevoke, _ := cs.GetSharedArtifact(token)
		if afterRevoke != nil {
			t.Errorf("GetSharedArtifact(%q) after Revoke = %+v, want nil", token, afterRevoke)
		}
	})

	// ── P2 ──

	t.Run("ClearUserDefaultModel", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)
		sub := addTestSubscription(t, cs, sender)

		if err := cs.SetUserDefaultModel(sender, sub.ID, "gpt-4o"); err != nil {
			t.Fatalf("SetUserDefaultModel: %v", err)
		}
		if err := cs.ClearUserDefaultModel(sender); err != nil {
			t.Fatalf("ClearUserDefaultModel: %v", err)
		}
		udm, err := cs.GetUserDefaultModel(sender)
		if err != nil {
			t.Fatalf("GetUserDefaultModel after Clear: %v", err)
		}
		if udm != nil {
			t.Errorf("GetUserDefaultModel after Clear = %+v, want nil", udm)
		}
	})

	t.Run("SetModelVisionConfig", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.UpsertModel(sub.ID, "gpt-4o", 128000, 4096, "", ""); err != nil {
			t.Fatalf("UpsertModel: %v", err)
		}
		if err := cs.SetModelVisionConfig(sub.ID, "gpt-4o", true, "high"); err != nil {
			t.Fatalf("SetModelVisionConfig: %v", err)
		}
		m, err := cs.GetModel(sub.ID, "gpt-4o")
		if err != nil {
			t.Fatalf("GetModel: %v", err)
		}
		if m == nil {
			t.Fatalf("GetModel = nil after SetModelVisionConfig")
		}
		if !m.Vision {
			t.Errorf("GetModel.Vision = false, want true")
		}
		if m.VisionDetail != "high" {
			t.Errorf("GetModel.VisionDetail = %q, want %q", m.VisionDetail, "high")
		}
	})

	// ── ConfigStore-style subscription verbs + remaining SubscriptionBackend gaps ──
	//
	// ConfigStore carries the subscription API under TWO naming families: the
	// SubscriptionBackend-style verbs (Add/List/Update/Remove/SetDefault/…,
	// covered above) and the ConfigStore-style verbs
	// (AddSubscription/ListSubscriptions/UpdateSubscription/GetDefaultSubscription/
	// SetDefaultSubscription/RemoveSubscription). The SQLite adapter implements the
	// latter as one-line wrappers over the former (sqlite_adapter.go), but PG
	// implements them independently (pg_config.go), so they need their own
	// coverage. Every case below asserts through a SEQUENCE of operations
	// (write → re-read / re-list) rather than trusting a single call, because the
	// stored row — not the return value — is what callers end up reading.

	t.Run("Subscriptions_ConfigStoreStyle_CRUD", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		mkSub := func(id, name, model string) *sqlite.LLMSubscription {
			return &sqlite.LLMSubscription{
				ID:       id,
				SenderID: sender,
				Name:     name,
				Provider: "openai",
				BaseURL:  "https://api.example.com",
				APIKey:   "sk-contract-test",
				Model:    model,
			}
		}
		hasID := func(subs []*sqlite.LLMSubscription, id string) bool {
			for _, sub := range subs {
				if sub.ID == id {
					return true
				}
			}
			return false
		}

		first := mkSub(configTestID(t)+"-first", "Contract First", "gpt-4o")
		if err := cs.AddSubscription(first); err != nil {
			t.Fatalf("AddSubscription(first): %v", err)
		}

		list, err := cs.ListSubscriptions(sender)
		if err != nil {
			t.Fatalf("ListSubscriptions: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("ListSubscriptions returned %d subscriptions, want 1", len(list))
		}
		if list[0].ID != first.ID {
			t.Errorf("ListSubscriptions[0].ID = %q, want %q", list[0].ID, first.ID)
		}
		if list[0].Name != "Contract First" {
			t.Errorf("ListSubscriptions[0].Name = %q, want %q", list[0].Name, "Contract First")
		}

		// Adding a subscription must NOT silently mint a default: user_default_model
		// is written only by an explicit selection, and inventing one here would
		// make the reader resolve a model the operator never chose.
		def, err := cs.GetDefaultSubscription(sender)
		if err != nil {
			t.Fatalf("GetDefaultSubscription before any selection: %v", err)
		}
		if def != nil {
			t.Errorf("GetDefaultSubscription before any selection = %+v, want nil", def)
		}

		second := mkSub(configTestID(t)+"-second", "Contract Second", "gpt-4o-mini")
		if err := cs.AddSubscription(second); err != nil {
			t.Fatalf("AddSubscription(second): %v", err)
		}
		list, err = cs.ListSubscriptions(sender)
		if err != nil {
			t.Fatalf("ListSubscriptions after second add: %v", err)
		}
		if len(list) != 2 {
			t.Fatalf("ListSubscriptions returned %d subscriptions, want 2", len(list))
		}
		if !hasID(list, first.ID) || !hasID(list, second.ID) {
			t.Errorf("ListSubscriptions is missing an added subscription: %+v", list)
		}

		if err := cs.SetDefaultSubscription(first.ID); err != nil {
			t.Fatalf("SetDefaultSubscription(first): %v", err)
		}
		def, err = cs.GetDefaultSubscription(sender)
		if err != nil {
			t.Fatalf("GetDefaultSubscription after SetDefaultSubscription: %v", err)
		}
		if def == nil {
			t.Fatalf("GetDefaultSubscription = nil after SetDefaultSubscription")
		}
		if def.ID != first.ID {
			t.Errorf("GetDefaultSubscription.ID = %q, want %q", def.ID, first.ID)
		}

		// "At most one default per sender": switching the default must REPLACE the
		// pointer, never accumulate a second row for the same sender.
		if err := cs.SetDefaultSubscription(second.ID); err != nil {
			t.Fatalf("SetDefaultSubscription(second): %v", err)
		}
		def, err = cs.GetDefaultSubscription(sender)
		if err != nil {
			t.Fatalf("GetDefaultSubscription after switching default: %v", err)
		}
		if def == nil {
			t.Fatalf("GetDefaultSubscription = nil after switching the default")
		}
		if def.ID != second.ID {
			t.Errorf("GetDefaultSubscription.ID after switch = %q, want %q", def.ID, second.ID)
		}
	})

	t.Run("UpdateSubscription", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		sub := &sqlite.LLMSubscription{
			ID:       configTestID(t),
			SenderID: sender,
			Name:     "Before Update",
			Provider: "openai",
			BaseURL:  "https://api.example.com",
			APIKey:   "sk-contract-test",
			Model:    "gpt-4o",
		}
		if err := cs.AddSubscription(sub); err != nil {
			t.Fatalf("AddSubscription: %v", err)
		}

		got, err := cs.GetSubscription(sub.ID)
		if err != nil {
			t.Fatalf("GetSubscription: %v", err)
		}
		if got == nil {
			t.Fatalf("GetSubscription(%q) = nil after AddSubscription", sub.ID)
		}

		// Round-trip the fetched struct: this is how callers (RPC handlers) edit a
		// subscription, so the update must not depend on out-of-band fields.
		got.Name = "After Update"
		got.Model = "gpt-4o-mini"
		got.Provider = "anthropic"
		if err := cs.UpdateSubscription(got); err != nil {
			t.Fatalf("UpdateSubscription: %v", err)
		}

		refetched, err := cs.GetSubscription(sub.ID)
		if err != nil {
			t.Fatalf("GetSubscription after UpdateSubscription: %v", err)
		}
		if refetched == nil {
			t.Fatalf("GetSubscription(%q) = nil after UpdateSubscription", sub.ID)
		}
		if refetched.Name != "After Update" {
			t.Errorf("Name after update = %q, want %q", refetched.Name, "After Update")
		}
		if refetched.Model != "gpt-4o-mini" {
			t.Errorf("Model after update = %q, want %q", refetched.Model, "gpt-4o-mini")
		}
		if refetched.Provider != "anthropic" {
			t.Errorf("Provider after update = %q, want %q", refetched.Provider, "anthropic")
		}

		// The list reader must observe the very same row (no second cache/copy).
		list, err := cs.ListSubscriptions(sender)
		if err != nil {
			t.Fatalf("ListSubscriptions after update: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("ListSubscriptions returned %d subscriptions, want 1", len(list))
		}
		if list[0].Name != "After Update" || list[0].Model != "gpt-4o-mini" {
			t.Errorf("ListSubscriptions[0] = {Name:%q Model:%q}, want {Name:%q Model:%q}",
				list[0].Name, list[0].Model, "After Update", "gpt-4o-mini")
		}
	})

	t.Run("SetDefaultSubscription_RemoveSubscription", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sender := configTestID(t)

		mkSub := func(id, name string) *sqlite.LLMSubscription {
			return &sqlite.LLMSubscription{
				ID:       id,
				SenderID: sender,
				Name:     name,
				Provider: "openai",
				BaseURL:  "https://api.example.com",
				APIKey:   "sk-contract-test",
				Model:    "gpt-4o",
			}
		}
		keep := mkSub(configTestID(t)+"-keep", "Contract Keep")
		drop := mkSub(configTestID(t)+"-drop", "Contract Drop")
		for _, sub := range []*sqlite.LLMSubscription{keep, drop} {
			if err := cs.AddSubscription(sub); err != nil {
				t.Fatalf("AddSubscription(%s): %v", sub.Name, err)
			}
		}

		if err := cs.SetDefaultSubscription(drop.ID); err != nil {
			t.Fatalf("SetDefaultSubscription: %v", err)
		}
		def, err := cs.GetDefaultSubscription(sender)
		if err != nil {
			t.Fatalf("GetDefaultSubscription: %v", err)
		}
		if def == nil || def.ID != drop.ID {
			t.Fatalf("GetDefaultSubscription = %+v, want id %q", def, drop.ID)
		}

		if err := cs.RemoveSubscription(drop.ID); err != nil {
			t.Fatalf("RemoveSubscription: %v", err)
		}

		gone, err := cs.GetSubscription(drop.ID)
		if err != nil {
			t.Fatalf("GetSubscription after RemoveSubscription: %v", err)
		}
		if gone != nil {
			t.Errorf("GetSubscription(%q) after RemoveSubscription = %+v, want nil", drop.ID, gone)
		}

		list, err := cs.ListSubscriptions(sender)
		if err != nil {
			t.Fatalf("ListSubscriptions after RemoveSubscription: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("ListSubscriptions returned %d subscriptions, want 1", len(list))
		}
		if list[0].ID != keep.ID {
			t.Errorf("ListSubscriptions[0].ID = %q, want %q (the survivor)", list[0].ID, keep.ID)
		}

		// The default pointer must not survive the subscription it points at:
		// resolving a deleted row as "the default" would hand callers a nil
		// subscription (or a stale model) instead of falling back.
		defAfter, err := cs.GetDefaultSubscription(sender)
		if err != nil {
			t.Fatalf("GetDefaultSubscription after removing the default: %v", err)
		}
		if defAfter != nil {
			t.Errorf("GetDefaultSubscription after removing the default = %+v, want nil", defAfter)
		}
	})

	t.Run("ListAll", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()

		a := &sqlite.LLMSubscription{
			ID:       configTestID(t) + "-a",
			SenderID: configTestID(t) + "-sender-a",
			Name:     "Sub A",
			Provider: "openai",
			BaseURL:  "https://api.example.com",
			APIKey:   "sk-a",
			Model:    "gpt-4o",
		}
		b := &sqlite.LLMSubscription{
			ID:       configTestID(t) + "-b",
			SenderID: configTestID(t) + "-sender-b",
			Name:     "Sub B",
			Provider: "deepseek",
			BaseURL:  "https://api.example.com",
			APIKey:   "sk-b",
			Model:    "deepseek-chat",
		}
		if err := cs.AddSubscription(a); err != nil {
			t.Fatalf("AddSubscription(a): %v", err)
		}
		if err := cs.AddSubscription(b); err != nil {
			t.Fatalf("AddSubscription(b): %v", err)
		}

		// ListAll is the sender-agnostic view (the admin/console listing): both
		// subscriptions must show up no matter who owns them.
		all, err := cs.ListAll()
		if err != nil {
			t.Fatalf("ListAll: %v", err)
		}
		if len(all) < 2 {
			t.Fatalf("ListAll returned %d subscriptions, want >= 2", len(all))
		}
		found := map[string]bool{}
		for _, sub := range all {
			found[sub.ID] = true
		}
		if !found[a.ID] || !found[b.ID] {
			t.Errorf("ListAll is missing a subscription: has %v, want both %q and %q", found, a.ID, b.ID)
		}
	})

	t.Run("Rename_SetModel", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.Rename(sub.ID, "Renamed Via Backend"); err != nil {
			t.Fatalf("Rename: %v", err)
		}
		renamed, err := cs.GetSubscription(sub.ID)
		if err != nil {
			t.Fatalf("GetSubscription after Rename: %v", err)
		}
		if renamed == nil {
			t.Fatalf("GetSubscription(%q) = nil after Rename", sub.ID)
		}
		if renamed.Name != "Renamed Via Backend" {
			t.Errorf("Name after Rename = %q, want %q", renamed.Name, "Renamed Via Backend")
		}

		// SetModel is the shared SubscriptionBackend verb used by the RPC layer to
		// repoint a subscription at a different default model. It must persist the
		// new default: a silently-successful no-op would leave the picker and the
		// stored subscription disagreeing.
		if err := cs.SetModel(sub.ID, "claude-3-opus"); err != nil {
			t.Fatalf("SetModel: %v", err)
		}
		repointed, err := cs.GetSubscription(sub.ID)
		if err != nil {
			t.Fatalf("GetSubscription after SetModel: %v", err)
		}
		if repointed == nil {
			t.Fatalf("GetSubscription(%q) = nil after SetModel", sub.ID)
		}
		if repointed.Model != "claude-3-opus" {
			t.Errorf("Model after SetModel = %q, want %q", repointed.Model, "claude-3-opus")
		}
		// SetModel is model-scoped: the display name touched by Rename must survive.
		if repointed.Name != "Renamed Via Backend" {
			t.Errorf("Name after SetModel = %q, want %q (SetModel must not clobber it)",
				repointed.Name, "Renamed Via Backend")
		}
	})

	t.Run("SetModelMaxContext_SetModelMaxOutput", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.UpsertModel(sub.ID, "gpt-4o", 128000, 4096, "", ""); err != nil {
			t.Fatalf("UpsertModel: %v", err)
		}

		if err := cs.SetModelMaxContext(sub.ID, "gpt-4o", 200000); err != nil {
			t.Fatalf("SetModelMaxContext: %v", err)
		}
		m, err := cs.GetModel(sub.ID, "gpt-4o")
		if err != nil {
			t.Fatalf("GetModel after SetModelMaxContext: %v", err)
		}
		if m == nil {
			t.Fatalf("GetModel(%q, gpt-4o) = nil after SetModelMaxContext", sub.ID)
		}
		if m.MaxContext != 200000 {
			t.Errorf("MaxContext after SetModelMaxContext = %d, want 200000", m.MaxContext)
		}
		// The setter is single-column: the sibling budget must survive untouched.
		if m.MaxOutputTokens != 4096 {
			t.Errorf("MaxOutputTokens after SetModelMaxContext = %d, want 4096 (must be preserved)", m.MaxOutputTokens)
		}

		if err := cs.SetModelMaxOutput(sub.ID, "gpt-4o", 8192); err != nil {
			t.Fatalf("SetModelMaxOutput: %v", err)
		}
		m2, err := cs.GetModel(sub.ID, "gpt-4o")
		if err != nil {
			t.Fatalf("GetModel after SetModelMaxOutput: %v", err)
		}
		if m2 == nil {
			t.Fatalf("GetModel(%q, gpt-4o) = nil after SetModelMaxOutput", sub.ID)
		}
		if m2.MaxOutputTokens != 8192 {
			t.Errorf("MaxOutputTokens after SetModelMaxOutput = %d, want 8192", m2.MaxOutputTokens)
		}
		if m2.MaxContext != 200000 {
			t.Errorf("MaxContext after SetModelMaxOutput = %d, want 200000 (must be preserved)", m2.MaxContext)
		}
	})

	t.Run("RemoveModel", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		if err := cs.UpsertModel(sub.ID, "model-a", 1000, 100, "", ""); err != nil {
			t.Fatalf("UpsertModel(model-a): %v", err)
		}
		if err := cs.UpsertModel(sub.ID, "model-b", 2000, 200, "", ""); err != nil {
			t.Fatalf("UpsertModel(model-b): %v", err)
		}

		if err := cs.RemoveModel(sub.ID, "model-a"); err != nil {
			t.Fatalf("RemoveModel: %v", err)
		}

		gone, err := cs.GetModel(sub.ID, "model-a")
		if err != nil {
			t.Fatalf("GetModel after RemoveModel: %v", err)
		}
		if gone != nil {
			t.Errorf("GetModel(%q, model-a) after RemoveModel = %+v, want nil", sub.ID, gone)
		}

		models, err := cs.GetModels(sub.ID)
		if err != nil {
			t.Fatalf("GetModels after RemoveModel: %v", err)
		}
		if len(models) != 1 {
			t.Fatalf("GetModels returned %d models, want 1", len(models))
		}
		if models[0].Model != "model-b" {
			t.Errorf("GetModels[0].Model = %q, want %q (the survivor)", models[0].Model, "model-b")
		}

		// Deleting a model that is already gone is idempotent, not an error —
		// concurrent removals (UI + catalog refresh) must not fail the caller.
		if err := cs.RemoveModel(sub.ID, "model-a"); err != nil {
			t.Errorf("RemoveModel on an already-removed model: %v", err)
		}
	})

	t.Run("UpdatePerModelConfigs", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		cs := s.Config()
		sub := addTestSubscription(t, cs, configTestID(t))

		// The model rows are registered first: UpdatePerModelConfigs writes the
		// per-model config of models that exist (registering a model is the job of
		// UpsertModel/EnsureModel, not of this call).
		if err := cs.UpsertModel(sub.ID, "model-a", 1000, 100, "", ""); err != nil {
			t.Fatalf("UpsertModel(model-a): %v", err)
		}
		if err := cs.UpsertModel(sub.ID, "model-b", 2000, 200, "", ""); err != nil {
			t.Fatalf("UpsertModel(model-b): %v", err)
		}

		if err := cs.UpdatePerModelConfigs(sub.ID, map[string]sqlite.PerModelConfig{
			"model-a": {MaxContext: 111000, MaxOutputTokens: 111, APIType: "responses"},
			"model-b": {MaxContext: 222000, MaxOutputTokens: 222},
		}); err != nil {
			t.Fatalf("UpdatePerModelConfigs: %v", err)
		}

		mA, err := cs.GetModel(sub.ID, "model-a")
		if err != nil {
			t.Fatalf("GetModel(model-a): %v", err)
		}
		if mA == nil {
			t.Fatalf("GetModel(%q, model-a) = nil after UpdatePerModelConfigs", sub.ID)
		}
		if mA.MaxContext != 111000 {
			t.Errorf("model-a MaxContext = %d, want 111000", mA.MaxContext)
		}
		if mA.MaxOutputTokens != 111 {
			t.Errorf("model-a MaxOutputTokens = %d, want 111", mA.MaxOutputTokens)
		}
		// APIType is the per-model override for the API dialect: it must land in
		// the api_type column ("" means "inherit the subscription default"), never
		// in thinking_mode.
		if mA.APIType != "responses" {
			t.Errorf("model-a APIType = %q, want %q", mA.APIType, "responses")
		}

		mB, err := cs.GetModel(sub.ID, "model-b")
		if err != nil {
			t.Fatalf("GetModel(model-b): %v", err)
		}
		if mB == nil {
			t.Fatalf("GetModel(%q, model-b) = nil after UpdatePerModelConfigs", sub.ID)
		}
		if mB.MaxContext != 222000 {
			t.Errorf("model-b MaxContext = %d, want 222000", mB.MaxContext)
		}
		if mB.MaxOutputTokens != 222 {
			t.Errorf("model-b MaxOutputTokens = %d, want 222", mB.MaxOutputTokens)
		}
		if mB.APIType != "" {
			t.Errorf("model-b APIType = %q, want %q (unset means inherit)", mB.APIType, "")
		}

		models, err := cs.GetModels(sub.ID)
		if err != nil {
			t.Fatalf("GetModels after UpdatePerModelConfigs: %v", err)
		}
		if len(models) != 2 {
			t.Fatalf("GetModels returned %d models, want 2", len(models))
		}
	})
}

// configTestID returns a per-subtest unique identifier. ConfigStore rows are
// not tenant-scoped, so the name+timestamp suffix keeps data from different
// subtests disjoint even when a backend reuses storage across factory calls.
func configTestID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())
}

// addTestSubscription inserts an enabled subscription owned by sender and
// returns it. The ID is set explicitly because not every backend mints one on
// insert (the SQLite adapter does; the PG adapter uses the caller's ID).
func addTestSubscription(t *testing.T, cs ConfigStore, sender string) *sqlite.LLMSubscription {
	t.Helper()
	sub := &sqlite.LLMSubscription{
		ID:       configTestID(t),
		SenderID: sender,
		Name:     "Contract Sub",
		Provider: "openai",
		BaseURL:  "https://api.example.com",
		APIKey:   "sk-contract-test",
		Model:    "gpt-4o",
	}
	if err := cs.Add(sub); err != nil {
		t.Fatalf("Add subscription: %v", err)
	}
	return sub
}

// requireNilSubscription asserts a subscription id no longer resolves.
func requireNilSubscription(t *testing.T, cs ConfigStore, id string) {
	t.Helper()
	got, err := cs.Get(id)
	if err != nil {
		t.Fatalf("Get(%q) after remove: %v", id, err)
	}
	if got != nil {
		t.Errorf("Get(%q) after remove = %+v, want nil", id, got)
	}
}
