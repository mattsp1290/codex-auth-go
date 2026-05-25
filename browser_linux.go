//go:build linux

package codexauth

import (
	"os"
	"os/exec"
)

// canOpenBrowserNative reports whether a graphical browser can be opened.
// It requires both xdg-open in PATH and an active display server
// ($DISPLAY or $WAYLAND_DISPLAY). Headless hosts with xdg-open installed as
// a transitive dep satisfy the PATH check but have no display, causing a
// silent 5-minute timeout rather than a clean device-flow fallback — hence
// the display-server gate.
func canOpenBrowserNative() error {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return ErrNoBrowser
	}
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return ErrNoBrowser
	}
	return nil
}

// openBrowser opens url via xdg-open. Returns ErrNoBrowser if xdg-open is not
// found in PATH (common in headless / server environments).
func openBrowser(url string) error {
	if err := validateURL(url); err != nil {
		return err
	}
	path, err := exec.LookPath("xdg-open")
	if err != nil {
		return ErrNoBrowser
	}
	return exec.Command(path, url).Start()
}
