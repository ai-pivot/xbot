package memory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Providers must never touch a database driver or a SQL dialect.
//
// Why: a provider that holds a *sql.DB will inevitably grow dialect-specific SQL
// (placeholders, FTS syntax, boolean literals) and then be unrunnable on some
// backend. DB-backed memory is implemented in the storage layer and reaches
// providers only through storage.* interfaces (tokenized query in, ordered
// results out). Memory providers are officially managed — there is no external
// provider extension point.
//
// Guarded here (source-level, non-test files under memory/):
//   - no database/sql, no xbot/storage/sqlite, no pgx imports
//   - ProviderDeps carries no driver / DSN / dialect field
//
// This test is intentionally strict: adding such an import is the exact change
// that reintroduces the coupling, so it must fail loudly in CI.
func TestProvidersDoNotDependOnDatabaseDrivers(t *testing.T) {
	forbidden := []string{
		`"database/sql"`,
		`"xbot/storage/sqlite"`,
		`"github.com/jackc/pgx`,
		`"github.com/mattn/go-sqlite3"`,
		`"modernc.org/sqlite"`,
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob memory/*.go: %v", err)
	}
	subDirs, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read memory dir: %v", err)
	}
	for _, d := range subDirs {
		if !d.IsDir() {
			continue
		}
		sub, err := filepath.Glob(filepath.Join(d.Name(), "*.go"))
		if err != nil {
			t.Fatalf("glob %s/*.go: %v", d.Name(), err)
		}
		files = append(files, sub...)
	}

	checked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(src)
		for _, f := range forbidden {
			if strings.Contains(body, f) {
				t.Errorf("%s imports %s — memory providers must reach storage only through storage.* interfaces "+
					"(see docs/plans/memory-providers-pg-parity.md)", path, f)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no memory provider sources were checked — the glob is broken")
	}
}

// TestProviderDepsHasNoDriverSurface pins the shape of ProviderDeps: a driver
// handle, DSN or dialect knob in the deps struct is how a provider becomes
// backend-bound, so the struct must stay semantic-only.
func TestProviderDepsHasNoDriverSurface(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "memory.go", nil, 0)
	if err != nil {
		t.Fatalf("parse memory.go: %v", err)
	}

	badNames := map[string]bool{
		"DB": true, "DBConn": true, "Conn": true, "SQLDB": true,
		"DSN": true, "Driver": true, "Dialect": true, "Tx": true,
	}

	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "ProviderDeps" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		found = true
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if badNames[name.Name] {
					t.Errorf("memory.ProviderDeps.%s is a driver/dialect surface — providers must receive "+
						"storage.* interfaces instead (see docs/plans/memory-providers-pg-parity.md)", name.Name)
				}
			}
			if sel, ok := field.Type.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "sql" {
					t.Errorf("memory.ProviderDeps has a database/sql field type %s.%s — not allowed",
						pkg.Name, sel.Sel.Name)
				}
			}
		}
		return true
	})
	if !found {
		t.Fatal("ProviderDeps struct not found in memory.go — the guard is stale")
	}
}
