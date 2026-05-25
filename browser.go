package codexauth

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoBrowser is returned by openBrowser when no suitable browser-launcher
// binary can be found on the current platform. Callers should treat any
// non-nil error from openBrowser as a signal to print the URL for the user
// to copy — ErrNoBrowser specifically indicates the launcher binary was absent,
// but darwin and windows implementations may return other exec errors on failure.
var ErrNoBrowser = errors.New("codexauth: no browser launcher found")

// validateURL returns an error if url is not an HTTP or HTTPS URL. This guards
// against leading-dash argument injection (e.g. "-a Calculator") if a future
// caller bypasses BuildAuthorizeURL, since system browser launchers vary in
// whether they honor "--" end-of-options.
func validateURL(url string) error {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("openBrowser: URL %q must begin with http:// or https://", url)
	}
	return nil
}
