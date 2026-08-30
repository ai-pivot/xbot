package serverapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// m6: app_pack builds its zip output path from a user-supplied name. A
// traversal name ("../../etc/cron.d/x") must never escape os.TempDir().
func TestAppPackOutputPathSanitizesTraversal(t *testing.T) {
	for _, name := range []string{
		"../../../etc/cron.d/x",
		"..",
		"../..",
		"a/../../b",
		"/etc/passwd",
	} {
		p := appPackOutputPath(name)
		if filepath.Dir(p) != os.TempDir() {
			t.Errorf("appPackOutputPath(%q) = %q escapes %s", name, p, os.TempDir())
		}
		// A traversal component is a full ".." PATH SEGMENT; a filename like
		// "...xbot.zip" (from filepath.Base("..") == ".") is legal and harmless.
		for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
			if seg == ".." {
				t.Errorf("appPackOutputPath(%q) = %q keeps a traversal segment", name, p)
			}
		}
	}

	// Normal names keep the existing behavior (base + .xbot.zip under TempDir).
	if got, want := appPackOutputPath("myapp"), filepath.Join(os.TempDir(), "myapp.xbot.zip"); got != want {
		t.Errorf("appPackOutputPath(myapp) = %q, want %q", got, want)
	}
	if got, want := appPackOutputPath("sub/myapp"), filepath.Join(os.TempDir(), "myapp.xbot.zip"); got != want {
		t.Errorf("appPackOutputPath(sub/myapp) = %q, want %q (base only)", got, want)
	}
}
