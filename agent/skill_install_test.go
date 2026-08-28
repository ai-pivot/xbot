package agent

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"xbot/tools"
)

// helper: create a zip file from a map of path → content
func createSkillZip(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallSkillFromFile_TopLevelDir(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSkillStore(tmpDir, []string{filepath.Join(tmpDir, "skills")}, nil)
	rm := &RegistryManager{
		store:    store,
		workDir:  tmpDir,
		xbotHome: tmpDir,
	}

	zipPath := filepath.Join(t.TempDir(), "aws-ops.zip")
	createSkillZip(t, zipPath, map[string]string{
		"aws-ops/SKILL.md":                          "---\nname: aws-ops\ndescription: AWS ops skill\n---\n# AWS Ops\n",
		"aws-ops/references/faq.md":                 "# FAQ\n",
		"aws-ops/scripts/aws_ops_check.sh":           "#!/bin/bash\necho check\n",
	})

	name, err := rm.InstallSkillFromFile(zipPath, "test-user", false)
	if err != nil {
		t.Fatalf("InstallSkillFromFile failed: %v", err)
	}
	if name != "aws-ops" {
		t.Errorf("expected skill name 'aws-ops', got %q", name)
	}

	// Verify files installed
	destDir := filepath.Join(tools.UserSkillsRoot(tmpDir, "test-user"), "aws-ops")
	for _, f := range []string{"SKILL.md", "references/faq.md", "scripts/aws_ops_check.sh"} {
		if _, err := os.Stat(filepath.Join(destDir, f)); err != nil {
			t.Errorf("expected file %s to exist: %v", f, err)
		}
	}
}

func TestInstallSkillFromFile_RootSkillMD(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSkillStore(tmpDir, []string{filepath.Join(tmpDir, "skills")}, nil)
	rm := &RegistryManager{
		store:    store,
		workDir:  tmpDir,
		xbotHome: tmpDir,
	}

	zipPath := filepath.Join(t.TempDir(), "my-skill.zip")
	createSkillZip(t, zipPath, map[string]string{
		"SKILL.md": "---\nname: my-skill\ndescription: test\n---\n# My Skill\n",
	})

	name, err := rm.InstallSkillFromFile(zipPath, "test-user", false)
	if err != nil {
		t.Fatalf("InstallSkillFromFile failed: %v", err)
	}
	if name != "my-skill" {
		t.Errorf("expected skill name 'my-skill', got %q", name)
	}

	destDir := filepath.Join(tools.UserSkillsRoot(tmpDir, "test-user"), "my-skill")
	if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err != nil {
		t.Errorf("expected SKILL.md to exist: %v", err)
	}
}

func TestInstallSkillFromFile_NoSkillMD(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSkillStore(tmpDir, []string{filepath.Join(tmpDir, "skills")}, nil)
	rm := &RegistryManager{
		store:    store,
		workDir:  tmpDir,
		xbotHome: tmpDir,
	}

	zipPath := filepath.Join(t.TempDir(), "bad.zip")
	createSkillZip(t, zipPath, map[string]string{
		"some-dir/readme.md": "# Not a skill\n",
	})

	_, err := rm.InstallSkillFromFile(zipPath, "test-user", false)
	if err == nil {
		t.Fatal("expected error for zip without SKILL.md")
	}
}

func TestInstallSkillFromFile_AlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewSkillStore(tmpDir, []string{filepath.Join(tmpDir, "skills")}, nil)
	rm := &RegistryManager{
		store:    store,
		workDir:  tmpDir,
		xbotHome: tmpDir,
	}

	zipPath := filepath.Join(t.TempDir(), "aws-ops.zip")
	createSkillZip(t, zipPath, map[string]string{
		"aws-ops/SKILL.md": "---\nname: aws-ops\n---\n# AWS Ops\n",
	})

	// First install — should succeed
	if _, err := rm.InstallSkillFromFile(zipPath, "test-user", false); err != nil {
		t.Fatalf("first install failed: %v", err)
	}

	// Second install without force — should fail
	_, err := rm.InstallSkillFromFile(zipPath, "test-user", false)
	if err == nil {
		t.Fatal("expected error for already-existing skill")
	}

	// Third install with force — should succeed
	if _, err := rm.InstallSkillFromFile(zipPath, "test-user", true); err != nil {
		t.Fatalf("force install failed: %v", err)
	}
}
