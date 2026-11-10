package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillToolProjectDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, location, dirName, skillName string
		useCWD, symlink                    bool
	}{
		{name: "workspace agents", location: ".agents/skills", dirName: "bifrost-mock-router", skillName: "bifrost-mock-router"},
		{name: "parent agents", location: ".agents/skills", dirName: "bifrost-mock-router", skillName: "bifrost-mock-router", useCWD: true},
		{name: "frontmatter alias", location: ".agents/skills", dirName: "folder", skillName: "bifrost-mock-router"},
		{name: "symlink alias", location: ".xbot/skills", dirName: "folder", skillName: "bifrost-mock-router", symlink: true},
		{name: "legacy workspace", location: "skills", dirName: "bifrost-mock-router", skillName: "bifrost-mock-router"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, tc.location, tc.dirName)
			target := dir
			if tc.symlink {
				target = t.TempDir()
			}
			if err := os.MkdirAll(target, 0755); err != nil {
				t.Fatal(err)
			}
			content := "---\nname: " + tc.skillName + "\ndescription: test\n---\nSkill instructions\n"
			if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			if tc.symlink {
				if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			}
			ctx := &ToolContext{Ctx: context.Background(), WorkspaceRoot: root}
			if tc.useCWD {
				ctx.WorkspaceRoot = t.TempDir()
				ctx.CurrentDir = filepath.Join(root, "nested", "project")
				if err := os.MkdirAll(ctx.CurrentDir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			for _, action := range []string{"load", "list_files"} {
				input, err := json.Marshal(map[string]string{"name": tc.skillName, "action": action})
				if err != nil {
					t.Fatal(err)
				}
				result, err := (&SkillTool{}).Execute(ctx, string(input))
				if err != nil {
					t.Errorf("%s: %v", action, err)
					continue
				}
				if result == nil {
					t.Fatalf("%s: missing result", action)
				}
				want := content
				if action == "list_files" {
					want = filepath.Join(dir, "SKILL.md")
				}
				if !strings.Contains(result.Summary, want) {
					t.Errorf("%s: got %q, want %q", action, result.Summary, want)
				}
			}
		})
	}
}
