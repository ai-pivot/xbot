package version

import "fmt"

// Version, Commit, BuildTime, Channel are injected via -ldflags at build time.
//
//	go build -ldflags "-X xbot/version.Version=v1.0.0 -X xbot/version.Commit=$(git rev-parse --short HEAD) -X xbot/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X xbot/version.Channel=stable"
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
	Channel   = "" // "stable", "beta", "nightly", or "" for dev builds
)

// Info returns a formatted version string.
func Info() string {
	ch := Channel
	if ch == "" {
		ch = "dev"
	}
	return fmt.Sprintf("xbot %s (channel: %s, commit: %s, built: %s)", Version, ch, Commit, BuildTime)
}
