package tools

import (
	"embed"
	"io/fs"
	"path"
	"path/filepath"
)

// EmbeddedSkills contains skill templates built into the binary.
//
//go:embed embed_skills/*
var EmbeddedSkills embed.FS

// ListEmbeddedSkills returns names of all embedded skills (directory names under embed_skills/).
func ListEmbeddedSkills() []string {
	entries, err := fs.ReadDir(EmbeddedSkills, "embed_skills")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// ReadEmbeddedSkillFile reads a file from an embedded skill directory.
// skillName is the directory name (e.g., "agent-creator").
// file is the relative path within the skill directory (e.g., "SKILL.md").
// Returns nil if the file doesn't exist.
func ReadEmbeddedSkillFile(skillName, file string) ([]byte, error) {
	path := path.Join("embed_skills", skillName, file)
	return EmbeddedSkills.ReadFile(path)
}

// ListEmbeddedSkillFiles returns all file paths (relative to skill root) in an
// embedded skill, recursing into subdirectories. Paths use forward slashes
// (e.g., "SKILL.md", "examples/debug.go").
func ListEmbeddedSkillFiles(skillName string) ([]string, error) {
	skillDir := path.Join("embed_skills", skillName)
	var files []string
	err := fs.WalkDir(EmbeddedSkills, skillDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Convert to path relative to skill root
		rel, err := filepath.Rel(skillDir, p)
		if err != nil {
			return nil // skip on error
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

// HasEmbeddedSkill checks if a skill with the given name exists in the embedded FS.
func HasEmbeddedSkill(name string) bool {
	for _, n := range ListEmbeddedSkills() {
		if n == name {
			return true
		}
	}
	return false
}
