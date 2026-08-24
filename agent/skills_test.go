package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/tools"
)

func writeSkill(t *testing.T, rootDir, folder, name, desc string) string {
	t.Helper()
	dir := filepath.Join(rootDir, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := "---\n" +
		"name: " + name + "\n" +
		"description: " + desc + "\n" +
		"---\n\n" +
		"# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return filepath.Join(dir, "SKILL.md")
}

func TestSkillStore_GlobalAndPrivateCatalog(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")
	privateDir := tools.UserSkillsRoot(workDir, "user-1")

	writeSkill(t, globalDir, "global-tool", "global-tool", "global skill")
	writeSkill(t, privateDir, "private-tool", "private-tool", "private skill")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	catalog := store.GetSkillsCatalog(context.Background(), "user-1")

	if !strings.Contains(catalog, "<name>global-tool</name>") {
		t.Fatalf("expected global skill in catalog, got: %s", catalog)
	}
	if !strings.Contains(catalog, "<name>private-tool</name>") {
		t.Fatalf("expected private skill in catalog, got: %s", catalog)
	}
	// Catalog must NOT contain host filesystem paths
	if strings.Contains(catalog, "<location>") {
		t.Fatalf("catalog must not contain <location> tags (path leakage), got: %s", catalog)
	}
}

func TestSkillStore_PrivateOverrideGlobal(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")
	privateDir := tools.UserSkillsRoot(workDir, "user-1")

	writeSkill(t, globalDir, "dup", "dup", "global dup")
	writeSkill(t, privateDir, "dup", "dup", "private dup")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	catalog := store.GetSkillsCatalog(context.Background(), "user-1")

	if strings.Count(catalog, "<name>dup</name>") != 1 {
		t.Fatalf("expected deduped skill entry, got: %s", catalog)
	}
	if !strings.Contains(catalog, "private dup") {
		t.Fatalf("expected private dup to override global dup, got: %s", catalog)
	}
}

func TestSkillStore_EmbeddedDebugSkillPresent(t *testing.T) {
	store := NewSkillStore(t.TempDir(), nil, nil)
	catalog := store.GetSkillsCatalog(context.Background(), "user-1")

	if !strings.Contains(catalog, "<name>debug</name>") {
		t.Fatalf("expected embedded debug skill in catalog, got: %s", catalog)
	}
	if !strings.Contains(catalog, "Investigate and fix bugs") {
		t.Fatalf("expected debug skill description in catalog, got: %s", catalog)
	}
}

func TestSkillStore_ProjectLocalSkills(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")
	projectDir := t.TempDir()

	// Create global skill
	writeSkill(t, globalDir, "global-tool", "global-tool", "global skill")
	// Create project-local skill
	writeSkill(t, filepath.Join(projectDir, ".xbot", "skills"), "project-tool", "project-tool", "project skill")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	catalog := store.GetSkillsCatalog(context.Background(), "user-1", projectDir)

	if !strings.Contains(catalog, "<name>global-tool</name>") {
		t.Fatalf("expected global skill in catalog, got: %s", catalog)
	}
	if !strings.Contains(catalog, "<name>project-tool</name>") {
		t.Fatalf("expected project-local skill in catalog, got: %s", catalog)
	}
	if !strings.Contains(catalog, "project skill") {
		t.Fatalf("expected project skill description in catalog, got: %s", catalog)
	}
	// Verify project Skills directory hint is present
	if !strings.Contains(catalog, "项目 Skills 目录") {
		t.Fatalf("expected project Skills directory hint, got: %s", catalog)
	}
}

func TestSkillStore_ProjectLocalNoDuplicate(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")
	projectDir := t.TempDir()

	// Create same-named skill in both global and project
	writeSkill(t, globalDir, "dup", "dup", "global dup")
	writeSkill(t, filepath.Join(projectDir, ".xbot", "skills"), "dup", "dup", "project dup")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	catalog := store.GetSkillsCatalog(context.Background(), "user-1", projectDir)

	// Global should win since it's scanned first; project-local deduplicates against existing
	if strings.Count(catalog, "<name>dup</name>") != 1 {
		t.Fatalf("expected deduped skill entry, got: %s", catalog)
	}
}

func TestSkillStore_ProjectLocalNoDir(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")
	projectDir := t.TempDir() // empty — no .xbot/skills

	writeSkill(t, globalDir, "global-tool", "global-tool", "global skill")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	catalog := store.GetSkillsCatalog(context.Background(), "user-1", projectDir)

	if !strings.Contains(catalog, "<name>global-tool</name>") {
		t.Fatalf("expected global skill in catalog, got: %s", catalog)
	}
}

func TestSkillStore_DisabledSkillsBlacklist(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")

	writeSkill(t, globalDir, "skill-a", "skill-a", "skill a desc")
	writeSkill(t, globalDir, "skill-b", "skill-b", "skill b desc")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	store.SetDisabledSkills([]string{"skill-a"})

	catalog := store.GetSkillsCatalog(context.Background(), "user-1")

	if strings.Contains(catalog, "<name>skill-a</name>") {
		t.Fatalf("blacklisted skill-a must be excluded from catalog, got: %s", catalog)
	}
	if !strings.Contains(catalog, "<name>skill-b</name>") {
		t.Fatalf("non-blacklisted skill-b must remain in catalog, got: %s", catalog)
	}
}

func TestSkillStore_DisabledSkillsEmptyAndWhitespace(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")

	writeSkill(t, globalDir, "skill-a", "skill-a", "skill a desc")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	// 空串和空白名应被忽略，不应 panic 或影响 catalog
	store.SetDisabledSkills([]string{"", "  ", "skill-a"})

	catalog := store.GetSkillsCatalog(context.Background(), "user-1")
	if strings.Contains(catalog, "<name>skill-a</name>") {
		t.Fatalf("blacklisted skill-a must be excluded, got: %s", catalog)
	}
}

func TestSkillStore_IsKnownSkillPathFor_SenderScoped(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")
	userDir := tools.UserSkillsRoot(workDir, "user-1")

	globalSkillMD := writeSkill(t, globalDir, "global-tool", "global-tool", "global skill")
	userSkillMD := writeSkill(t, userDir, "private-tool", "private-tool", "private skill")

	store := NewSkillStore(workDir, []string{globalDir}, nil)

	// Global skills: recognized for any sender.
	if !store.IsKnownSkillPathFor("user-1", filepath.Dir(globalSkillMD)) {
		t.Fatalf("global skill must be recognized for any sender")
	}

	// User skills: recognized ONLY for the owning sender.
	// Regression (code review): the old IsKnownSkillPath used userSkillsDir("")
	// (empty senderID) → never matched {workDir}/.xbot/users/{sender}/workspace/skills,
	// so skill_get_content / skill_validate_path failed for user-installed skills.
	if !store.IsKnownSkillPathFor("user-1", filepath.Dir(userSkillMD)) {
		t.Fatalf("user skill must be recognized for its owner")
	}
	if store.IsKnownSkillPathFor("user-2", filepath.Dir(userSkillMD)) {
		t.Fatalf("user skill must NOT be recognized for another sender")
	}
	if store.IsKnownSkillPathFor("", filepath.Dir(userSkillMD)) {
		t.Fatalf("empty senderID must NOT claim user skills")
	}

	// GetSkillContentFor: owner succeeds, others / arbitrary paths fail.
	if _, err := store.GetSkillContentFor("user-1", filepath.Dir(userSkillMD)); err != nil {
		t.Fatalf("GetSkillContentFor(owner) must succeed: %v", err)
	}
	if _, err := store.GetSkillContentFor("", filepath.Dir(userSkillMD)); err == nil {
		t.Fatalf("GetSkillContentFor(empty senderID) must fail for user skills")
	}
	if _, err := store.GetSkillContentFor("user-1", filepath.Join(workDir, "outside", "SKILL.md")); err == nil {
		t.Fatalf("GetSkillContentFor must reject arbitrary paths")
	}
}
