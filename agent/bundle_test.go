package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBundlePackager_PackAndUnpack(t *testing.T) {
	tmpDir := t.TempDir()

	// Build a RegistryManager with the temp dir as workDir
	skillsDir := filepath.Join(tmpDir, "skills")
	agentsDir := filepath.Join(tmpDir, "agents")
	store := NewSkillStore(tmpDir, []string{skillsDir}, nil)
	agentStore := NewAgentStore(tmpDir, agentsDir, nil)
	rm := &RegistryManager{
		store:       store,
		agentStore:  agentStore,
		workDir:     tmpDir,
	}

	// Create skill and agent in the expected locations
	skillDir := filepath.Join(skillsDir, "test-skill")
	os.MkdirAll(skillDir, 0o755)
	skillMD := `---
name: test-skill
description: A test skill
---
# Test Skill
This is a test skill.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}

	agentMD := `---
name: test-agent
description: A test agent
model: swift
---
You are a test agent.`
	agentFile := filepath.Join(agentsDir, "test-agent.md")
	os.MkdirAll(agentsDir, 0o755)
	if err := os.WriteFile(agentFile, []byte(agentMD), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pack
	zipPath := filepath.Join(t.TempDir(), "test-bundle.xbot.zip")
	items := []PackItem{
		{Type: "skill", Name: "test-skill"},
		{Type: "agent", Name: "test-agent"},
	}
	bp := NewBundlePackager(tmpDir)
	if err := bp.Pack(rm, items, zipPath, "test-author"); err != nil {
		t.Fatalf("Pack failed: %v", err)
	}

	// Verify zip exists
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatalf("zip file not created: %v", err)
	}

	// Unpack
	manifest, unpackDir, err := bp.Unpack(zipPath)
	if err != nil {
		t.Fatalf("Unpack failed: %v", err)
	}
	defer os.RemoveAll(unpackDir)

	// Verify manifest
	if manifest.Schema != BundleManifestSchema {
		t.Errorf("expected schema %d, got %d", BundleManifestSchema, manifest.Schema)
	}
	if len(manifest.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(manifest.Contents))
	}

	// Verify skill content
	skillFound := false
	agentFound := false
	for _, c := range manifest.Contents {
		switch c.Type {
		case "skill":
			skillFound = true
			if c.Name != "test-skill" {
				t.Errorf("expected skill name 'test-skill', got %q", c.Name)
			}
		case "agent":
			agentFound = true
			if c.Name != "test-agent" {
				t.Errorf("expected agent name 'test-agent', got %q", c.Name)
			}
		}
	}
	if !skillFound {
		t.Error("skill content not found in manifest")
	}
	if !agentFound {
		t.Error("agent content not found in manifest")
	}

	// Validate
	if err := bp.Validate(manifest, unpackDir); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	// Verify files exist in unpacked dir
	skillMDPath := filepath.Join(unpackDir, "skills", "test-skill", "SKILL.md")
	if _, err := os.Stat(skillMDPath); err != nil {
		t.Errorf("SKILL.md not found in unpacked bundle: %v", err)
	}
	agentMDPath := filepath.Join(unpackDir, "agents", "test-agent.md")
	if _, err := os.Stat(agentMDPath); err != nil {
		t.Errorf("agent .md not found in unpacked bundle: %v", err)
	}
}

func TestBundleManifest_JSONRoundTrip(t *testing.T) {
	original := BundleManifest{
		Schema:      BundleManifestSchema,
		ID:          "test-bundle",
		Name:        "Test Bundle",
		Version:     "1.2.3",
		Author:      "user@test.com",
		Description: "A test bundle",
		Contents: []BundleContent{
			{
				Type:        "skill",
				Name:        "my-skill",
				Source:      "skills/my-skill/",
				Description: "A skill",
			},
			{
				Type:        "agent",
				Name:        "my-agent",
				Source:      "agents/my-agent.md",
				Description: "An agent",
				Model:       "swift",
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded BundleManifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: %q vs %q", decoded.ID, original.ID)
	}
	if len(decoded.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(decoded.Contents))
	}
	if decoded.Contents[1].Model != "swift" {
		t.Errorf("expected model 'swift', got %q", decoded.Contents[1].Model)
	}
}
