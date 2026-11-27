package tools

// Upload reference resolution — ONE implementation, shared by every tool that
// consumes a user attachment reference (view_image, DownloadFile).
//
// The Web channel writes an upload reference into the message content. With the
// default LOCAL storage backend the file lives on this machine, so the reference
// the model is handed is an absolute path (see channel/web/uploadModelRef).
//
// Historically that reference was the same-origin RELATIVE URL
// "/api/files/download?key=uploads%2F…[&inline=1]" — which no tool can consume:
// it carries no host/port, and the endpoint requires a session cookie.
//
// P0 user report 2026-11-27 (real session, turn 5384): the model copied that URL
// out of the user message and failed twice in a row —
//
//	view_image:   Error: unsupported url scheme (want http/https)
//	DownloadFile: Error: download request failed: Get "/api/files/download?…":
//	              unsupported protocol scheme ""
//
// The scheme must therefore stay resolvable (old content is already persisted in
// session DBs), and content that lands in a TOOL message — which the multimodal
// layer never parses — must be localized before the model sees it.

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"xbot/config"
)

// uploadRefPath is the endpoint path of the canonical upload reference.
const uploadRefPath = "/api/files/download"

// LocalUploadRefPath maps a web upload reference to the file on THIS machine.
//
// Accepted forms (all of them produced by the Web channel over time):
//   - /api/files/download?key=uploads%2F4%2F<x>.png[&inline=1]
//   - <publicBase>/api/files/download?key=…      (operator-declared public_url)
//
// Returns false for anything else (http(s) image URLs, plain paths, foreign
// endpoints) and for keys that would escape the upload root — the caller then
// proceeds with its normal URL path.
func LocalUploadRefPath(ref string) (string, bool) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	if !strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), uploadRefPath) {
		return "", false
	}
	key := u.Query().Get("key")
	if key == "" {
		return "", false
	}
	// Keys are slash-separated and always live under uploads/ (the local storage
	// layout is <xbotHome>/uploads/<key>). Clean first so "../" can never escape:
	// "uploads/../../etc/passwd" cleans to "../etc/passwd" and fails the prefix.
	clean := path.Clean(key)
	if !strings.HasPrefix(clean, "uploads/") || strings.Contains(clean, "..") {
		return "", false
	}
	file := filepath.Join(localUploadRoot(), filepath.FromSlash(clean))
	if st, err := os.Stat(file); err != nil || st.IsDir() {
		return "", false
	}
	return file, true
}

// localUploadRoot is the local storage root. Mirrors channel/web.LocalUploadRoot
// (the writer side): <xbotHome>/uploads. Kept here so the tools layer never
// depends on the channel layer for a plain layout constant.
func localUploadRoot() string {
	return filepath.Join(config.XbotHome(), "uploads")
}

// existingLocalFile reports whether ref is a plain local path that exists — the
// form the model is told to use for locally stored uploads.
func existingLocalFile(ref string) (string, bool) {
	if ref == "" || !filepath.IsAbs(ref) {
		return "", false
	}
	if st, err := os.Stat(ref); err != nil || st.IsDir() {
		return "", false
	}
	return ref, true
}

var uploadImageRefRe = regexp.MustCompile(`!\[([^\]\n]*)\]\(([^)\s]+)\)`)

// LocalizeUploadImageRefs rewrites upload image references into local-path
// captions. Used for content that reaches the model WITHOUT the multimodal
// layer (tool-role messages: user interjections, synthetic pairs, injected
// notifications) — there an image can only be text, so the model needs a path it
// can hand to view_image/Read, not a web URL it cannot resolve.
//
// Non-upload image references (http(s) URLs, viewimg:// refs) are left alone.
func LocalizeUploadImageRefs(content string) string {
	if !strings.Contains(content, uploadRefPath) {
		return content
	}
	return uploadImageRefRe.ReplaceAllStringFunc(content, func(match string) string {
		m := uploadImageRefRe.FindStringSubmatch(match)
		if m == nil {
			return match
		}
		local, ok := LocalUploadRefPath(m[2])
		if !ok {
			return match
		}
		return `[image alt="` + m[1] + `" local="` + local + `"]`
	})
}
