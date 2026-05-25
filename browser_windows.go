//go:build windows

package codexauth

import "os/exec"

// canOpenBrowserNative reports whether a browser launcher is available. On
// Windows, rundll32 is always present.
func canOpenBrowserNative() error { return nil }

// openBrowser opens url via Windows' rundll32 URL handler.
func openBrowser(url string) error {
	if err := validateURL(url); err != nil {
		return err
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
