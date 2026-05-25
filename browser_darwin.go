//go:build darwin

package codexauth

import "os/exec"

// canOpenBrowserNative reports whether a browser launcher is available on this
// platform. On Darwin, `open` is always present.
func canOpenBrowserNative() error { return nil }

// openBrowser opens url in the user's default browser via the macOS `open` helper.
func openBrowser(url string) error {
	if err := validateURL(url); err != nil {
		return err
	}
	return exec.Command("open", url).Start()
}
