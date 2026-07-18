package serverapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"xbot/agent"
	"xbot/channel"
	"xbot/channel/web"
	"xbot/config"
	llm "xbot/llm"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

func newTestConfig() *config.Config {
	enableAutoCompress := false
	return &config.Config{
		LLM: config.LLMConfig{
			Provider: "openai",
			APIKey:   "sk-test",
			Model:    "gpt-4.1",
			BaseURL:  "https://api.example.com/v1",
		},
		Sandbox: config.SandboxConfig{Mode: "docker"},
		Agent: config.AgentConfig{
			MemoryProvider:     "flat",
			ContextMode:        "manual",
			MaxIterations:      321,
			MaxConcurrency:     7,
			MaxContextTokens:   456789,
			EnableAutoCompress: &enableAutoCompress,
		},
		TavilyAPIKey: "tv-test",
	}
}

func TestSessionKeyOwnerUsesLastSlashForCLIAbsolutePaths(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{key: "cli:/repo/project:Agent-main/review:1", want: "/repo/project:Agent-main"},
		{key: "agent:cli:/repo/project:Agent-main/review:1/fix:2", want: "cli:/repo/project:Agent-main/review:1"},
		{key: "web:chat_123/explore", want: "chat_123"},
	}
	for _, tc := range cases {
		if got := sessionKeyOwner(tc.key); got != tc.want {
			t.Fatalf("sessionKeyOwner(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestKillBackgroundTaskRejectsForeignRootSession(t *testing.T) {
	manager := tools.NewBackgroundTaskManager()
	task := manager.Start("web:web-foreign", "web-foreign", "wait", func(ctx context.Context, output func(string)) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	})
	t.Cleanup(func() { _ = manager.Kill(task.ID) })

	ag := &agent.Agent{}
	ag.SetBgTaskManager(manager)
	table := BuildRPCTable(&config.Config{}, ag, nil, nil, nil)
	params, err := json.Marshal(map[string]string{"task_id": task.ID})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRPCCtxResolved(context.Background(), "web-2", "web-2", 2, "user")
	if _, err := table.Dispatch(ctx, "kill_bg_task", params); err == nil || err.Error() != "access denied" {
		t.Fatalf("kill_bg_task error = %v, want access denied", err)
	}
	status, err := manager.Status(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != tools.BgTaskRunning {
		t.Fatalf("foreign task status = %s, want %s", status.Status, tools.BgTaskRunning)
	}
}

func TestIsInteractiveSubAgentTenant(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		chatID  string
		want    bool
	}{
		{
			name:    "agent channel tenant",
			channel: "agent",
			chatID:  "cli:/repo:Agent-main/explore:oneshot",
			want:    true,
		},
		{
			name:    "qualified interactive key",
			channel: "cli",
			chatID:  "cli:/repo:Agent-main/explore:oneshot",
			want:    true,
		},
		{
			name:    "cross channel qualified interactive key",
			channel: "web",
			chatID:  "cli:/repo:Agent-main/explore:oneshot",
			want:    true,
		},
		{
			name:    "normal cli session with slash path",
			channel: "cli",
			chatID:  "/repo/project:Agent-main",
			want:    false,
		},
		{
			name:    "web default session",
			channel: "web",
			chatID:  "admin",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInteractiveSubAgentTenant(tc.channel, tc.chatID); got != tc.want {
				t.Fatalf("isInteractiveSubAgentTenant(%q, %q) = %v, want %v", tc.channel, tc.chatID, got, tc.want)
			}
		})
	}
}

func TestDisplayLabelForTenantCLI(t *testing.T) {
	cases := []struct {
		name   string
		chatID string
		label  string
		want   string
	}{
		{
			name:   "db label wins",
			chatID: "/repo:Agent-main",
			label:  "custom label",
			want:   "custom label",
		},
		{
			name:   "named cli session",
			chatID: "/repo/project:Agent-main",
			want:   "Agent-main",
		},
		{
			name:   "default cli directory session",
			chatID: "/repo/project",
			want:   "project",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayLabelForTenant("cli", tc.chatID, tc.label); got != tc.want {
				t.Fatalf("displayLabelForTenant(cli, %q, %q) = %q, want %q", tc.chatID, tc.label, got, tc.want)
			}
		})
	}
}

func TestParseAgentTenantChatID(t *testing.T) {
	info, ok := parseAgentTenantChatID("cli:/repo/project:Agent-main/code-reviewer:oneshot-1")
	if !ok {
		t.Fatal("parseAgentTenantChatID returned !ok")
	}
	if info.parentChannel != "cli" {
		t.Fatalf("parentChannel = %q", info.parentChannel)
	}
	if info.parentChatID != "/repo/project:Agent-main" {
		t.Fatalf("parentChatID = %q", info.parentChatID)
	}
	if info.role != "code-reviewer" {
		t.Fatalf("role = %q", info.role)
	}
	if info.instance != "oneshot-1" {
		t.Fatalf("instance = %q", info.instance)
	}

	info, ok = parseAgentTenantChatID("web:chat_123/explore")
	if !ok {
		t.Fatal("parseAgentTenantChatID without instance returned !ok")
	}
	if info.parentChannel != "web" || info.parentChatID != "chat_123" || info.role != "explore" || info.instance != "" {
		t.Fatalf("unexpected no-instance parse: %#v", info)
	}
}

func TestBuildSessionTreeAttachesAgentToParent(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{{
			ChatID: "cli:/repo/project:Agent-main/review:oneshot-1", Channel: "agent",
			Type: "agent", ParentChannel: "cli", ParentChatID: "/repo/project:Agent-main",
			Role: "review", Instance: "oneshot-1",
		}},
	)
	if len(tree.Sessions) != 1 {
		t.Fatalf("len(tree.Sessions) = %d", len(tree.Sessions))
	}
	if len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected one child, got %#v", tree.Sessions[0].Children)
	}
	if tree.Sessions[0].Children[0].ChatID != "cli:/repo/project:Agent-main/review:oneshot-1" {
		t.Fatalf("wrong child attached: %#v", tree.Sessions[0].Children[0])
	}
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
}

func TestBuildSessionTreeAttachesLegacyCLIParentByUniqueSessionName(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{{
			ChatID: "cli:Agent-main/review:oneshot-1", Channel: "agent",
			Type: "agent", ParentChannel: "cli", ParentChatID: "Agent-main",
			Role: "review", Instance: "oneshot-1",
		}},
	)
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected legacy child attached by unique session name, got %#v", tree)
	}
	if tree.Sessions[0].Children[0].ParentChatID != "/repo/project:Agent-main" {
		t.Fatalf("child parent was not canonicalized: %#v", tree.Sessions[0].Children[0])
	}
}

func TestBuildSessionTreeNormalizesAgentRowsFromChatID(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{{
			ChatID:  "cli:/repo/project:Agent-main/review:oneshot-1",
			Channel: "agent",
			Label:   "default",
			Preview: "checking code",
		}},
	)
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected normalized child attached, got %#v", tree)
	}
	child := tree.Sessions[0].Children[0]
	if child.Type != "agent" || child.ParentChannel != "cli" || child.ParentChatID != "/repo/project:Agent-main" {
		t.Fatalf("child was not normalized: %#v", child)
	}
	if child.Role != "review" || child.Instance != "oneshot-1" {
		t.Fatalf("child role/instance not parsed: %#v", child)
	}
	if child.FullKey != "cli:/repo/project:Agent-main/review:oneshot-1" {
		t.Fatalf("child full key not exposed: %#v", child)
	}
	if child.Label == "default" || child.Label == "" {
		t.Fatalf("child label was not normalized: %#v", child)
	}
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
}

func TestBuildSessionTreePartitionsSubAgentRowsFromMains(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{
			{
				ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
			},
			{
				ChatID:  "cli:/repo/project:Agent-main/review:oneshot-1",
				Channel: "web",
				Label:   "default",
				Preview: "checking code",
			},
		},
		nil,
	)
	if len(tree.Sessions) != 1 {
		t.Fatalf("expected only main session at top level, got %#v", tree.Sessions)
	}
	if tree.Sessions[0].ChatID != "/repo/project:Agent-main" {
		t.Fatalf("wrong top-level session: %#v", tree.Sessions[0])
	}
	if len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected SubAgent child, got %#v", tree.Sessions[0].Children)
	}
	child := tree.Sessions[0].Children[0]
	if child.ChatID != "cli:/repo/project:Agent-main/review:oneshot-1" || child.Label != "review/oneshot-1" {
		t.Fatalf("wrong child attached: %#v", child)
	}
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
}

func TestNormalizeSubAgentRowIgnoresStaleMainParentMetadata(t *testing.T) {
	row, ok := normalizeSubAgentRow(web.UserChatWithPreview{
		ChatID:        "cli:/repo/project:Agent-main/review:oneshot-1",
		Channel:       "agent",
		ParentChannel: "web",
		ParentChatID:  "stale",
		Role:          "stale-role",
		Instance:      "stale-instance",
	}, "")
	if !ok {
		t.Fatal("normalizeSubAgentRow returned !ok")
	}
	if row.ParentChannel != "cli" || row.ParentChatID != "/repo/project:Agent-main" {
		t.Fatalf("stale main parent metadata should not override full key: %#v", row)
	}
	if row.Role != "review" || row.Instance != "oneshot-1" {
		t.Fatalf("stale role/instance was not overwritten: %#v", row)
	}
}

func TestNormalizeSubAgentRowPreservesExplicitAgentParentMetadata(t *testing.T) {
	row, ok := normalizeSubAgentRow(web.UserChatWithPreview{
		ChatID:        "agent:cli:/repo/project:Agent-main/review:1/fix:2",
		Channel:       "agent",
		ParentChannel: "agent",
		ParentChatID:  "cli:/repo/project:Agent-main/review:1",
	}, "")
	if !ok {
		t.Fatal("normalizeSubAgentRow returned !ok")
	}
	if row.ParentChannel != "agent" || row.ParentChatID != "cli:/repo/project:Agent-main/review:1" {
		t.Fatalf("explicit agent parent metadata was not preserved: %#v", row)
	}
}

func TestNormalizeSubAgentRowParsesExplicitFullKey(t *testing.T) {
	row, ok := normalizeSubAgentRow(web.UserChatWithPreview{
		ChatID:  "row-id",
		FullKey: "cli:/repo/project:Agent-main/review:oneshot-1",
		Channel: "agent",
	}, "")
	if !ok {
		t.Fatal("normalizeSubAgentRow returned !ok")
	}
	if row.ChatID != "cli:/repo/project:Agent-main/review:oneshot-1" || row.FullKey != "cli:/repo/project:Agent-main/review:oneshot-1" {
		t.Fatalf("unexpected ids: %#v", row)
	}
	if row.ParentChannel != "cli" || row.ParentChatID != "/repo/project:Agent-main" || row.Role != "review" || row.Instance != "oneshot-1" {
		t.Fatalf("explicit full key was not parsed: %#v", row)
	}
}

