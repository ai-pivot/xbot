package agent

// PackageManifestSchema is the schema version for package.json.
const PackageManifestSchema = 2

// PackageManifest represents the package.json manifest inside a package.
// A package is an "extension pack": its contents reference catalog components
// (skills/agents/plugins) by name, and it may declare dependencies on other
// catalog items/packages that must be installed first.
type PackageManifest struct {
	Schema       int                 `json:"schema"`
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Author       string              `json:"author"`
	Description  string              `json:"description"`
	Homepage     string              `json:"homepage,omitempty"`
	License      string              `json:"license,omitempty"`
	Contents     []PackageContent    `json:"contents"`
	Dependencies []PackageDependency `json:"dependencies,omitempty"`
}

// PackageContent declares a single item inside a package.
type PackageContent struct {
	Type   string `json:"type"`   // skill | agent | plugin
	Name   string `json:"name"`   // item name (skill dir name or agent .md name or plugin ID)
	Source string `json:"source"` // relative path inside the package directory
}

// PackageDependency declares a catalog item (skill/agent/plugin/package) that
// must be installed for this package to work. Dependencies are resolved from the
// catalog by name and installed BEFORE the package's own contents.
type PackageDependency struct {
	Type    string `json:"type,omitempty"`    // skill | agent | plugin | package (optional when unambiguous)
	Name    string `json:"name"`              // catalog item name
	Version string `json:"version,omitempty"` // informational for now (no semver resolution yet)
}
