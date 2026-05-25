//go:build linux

package codexauth

import (
	"path/filepath"
	"testing"
)

// TestPath_Linux_WithXDGConfigHome verifies that Path() uses $XDG_CONFIG_HOME
// when that variable is set, per the XDG Base Directory Specification.
func TestPath_Linux_WithXDGConfigHome(t *testing.T) {
	const fakeXDG = "/home/fake-credpath-test/.config-xdg"
	t.Setenv("XDG_CONFIG_HOME", fakeXDG)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path(): %v", err)
	}

	want := filepath.Join(fakeXDG, "advisor", "auth.json")
	if path != want {
		t.Errorf("Path() = %q; want %q", path, want)
	}
}

// TestPath_Linux_FallbackToHome verifies that Path() falls back to
// $HOME/.config/advisor/auth.json when XDG_CONFIG_HOME is not set.
func TestPath_Linux_FallbackToHome(t *testing.T) {
	const fakeHome = "/home/fake-credpath-test"
	t.Setenv("XDG_CONFIG_HOME", "") // empty → fall through to $HOME/.config
	t.Setenv("HOME", fakeHome)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path(): %v", err)
	}

	want := filepath.Join(fakeHome, ".config", "advisor", "auth.json")
	if path != want {
		t.Errorf("Path() = %q; want %q", path, want)
	}
}
