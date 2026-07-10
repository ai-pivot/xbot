package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppPackager_PackAndUnpack(t *testing.T) {
	tmpDir := t.TempDir()

	// Build a RegistryManager with the temp dir as workDir and xbotHome
	skillsDir := filepath.Join(tmpDir, "skills")
	agentsDir := filepath.Join(tmpDir, "agents")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	store := NewSkillStore(tmpDir, []string{skillsDir}, nil)
	agentStore := NewAgentStore(tmpDir, agentsDir, nil)
	rm := &RegistryManager{
		store:      store,
		agentStore: agentStore,
		workDir:    tmpDir,
		xbotHome:   tmpDir,
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

	// Create a plugin in the plugins directory
	pluginDir := filepath.Join(pluginsDir, "test-plugin")
	os.MkdirAll(pluginDir, 0o755)
	pluginJSON := `{
		"id": "test-plugin",
		"name": "Test Plugin",
		"version": "1.0.0",
		"description": "A test plugin",
		"runtime": "script",
		"entry": "bash main.sh"
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "main.sh"), []byte("#!/bin/bash\necho hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pack
	zipPath := filepath.Join(t.TempDir(), "test-app.xbot.zip")
	items := []AppItem{
		{Type: "skill", Name: "test-skill"},
		{Type: "agent", Name: "test-agent"},
		{Type: "plugin", Name: "test-plugin"},
	}
	bp := NewAppPackager(tmpDir)
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
	if manifest.Schema != AppManifestSchema {
		t.Errorf("expected schema %d, got %d", AppManifestSchema, manifest.Schema)
	}
	if len(manifest.Contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(manifest.Contents))
	}

	// Verify skill, agent, and plugin content
	skillFound := false
	agentFound := false
	pluginFound := false
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
		case "plugin":
			pluginFound = true
			if c.Name != "test-plugin" {
				t.Errorf("expected plugin name 'test-plugin', got %q", c.Name)
			}
			if c.Runtime != "script" {
				t.Errorf("expected runtime 'script', got %q", c.Runtime)
			}
		}
	}
	if !skillFound {
		t.Error("skill content not found in manifest")
	}
	if !agentFound {
		t.Error("agent content not found in manifest")
	}
	if !pluginFound {
		t.Error("plugin content not found in manifest")
	}

	// Validate
	if err := bp.Validate(manifest, unpackDir); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	// Verify files exist in unpacked dir
	skillMDPath := filepath.Join(unpackDir, "skills", "test-skill", "SKILL.md")
	if _, err := os.Stat(skillMDPath); err != nil {
		t.Errorf("SKILL.md not found in unpacked app: %v", err)
	}
	agentMDPath := filepath.Join(unpackDir, "agents", "test-agent.md")
	if _, err := os.Stat(agentMDPath); err != nil {
		t.Errorf("agent .md not found in unpacked app: %v", err)
	}
	pluginJSONPath := filepath.Join(unpackDir, "plugins", "test-plugin", "plugin.json")
	if _, err := os.Stat(pluginJSONPath); err != nil {
		t.Errorf("plugin.json not found in unpacked app: %v", err)
	}
}

func TestAppManifest_JSONRoundTrip(t *testing.T) {
	original := AppManifest{
		Schema:      AppManifestSchema,
		ID:          "test-app",
		Name:        "Test App",
		Version:     "1.2.3",
		Author:      "user@test.com",
		Description: "A test app",
		Contents: []AppContent{
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
			{
				Type:        "plugin",
				Name:        "my-plugin",
				Source:      "plugins/my-plugin/",
				Description: "A plugin",
				Runtime:     "script",
				Permissions: []string{"fs.read", "fs.write"},
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded AppManifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: %q vs %q", decoded.ID, original.ID)
	}
	if len(decoded.Contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(decoded.Contents))
	}
	if decoded.Contents[1].Model != "swift" {
		t.Errorf("expected model 'swift', got %q", decoded.Contents[1].Model)
	}
	if decoded.Contents[2].Runtime != "script" {
		t.Errorf("expected runtime 'script', got %q", decoded.Contents[2].Runtime)
	}
}
