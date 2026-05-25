//go:build windows

package codexauth

import (
	"path/filepath"
	"testing"
)

// TestPath_Windows verifies that Path() resolves to
// %AppData%\advisor\auth.json on Windows.
// AppData is overridden via t.Setenv so the production pathFunc
// (which calls os.UserConfigDir) returns a controlled path.
func TestPath_Windows(t *testing.T) {
	const fakeAppData = `C:\FakeAppData\CredPathTest`
	t.Setenv("AppData", fakeAppData)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path(): %v", err)
	}

	want := filepath.Join(fakeAppData, "advisor", "auth.json")
	if path != want {
		t.Errorf("Path() = %q; want %q", path, want)
	}
}
