package cli

import (
	"os"
	"path/filepath"

	log "xbot/logger"

	"golang.design/x/clipboard"
)

// clipboardInitialized tracks whether clipboard.Init() has been called successfully.
var clipboardInitialized bool

// clipboardInit initializes the clipboard library.
// Returns an error if the system doesn't support clipboard access
// (e.g. headless Linux without X11/Wayland).
func clipboardInit() error {
	if clipboardInitialized {
		return nil
	}

	// clipboard.Init may panic on headless systems — we catch it here
	// rather than letting it crash the TUI.
	defer func() {
		if r := recover(); r != nil {
			log.WithField("panic", r).Warn("clipboard.Init panicked")
		}
	}()

	clipboard.Init()
	clipboardInitialized = true
	return nil
}

// clipboardReadImage reads image data from the system clipboard.
// Returns nil if the clipboard doesn't contain an image.
func clipboardReadImage() []byte {
	data := clipboard.Read(clipboard.FmtImage)
	return data
}

// getXbotHome returns the ~/.xbot directory path.
func getXbotHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".xbot"), nil
}
