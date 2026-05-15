package version

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	newRepoAPIURL = "https://api.github.com/repos/ai-pivot/xbot"
	oldRepoAPIURL = "https://api.github.com/repos/CjiW/xbot"
	checkTimeout  = 10 * time.Second
)

// ReleaseChannel represents a distribution channel.
type ReleaseChannel string

const (
	ChannelStable  ReleaseChannel = "stable"
	ChannelBeta    ReleaseChannel = "beta"
	ChannelNightly ReleaseChannel = "nightly"
)

// githubRelease represents the GitHub API response for a release.
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Name    string `json:"name"`
	Body    string `json:"body"`
}

// UpdateInfo holds the result of an update check.
type UpdateInfo struct {
	Current   string // local version
	Latest    string // remote latest version
	URL       string // release page URL
	HasUpdate bool
	Channel   ReleaseChannel // which channel was checked
}

// semverRegex matches semantic versioning patterns like v1.2.3, 1.2.3, v1.2.3-rc1, etc.
var semverRegex = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-(.+))?$`)

// parseSemver extracts major, minor, patch from a version string.
// Returns -1,-1,-1 if the string doesn't match semver.
func parseSemver(v string) (major, minor, patch int) {
	v = strings.TrimSpace(v)
	m := semverRegex.FindStringSubmatch(v)
	if m == nil {
		return -1, -1, -1
	}
	fmt.Sscanf(m[1], "%d", &major)
	fmt.Sscanf(m[2], "%d", &minor)
	fmt.Sscanf(m[3], "%d", &patch)
	return
}

// isNewer returns true if b is newer than a (semver comparison).
// Falls back to string comparison if either version is not valid semver.
func isNewer(a, b string) bool {
	aMaj, aMin, aPat := parseSemver(a)
	bMaj, bMin, bPat := parseSemver(b)
	if aMaj < 0 || bMaj < 0 {
		// Can't compare as semver, just check if they're different
		return a != b
	}
	if bMaj != aMaj {
		return bMaj > aMaj
	}
	if bMin != aMin {
		return bMin > aMin
	}
	return bPat > aPat
}

// CheckUpdate queries GitHub Releases API for the latest version and compares
// with the local build version. Returns nil if the check fails or version is a
// dev build (in which case the caller should silently ignore).
//
// By default, only checks the stable channel. Nightly/beta builds skip the
// automatic check (they track their own channel manually via install scripts).
//
// Migration: tries the new repo (ai-pivot/xbot) first. If the new repo has no
// releases yet, falls back to the old repo (CjiW/xbot) so existing users don't
// lose update notifications during the transition.
func CheckUpdate(ctx context.Context) *UpdateInfo {
	// Skip if version is completely empty or dev build
	if Version == "" || Version == "dev" {
		return nil
	}

	// Only stable channel checks for updates automatically.
	// Nightly/beta users manage their own channel via install scripts.
	if Channel != "" && Channel != string(ChannelStable) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	release := fetchLatestStable(ctx, newRepoAPIURL)
	if release == nil {
		release = fetchLatestStable(ctx, oldRepoAPIURL)
	}
	if release == nil || release.TagName == "" {
		return nil
	}

	hasUpdate := isNewer(Version, release.TagName)
	return &UpdateInfo{
		Current:   Version,
		Latest:    release.TagName,
		URL:       release.HTMLURL,
		HasUpdate: hasUpdate,
		Channel:   ChannelStable,
	}
}

// ResolveChannel determines the release channel from a tag name.
//   - "v1.2.3"         → stable
//   - "v1.2.3-beta.1"  → beta
//   - "nightly-20260515" → nightly
func ResolveChannel(tag string) ReleaseChannel {
	if strings.HasPrefix(tag, "nightly-") {
		return ChannelNightly
	}
	if m := semverRegex.FindStringSubmatch(tag); len(m) == 5 && m[4] != "" {
		// Has prerelease suffix like -beta.1, -rc.2
		pre := strings.ToLower(m[4])
		if strings.HasPrefix(pre, "beta") {
			return ChannelBeta
		}
	}
	return ChannelStable
}

// fetchLatestStable queries the /releases/latest endpoint which returns the
// latest non-prerelease, non-draft release — which is exactly the stable channel.
func fetchLatestStable(ctx context.Context, repoAPIURL string) *githubRelease {
	apiURL := strings.TrimRight(repoAPIURL, "/") + "/releases/latest"
	return fetchRelease(ctx, apiURL)
}

// FetchLatestByChannel returns the latest release for a specific channel.
// It uses different strategies per channel:
//   - stable: /releases/latest (non-prerelease)
//   - beta: lists all releases, finds latest with -beta suffix
//   - nightly: lists all releases, finds latest nightly- tag
func FetchLatestByChannel(ctx context.Context, ch ReleaseChannel) *githubRelease {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	for _, repoURL := range []string{newRepoAPIURL, oldRepoAPIURL} {
		var release *githubRelease
		switch ch {
		case ChannelStable:
			release = fetchLatestStable(ctx, repoURL)
		case ChannelBeta, ChannelNightly:
			release = fetchLatestByTagPrefix(ctx, repoURL, ch)
		}
		if release != nil {
			return release
		}
	}
	return nil
}

// fetchLatestByTagPrefix lists all releases and finds the latest one matching
// the channel's tag prefix pattern.
func fetchLatestByTagPrefix(ctx context.Context, repoAPIURL string, ch ReleaseChannel) *githubRelease {
	apiURL := strings.TrimRight(repoAPIURL, "/") + "/releases"
	releases := fetchReleaseList(ctx, apiURL)
	if releases == nil {
		return nil
	}

	for _, r := range releases {
		tag := r.TagName
		resolved := ResolveChannel(tag)
		if resolved == ch {
			return &r
		}
	}
	return nil
}

// fetchRelease queries a single release API endpoint.
func fetchRelease(ctx context.Context, apiURL string) *githubRelease {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "xbot-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil
	}

	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil
	}

	return &release
}

// fetchReleaseList queries the releases list API and returns all releases.
func fetchReleaseList(ctx context.Context, apiURL string) []githubRelease {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "xbot-cli")
	q := req.URL.Query()
	q.Set("per_page", "20")
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil
	}

	var releases []githubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil
	}

	return releases
}