func TestBuildSessionTreeKeepsAmbiguousLegacyCLIParentOrphan(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{
			{ChatID: "/repo/a:Agent-main", Channel: "cli", Label: "Agent-main"},
			{ChatID: "/repo/b:Agent-main", Channel: "cli", Label: "Agent-main"},
		},
		[]web.UserChatWithPreview{{
			ChatID: "cli:Agent-main/review:oneshot-1", Channel: "agent",
			Type: "agent", ParentChannel: "cli", ParentChatID: "Agent-main",
			Role: "review", Instance: "oneshot-1",
		}},
	)
	if len(tree.OrphanSubAgents) != 1 {
		t.Fatalf("expected ambiguous legacy child to remain orphan, got %#v", tree)
	}
}

func TestListCLIChatSessionsUsesLocalSessionStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	sessionsDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionData := `{
  "dir": "/repo/project",
  "sessions": [
    {"name":"default","chat_id":"/repo/project","created_at":"2026-07-01T00:00:00Z"},
    {"name":"Agent-main","chat_id":"/repo/project:Agent-main","created_at":"2026-07-02T00:00:00Z"}
  ],
  "last_active": "/repo/project:Agent-main"
}`
	if err := os.WriteFile(filepath.Join(sessionsDir, "test.json"), []byte(sessionData), 0o600); err != nil {
		t.Fatal(err)
	}

	db := newTenantPreviewDB(t)
	insertTenant(t, db, "cli", "/repo/project:Agent-main", "2026-07-08T10:00:00Z", "db label", "latest preview")
	insertTenant(t, db, "cli", "cli:/repo/project:Agent-main/review:1", "2026-07-08T11:00:00Z", "", "should not be main")
	insertTenant(t, db, "agent", "cli:/repo/project:Agent-main/review:1", "2026-07-08T11:00:00Z", "", "agent preview")

	rows, err := listCLIChatSessions(db, "/repo/project:Agent-main")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, rows = %#v", len(rows), rows)
	}
	if rows[0].ChatID != "/repo/project:Agent-main" || rows[0].Label != "db label" || rows[0].Preview != "latest preview" {
		t.Fatalf("unexpected first row: %#v", rows[0])
	}
	for _, row := range rows {
		if row.ChatID == "cli:/repo/project:Agent-main/review:1" {
			t.Fatalf("interactive key leaked as main session: %#v", rows)
		}
	}

	agents, err := listTenantsByChannel(db, "agent", "")
	if err != nil {
		t.Fatal(err)
	}
	tree := buildSessionTree(rows, agents)
	if len(tree.Sessions) == 0 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected agent child attached to local CLI parent, got %#v", tree)
	}
}

func TestListCLIChatSessionsKeepsDBOnlyParents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	sessionsDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionData := `{
  "dir": "/repo/local",
  "sessions": [
    {"name":"default","chat_id":"/repo/local","created_at":"2026-07-01T00:00:00Z"}
  ]
}`
	if err := os.WriteFile(filepath.Join(sessionsDir, "test.json"), []byte(sessionData), 0o600); err != nil {
		t.Fatal(err)
	}

	db := newTenantPreviewDB(t)
	insertTenant(t, db, "cli", "/repo/db-only:Agent-main", "2026-07-08T10:00:00Z", "", "parent preview")
	insertTenant(t, db, "agent", "cli:/repo/db-only:Agent-main/review:1", "2026-07-08T11:00:00Z", "", "agent preview")

	rows, err := listCLIChatSessions(db, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ChatID == "/repo/db-only:Agent-main" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("DB-only CLI parent was dropped: %#v", rows)
	}
	agents, err := listTenantsByChannel(db, "agent", "")
	if err != nil {
		t.Fatal(err)
	}
	tree := buildSessionTree(rows, agents)
	var attached bool
	for _, node := range tree.Sessions {
		if node.ChatID == "/repo/db-only:Agent-main" && len(node.Children) == 1 {
			attached = true
		}
	}
	if !attached {
		t.Fatalf("expected DB-only parent to receive agent child, got %#v", tree)
	}
}

func TestBuildSessionTreeKeepsOrphanSubAgents(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "parent", Channel: "web", Label: "parent",
		}},
		[]web.UserChatWithPreview{{
			ChatID: "feishu:missing/review", Channel: "agent",
			Type: "agent", ParentChannel: "feishu", ParentChatID: "missing",
			Role: "review",
		}},
	)
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 0 {
		t.Fatalf("unexpected attached tree: %#v", tree)
	}
	if len(tree.OrphanSubAgents) != 1 || tree.OrphanSubAgents[0].ParentChatID != "missing" {
		t.Fatalf("expected orphan child, got %#v", tree.OrphanSubAgents)
	}
}

func TestBuildSessionTreeSynthesizesMissingCLIParent(t *testing.T) {
	tree := buildSessionTree(
		nil,
		[]web.UserChatWithPreview{{
			ChatID:     "cli:/repo/project:Agent-main/review:oneshot-1",
			Channel:    "agent",
			LastActive: "2026-07-08T10:00:00Z",
			Preview:    "agent preview",
		}},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 {
		t.Fatalf("expected synthesized parent, got %#v", tree)
	}
	parent := tree.Sessions[0]
	if parent.Channel != "cli" || parent.ChatID != "/repo/project:Agent-main" || parent.Label != "Agent-main" {
		t.Fatalf("unexpected synthesized parent: %#v", parent.UserChatWithPreview)
	}
	if !parent.Synthetic {
		t.Fatalf("synthesized parent must be marked synthetic: %#v", parent.UserChatWithPreview)
	}
	if len(parent.Children) != 1 || parent.Children[0].ParentChatID != parent.ChatID {
		t.Fatalf("child not attached to synthesized parent: %#v", tree)
	}
}

func TestBuildSessionTreeSynthesizesMissingWebParent(t *testing.T) {
	tree := buildSessionTree(
		nil,
		[]web.UserChatWithPreview{{
			ChatID:     "web:chat_123/explore:sub2",
			Channel:    "agent",
			LastActive: "2026-07-08T10:00:00Z",
			Preview:    "agent preview",
		}},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 {
		t.Fatalf("expected synthesized parent, got %#v", tree)
	}
	parent := tree.Sessions[0]
	if parent.Channel != "web" || parent.ChatID != "chat_123" {
		t.Fatalf("unexpected synthesized parent: %#v", parent.UserChatWithPreview)
	}
	if !parent.Synthetic {
		t.Fatalf("synthesized parent must be marked synthetic: %#v", parent.UserChatWithPreview)
	}
	if len(parent.Children) != 1 || parent.Children[0].ParentChatID != parent.ChatID {
		t.Fatalf("child not attached to synthesized parent: %#v", tree)
	}
}

func TestBuildSessionTreeAttachesNestedAgentToAgentParent(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{
			{
				ChatID:     "cli:/repo/project:Agent-main/review:1",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:00Z",
			},
			{
				ChatID:     "agent:cli:/repo/project:Agent-main/review:1/fix:2",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:01Z",
			},
		},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected root child, got %#v", tree)
	}
	review := tree.Sessions[0].Children[0]
	if review.ParentChannel != "cli" || review.ParentChatID != "/repo/project:Agent-main" {
		t.Fatalf("unexpected review parent: %#v", review)
	}
	if len(review.Children) != 1 {
		t.Fatalf("expected nested child, got %#v", review.Children)
	}
	fix := review.Children[0]
	if fix.ParentChannel != "agent" || fix.ParentChatID != review.ChatID || fix.Role != "fix" || fix.Instance != "2" {
		t.Fatalf("unexpected nested child: %#v", fix)
	}
}

func TestBuildSessionTreePreservesExplicitAgentParentMetadata(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{
			{
				ChatID:     "cli:/repo/project:Agent-main/review:1",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:00Z",
			},
			{
				ChatID:        "cli:/repo/project:Agent-main/fix:2",
				FullKey:       "cli:/repo/project:Agent-main/fix:2",
				Channel:       "agent",
				ParentChannel: "agent",
				ParentChatID:  "cli:/repo/project:Agent-main/review:1",
				LastActive:    "2026-07-08T10:00:01Z",
				Preview:       "patching",
			},
		},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	review := tree.Sessions[0].Children[0]
	if len(review.Children) != 1 {
		t.Fatalf("expected explicit-parent child under review, got %#v", review.Children)
	}
	fix := review.Children[0]
	if fix.ParentChannel != "agent" || fix.ParentChatID != review.ChatID {
		t.Fatalf("explicit parent metadata was not preserved: %#v", fix)
	}
	if fix.Label != "fix/2" || fix.Preview != "patching" {
		t.Fatalf("label/preview should remain separate: %#v", fix)
	}
}

func TestBuildSessionTreeMatchesNestedParentByFullKeyAlias(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{
			{
				ChatID:     "row-review",
				FullKey:    "cli:/repo/project:Agent-main/review:1",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:00Z",
			},
			{
				ChatID:        "row-fix",
				FullKey:       "agent:cli:/repo/project:Agent-main/review:1/fix:2",
				Channel:       "agent",
				ParentChannel: "agent",
				ParentChatID:  "cli:/repo/project:Agent-main/review:1",
				LastActive:    "2026-07-08T10:00:01Z",
			},
		},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	review := tree.Sessions[0].Children[0]
	if review.ChatID != "cli:/repo/project:Agent-main/review:1" || review.FullKey != "cli:/repo/project:Agent-main/review:1" {
		t.Fatalf("unexpected review: %#v", review)
	}
	if len(review.Children) != 1 {
		t.Fatalf("expected nested child by full key alias, got %#v", review.Children)
	}
	if review.Children[0].ChatID != "agent:cli:/repo/project:Agent-main/review:1/fix:2" || review.Children[0].FullKey != "agent:cli:/repo/project:Agent-main/review:1/fix:2" {
		t.Fatalf("unexpected nested child: %#v", review.Children[0])
	}
}

func TestBuildSessionTreeMatchesNestedParentByFullKeyWhenParentCameFromMains(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{
			{
				ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
			},
			{
				ChatID:     "row-review",
				FullKey:    "cli:/repo/project:Agent-main/review:1",
				Channel:    "web",
				Label:      "default",
				LastActive: "2026-07-08T10:00:00Z",
			},
			{
				ChatID:        "row-fix",
				FullKey:       "agent:cli:/repo/project:Agent-main/review:1/fix:2",
				Channel:       "web",
				Label:         "default",
				ParentChannel: "agent",
				ParentChatID:  "cli:/repo/project:Agent-main/review:1",
				LastActive:    "2026-07-08T10:00:01Z",
			},
		},
		nil,
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected review under main only, got %#v", tree)
	}
	review := tree.Sessions[0].Children[0]
	if review.ChatID != "cli:/repo/project:Agent-main/review:1" || review.Label != "review/1" {
		t.Fatalf("unexpected review child: %#v", review)
	}
	if len(review.Children) != 1 || review.Children[0].ChatID != "agent:cli:/repo/project:Agent-main/review:1/fix:2" || review.Children[0].Label != "fix/2" {
		t.Fatalf("expected fix under review, got %#v", review.Children)
	}
}

