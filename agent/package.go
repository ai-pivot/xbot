package agent

// PackageManifestSchema is the schema version for package.json.
const PackageManifestSchema = 2

// PackageManifest represents the package.json manifest inside a package.
// A package bundles related skills, agents, and plugins into a single
// distributable unit. The manifest declares what the package contains;
// installation delegates to per-type installers (skill, agent, plugin).
type PackageManifest struct {
	Schema      int              `json:"schema"`
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Author      string           `json:"author"`
	Description string           `json:"description"`
	Homepage    string           `json:"homepage,omitempty"`
	License     string           `json:"license,omitempty"`
	Contents    []PackageContent `json:"contents"`
}

// PackageContent declares a single item inside a package.
type PackageContent struct {
	Type   string `json:"type"`   // skill | agent | plugin
	Name   string `json:"name"`   // item name (skill dir name or agent .md name or plugin ID)
	Source string `json:"source"` // relative path inside the package directory
}
