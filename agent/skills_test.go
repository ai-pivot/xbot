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