func TestBuildSessionTreeKeepsSameAgentInstanceUnderDifferentParents(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{
			{ChatID: "/repo/a:Agent-main", Channel: "cli", Label: "Agent A"},
			{ChatID: "/repo/b:Agent-main", Channel: "cli", Label: "Agent B"},
		},
		[]web.UserChatWithPreview{
			{ChatID: "cli:/repo/a:Agent-main/review:1", Channel: "agent", LastActive: "2026-07-08T10:00:00Z"},
			{ChatID: "cli:/repo/b:Agent-main/review:1", Channel: "agent", LastActive: "2026-07-08T10:00:01Z"},
		},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	for _, node := range tree.Sessions {
		if len(node.Children) != 1 || node.Children[0].Role != "review" || node.Children[0].Instance != "1" {
			t.Fatalf("each parent should keep its own review/1 child, got %#v", tree)
		}
	}
}

func TestBuildSessionTreeAliasesSurviveMainSliceGrowth(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/real:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{
			{
				ChatID:     "cli:/repo/missing:Agent-other/review:1",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:00Z",
			},
			{
				ChatID:     "cli:/repo/real:Agent-main/fix:1",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:01Z",
			},
		},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	var foundReal bool
	for _, node := range tree.Sessions {
		if node.ChatID != "/repo/real:Agent-main" {
			continue
		}
		foundReal = true
		if len(node.Children) != 1 || node.Children[0].ChatID != "cli:/repo/real:Agent-main/fix:1" {
			t.Fatalf("real parent lost child after main slice growth: %#v", tree)
		}
	}
	if !foundReal {
		t.Fatalf("real parent missing: %#v", tree)
	}
}

func TestBuildSessionTreeAliasesSurviveChildSliceGrowth(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{
			{
				ChatID:     "cli:/repo/project:Agent-main/review:1",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:00Z",
			},
			{
				ChatID:     "cli:/repo/project:Agent-main/lint:1",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:01Z",
			},
			{
				ChatID:     "agent:cli:/repo/project:Agent-main/review:1/fix:2",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:02Z",
			},
		},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 2 {
		t.Fatalf("expected two direct children, got %#v", tree)
	}
	var review *web.UserChatWithPreview
	for i := range tree.Sessions[0].Children {
		if tree.Sessions[0].Children[i].Role == "review" {
			review = &tree.Sessions[0].Children[i]
		}
	}
	if review == nil || len(review.Children) != 1 || review.Children[0].Role != "fix" {
		t.Fatalf("nested child was not attached to review after child slice growth: %#v", tree)
	}
}

func TestBuildSessionTreeUsesFullKeyWhenAgentChatIDIsReused(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{
			{ChatID: "/repo/a:Agent-main", Channel: "cli", Label: "Agent A"},
			{ChatID: "/repo/b:Agent-main", Channel: "cli", Label: "Agent B"},
		},
		[]web.UserChatWithPreview{
			{ChatID: "review-row", FullKey: "cli:/repo/a:Agent-main/review:1", Channel: "agent", LastActive: "2026-07-08T10:00:00Z"},
			{ChatID: "review-row", FullKey: "cli:/repo/b:Agent-main/review:1", Channel: "agent", LastActive: "2026-07-08T10:00:01Z"},
			{ChatID: "fix-row-a", FullKey: "agent:cli:/repo/a:Agent-main/review:1/fix:2", Channel: "agent", ParentChannel: "agent", ParentChatID: "cli:/repo/a:Agent-main/review:1", LastActive: "2026-07-08T10:00:02Z"},
			{ChatID: "fix-row-b", FullKey: "agent:cli:/repo/b:Agent-main/review:1/fix:2", Channel: "agent", ParentChannel: "agent", ParentChatID: "cli:/repo/b:Agent-main/review:1", LastActive: "2026-07-08T10:00:03Z"},
		},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	for _, node := range tree.Sessions {
		if len(node.Children) != 1 {
			t.Fatalf("expected one review child per parent, got %#v", tree)
		}
		review := node.Children[0]
		if review.ChatID != "cli:"+node.ChatID+"/review:1" || len(review.Children) != 1 {
			t.Fatalf("expected nested fix under reused review-row for %s, got %#v", node.ChatID, review)
		}
		wantPrefix := "agent:cli:" + node.ChatID + "/review:1/fix:2"
		if review.Children[0].FullKey != wantPrefix {
			t.Fatalf("fix attached to wrong reused review row: parent=%s child=%#v", node.ChatID, review.Children[0])
		}
	}
}

func TestBuildSessionTreeExplicitAgentParentSortsBeforeChild(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{
			{
				ChatID:        "row-fix",
				FullKey:       "cli:/repo/project:Agent-main/fix:2",
				Channel:       "agent",
				ParentChannel: "agent",
				ParentChatID:  "cli:/repo/project:Agent-main/review:1",
				LastActive:    "2026-07-08T10:00:01Z",
			},
			{
				ChatID:     "row-review",
				FullKey:    "cli:/repo/project:Agent-main/review:1",
				Channel:    "agent",
				LastActive: "2026-07-08T10:00:00Z",
			},
		},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected one review child, got %#v", tree)
	}
	review := tree.Sessions[0].Children[0]
	if review.ChatID != "cli:/repo/project:Agent-main/review:1" || review.Synthetic {
		t.Fatalf("expected real review parent, got %#v", review)
	}
	if len(review.Children) != 1 || review.Children[0].ChatID != "cli:/repo/project:Agent-main/fix:2" {
		t.Fatalf("expected fix under real review without duplicate synthetic parent, got %#v", review.Children)
	}
}

func TestBuildSessionTreeSynthesizesMissingNestedAgentParent(t *testing.T) {
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID: "/repo/project:Agent-main", Channel: "cli", Label: "Agent-main",
		}},
		[]web.UserChatWithPreview{{
			ChatID:     "agent:cli:/repo/project:Agent-main/review:1/fix:2",
			Channel:    "agent",
			LastActive: "2026-07-08T10:00:01Z",
			Preview:    "fixing",
		}},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected synthesized agent parent, got %#v", tree)
	}
	review := tree.Sessions[0].Children[0]
	if review.ChatID != "cli:/repo/project:Agent-main/review:1" || review.ParentChannel != "cli" || review.ParentChatID != "/repo/project:Agent-main" {
		t.Fatalf("unexpected synthesized review: %#v", review)
	}
	if !review.Synthetic || review.Role != "review" || review.Instance != "1" {
		t.Fatalf("synthesized agent metadata missing: %#v", review)
	}
	if len(review.Children) != 1 {
		t.Fatalf("expected nested child under synthesized agent, got %#v", review.Children)
	}
	fix := review.Children[0]
	if fix.ChatID != "agent:cli:/repo/project:Agent-main/review:1/fix:2" || fix.Role != "fix" || fix.Instance != "2" {
		t.Fatalf("unexpected nested child: %#v", fix)
	}
}

func TestBuildSessionTreeSynthesizesMissingCLIParentForHistoricalAgent(t *testing.T) {
	tree := buildSessionTree(
		nil,
		[]web.UserChatWithPreview{{
			ChatID:     "cli:/vePFS-Mindverse/user/intern/yihang:Agent-brave-panda/code-reviewer:oneshot-code-reviewer-1",
			Channel:    "agent",
			LastActive: "2026-07-06T02:24:54Z",
			Preview:    "reviewing",
		}},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 {
		t.Fatalf("expected synthesized CLI parent, got %#v", tree.Sessions)
	}
	parent := tree.Sessions[0]
	if parent.Channel != "cli" || parent.ChatID != "/vePFS-Mindverse/user/intern/yihang:Agent-brave-panda" || !parent.Synthetic {
		t.Fatalf("unexpected synthesized parent: %#v", parent)
	}
	if parent.Label != "Agent-brave-panda" {
		t.Fatalf("expected CLI session name label, got %#v", parent)
	}
	if len(parent.Children) != 1 {
		t.Fatalf("expected SubAgent child, got %#v", parent.Children)
	}
	child := parent.Children[0]
	if child.Label != "code-reviewer/oneshot-code-reviewer-1" || child.ParentChannel != "cli" || child.ParentChatID != parent.ChatID {
		t.Fatalf("unexpected child: %#v", child)
	}
}

func TestSubAgentRowFromNestedInteractiveSessionUsesFullKeyParent(t *testing.T) {
	row := subAgentRowFromInteractiveSession("agent", agent.InteractiveSessionInfo{
		ChatID:   "web:web-chat/review:1",
		Key:      "agent:web:web-chat/review:1/fix:2",
		Role:     "fix",
		Instance: "2",
		Preview:  "patching",
		Running:  true,
	})
	if row.ChatID != "agent:web:web-chat/review:1/fix:2" {
		t.Fatalf("unexpected chat id: %#v", row)
	}
	if row.ParentChannel != "agent" || row.ParentChatID != "web:web-chat/review:1" {
		t.Fatalf("unexpected parent: %#v", row)
	}
	if row.Role != "fix" || row.Instance != "2" || !row.Running || row.Historical {
		t.Fatalf("unexpected row fields: %#v", row)
	}
	if row.Label != "fix/2" || row.Preview != "patching" {
		t.Fatalf("label and preview should be separate: %#v", row)
	}
}

func TestSubAgentRowFromInteractiveSessionUsesDirectParentMetadata(t *testing.T) {
	row := subAgentRowFromInteractiveSession("agent", agent.InteractiveSessionInfo{
		ChatID:        "web:web-chat/review:1",
		Key:           "agent:web:web-chat/review:1/fix:2",
		ParentKey:     "web:web-chat/review:1",
		ParentChannel: "agent",
		ParentChatID:  "web:web-chat/review:1",
		Role:          "fix",
		Instance:      "2",
		Preview:       "patching",
		Running:       true,
	})
	if row.ChatID != "agent:web:web-chat/review:1/fix:2" || row.FullKey != row.ChatID {
		t.Fatalf("unexpected chat id: %#v", row)
	}
	if row.ParentChannel != "agent" || row.ParentChatID != "web:web-chat/review:1" {
		t.Fatalf("unexpected direct parent metadata: %#v", row)
	}
	if row.Label == "default" || row.Role != "fix" || row.Instance != "2" {
		t.Fatalf("unexpected label: %#v", row)
	}
}

func TestBuildSessionTreeUsesLiveOneShotSubAgentRows(t *testing.T) {
	row := subAgentRowFromInteractiveSession("cli", agent.InteractiveSessionInfo{
		ChatID:   "/repo:Agent-main",
		Key:      "cli:/repo:Agent-main/review:oneshot-review-1",
		Role:     "review",
		Instance: "oneshot-review-1",
		Preview:  "checking",
		Running:  false,
	})
	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID:     "/repo:Agent-main",
			Channel:    "cli",
			Label:      "Agent-main",
			LastActive: "2026-07-08T00:00:00Z",
		}},
		[]web.UserChatWithPreview{row},
	)
	if len(tree.OrphanSubAgents) != 0 {
		t.Fatalf("unexpected orphans: %#v", tree.OrphanSubAgents)
	}
	if len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected one child under parent, got %#v", tree)
	}
	child := tree.Sessions[0].Children[0]
	if child.Channel != "agent" || child.ChatID != "cli:/repo:Agent-main/review:oneshot-review-1" {
		t.Fatalf("unexpected child identity: %#v", child)
	}
	if child.ParentChannel != "cli" || child.ParentChatID != "/repo:Agent-main" {
		t.Fatalf("unexpected parent metadata: %#v", child)
	}
	if child.Label != "review/oneshot-review-1" || child.Label == "default" {
		t.Fatalf("unexpected child label: %#v", child)
	}
}

