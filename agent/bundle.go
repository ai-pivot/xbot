package agent

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"xbot/tools"
)

// BundleManifestSchema is the schema version for xbot.json.
const BundleManifestSchema = 1

// BundleManifest represents the xbot.json manifest inside a .xbot.zip bundle.
type BundleManifest struct {
	Schema      int             `json:"schema"`
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Author      string          `json:"author"`
	Description string          `json:"description"`
	Homepage    string          `json:"homepage,omitempty"`
	License     string          `json:"license,omitempty"`
	Contents    []BundleContent `json:"contents"`
}

// BundleContent declares a single item inside the bundle.
type BundleContent struct {
	Type        string   `json:"type"`         // skill | agent | plugin
	Name        string   `json:"name"`
	Source      string   `json:"source"`       // relative path inside the zip
	Description string   `json:"description,omitempty"`
	Model       string   `json:"model,omitempty"`         // agent only
	Tools       []string `json:"tools,omitempty"`        // agent only
	Runtime     string   `json:"runtime,omitempty"`      // plugin only
	Permissions []string `json:"permissions,omitempty"`   // plugin only
}

// PackItem specifies a local item to include when building a bundle.
type PackItem struct {
	Type string // skill | agent | plugin
	Name string // item name (matches skill dir name or agent .md name)
}

// InstallResult records what was installed from a bundle.
type InstallResult struct {
	Manifest  BundleManifest
	Installed []string // human-readable descriptions
}

// BundlePackager handles packing and unpacking .xbot.zip files.
type BundlePackager struct {
	workDir string
}

// NewBundlePackager creates a new BundlePackager.
func NewBundlePackager(workDir string) *BundlePackager {
	return &BundlePackager{workDir: workDir}
}

// Pack builds a .xbot.zip from the given items and writes it to outputPath.
// The rm (RegistryManager) is used to locate skill/agent source files.
func (bp *BundlePackager) Pack(rm *RegistryManager, items []PackItem, outputPath, author string) error {
	manifest := BundleManifest{
		Schema:      BundleManifestSchema,
		ID:          filepath.Base(strings.TrimSuffix(outputPath, ".xbot.zip")),
		Name:        filepath.Base(strings.TrimSuffix(outputPath, ".xbot.zip")),
		Version:     "1.0.0",
		Author:      author,
		Description: "",
		Contents:    []BundleContent{},
	}

	// Create temp dir for staging
	tmpDir, err := os.MkdirTemp("", "xbot-bundle-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	for _, item := range items {
		switch item.Type {
		case "skill":
			content, err := bp.stageSkill(rm, item, tmpDir, author)
			if err != nil {
				return err
			}
			manifest.Contents = append(manifest.Contents, content)
		case "agent":
			content, err := bp.stageAgent(rm, item, tmpDir, author)
			if err != nil {
				return err
			}
			manifest.Contents = append(manifest.Contents, content)
		default:
			return fmt.Errorf("unsupported type %q (Phase 1 supports skill and agent only)", item.Type)
		}
	}

	// Write xbot.json
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "xbot.json"), manifestData, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	// Zip the temp dir
	return zipDir(tmpDir, outputPath)
}

