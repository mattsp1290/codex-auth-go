package codexauth

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// TestCrossProcessFlockSubprocess is an internal subprocess helper used by
// TestCrossProcessFlock_MergeNoClobber and TestCrossProcessFlock_SameProvider.
// It is NOT meant to run directly — when CRED_FLOCK_MODE is unset (normal
// `go test` execution) it skips immediately.
//
// When invoked by the parent test via os.Executable + -test.run, it reads its
// configuration from environment variables and calls Save exactly once.
//
// Env vars consumed:
//
//	CRED_FLOCK_MODE   — "openai" | "anthropic" (required, non-empty activates)
//	CRED_FLOCK_PATH   — absolute path to auth.json (required)
//	CRED_FLOCK_ACCESS — value for Credentials.Access or Anthropic api_key
//	CRED_FLOCK_REFRESH — value for Credentials.Refresh (openai only)
func TestCrossProcessFlockSubprocess(t *testing.T) {
	mode := os.Getenv("CRED_FLOCK_MODE")
	if mode == "" {
		t.Skip("internal subprocess helper; set CRED_FLOCK_MODE to activate")
	}

	authPath := os.Getenv("CRED_FLOCK_PATH")
	if authPath == "" {
		t.Fatal("CRED_FLOCK_PATH not set")
	}

	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	pathMu.Lock()
	orig := pathFunc
	pathFunc = func() (string, error) { return authPath, nil }
	pathMu.Unlock()
	defer func() {
		pathMu.Lock()
		pathFunc = orig
		pathMu.Unlock()
	}()

	var af AuthFile
	switch mode {
	case "openai":
		af.OpenAI = &Credentials{
			Access:  os.Getenv("CRED_FLOCK_ACCESS"),
			Refresh: os.Getenv("CRED_FLOCK_REFRESH"),
			Expires: 1,
		}
	case "anthropic":
		raw := json.RawMessage(`{"api_key":"` + os.Getenv("CRED_FLOCK_ACCESS") + `"}`)
		af.Anthropic = &raw
	default:
		t.Fatalf("unknown CRED_FLOCK_MODE: %s", mode)
	}

	if err := Save(af); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestCrossProcessFlock_MergeNoClobber verifies that two OS-level processes
// calling Save concurrently on distinct providers do not corrupt auth.json.
// Process A writes OpenAI creds; Process B writes Anthropic creds. The flock
// serializes their writes; each re-reads under lock and merges. Both providers
// must be present in the final file.
//
// Runs 10 consecutive iterations to satisfy the acceptance criterion.
func TestCrossProcessFlock_MergeNoClobber(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cross-process flock: advisory flock semantics differ on Windows")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	for i := range 10 {
		t.Run(fmt.Sprintf("run%02d", i+1), func(t *testing.T) {
			crossProcessMergeRun(t, exe)
		})
	}
}

// TestCrossProcessFlock_SameProvider verifies that two OS-level processes racing
// to Save the same provider (OpenAI) produce a valid, non-corrupted auth.json.
// Both processes write distinct refresh tokens; one must survive intact. The test
// does not assert WHICH writer wins — flock serializes them but order is not
// deterministic without a barrier.
//
// Runs 10 consecutive iterations.
func TestCrossProcessFlock_SameProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cross-process flock: advisory flock semantics differ on Windows")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	for i := range 10 {
		t.Run(fmt.Sprintf("run%02d", i+1), func(t *testing.T) {
			crossProcessSameProviderRun(t, exe)
		})
	}
}

