package storage

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoRawBackendImports enforces the storage boundary:
//
//	production code → storage / storage/common (interfaces + DTOs)
//	backend impls   → storage/sqlite, storage/internal/pg (not imported directly)
//
// `session` is the composition root (it owns the per-session SQLite DB pool),
// `cmd/xbot-migrate` and `scripts` are migration/dev tooling; those are the
// only non-test exceptions. Everything else must go through Storage/SessionStore.
func TestNoRawBackendImports(t *testing.T) {
	root := ".."
	forbidden := map[string]bool{
		"xbot/storage/sqlite":      true,
		"xbot/storage/pg":          true,
		"xbot/storage/internal/pg": true,
	}
	allowed := func(rel string) bool {
		if strings.HasPrefix(rel, "storage/") {
			return true
		}
		switch {
		case strings.HasPrefix(rel, "session/"):
			return true
		case strings.HasPrefix(rel, "cmd/xbot-migrate/"):
			return true
		case strings.HasPrefix(rel, "scripts/"):
			return true
		case rel == "oauth/storage.go":
			return true
		default:
			return false
		}
	}

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "web", "docs-site":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if allowed(rel) {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if forbidden[p] {
				t.Errorf("%s imports raw backend package %q; use storage/common or the storage facade", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
}