func TestListTenantsByChannelReturnsPersistedOneShotSubAgentRows(t *testing.T) {
	db := newTenantPreviewDB(t)
	insertTenant(
		t,
		db,
		"agent",
		"cli:/repo:Agent-main/review:oneshot-review-1",
		"2026-07-08T00:00:01Z",
		"",
		"review result",
	)
	rows, err := listTenantsByChannel(db, "agent", "")
	if err != nil {
		t.Fatalf("listTenantsByChannel: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one agent row, got %#v", rows)
	}
	row := rows[0]
	if row.Channel != "agent" || row.Type != "agent" || row.FullKey != row.ChatID {
		t.Fatalf("unexpected agent identity: %#v", row)
	}
	if row.ParentChannel != "cli" || row.ParentChatID != "/repo:Agent-main" {
		t.Fatalf("unexpected parent metadata: %#v", row)
	}
	if row.Role != "review" || row.Instance != "oneshot-review-1" || row.Label != "review/oneshot-review-1" {
		t.Fatalf("unexpected label fields: %#v", row)
	}

	tree := buildSessionTree(
		[]web.UserChatWithPreview{{
			ChatID:     "/repo:Agent-main",
			Channel:    "cli",
			Label:      "Agent-main",
			LastActive: "2026-07-08T00:00:00Z",
		}},
		rows,
	)
	if len(tree.OrphanSubAgents) != 0 || len(tree.Sessions) != 1 || len(tree.Sessions[0].Children) != 1 {
		t.Fatalf("expected persisted SubAgent under parent, got %#v", tree)
	}
}

func TestSubAgentRowBelongsToAllowedWebParentFollowsNestedAgentChain(t *testing.T) {
	row, ok := normalizeSubAgentRow(web.UserChatWithPreview{
		ChatID: "agent:web:web-chat/review:1/fix:2",
	}, "")
	if !ok {
		t.Fatal("normalizeSubAgentRow returned !ok")
	}
	if !subAgentRowBelongsToAllowedWebParent(row, map[string]bool{"web-chat": true}) {
		t.Fatalf("expected nested row to belong to web-chat: %#v", row)
	}
	if subAgentRowBelongsToAllowedWebParent(row, map[string]bool{"other": true}) {
		t.Fatalf("unexpected access for wrong parent: %#v", row)
	}
}

func newTenantPreviewDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := `
CREATE TABLE tenants (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel TEXT NOT NULL,
  chat_id TEXT NOT NULL,
  last_active_at TEXT NOT NULL
);
CREATE TABLE user_chats (
  channel TEXT NOT NULL,
  sender_id TEXT NOT NULL,
  chat_id TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT ''
);
CREATE TABLE session_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertTenant(t *testing.T, db *sql.DB, channel, chatID, lastActive, label, preview string) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tenants(channel, chat_id, last_active_at) VALUES (?, ?, ?)`, channel, chatID, lastActive)
	if err != nil {
		t.Fatal(err)
	}
	if label != "" {
		if _, err := db.Exec(`INSERT INTO user_chats(channel, sender_id, chat_id, label) VALUES (?, 'user', ?, ?)`, channel, chatID, label); err != nil {
			t.Fatal(err)
		}
	}
	if preview != "" {
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO session_messages(tenant_id, role, content) VALUES (?, 'assistant', ?)`, id, preview); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseTenantTimeHandlesGoTimeString(t *testing.T) {
	got := parseTenantTime("2026-07-08 17:19:29.830941332 +0000 UTC m=+612.945459238")
	if got.IsZero() {
		t.Fatal("parseTenantTime returned zero")
	}
	if got.Format("2006-01-02T15:04:05") != "2026-07-08T17:19:29" {
		t.Fatalf("unexpected parsed time: %s", got.Format(time.RFC3339Nano))
	}
}

func TestListWebChatIDsForSenderIncludesDefaultAndCreatedChats(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Conn().Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES ('web', 'web-2', 'chat_abc', 'A')",
	); err != nil {
		t.Fatalf("insert user chat: %v", err)
	}
	got, err := listWebChatIDsForSender(db.Conn(), "web-2")
	if err != nil {
		t.Fatalf("listWebChatIDsForSender: %v", err)
	}
	if !got["web-2"] || !got["chat_abc"] {
		t.Fatalf("missing expected chat ids: %#v", got)
	}
	if got["web-1"] {
		t.Fatalf("unexpected other sender in chat ids: %#v", got)
	}
}

// TestHandleCLIRPCAdminAddSubscription_ListRoundTrip verifies that a subscription
// added via adminAddSubscription (SenderID="cli_user") is visible when listing
// with an empty senderID (which falls back to WS auth "admin").
// This was a real bug: openQuickSwitch passes senderID="" → server falls back
// to authSenderID "admin" → svc.List("admin") returns nothing because subs are
// stored under "cli_user".
func TestHandleCLIRPCAdminAddSubscription_ListRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Add subscription via admin path (same as remote CLI does)
	sub := channel.Subscription{
		Name: "test", Provider: "openai",
		BaseURL: "https://api.openai.com/v1", APIKey: "sk-test", Model: "gpt-4",
	}
	addParams, _ := json.Marshal(map[string]any{"sub": sub})
	if _, err := HandleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// List with empty senderID (simulates openQuickSwitch behavior)
	// Before fix: senderIDFromParams falls back to "admin" → empty list
	// After fix: should return the subscription
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	raw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list_subscriptions: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(raw, &subs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs) == 0 {
		t.Fatal("list_subscriptions returned empty, expected the subscription added by admin")
	}
	if subs[0].Name != "test" {
		t.Fatalf("expected subscription name 'test', got %q", subs[0].Name)
	}
}

// TestHandleCLIRPCAddSubscription_PreservesCredentials verifies that add_subscription
// RPC correctly deserializes base_url and api_key from the snake_case JSON payload.
// This was a real bug: rpc_table.go used sqlite.LLMSubscription (no JSON tags) to
// receive the RPC parameter, but the client sends channelSubscriptionJSON (with
// json:"base_url" / json:"api_key" tags). Go's json package couldn't match the
// fields → base_url and api_key were silently dropped (always empty).
func TestHandleCLIRPCAddSubscription_PreservesCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Use snake_case keys matching channelSubscriptionJSON — the format the real
	// backend sends via RPC (backend_impl.go UpdateSubscription).
	addParams, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name":     "codex",
			"provider": "openai",
			"base_url": "https://api.openai-proxy.org/v1",
			"api_key":  "sk-secret-key-12345",
			"model":    "gpt-5.5",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// List and verify base_url/api_key are preserved
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	raw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list_subscriptions: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(raw, &subs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs) == 0 {
		t.Fatal("list_subscriptions returned empty")
	}
	// subToChannel masks API key
	if subs[0].BaseURL != "https://api.openai-proxy.org/v1" {
		t.Fatalf("expected base_url 'https://api.openai-proxy.org/v1', got %q", subs[0].BaseURL)
	}
	if subs[0].APIKey != "sk-s****" {
		t.Fatalf("expected masked api_key 'sk-s****', got %q", subs[0].APIKey)
	}
}

func TestHandleCLIRPCAddSubscription_PreservesIDAndPerModelConfigs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	addParams, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"id":       "sub_ui_created",
			"name":     "codex",
			"provider": "openai",
			"base_url": "https://api.openai-proxy.org/v1",
			"api_key":  "sk-secret-key-12345",
			"per_model_configs": map[string]any{
				"glm-5.2": map[string]any{
					"max_context":       1000000,
					"max_output_tokens": 8192,
					"api_type":          "responses",
				},
			},
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	sub, err := subSvc.Get("sub_ui_created")
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if sub == nil {
		t.Fatal("expected subscription with client-provided ID")
	}
	pmc, ok := sub.PerModelConfigs["glm-5.2"]
	if !ok {
		t.Fatal("expected per-model config to be preserved")
	}
	if pmc.MaxContext != 1000000 || pmc.MaxOutputTokens != 8192 || pmc.APIType != "responses" {
		t.Fatalf("unexpected per-model config: %+v", pmc)
	}
}

// TestHandleCLIRPCUpdateSubscription_PreservesCredentials verifies that
// update_subscription RPC correctly deserializes and preserves base_url and api_key.
func TestHandleCLIRPCUpdateSubscription_PreservesCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Add a subscription first (using snake_case matching real client)
	addParams, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name":     "codex",
			"provider": "openai",
			"base_url": "https://api.openai-proxy.org/v1",
			"api_key":  "sk-secret-key-12345",
			"model":    "gpt-5.5",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// Get the subscription ID via list
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	listRaw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list_subscriptions: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(listRaw, &subs); err != nil || len(subs) == 0 {
		t.Fatalf("unmarshal list: %v", err)
	}
	subID := subs[0].ID

	// Update the subscription with a new name but same credentials
	// Using snake_case matching real client (channelSubscriptionJSON tags)
	updateParams, _ := json.Marshal(map[string]any{
		"id": subID,
		"sub": map[string]any{
			"name":              "codex-updated",
			"provider":          "openai",
			"base_url":          "https://api.openai-proxy.org/v1",
			"api_key":           "sk-secret-key-12345",
			"model":             "gpt-5.5",
			"max_output_tokens": 0,
			"thinking_mode":     "",
		},
	})
	if _, err := HandleCLIRPC(table, "update_subscription", updateParams, "admin"); err != nil {
		t.Fatalf("update_subscription: %v", err)
	}

	// Verify base_url and api_key are preserved
	listRaw2, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list_subscriptions after update: %v", err)
	}
	var subs2 []channel.Subscription
	if err := json.Unmarshal(listRaw2, &subs2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs2) == 0 {
		t.Fatal("list_subscriptions returned empty after update")
	}
	if subs2[0].Name != "codex-updated" {
		t.Fatalf("expected name 'codex-updated', got %q", subs2[0].Name)
	}
	if subs2[0].BaseURL != "https://api.openai-proxy.org/v1" {
		t.Fatalf("expected base_url preserved, got %q", subs2[0].BaseURL)
	}
	if subs2[0].APIKey != "sk-s****" {
		t.Fatalf("expected masked api_key 'sk-s****', got %q", subs2[0].APIKey)
	}
}

func newTestBackendWithSettings(t *testing.T) (*agent.Agent, *sqlite.UserSettingsService) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.NewUserSettingsService(db)
	agentSvc := agent.NewSettingsService(store)
	ag := &agent.Agent{}
	ag.SetSettingsService(agentSvc)
	return ag, store
}

func TestMigrateCLIUserSettingsFromGlobalIfNeeded_SeedsOnlyWhenEmpty(t *testing.T) {
	cfg := newTestConfig()
	ag, store := newTestBackendWithSettings(t)
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, ag, "cli", "cli_user"); err != nil {
		t.Fatalf("migrateCLIUserSettingsFromGlobalIfNeeded() error = %v", err)
	}
	seeded, err := store.Get("cli", "cli_user")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if len(seeded) == 0 {
		t.Fatal("expected seeded settings, got none")
	}
	if seeded["context_mode"] != "manual" {
		t.Fatalf("context_mode = %q, want manual", seeded["context_mode"])
	}
	if seeded["theme"] != "midnight" {
		t.Fatalf("theme = %q, want midnight", seeded["theme"])
	}
	if seeded["enable_auto_compress"] != "false" {
		t.Fatalf("enable_auto_compress = %q, want false", seeded["enable_auto_compress"])
	}
	if _, ok := seeded["llm_model"]; ok {
		t.Fatalf("llm_model should not be seeded into user settings: %#v", seeded)
	}
}

func TestMigrateCLIUserSettingsFromGlobalIfNeeded_SkipsWhenUserAlreadyHasSettings(t *testing.T) {
	cfg := newTestConfig()
	ag, store := newTestBackendWithSettings(t)
	if err := store.Set("cli", "cli_user", "theme", "mono"); err != nil {
		t.Fatalf("store.Set() error = %v", err)
	}
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, ag, "cli", "cli_user"); err != nil {
		t.Fatalf("migrateCLIUserSettingsFromGlobalIfNeeded() error = %v", err)
	}
	vals, err := store.Get("cli", "cli_user")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if len(vals) != 1 || vals["theme"] != "mono" {
		t.Fatalf("expected existing settings to remain untouched, got %#v", vals)
	}
}

func TestApplyRuntimeSetting_UpdatesConfig(t *testing.T) {
	cfg := newTestConfig()
	var ag *agent.Agent // nil is fine — we only test cfg mutation
	// LLM fields (llm_model, llm_base_url) are no longer handled by
	// applyRuntimeSetting — they go through update_subscription RPC.
	// Test a non-LLM config mutation instead.
	applyRuntimeSetting(cfg, ag, "cli_user", "max_concurrency", "99")
	if cfg.Agent.MaxConcurrency != 99 {
		t.Fatalf("max_concurrency = %d, want %d", cfg.Agent.MaxConcurrency, 99)
	}
}

func TestAllRuntimeKeysHaveHandlers(t *testing.T) {
	missing := missingHandlerKeys()
	if len(missing) > 0 {
		t.Errorf("settingHandlerRegistry is missing handlers for keys in channel.CLIRuntimeSettingKeys: %v\n"+
			"Add entries to settingHandlerRegistry in setting_handlers.go for each missing key.", missing)
	}
}

func TestApplyRuntimeSetting_WarnsOnUnknownKey(t *testing.T) {
	cfg := newTestConfig()
	var ag *agent.Agent
	applyRuntimeSetting(cfg, ag, "cli_user", "totally_unknown_key", "value")
	// Should not panic, just log a warning
}

func TestHandleCLIRPCSetDefaultSubscriptionRefreshesSenderCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))
	// Admin's subscriptions are stored under cliSenderID ("cli_user") in production.
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-gpt", SenderID: "cli_user", Name: "gpt", Provider: "openai", BaseURL: "https://gpt.example/v1", APIKey: "sk-gpt", Model: "gpt-4.1", IsDefault: true}); err != nil {
		t.Fatalf("add gpt: %v", err)
	}
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-glm", SenderID: "cli_user", Name: "glm", Provider: "openai", BaseURL: "https://glm.example/v1", APIKey: "sk-glm", Model: "glm-5.1", IsDefault: false}); err != nil {
		t.Fatalf("add glm: %v", err)
	}
	// Explicitly seed user_default_model (Add no longer seeds it when IsDefault=true).
	if err := subSvc.SetDefault("sub-gpt"); err != nil {
		t.Fatalf("set default: %v", err)
	}

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)
	_, model, _, _, _ := factory.GetLLM("cli_user")
	if model != "gpt-4.1" {
		t.Fatalf("expected initial gpt model, got %q", model)
	}

	params, _ := json.Marshal(map[string]string{"id": "sub-glm"})
	if _, err := HandleCLIRPC(table, "set_default_subscription", params, "admin"); err != nil {
		t.Fatalf("HandleCLIRPC set_default_subscription: %v", err)
	}
	// Set user-level default model (model is user-level now, not sub.Model)
	setDefModel, _ := json.Marshal(map[string]any{"sub_id": "sub-glm", "model": "glm-5.1"})
	if _, err := HandleCLIRPC(table, "set_default_model", setDefModel, "admin"); err != nil {
		t.Fatalf("HandleCLIRPC set_default_model: %v", err)
	}
	_, model, _, _, _ = factory.GetLLM("cli_user")
	if model != "glm-5.1" {
		t.Fatalf("expected switched glm model, got %q", model)
	}
}

// TestHandleCLIRPCSetDefaultSubscription_CrossIdentity verifies that when
// the WS auth identity ("admin") differs from the subscription's business
// senderID ("cli_user"), the LLM factory cache is still updated correctly.
// This was a real bug: the server used senderIDFromParams (→ "admin") as
// the cache key instead of sub.SenderID ("cli_user"), so GetLLM("cli_user")
// kept returning the old client after a subscription switch.
func TestHandleCLIRPCSetDefaultSubscription_CrossIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))
	// Subscriptions belong to "cli_user" (business identity)
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-gpt", SenderID: "cli_user", Name: "gpt", Provider: "openai", BaseURL: "https://gpt.example/v1", APIKey: "sk-gpt", Model: "gpt-4.1", IsDefault: true}); err != nil {
		t.Fatalf("add gpt: %v", err)
	}
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-glm", SenderID: "cli_user", Name: "glm", Provider: "openai", BaseURL: "https://glm.example/v1", APIKey: "sk-glm", Model: "glm-5.1", IsDefault: false}); err != nil {
		t.Fatalf("add glm: %v", err)
	}
	// Explicitly seed user_default_model (Add no longer seeds it when IsDefault=true).
	if err := subSvc.SetDefault("sub-gpt"); err != nil {
		t.Fatalf("set default: %v", err)
	}

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)
	// Agent calls GetLLM with "cli_user" (business identity)
	_, model, _, _, _ := factory.GetLLM("cli_user")
	if model != "gpt-4.1" {
		t.Fatalf("expected initial gpt model for cli_user, got %q", model)
	}

	// RPC call with WS auth "admin", no sender_id in params (matches real CLI behavior)
	params, _ := json.Marshal(map[string]string{"id": "sub-glm"})
	if _, err := HandleCLIRPC(table, "set_default_subscription", params, "admin"); err != nil {
		t.Fatalf("HandleCLIRPC set_default_subscription: %v", err)
	}
	// Set user-level default model (model is user-level now)
	setDefModel, _ := json.Marshal(map[string]any{"sub_id": "sub-glm", "model": "glm-5.1"})
	if _, err := HandleCLIRPC(table, "set_default_model", setDefModel, "admin"); err != nil {
		t.Fatalf("HandleCLIRPC set_default_model: %v", err)
	}
	// The key assertion: GetLLM("cli_user") must see the new model
	_, model, _, _, _ = factory.GetLLM("cli_user")
	if model != "glm-5.1" {
		t.Fatalf("expected switched glm model for cli_user, got %q (LLM factory cached under wrong key)", model)
	}
}

// TestSelectModelRPC_UsesRequestedChannel verifies /su model selection writes the
// target channel tenant row instead of always writing cli:<chatID>.
func TestSelectModelRPC_UsesRequestedChannel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	tenantSvc := sqlite.NewTenantService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(tenantSvc)
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-feishu", SenderID: "cli_user", Name: "xin", Provider: "openai", BaseURL: "https://api.example/v1", APIKey: "sk-test", Model: ""}); err != nil {
		t.Fatalf("add sub: %v", err)
	}

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	chatID := "oc_bdfc1763e017e00ed4d7341de424f438"
	if err := tenantSvc.SetTenantSubscription("cli", chatID, "sub-cli", "old-cli-model"); err != nil {
		t.Fatalf("seed cli tenant: %v", err)
	}

	params, _ := json.Marshal(map[string]string{
		"sender_id": "cli_user",
		"channel":   "feishu",
		"chat_id":   chatID,
		"sub_id":    "sub-feishu",
		"model":     "glm-5.2",
	})
	if _, err := HandleCLIRPC(table, "select_model", params, "admin"); err != nil {
		t.Fatalf("select_model: %v", err)
	}

	subID, model, err := tenantSvc.GetTenantSubscription("feishu", chatID)
	if err != nil {
		t.Fatalf("get feishu tenant: %v", err)
	}
	if subID != "sub-feishu" || model != "glm-5.2" {
		t.Fatalf("feishu tenant = (%q,%q), want (sub-feishu,glm-5.2)", subID, model)
	}

	cliSubID, cliModel, err := tenantSvc.GetTenantSubscription("cli", chatID)
	if err != nil {
		t.Fatalf("get cli tenant: %v", err)
	}
	if cliSubID != "sub-cli" || cliModel != "old-cli-model" {
		t.Fatalf("cli tenant was changed to (%q,%q), want original (sub-cli,old-cli-model)", cliSubID, cliModel)
	}
}

func TestGetContextUsageRPC_ExactSnapshotFallbackAndAuthorization(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	ag, err := agent.New(agent.Config{
		LLM:            &llm.MockLLM{},
		Model:          "default-model",
		WorkDir:        dir,
		DBPath:         dir + "/xbot.db",
		SandboxMode:    "none",
		MemoryProvider: "flat",
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	defer ag.Close()

	db := ag.MultiSession().DB()
	subSvc := ag.LLMFactory().GetSubscriptionSvc()
	tenantSvc := sqlite.NewTenantService(db)
	sub := &sqlite.LLMSubscription{
		ID: "sub-context", SenderID: "cli_user", Name: "Context Sub", Provider: "openai",
		BaseURL: "https://api.example/v1", APIKey: "sk-test", Model: "context-model",
		PerModelConfigs: map[string]sqlite.PerModelConfig{"context-model": {MaxContext: 100000}},
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}
	if err := tenantSvc.SetTenantSubscription("web", "chat-context", sub.ID, "context-model"); err != nil {
		t.Fatalf("set tenant subscription: %v", err)
	}
	tenantID, err := tenantSvc.GetTenantIDByChannelChatID("web", "chat-context")
	if err != nil || tenantID == 0 {
		t.Fatalf("get tenant: id=%d err=%v", tenantID, err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO session_messages (tenant_id, role, content, context_tokens) VALUES (?, 'user', 'hello', 120000)",
		tenantID,
	); err != nil {
		t.Fatalf("insert user message: %v", err)
	}
	if err := sqlite.NewMemoryService(db).SetTokenState(context.Background(), tenantID, 50000, 1000); err != nil {
		t.Fatalf("set fallback token state: %v", err)
	}

	aCfg := &config.Config{Agent: config.AgentConfig{MaxContextTokens: 200000}}
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)
	params, _ := json.Marshal(map[string]string{"channel": "web", "chat_id": "chat-context"})
	raw, err := HandleCLIRPC(table, agent.MethodGetContextUsage, params, "admin")
	if err != nil {
		t.Fatalf("get_context_usage: %v", err)
	}
	var usage protocol.ContextUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if !usage.Available || usage.PromptTokens != 120000 || usage.CompletionTokens != 1000 || usage.MaxContextTokens != 100000 {
		t.Fatalf("usage=%+v", usage)
	}
	if usage.UsagePercent == nil || *usage.UsagePercent != 120 {
		t.Fatalf("usage percent=%v, want 120", usage.UsagePercent)
	}
	if usage.Model != "context-model" || usage.SubscriptionID != sub.ID || usage.SubscriptionName != sub.Name {
		t.Fatalf("model metadata=%+v", usage)
	}

	if _, err := db.Conn().Exec("UPDATE session_messages SET context_tokens = 0 WHERE tenant_id = ?", tenantID); err != nil {
		t.Fatalf("clear message context: %v", err)
	}
	raw, err = HandleCLIRPC(table, agent.MethodGetContextUsage, params, "admin")
	if err != nil {
		t.Fatalf("get fallback context usage: %v", err)
	}
	if err := json.Unmarshal(raw, &usage); err != nil {
		t.Fatalf("unmarshal fallback usage: %v", err)
	}
	if usage.PromptTokens != 50000 || usage.CompletionTokens != 1000 {
		t.Fatalf("fallback usage=%+v", usage)
	}

	if err := tenantSvc.SetTenantSubscription("web", "chat-context", sub.ID, "unconfigured-model"); err != nil {
		t.Fatalf("switch to unconfigured model: %v", err)
	}
	raw, err = HandleCLIRPC(table, agent.MethodGetContextUsage, params, "admin")
	if err != nil {
		t.Fatalf("get no-data context usage: %v", err)
	}
	if err := json.Unmarshal(raw, &usage); err != nil {
		t.Fatalf("unmarshal no-data usage: %v", err)
	}
	if usage.Available || usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.UsagePercent != nil {
		t.Fatalf("no-data usage=%+v", usage)
	}
	if usage.Model != "unconfigured-model" || usage.MaxContextTokens != 200000 {
		t.Fatalf("global context fallback=%+v", usage)
	}

	if _, err := db.Conn().Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label, user_id) VALUES ('web', 'web-2', 'chat-context', 'legacy', 0)",
	); err != nil {
		t.Fatalf("seed legacy web chat ownership: %v", err)
	}
	legacyOwner := WithRPCCtxResolved(context.Background(), "web-2", "web-2", 2, "user")
	if _, err := table.Dispatch(legacyOwner, agent.MethodGetContextUsage, params); err != nil {
		t.Fatalf("legacy Web owner get_context_usage: %v", err)
	}

	unauthorized := WithRPCCtxResolved(context.Background(), "web-3", "web-3", 3, "user")
	if _, err := table.Dispatch(unauthorized, agent.MethodGetContextUsage, params); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("cross-user get_context_usage error=%v", err)
	}
	if err := tenantSvc.SetTenantSubscription("cli", "web-3", sub.ID, "context-model"); err != nil {
		t.Fatalf("seed same-chat-id cross-channel tenant: %v", err)
	}
	crossChannelParams, _ := json.Marshal(map[string]string{"channel": "cli", "chat_id": "web-3"})
	if _, err := table.Dispatch(unauthorized, agent.MethodGetContextUsage, crossChannelParams); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("cross-channel same-chat-id error=%v", err)
	}
}

// TestHandleCLIRPCGetSessionSubscription verifies the get_session_subscription RPC.
// Tests the fallback path (LLMFactory cache) since MultiSession is not wired in this test.
func TestHandleCLIRPCGetSessionSubscription(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-a", SenderID: "cli_user", Name: "sub-a", Provider: "openai", BaseURL: "https://a.example/v1", APIKey: "sk-a", Model: "gpt-4o", IsDefault: true}); err != nil {
		t.Fatalf("add sub-a: %v", err)
	}

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	chatID := "/home/test/project:Agent-001"

	// Set per-session subscription via set_default_subscription (LLMFactory cache only, no DB)
	params, _ := json.Marshal(map[string]string{"id": "sub-a", "chat_id": chatID})
	if _, err := HandleCLIRPC(table, "set_default_subscription", params, "admin"); err != nil {
		t.Fatalf("set_default_subscription: %v", err)
	}

	// get_session_subscription uses LLMFactory fallback when no MultiSession
	params, _ = json.Marshal(map[string]string{"chat_id": chatID})
	raw, err := HandleCLIRPC(table, "get_session_subscription", params, "admin")
	if err != nil {
		t.Fatalf("get_session_subscription: %v", err)
	}
	var res map[string]string
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m, ok := res["model"]; !ok || m != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", res["model"])
	}
}

// TestHandleCLIRPCGetSessionSubscription_Empty verifies get_session_subscription
// handles sessions with no prior subscription mapping gracefully (returns empty/fallback).
func TestHandleCLIRPCGetSessionSubscription_Empty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	factory.SetSubscriptionSvc(sqlite.NewLLMSubscriptionService(db))

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Query for a session that has never been registered
	params, _ := json.Marshal(map[string]string{"chat_id": "/no/such/session"})
	raw, err := HandleCLIRPC(table, "get_session_subscription", params, "admin")
	if err != nil {
		t.Fatalf("get_session_subscription should not error for unknown session: %v", err)
	}
	// Without MultiSession, the handler falls back to LLMFactory's default model.
	// subscription_id should be empty (no DB mapping), model comes from fallback.
	var res map[string]string
	json.Unmarshal(raw, &res)
	if res["subscription_id"] != "" {
		t.Errorf("subscription_id should be empty for unknown session, got %q", res["subscription_id"])
	}
	// Model from LLMFactory fallback is expected; we just verify subscription_id is empty.
}

func TestSetCWDAllowsOwnedGeneratedWebChat(t *testing.T) {
	dir := t.TempDir()
	ag, err := agent.New(agent.Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	ag.SetIdentityResolver(agent.NewIdentityResolver(ag.MultiSession().DB().Conn()))

	const senderID = "web-2"
	const chatID = "generated-chat"
	if _, err := ag.MultiSession().DB().Conn().Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)",
		"web", senderID, chatID, "Generated",
	); err != nil {
		t.Fatal(err)
	}

	table := BuildRPCTable(&config.Config{}, ag, nil, nil, nil)
	params, _ := json.Marshal(map[string]string{
		"channel": "web",
		"chat_id": chatID,
		"dir":     dir,
	})
	ctx := WithRPCCtxResolved(context.Background(), senderID, senderID, 42, "user")
	if _, err := table.Dispatch(ctx, "set_cwd", params); err != nil {
		t.Fatalf("set_cwd for owned generated chat: %v", err)
	}
	sess, ok := ag.MultiSession().GetSession("web", chatID)
	if !ok {
		t.Fatal("owned generated chat session was not created")
	}
	if got := sess.GetCurrentDir(); got != dir {
		t.Fatalf("session CWD = %q, want %q", got, dir)
	}
}

func TestAgentRPCsCheckGeneratedWebChatOwner(t *testing.T) {
	dir := t.TempDir()
	ag, err := agent.New(agent.Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })

	for _, chat := range []struct {
		senderID string
		chatID   string
	}{
		{senderID: "web-2", chatID: "owned-chat"},
		{senderID: "web-3", chatID: "foreign-chat"},
	} {
		if _, err := ag.MultiSession().DB().Conn().Exec(
			"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)",
			"web", chat.senderID, chat.chatID, chat.chatID,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := ag.MultiSession().DB().Conn().Exec(
			"INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)",
			"agent", "web:"+chat.chatID+"/review:1", time.Now().Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
	}
	for _, tenant := range []struct {
		channel     string
		chatID      string
		ownerUserID int64
	}{
		{channel: "cli", chatID: "web-2", ownerUserID: 99},
		{channel: "agent", chatID: "cli:web-2/review:1", ownerUserID: 99},
		{channel: "cli", chatID: "owned-cli", ownerUserID: 42},
		{channel: "agent", chatID: "cli:owned-cli/review:1", ownerUserID: 42},
	} {
		if _, err := ag.MultiSession().DB().Conn().Exec(
			"INSERT INTO tenants (channel, chat_id, owner_user_id, last_active_at) VALUES (?, ?, ?, ?)",
			tenant.channel, tenant.chatID, tenant.ownerUserID, time.Now().Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
	}

	table := BuildRPCTable(&config.Config{}, ag, nil, nil, nil)
	ctx := WithRPCCtxResolved(context.Background(), "web-2", "web-2", 42, "user")
	ownedChatID := "web:owned-chat/review:1"
	foreignChatID := "web:foreign-chat/review:1"
	owned, _ := json.Marshal(map[string]string{"channel": "agent", "chat_id": ownedChatID})
	if _, err := table.Dispatch(ctx, "get_active_progress", owned); err != nil {
		t.Fatalf("owned agent progress: %v", err)
	}
	foreign, _ := json.Marshal(map[string]string{"channel": "agent", "chat_id": foreignChatID})
	if _, err := table.Dispatch(ctx, "get_active_progress", foreign); err == nil {
		t.Fatal("foreign agent progress should be denied")
	}

	ownedDump, _ := json.Marshal(map[string]string{"full_key": ownedChatID})
	if _, err := table.Dispatch(ctx, "get_agent_session_dump_by_full_key", ownedDump); err != nil {
		t.Fatalf("owned agent dump: %v", err)
	}
	foreignDump, _ := json.Marshal(map[string]string{"full_key": foreignChatID})
	if _, err := table.Dispatch(ctx, "get_agent_session_dump_by_full_key", foreignDump); err == nil {
		t.Fatal("foreign agent dump should be denied")
	}

	for _, method := range []string{"get_session_messages", "get_agent_session_dump"} {
		ownedParent, _ := json.Marshal(map[string]string{
			"channel":  "web",
			"chat_id":  "owned-chat",
			"role":     "review",
			"instance": "1",
		})
		if _, err := table.Dispatch(ctx, method, ownedParent); err != nil {
			t.Fatalf("%s for owned generated parent: %v", method, err)
		}
		foreignParent, _ := json.Marshal(map[string]string{
			"channel":  "web",
			"chat_id":  "foreign-chat",
			"role":     "review",
			"instance": "1",
		})
		if _, err := table.Dispatch(ctx, method, foreignParent); err == nil {
			t.Fatalf("%s for foreign generated parent should be denied", method)
		}

		ownedCLI, _ := json.Marshal(map[string]string{
			"channel":  "cli",
			"chat_id":  "owned-cli",
			"role":     "review",
			"instance": "1",
		})
		if _, err := table.Dispatch(ctx, method, ownedCLI); err != nil {
			t.Fatalf("%s for canonical-owned CLI parent: %v", method, err)
		}
		collidingCLI, _ := json.Marshal(map[string]string{
			"channel":  "cli",
			"chat_id":  "web-2",
			"role":     "review",
			"instance": "1",
		})
		if _, err := table.Dispatch(ctx, method, collidingCLI); err == nil {
			t.Fatalf("%s for foreign CLI self-ID collision should be denied", method)
		}
		emptyCollidingCLI, _ := json.Marshal(map[string]string{
			"channel": "cli",
			"chat_id": "",
			"role":    "review",
		})
		if _, err := table.Dispatch(ctx, method, emptyCollidingCLI); err == nil || !strings.Contains(err.Error(), "access denied") {
			t.Fatalf("%s empty chat ID should default then deny foreign CLI collision, got %v", method, err)
		}
	}

	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{method: "clear_memory", params: map[string]any{"target_type": "all"}},
		{method: "get_memory_stats", params: map[string]any{}},
		{method: "count_interactive_sessions", params: map[string]any{}},
		{method: "list_interactive_sessions", params: map[string]any{}},
		{method: "inspect_interactive_session", params: map[string]any{"role": "review"}},
		{method: "get_history", params: map[string]any{}},
		{method: "delete_chat", params: map[string]any{}},
		{method: "rename_chat", params: map[string]any{"new_name": "forbidden"}},
		{method: "get_token_state", params: map[string]any{}},
		{method: "trim_history", params: map[string]any{"cutoff": int64(0)}},
		{method: "is_processing", params: map[string]any{}},
		{method: "get_active_progress", params: map[string]any{}},
		{method: "get_pending_ask_user", params: map[string]any{}},
		{method: "get_todos", params: map[string]any{}},
	} {
		t.Run("deny CLI self-ID collision/"+tc.method, func(t *testing.T) {
			tc.params["channel"] = "cli"
			tc.params["chat_id"] = "web-2"
			params, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := table.Dispatch(ctx, tc.method, params); err == nil || !strings.Contains(err.Error(), "access denied") {
				t.Fatalf("foreign CLI self-ID collision should be denied, got %v", err)
			}
		})
	}

	ownedCLIChild, _ := json.Marshal(map[string]string{"full_key": "cli:owned-cli/review:1"})
	if _, err := table.Dispatch(ctx, "get_agent_session_dump_by_full_key", ownedCLIChild); err != nil {
		t.Fatalf("canonical-owned CLI-rooted agent dump: %v", err)
	}
	collidingCLIChild, _ := json.Marshal(map[string]string{"full_key": "cli:web-2/review:1"})
	if _, err := table.Dispatch(ctx, "get_agent_session_dump_by_full_key", collidingCLIChild); err == nil {
		t.Fatal("foreign CLI-rooted agent with colliding parent ID should be denied")
	}
}

func TestWebChatCRUDCallbacksKeepChannelsIsolated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	const chatID = "/repo/project:Agent-main"
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	localSessions, err := json.Marshal(map[string]any{
		"dir": "/repo/project",
		"sessions": []map[string]any{
			{"name": "Agent-main", "chat_id": chatID, "created_at": "2026-07-02T00:00:00Z"},
			{"name": "keep", "chat_id": "/repo/project:keep", "created_at": "2026-07-01T00:00:00Z", "model": "model-a"},
		},
		"last_active": chatID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "project.json"), localSessions, 0o600); err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(agent.Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	db := ag.MultiSession().DB()
	for _, channelName := range []string{"web", "cli"} {
		if _, err := db.Conn().Exec(
			"INSERT INTO tenants (channel, chat_id, last_active_at) VALUES (?, ?, ?)",
			channelName, chatID, time.Now().Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		senderID := "web-1"
		if channelName == "cli" {
			senderID = "cli_user"
		}
		if _, err := db.Conn().Exec(
			"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)",
			channelName, senderID, chatID, channelName+" label",
		); err != nil {
			t.Fatal(err)
		}
	}
	callbacks := buildWebCallbacks(&config.Config{}, ag, db)
	if err := callbacks.ChatRename("web-1", "cli", chatID, "renamed cli"); err != nil {
		t.Fatalf("rename CLI session: %v", err)
	}

	labels := make(map[string]string)
	for _, channelName := range []string{"web", "cli"} {
		rows, err := listTenantsByChannel(db.Conn(), channelName, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("%s rows = %#v", channelName, rows)
		}
		labels[channelName] = rows[0].Label
	}
	if labels["web"] != "web label" || labels["cli"] != "renamed cli" {
		t.Fatalf("cross-channel labels = %#v", labels)
	}

	if err := callbacks.ChatDelete("web-1", "cli", chatID); err != nil {
		t.Fatalf("delete CLI session: %v", err)
	}
	for channelName, want := range map[string]int{"web": 1, "cli": 0} {
		var count int
		if err := db.Conn().QueryRow(
			"SELECT COUNT(*) FROM tenants WHERE channel = ? AND chat_id = ?",
			channelName, chatID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s tenant count = %d, want %d", channelName, count, want)
		}
	}
	var cliLabels int
	if err := db.Conn().QueryRow(
		"SELECT COUNT(*) FROM user_chats WHERE channel = ? AND chat_id = ?",
		"cli", chatID,
	).Scan(&cliLabels); err != nil {
		t.Fatal(err)
	}
	if cliLabels != 0 {
		t.Fatalf("deleted CLI label rows = %d, want 0", cliLabels)
	}
	cliRows, err := listCLIChatSessions(db.Conn(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range cliRows {
		if row.ChatID == chatID {
			t.Fatalf("deleted CLI session reappeared from local store: %#v", cliRows)
		}
	}
	const localOnlyChatID = "/repo/project:keep"
	if callbacks.LocalSessionExists == nil || !callbacks.LocalSessionExists("cli", localOnlyChatID) {
		t.Fatal("local-only CLI session was not exposed to Web authorization")
	}
	if err := callbacks.ChatRename("web-1", "cli", localOnlyChatID, "renamed-local"); err != nil {
		t.Fatalf("rename local-only CLI session: %v", err)
	}
	cliRows, err = listCLIChatSessions(db.Conn(), "")
	if err != nil {
		t.Fatal(err)
	}
	foundRenamed := false
	for _, row := range cliRows {
		if row.ChatID == localOnlyChatID && row.Label == "renamed-local" {
			foundRenamed = true
		}
	}
	if !foundRenamed {
		t.Fatalf("renamed local-only CLI session was not relisted: %#v", cliRows)
	}
	if err := callbacks.ChatDelete("web-1", "cli", localOnlyChatID); err != nil {
		t.Fatalf("delete local-only CLI session: %v", err)
	}
	if callbacks.LocalSessionExists("cli", localOnlyChatID) {
		t.Fatal("local-only CLI session metadata survived deletion")
	}
}

// TestSetDefaultSubscription_GlobalSwitch_PreservesPerSession verifies that a global
// subscription switch (chatID="") does NOT destroy other sessions' per-session
// subscriptions. This was a critical cross-session contamination bug:
// the old code used Invalidate() which wiped ALL per-chat entries, causing
// session A's per-session GLM to be lost when session B switched globally to DeepSeek.
func TestSetDefaultSubscription_GlobalSwitch_PreservesPerSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Add two subscriptions: GLM and DeepSeek
	addGLM, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name": "glm", "provider": "openai",
			"base_url": "https://glm.api/v1", "api_key": "sk-glm", "model": "glm-5",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addGLM, "admin"); err != nil {
		t.Fatalf("add glm: %v", err)
	}
	addDS, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name": "deepseek", "provider": "openai",
			"base_url": "https://deepseek.api/v1", "api_key": "sk-ds", "model": "deepseek-v4-pro",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addDS, "admin"); err != nil {
		t.Fatalf("add deepseek: %v", err)
	}

	// Get subscription IDs
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	listRaw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(listRaw, &subs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs) < 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}
	var glmID, dsID string
	for _, s := range subs {
		if s.Model == "glm-5" {
			glmID = s.ID
		}
		if s.Model == "deepseek-v4-pro" {
			dsID = s.ID
		}
	}

	// Step 1: Set per-session GLM for chatA + select its model
	setSessParams, _ := json.Marshal(map[string]any{
		"id":      glmID,
		"chat_id": "/home/user/src/proj-a:Agent-001",
	})
	if _, err := HandleCLIRPC(table, "set_default_subscription", setSessParams, "admin"); err != nil {
		t.Fatalf("set per-session GLM: %v", err)
	}
	selGLM, _ := json.Marshal(map[string]any{"sub_id": glmID, "model": "glm-5", "chat_id": "/home/user/src/proj-a:Agent-001"})
	if _, err := HandleCLIRPC(table, "select_model", selGLM, "admin"); err != nil {
		t.Fatalf("select glm model for chatA: %v", err)
	}

	// Verify: chatA has per-session GLM
	_, modelA, _, _, _ := factory.GetLLMForChat("cli_user", "/home/user/src/proj-a:Agent-001")
	if modelA != "glm-5" {
		t.Fatalf("chatA model after per-session set = %q, want glm-5", modelA)
	}

	// Step 2: Global switch to DeepSeek (chatID="") + set default model
	setGlobalParams, _ := json.Marshal(map[string]any{
		"id":      dsID,
		"chat_id": "",
	})
	if _, err := HandleCLIRPC(table, "set_default_subscription", setGlobalParams, "admin"); err != nil {
		t.Fatalf("global switch to deepseek: %v", err)
	}
	setDefModelDS, _ := json.Marshal(map[string]any{"sub_id": dsID, "model": "deepseek-v4-pro"})
	if _, err := HandleCLIRPC(table, "set_default_model", setDefModelDS, "admin"); err != nil {
		t.Fatalf("set default deepseek model: %v", err)
	}

	// Step 3: Verify: chatA STILL has per-session GLM (must not be wiped)
	_, modelA2, _, _, _ := factory.GetLLMForChat("cli_user", "/home/user/src/proj-a:Agent-001")
	if modelA2 != "glm-5" {
		t.Errorf("chatA model after global switch = %q, want glm-5 (per-session must survive)", modelA2)
	}

	// Step 4: Verify: chatB (no per-session) uses DeepSeek (user_default_model)
	_, modelB, _, _, _ := factory.GetLLMForChat("cli_user", "/home/user/src/proj-b:Agent-002")
	if modelB != "deepseek-v4-pro" {
		t.Errorf("chatB model after global switch = %q, want deepseek-v4-pro (user_default_model)", modelB)
	}

	// Step 5: Verify: user_default_model is DeepSeek
	udm, _ := factory.GetSubscriptionSvc().GetUserDefaultModel("cli_user")
	if udm == nil || udm.Model != "deepseek-v4-pro" {
		if udm == nil {
			t.Errorf("user_default_model is nil, want deepseek-v4-pro")
		} else {
			t.Errorf("defaultModel after global switch = %q, want deepseek-v4-pro", udm.Model)
		}
	}
}

// TestSetDefaultSubscription_PerSessionSwitch_DoesNotAffectOtherSessions verifies
// that setting a per-session subscription for chatA does not change the model
// used by chatB (which has no per-session override).
func TestSetDefaultSubscription_PerSessionSwitch_DoesNotAffectOtherSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	factory.SetTenantSvc(sqlite.NewTenantService(db))

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Add GLM subscription and set as global default
	addGLM, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name": "glm", "provider": "openai",
			"base_url": "https://glm.api/v1", "api_key": "sk-glm", "model": "glm-5",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addGLM, "admin"); err != nil {
		t.Fatalf("add glm: %v", err)
	}

	// Add DeepSeek subscription
	addDS, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name": "deepseek", "provider": "openai",
			"base_url": "https://deepseek.api/v1", "api_key": "sk-ds", "model": "deepseek-v4-pro",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addDS, "admin"); err != nil {
		t.Fatalf("add deepseek: %v", err)
	}

	// Get IDs
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	listRaw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var subs []channel.Subscription
	json.Unmarshal(listRaw, &subs)
	var glmID, dsID string
	for _, s := range subs {
		if s.Model == "glm-5" {
			glmID = s.ID
		}
		if s.Model == "deepseek-v4-pro" {
			dsID = s.ID
		}
	}

	// Set GLM as global default, then select its model as default
	setGlobalGLM, _ := json.Marshal(map[string]any{"id": glmID, "chat_id": ""})
	if _, err := HandleCLIRPC(table, "set_default_subscription", setGlobalGLM, "admin"); err != nil {
		t.Fatalf("set global default: %v", err)
	}
	// Set user-level default model (model is user-level now)
	selGLM, _ := json.Marshal(map[string]any{"sub_id": glmID, "model": "glm-5"})
	if _, err := HandleCLIRPC(table, "set_default_model", selGLM, "admin"); err != nil {
		t.Fatalf("set default glm model: %v", err)
	}

	// Set per-session DeepSeek for chatA + select its model
	setSessDS, _ := json.Marshal(map[string]any{"id": dsID, "chat_id": "/proj-a:Agent-001"})
	if _, err := HandleCLIRPC(table, "set_default_subscription", setSessDS, "admin"); err != nil {
		t.Fatalf("set per-session deepseek: %v", err)
	}
	selDS, _ := json.Marshal(map[string]any{"sub_id": dsID, "model": "deepseek-v4-pro", "chat_id": "/proj-a:Agent-001"})
	if _, err := HandleCLIRPC(table, "select_model", selDS, "admin"); err != nil {
		t.Fatalf("select deepseek model: %v", err)
	}

	// Verify: chatA uses DeepSeek (per-session)
	_, modelA, _, _, _ := factory.GetLLMForChat("cli_user", "/proj-a:Agent-001")
	if modelA != "deepseek-v4-pro" {
		t.Errorf("chatA = %q, want deepseek-v4-pro", modelA)
	}

	// Verify: chatB also uses DeepSeek — SelectModel updates user_default_model
	// (last-used-model semantics), so new sessions inherit the last selected model.
	_, modelB, _, _, _ := factory.GetLLMForChat("cli_user", "/proj-b:Agent-002")
	if modelB != "deepseek-v4-pro" {
		t.Errorf("chatB = %q, want deepseek-v4-pro (last-used model inherited)", modelB)
	}

	// Verify: defaultModel in user_default_model is DeepSeek (last-used model)
	udm, _ := factory.GetSubscriptionSvc().GetUserDefaultModel("cli_user")
	if udm == nil || udm.Model != "deepseek-v4-pro" {
		if udm == nil {
			t.Errorf("user_default_model is nil, want deepseek-v4-pro")
		} else {
			t.Errorf("user_default_model = %q, want deepseek-v4-pro (last-used model)", udm.Model)
		}
	}
}

// ---------------------------------------------------------------------------
// Regression tests for issue #246: webui bugs
// ---------------------------------------------------------------------------

func TestListDistinctChannelsReturnsAllChannels(t *testing.T) {
	db := newTenantPreviewDB(t)
	// Insert tenants on various channels including a plugin channel
	for _, tc := range []struct {
		channel, chatID, lastActive string
	}{
		{"web", "web-1", "2026-07-18T10:00:00Z"},
		{"cli", "/repo:Agent-main", "2026-07-18T11:00:00Z"},
		{"feishu", "feishu-chat-1", "2026-07-18T12:00:00Z"},
		{"qq", "qq-group-1", "2026-07-18T13:00:00Z"},
		{"my_plugin", "plugin-session-1", "2026-07-18T14:00:00Z"},
	} {
		insertTenant(t, db, tc.channel, tc.chatID, tc.lastActive, "", "")
	}

	channels, err := listDistinctChannels(db)
	if err != nil {
		t.Fatalf("listDistinctChannels: %v", err)
	}
	want := []string{"cli", "feishu", "my_plugin", "qq", "web"}
	if len(channels) != len(want) {
		t.Fatalf("got %d channels %v, want %d %v", len(channels), channels, len(want), want)
	}
	for i, ch := range channels {
		if ch != want[i] {
			t.Fatalf("channels[%d] = %q, want %q (full: %v)", i, ch, want[i], channels)
		}
	}
}

func TestListDistinctChannelsExcludesSharedChat(t *testing.T) {
	db := newTenantPreviewDB(t)
	insertTenant(t, db, "web", "_shared", "2026-07-18T10:00:00Z", "", "")
	insertTenant(t, db, "web", "web-1", "2026-07-18T11:00:00Z", "", "")

	channels, err := listDistinctChannels(db)
	if err != nil {
		t.Fatalf("listDistinctChannels: %v", err)
	}
	// _shared should not contribute a separate channel; "web" should appear once
	if len(channels) != 1 || channels[0] != "web" {
		t.Fatalf("got channels %v, want [web]", channels)
	}
}

func TestListTenantsForSenderReturnsNonWebSessions(t *testing.T) {
	db := newTenantPreviewDB(t)
	// Insert a feishu session owned by "web-1" via user_chats
	if _, err := db.Exec(`INSERT INTO tenants(channel, chat_id, last_active_at) VALUES (?, ?, ?)`,
		"feishu", "feishu-chat-1", "2026-07-18T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_chats(channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)`,
		"feishu", "web-1", "feishu-chat-1", "Feishu Chat"); err != nil {
		t.Fatal(err)
	}
	// Insert a web session (should be excluded)
	if _, err := db.Exec(`INSERT INTO tenants(channel, chat_id, last_active_at) VALUES (?, ?, ?)`,
		"web", "web-1", "2026-07-18T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_chats(channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)`,
		"web", "web-1", "web-1", "Web Chat"); err != nil {
		t.Fatal(err)
	}
	// Insert a feishu session owned by a different user (should be excluded)
	if _, err := db.Exec(`INSERT INTO tenants(channel, chat_id, last_active_at) VALUES (?, ?, ?)`,
		"feishu", "feishu-chat-other", "2026-07-18T13:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_chats(channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)`,
		"feishu", "web-2", "feishu-chat-other", "Other Chat"); err != nil {
		t.Fatal(err)
	}

	rows, err := listTenantsForSender(db, "web-1", "")
	if err != nil {
		t.Fatalf("listTenantsForSender: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %#v", len(rows), rows)
	}
	if rows[0].Channel != "feishu" || rows[0].ChatID != "feishu-chat-1" {
		t.Fatalf("unexpected row: %#v", rows[0])
	}
	if rows[0].Label != "Feishu Chat" {
		t.Fatalf("unexpected label: %q", rows[0].Label)
	}
}

func TestListTenantsForSenderExcludesSubAgentTenants(t *testing.T) {
	db := newTenantPreviewDB(t)
	// Insert an interactive sub-agent tenant (should be filtered out)
	if _, err := db.Exec(`INSERT INTO tenants(channel, chat_id, last_active_at) VALUES (?, ?, ?)`,
		"agent", "web:web-1/review:1", "2026-07-18T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_chats(channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)`,
		"agent", "web-1", "web:web-1/review:1", "SubAgent"); err != nil {
		t.Fatal(err)
	}

	rows, err := listTenantsForSender(db, "web-1", "")
	if err != nil {
		t.Fatalf("listTenantsForSender: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows (sub-agent filtered), got %d: %#v", len(rows), rows)
	}
}
