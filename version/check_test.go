package version

import (
	"context"
	"testing"
)

func TestResolveChannel(t *testing.T) {
	tests := []struct {
		tag      string
		expected ReleaseChannel
	}{
		{"v1.2.3", ChannelStable},
		{"v0.0.23", ChannelStable},
		{"v1.0.0-beta.1", ChannelBeta},
		{"v2.3.4-beta.3", ChannelBeta},
		{"nightly-20260515", ChannelNightly},
		{"nightly-20251231", ChannelNightly},
		{"v1.0.0-rc.1", ChannelStable}, // rc is not beta, treated as stable
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			got := ResolveChannel(tt.tag)
			if got != tt.expected {
				t.Errorf("ResolveChannel(%q) = %q, want %q", tt.tag, got, tt.expected)
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		a, b     string
		expected bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.1", "v1.0.0", false},
		{"v1.0.0", "v2.0.0", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.3.0", true},
		{"dev", "v1.0.0", true}, // non-semver fallback: a != b
		{"v1.0.0", "v1.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			got := isNewer(tt.a, tt.b)
			if got != tt.expected {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.expected)
			}
		})
	}
}

func TestCheckUpdateSkipDevBuild(t *testing.T) {
	// Save and restore
	origVersion := Version
	origChannel := Channel
	defer func() {
		Version = origVersion
		Channel = origChannel
	}()

	// dev version should skip
	Version = "dev"
	Channel = ""
	result := CheckUpdate(context.TODO())
	if result != nil {
		t.Error("CheckUpdate should return nil for dev version")
	}

	// empty version should skip
	Version = ""
	Channel = ""
	result = CheckUpdate(context.TODO())
	if result != nil {
		t.Error("CheckUpdate should return nil for empty version")
	}

	// non-stable channel should skip
	Version = "v1.0.0"
	Channel = "nightly"
	result = CheckUpdate(context.TODO())
	if result != nil {
		t.Error("CheckUpdate should return nil for non-stable channel")
	}
}

func TestInfoIncludesChannel(t *testing.T) {
	origVersion := Version
	origChannel := Channel
	origCommit := Commit
	defer func() {
		Version = origVersion
		Channel = origChannel
		Commit = origCommit
	}()

	Version = "v1.0.0"
	Channel = "stable"
	Commit = "abc123"
	info := Info()
	if info == "" {
		t.Fatal("Info() returned empty string")
	}
	// Should contain channel info
	if !contains(info, "channel: stable") {
		t.Errorf("Info() = %q, should contain 'channel: stable'", info)
	}
	if !contains(info, "v1.0.0") {
		t.Errorf("Info() = %q, should contain 'v1.0.0'", info)
	}
}

func TestInfoDevChannel(t *testing.T) {
	origVersion := Version
	origChannel := Channel
	defer func() {
		Version = origVersion
		Channel = origChannel
	}()

	Version = "v1.0.0"
	Channel = "" // empty means dev
	info := Info()
	if !contains(info, "channel: dev") {
		t.Errorf("Info() = %q, should contain 'channel: dev' for empty Channel", info)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
