package codexauth

import (
	"errors"
	"os"
	"runtime"
	"testing"
)

func TestErrNoBrowser_Sentinel(t *testing.T) {
	if ErrNoBrowser == nil {
		t.Fatal("ErrNoBrowser must not be nil")
	}
	if !errors.Is(ErrNoBrowser, ErrNoBrowser) {
		t.Fatal("errors.Is(ErrNoBrowser, ErrNoBrowser) must be true")
	}
}

// TestOpenBrowser_RejectsNonHTTPURL verifies that openBrowser returns an error
// for any URL that does not begin with http:// or https://, guarding against
// leading-dash argument injection into system browser launchers.
func TestOpenBrowser_RejectsNonHTTPURL(t *testing.T) {
	cases := []string{
		"",
		"-a",
		"--help",
		"file:///etc/passwd",
		"ftp://example.com",
		"javascript:alert(1)",
		"/tmp/evil",
	}
	for _, u := range cases {
		if err := openBrowser(u); err == nil {
			t.Errorf("openBrowser(%q) = nil, want error", u)
		}
	}
}

// TestOpenBrowser_CurrentPlatform exercises openBrowser on whatever OS the test
// suite runs on. Set CODEXAUTH_TEST_OPEN_BROWSER=1 to enable; skipped by
// default to avoid spawning a real browser on developer workstations.
func TestOpenBrowser_CurrentPlatform(t *testing.T) {
	if os.Getenv("CODEXAUTH_TEST_OPEN_BROWSER") == "" {
		t.Skip("set CODEXAUTH_TEST_OPEN_BROWSER=1 to run (opens a real browser tab)")
	}
	const testURL = "http://127.0.0.1:0/"
	err := openBrowser(testURL)
	switch runtime.GOOS {
	case "darwin", "windows":
		// Launcher binary is always present; Start() must not return an error.
		if err != nil {
			t.Errorf("openBrowser on %s: %v", runtime.GOOS, err)
		}
	case "linux":
		// xdg-open may or may not be present in headless CI; both outcomes are valid.
		if err != nil && !errors.Is(err, ErrNoBrowser) {
			t.Errorf("openBrowser on linux = %v, want nil or ErrNoBrowser", err)
		}
	default:
		if !errors.Is(err, ErrNoBrowser) {
			t.Errorf("openBrowser on %s = %v, want ErrNoBrowser", runtime.GOOS, err)
		}
	}
}
