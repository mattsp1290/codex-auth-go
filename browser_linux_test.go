//go:build linux

package codexauth

import (
	"errors"
	"testing"
)

// TestOpenBrowser_Linux_NoXdgOpen verifies that openBrowser returns ErrNoBrowser
// when xdg-open is absent from PATH (common in headless / server environments).
func TestOpenBrowser_Linux_NoXdgOpen(t *testing.T) {
	t.Setenv("PATH", "")
	err := openBrowser("http://example.com")
	if !errors.Is(err, ErrNoBrowser) {
		t.Errorf("openBrowser with empty PATH = %v, want ErrNoBrowser", err)
	}
}
