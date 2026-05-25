//go:build !darwin && !linux && !windows

package codexauth

// canOpenBrowserNative always returns ErrNoBrowser on unsupported platforms.
func canOpenBrowserNative() error { return ErrNoBrowser }

// openBrowser returns ErrNoBrowser on unsupported platforms. The caller should
// print the URL so the user can copy-paste it into a browser manually.
func openBrowser(url string) error {
	if err := validateURL(url); err != nil {
		return err
	}
	return ErrNoBrowser
}
