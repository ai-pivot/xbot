package common

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCommonDoesNotImportBackends enforces the dependency direction that makes
// the common/sqlite/pg split possible:
//
//	storage/common  ← storage/sqlite
//	storage/common  ← storage/internal/pg
//
// If common imports a backend, that backend's adapter (which imports common)
// becomes an import cycle. This is a package-level invariant, so keep it
// executable instead of relying on review.
func TestCommonDoesNotImportBackends(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			switch path {
			case "xbot/storage/sqlite", "xbot/storage/internal/pg":
				t.Errorf("%s imports backend package %q (common must stay backend-agnostic)", file, path)
			}
		}
	}
}
