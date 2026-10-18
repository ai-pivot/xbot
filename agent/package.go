package agent

// PackageManifestSchema is the schema version for package.json.
const PackageManifestSchema = 2

// PackageManifest represents a package: a flat "subscription list" of base
// components (skills/agents/plugins). It deliberately carries NO version
// constraints, dependency graph, or bundled files — base components are
// versioned and updated individually, and the publisher/user decide what to
// install. Keeping the package flat avoids building a package manager inside
// xbot (the publisher bumps component versions; the user reinstalls/updates the
// components they care about).
type PackageManifest struct {
	Schema      int           `json:"schema"`
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version"` // package's own metadata (not used for resolution)
	Author      string        `json:"author"`
	Description string        `json:"description"`
	Homepage    string        `json:"homepage,omitempty"`
	License     string        `json:"license,omitempty"`
	Items       []PackageItem `json:"items"`
}

// PackageItem names a base component (skill/agent/plugin) to install. Type is
// optional when the name is unambiguous. No version/source: a package only
// lists catalog components; versioning and packaging are the component's own
// concern.
type PackageItem struct {
	Type string `json:"type,omitempty"` // skill | agent | plugin (optional)
	Name string `json:"name"`           // catalog item name
}
