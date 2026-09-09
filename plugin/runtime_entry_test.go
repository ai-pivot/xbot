package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvePluginBinary_WindowsExeSuffix — release tarballs carry
// platform-suffixed binaries (bin/genui-plugin.exe) while plugin.json entries
// stay platform-neutral ("./bin/genui-plugin"). On windows the resolver must
// pick the suffixed sibling when it exists in the plugin dir.
func TestResolvePluginBinary_WindowsExeSuffix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "genui-plugin.exe"), []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		goos string
		want string
	}{
		{"windows + exe sibling exists", "./bin/genui-plugin", "windows", "./bin/genui-plugin.exe"},
		{"windows + already suffixed", "./bin/genui-plugin.exe", "windows", "./bin/genui-plugin.exe"},
		{"windows + no sibling falls through", "./bin/other-plugin", "windows", "./bin/other-plugin"},
		{"windows + absolute path untouched", "C:\\tools\\plugin.exe", "windows", "C:\\tools\\plugin.exe"},
		{"unix + same entry untouched", "./bin/genui-plugin", "linux", "./bin/genui-plugin"},
		{"unix + windows goos ignored", "./bin/genui-plugin", "darwin", "./bin/genui-plugin"},
		{"empty path", "", "windows", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePluginBinary(tc.path, dir, tc.goos)
			if got != tc.want {
				t.Errorf("resolvePluginBinary(%q, dir, %q) = %q, want %q", tc.path, tc.goos, got, tc.want)
			}
		})
	}
}
