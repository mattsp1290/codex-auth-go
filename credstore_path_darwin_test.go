//go:build darwin

package codexauth

import (
	"path/filepath"
	"testing"
)

// TestPath_Darwin verifies that Path() resolves to
// $HOME/Library/Application Support/advisor/auth.json on macOS.
// HOME is overridden via t.Setenv so the production pathFunc
// (which calls os.UserConfigDir) returns a controlled path.
func TestPath_Darwin(t *testing.T) {
	const fakeHome = "/Users/fake-credpath-test"
	t.Setenv("HOME", fakeHome)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path(): %v", err)
	}

	// Exact check anchored to fakeHome — a loose HasSuffix would pass even if
	// Path() returned /Library/Application Support/... with HOME ignored.
	want := filepath.Join(fakeHome, "Library", "Application Support", "advisor", "auth.json")
	if path != want {
		t.Errorf("Path() = %q; want %q", path, want)
	}
}
