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
