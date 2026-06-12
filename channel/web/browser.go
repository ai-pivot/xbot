package web

import "xbot/channel"

// openBrowser opens the given URL in the user's default browser.
func openBrowser(url string) error {
	return channel.OpenBrowser(url)
}