// crossProcessMergeRun is one iteration of TestCrossProcessFlock_MergeNoClobber.
func crossProcessMergeRun(t *testing.T, exe string) {
	t.Helper()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "advisor", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Build env for each subprocess independently to avoid backing-array aliasing.
	env1 := append(os.Environ(),
		"CRED_FLOCK_MODE=openai",
		"CRED_FLOCK_PATH="+authPath,
		"CRED_FLOCK_ACCESS=access-proc-1",
		"CRED_FLOCK_REFRESH=refresh-proc-1",
	)
	env2 := append(os.Environ(),
		"CRED_FLOCK_MODE=anthropic",
		"CRED_FLOCK_PATH="+authPath,
		"CRED_FLOCK_ACCESS=anth-secret",
	)

	cmd1 := exec.Command(exe, "-test.run=^TestCrossProcessFlockSubprocess$", "-test.v=false")
	cmd1.Env = env1

	cmd2 := exec.Command(exe, "-test.run=^TestCrossProcessFlockSubprocess$", "-test.v=false")
	cmd2.Env = env2

	var wg sync.WaitGroup
	var out1, out2 []byte
	var err1, err2 error
	wg.Add(2)
	go func() { defer wg.Done(); out1, err1 = cmd1.CombinedOutput() }()
	go func() { defer wg.Done(); out2, err2 = cmd2.CombinedOutput() }()
	wg.Wait()

	if err1 != nil {
		t.Errorf("subprocess 1 (openai): %v\noutput: %s", err1, out1)
	}
	if err2 != nil {
		t.Errorf("subprocess 2 (anthropic): %v\noutput: %s", err2, out2)
	}

	af := loadForTest(t, authPath)

	if af.OpenAI == nil {
		t.Error("OpenAI is nil — cross-process merge lost OpenAI creds")
	} else if af.OpenAI.Access != "access-proc-1" {
		t.Errorf("OpenAI.Access = %q, want %q", af.OpenAI.Access, "access-proc-1")
	} else if af.OpenAI.Refresh != "refresh-proc-1" {
		t.Errorf("OpenAI.Refresh = %q, want %q", af.OpenAI.Refresh, "refresh-proc-1")
	}

	if af.Anthropic == nil {
		t.Error("Anthropic is nil — cross-process merge lost Anthropic creds")
	}
}

// crossProcessSameProviderRun is one iteration of TestCrossProcessFlock_SameProvider.
func crossProcessSameProviderRun(t *testing.T, exe string) {
	t.Helper()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "advisor", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	env1 := append(os.Environ(),
		"CRED_FLOCK_MODE=openai",
		"CRED_FLOCK_PATH="+authPath,
		"CRED_FLOCK_ACCESS=access-proc-A",
		"CRED_FLOCK_REFRESH=refresh-proc-A",
	)
	env2 := append(os.Environ(),
		"CRED_FLOCK_MODE=openai",
		"CRED_FLOCK_PATH="+authPath,
		"CRED_FLOCK_ACCESS=access-proc-B",
		"CRED_FLOCK_REFRESH=refresh-proc-B",
	)

	cmd1 := exec.Command(exe, "-test.run=^TestCrossProcessFlockSubprocess$", "-test.v=false")
	cmd1.Env = env1
	cmd2 := exec.Command(exe, "-test.run=^TestCrossProcessFlockSubprocess$", "-test.v=false")
	cmd2.Env = env2

	var wg sync.WaitGroup
	var out1, out2 []byte
	var err1, err2 error
	wg.Add(2)
	go func() { defer wg.Done(); out1, err1 = cmd1.CombinedOutput() }()
	go func() { defer wg.Done(); out2, err2 = cmd2.CombinedOutput() }()
	wg.Wait()

	if err1 != nil {
		t.Errorf("subprocess 1 (openai-A): %v\noutput: %s", err1, out1)
	}
	if err2 != nil {
		t.Errorf("subprocess 2 (openai-B): %v\noutput: %s", err2, out2)
	}

	af := loadForTest(t, authPath)

	// Both write the same provider — one wins. We can't assert which, but
	// the result must be a valid, non-nil entry with one of the two refresh tokens.
	if af.OpenAI == nil {
		t.Fatal("OpenAI is nil after two concurrent saves — auth.json was corrupted")
	}
	switch af.OpenAI.Refresh {
	case "refresh-proc-A", "refresh-proc-B":
		// expected: one of the two writers won cleanly
	default:
		t.Errorf("OpenAI.Refresh = %q — not one of the two expected values (corruption?)", af.OpenAI.Refresh)
	}
}

// loadForTest redirects pathFunc to authPath, calls Load, then restores pathFunc.
// It is not equivalent to setTestPath (which uses t.TempDir) — it points to a
// caller-supplied path that already has data written by subprocesses.
func loadForTest(t *testing.T, authPath string) AuthFile {
	t.Helper()
	pathMu.Lock()
	orig := pathFunc
	pathFunc = func() (string, error) { return authPath, nil }
	pathMu.Unlock()
	defer func() {
		pathMu.Lock()
		pathFunc = orig
		pathMu.Unlock()
	}()

	af, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return af
}