// stageSkill copies a skill directory into the staging area.
func (bp *BundlePackager) stageSkill(rm *RegistryManager, item PackItem, tmpDir, author string) (BundleContent, error) {
	skillDir := rm.findSkillDirForUser(item.Name, author)
	if skillDir == "" {
		return BundleContent{}, fmt.Errorf("skill %q not found", item.Name)
	}

	// Read SKILL.md for metadata
	skillMDPath := filepath.Join(skillDir, "SKILL.md")
	data, err := os.ReadFile(skillMDPath)
	if err != nil {
		return BundleContent{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	info := parseSkillFrontmatterV2(data, skillDir)

	// Copy to staging
	stagingPath := filepath.Join(tmpDir, "skills", item.Name)
	if err := copyDir(skillDir, stagingPath); err != nil {
		return BundleContent{}, fmt.Errorf("copy skill to staging: %w", err)
	}

	return BundleContent{
		Type:        "skill",
		Name:        info.Name,
		Source:      "skills/" + item.Name + "/",
		Description: info.Description,
	}, nil
}

// stageAgent copies an agent .md file into the staging area.
func (bp *BundlePackager) stageAgent(rm *RegistryManager, item PackItem, tmpDir, author string) (BundleContent, error) {
	agentFile := rm.findAgentFile(item.Name, author)
	if agentFile == "" {
		return BundleContent{}, fmt.Errorf("agent %q not found", item.Name)
	}

	role, err := parseAgentFileSafe(agentFile, item.Name)
	if err != nil {
		return BundleContent{}, fmt.Errorf("parse agent %q: %w", item.Name, err)
	}

	// Copy to staging
	stagingDir := filepath.Join(tmpDir, "agents")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return BundleContent{}, fmt.Errorf("create agents staging dir: %w", err)
	}
	stagingPath := filepath.Join(stagingDir, item.Name+".md")
	if err := copyFile(agentFile, stagingPath); err != nil {
		return BundleContent{}, fmt.Errorf("copy agent to staging: %w", err)
	}

	content := BundleContent{
		Type:        "agent",
		Name:        role.Name,
		Source:      "agents/" + item.Name + ".md",
		Description: role.Description,
	}
	if role.Model != "" {
		content.Model = role.Model
	}
	if len(role.AllowedTools) > 0 {
		content.Tools = role.AllowedTools
	}
	return content, nil
}

// Unpack extracts a .xbot.zip to a temp directory and returns the manifest.
// Caller is responsible for cleaning up the temp directory.
func (bp *BundlePackager) Unpack(zipPath string) (*BundleManifest, string, error) {
	tmpDir, err := os.MkdirTemp("", "xbot-unpack-*")
	if err != nil {
		return nil, "", fmt.Errorf("create temp dir: %w", err)
	}

	if err := unzipToDir(zipPath, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("unzip: %w", err)
	}

	manifestPath := filepath.Join(tmpDir, "xbot.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("read xbot.json: %w (not a valid xbot bundle?)", err)
	}

	var manifest BundleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("parse xbot.json: %w", err)
	}

	if manifest.Schema != BundleManifestSchema {
		os.RemoveAll(tmpDir)
		return nil, "", fmt.Errorf("unsupported bundle schema %d (expected %d)", manifest.Schema, BundleManifestSchema)
	}

	return &manifest, tmpDir, nil
}

// Validate checks that all declared content sources exist in the unpacked directory.
func (bp *BundlePackager) Validate(manifest *BundleManifest, baseDir string) error {
	for _, c := range manifest.Contents {
		fullPath := filepath.Join(baseDir, c.Source)
		// For directories (skills), check dir exists; for files (agents), check file exists
		source := strings.TrimRight(c.Source, "/")
		fullPath = filepath.Join(baseDir, source)
		if _, err := os.Stat(fullPath); err != nil {
			return fmt.Errorf("content %q source %q not found in bundle", c.Name, c.Source)
		}
	}
	return nil
}

// ── helpers ──

func zipDir(srcDir, outputPath string) error {
	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	defer out.Close()

	w := zip.NewWriter(out)
	defer w.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		w, err := w.Create(relPath)
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(w, f)
		return err
	})
}

func unzipToDir(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		destPath := filepath.Join(destDir, f.Name)

		// Prevent zip slip
		if !strings.HasPrefix(filepath.Clean(destPath), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("zip slip detected: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}

		out, err := os.Create(destPath)
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}

		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// parseAgentFileSafe wraps tools.ParseAgentFile to avoid import cycles.
// It reads the file and delegates to tools.ParseAgentFileContent.
func parseAgentFileSafe(path, name string) (tools.SubAgentRole, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tools.SubAgentRole{}, err
	}
	return tools.ParseAgentFileContent(data, name)
}
