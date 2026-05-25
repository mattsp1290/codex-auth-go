package codexauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// setTestPath redirects pathFunc to a file under t.TempDir() for the
// duration of the test and restores it on cleanup. All reads and writes of
// pathFunc go through pathMu so this swap is race-free even when goroutines
// are calling Load/Save/Delete concurrently within the test.
func setTestPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "advisor", "auth.json")
	pathMu.Lock()
	orig := pathFunc
	pathFunc = func() (string, error) { return p, nil }
	pathMu.Unlock()
	t.Cleanup(func() {
		pathMu.Lock()
		pathFunc = orig
		pathMu.Unlock()
	})
	return p
}

func TestLoad_NoFile(t *testing.T) {
	setTestPath(t)

	af, err := Load()
	if err != nil {
		t.Fatalf("Load with no file: %v", err)
	}
	if af.OpenAI != nil || af.Anthropic != nil {
		t.Error("expected empty AuthFile when auth.json does not exist")
	}
}

func TestSave_FileMode(t *testing.T) {
	p := setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 1}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("auth.json mode = %04o, want 0600", got)
	}
}

func TestSave_DirMode(t *testing.T) {
	p := setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 1}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0700 {
		t.Errorf("advisor/ dir mode = %04o, want 0700", got)
	}
}

// TestSave_FileMode_ResavePersistsMode verifies that a second Save re-applies
// 0600 rather than leaving whatever mode was in place. The test deliberately
// relaxes the file to 0644 between saves to prove the atomic-rename + explicit
// chmod sequence actively enforces the mode on every write.
func TestSave_FileMode_ResavePersistsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod is a no-op on Windows; mode assertions are POSIX-only")
	}
	p := setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok1", Refresh: "ref1", Expires: 1}}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// Relax mode between saves to prove the second Save actively re-applies 0600.
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatalf("Chmod to 0644 between saves: %v", err)
	}
	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok2", Refresh: "ref2", Expires: 2}}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat after second Save: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("auth.json mode after re-save = %04o, want 0600", got)
	}

	// Content check: second save's data must be on disk (not tok1).
	af, err := Load()
	if err != nil {
		t.Fatalf("Load after second Save: %v", err)
	}
	if af.OpenAI == nil || af.OpenAI.Access != "tok2" {
		t.Errorf("Load().OpenAI.Access = %q, want tok2", func() string {
			if af.OpenAI == nil {
				return "<nil>"
			}
			return af.OpenAI.Access
		}())
	}
}

// TestSave_DirMode_TightensFromPermissive verifies that if the advisor/ directory
// already exists with a permissive mode (0755), Save tightens it to 0700.
// This is necessary because os.MkdirAll does not modify the mode of an existing
// directory — only the trailing os.Chmod(dir, 0700) in atomicWrite tightens it.
func TestSave_DirMode_TightensFromPermissive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod is a no-op on Windows; mode assertions are POSIX-only")
	}
	p := setTestPath(t)
	dir := filepath.Dir(p)

	// Pre-create the directory, then explicitly set 0755 (not umask-dependent).
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatalf("Chmod dir to 0755: %v", err)
	}
	// Verify the setup: dir must genuinely be 0755 before Save.
	if fi, err := os.Stat(dir); err != nil {
		t.Fatalf("Stat dir pre-Save: %v", err)
	} else if got := fi.Mode().Perm(); got != 0755 {
		t.Fatalf("test setup failed: dir mode = %04o, want 0755 before Save", got)
	}

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 1}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0700 {
		t.Errorf("advisor/ dir mode = %04o after Save on pre-created 0755 dir, want 0700", got)
	}
}

func TestSave_Load_RoundTrip(t *testing.T) {
	setTestPath(t)

	in := AuthFile{OpenAI: &Credentials{
		Access:    "access-tok",
		Refresh:   "refresh-tok",
		Expires:   9_999_999,
		AccountID: "acct-123",
		IDToken:   "should-not-be-saved",
	}}

	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OpenAI == nil {
		t.Fatal("OpenAI is nil after Load")
	}

	c := got.OpenAI
	if c.Access != in.OpenAI.Access {
		t.Errorf("Access = %q, want %q", c.Access, in.OpenAI.Access)
	}
	if c.Refresh != in.OpenAI.Refresh {
		t.Errorf("Refresh = %q, want %q", c.Refresh, in.OpenAI.Refresh)
	}
	if c.Expires != in.OpenAI.Expires {
		t.Errorf("Expires = %d, want %d", c.Expires, in.OpenAI.Expires)
	}
	if c.AccountID != in.OpenAI.AccountID {
		t.Errorf("AccountID = %q, want %q", c.AccountID, in.OpenAI.AccountID)
	}
}

func TestSave_IDTokenNotPersisted(t *testing.T) {
	setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{
		Access:  "tok",
		Refresh: "ref",
		IDToken: "secret-id-token",
		Expires: 1,
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OpenAI == nil {
		t.Fatal("OpenAI is nil")
	}
	if got.OpenAI.IDToken != "" {
		t.Errorf("IDToken = %q, want \"\" (IDToken must not be persisted)", got.OpenAI.IDToken)
	}
}

func TestSave_ExpiresZeroNotOmitted(t *testing.T) {
	p := setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 0}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Unmarshal into a raw map to inspect the JSON structure.
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	openai, ok := raw["openai"]
	if !ok {
		t.Fatal("\"openai\" key missing from auth.json")
	}
	expires, ok := openai["expires"]
	if !ok {
		t.Error("\"expires\" key missing (must not be omitted even when zero)")
	} else if v, _ := expires.(float64); v != 0 {
		t.Errorf("expires = %v, want 0", expires)
	}
}

// TestSave_AnthropicPreservation verifies that a Save call with af.Anthropic == nil
// does not clobber an existing Anthropic entry in auth.json (merge-don't-clobber).
func TestSave_AnthropicPreservation(t *testing.T) {
	setTestPath(t)

	rawAnthropic := json.RawMessage(`{"api_key":"anth-secret"}`)
	if err := Save(AuthFile{Anthropic: &rawAnthropic}); err != nil {
		t.Fatalf("first Save (Anthropic-only): %v", err)
	}

	// Second Save sets OpenAI only; af.Anthropic == nil → Anthropic must survive.
	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 42}}); err != nil {
		t.Fatalf("second Save (OpenAI-only): %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OpenAI == nil {
		t.Error("OpenAI is nil after second Save")
	}
	if got.Anthropic == nil {
		t.Fatal("Anthropic was clobbered by second Save (want preserved)")
	}
	if !bytes.Equal(*got.Anthropic, rawAnthropic) {
		t.Errorf("Anthropic bytes = %q, want %q", *got.Anthropic, rawAnthropic)
	}
}

// TestSave_OpenAIPreservation is the symmetric counterpart: saving Anthropic
// must not clobber an existing OpenAI entry.
func TestSave_OpenAIPreservation(t *testing.T) {
	setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 7}}); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	rawAnthropic := json.RawMessage(`{"api_key":"anth-secret"}`)
	if err := Save(AuthFile{Anthropic: &rawAnthropic}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OpenAI == nil {
		t.Error("OpenAI was clobbered by second Save (want preserved)")
	}
	if got.Anthropic == nil {
		t.Error("Anthropic is nil after second Save")
	}
}

func TestDelete_NoFile(t *testing.T) {
	setTestPath(t)

	// Delete with no auth.json present must be a no-op (not an error).
	if err := Delete("openai"); err != nil {
		t.Errorf("Delete with no file: %v", err)
	}
}

func TestDelete_PreservesOther(t *testing.T) {
	setTestPath(t)

	rawAnthropic := json.RawMessage(`{"api_key":"anth-secret"}`)
	if err := Save(AuthFile{
		OpenAI:    &Credentials{Access: "tok", Refresh: "ref", Expires: 1},
		Anthropic: &rawAnthropic,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := Delete("openai"); err != nil {
		t.Fatalf("Delete(\"openai\"): %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OpenAI != nil {
		t.Error("OpenAI not nil after Delete(\"openai\")")
	}
	if got.Anthropic == nil {
		t.Fatal("Anthropic was clobbered by Delete(\"openai\")")
	}
	if !bytes.Equal(*got.Anthropic, rawAnthropic) {
		t.Errorf("Anthropic bytes = %q, want %q", *got.Anthropic, rawAnthropic)
	}
}

func TestDelete_FileMode(t *testing.T) {
	p := setTestPath(t)

	rawAnthropic := json.RawMessage(`{"api_key":"anth-secret"}`)
	if err := Save(AuthFile{
		OpenAI:    &Credentials{Access: "tok", Refresh: "ref", Expires: 1},
		Anthropic: &rawAnthropic,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := Delete("openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("auth.json mode after Delete = %04o, want 0600", got)
	}
}

func TestDelete_Idempotent(t *testing.T) {
	setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 1}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Delete the same provider twice — second call must not error.
	if err := Delete("openai"); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := Delete("openai"); err != nil {
		t.Errorf("second Delete (idempotent): %v", err)
	}
}

func TestDelete_UnknownProvider(t *testing.T) {
	setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 1}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Unknown provider is silently ignored.
	if err := Delete("not-a-real-provider"); err != nil {
		t.Errorf("Delete(unknown provider): %v", err)
	}

	// OpenAI entry must still be present.
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.OpenAI == nil {
		t.Error("OpenAI removed by Delete(unknown provider)")
	}
}

// TestSave_GoldenFileBytes verifies the exact JSON wire format of auth.json.
// It catches field renames, omitempty regressions, and field reorders — all
// of which would silently break any external tooling that parses auth.json.
func TestSave_GoldenFileBytes(t *testing.T) {
	p := setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{
		Access:    "a-access",
		Refresh:   "a-refresh",
		Expires:   1234567890,
		AccountID: "acct-abc",
		IDToken:   "must-not-appear",
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := `{"openai":{"type":"openai_codex","access":"a-access","refresh":"a-refresh","expires":1234567890,"accountId":"acct-abc"}}`
	if got := string(data); got != want {
		t.Errorf("golden bytes mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

// TestSave_GoldenFileBytes_NoAccountID verifies that AccountID omitempty is
// honored (empty AccountID must not appear in the JSON output).
func TestSave_GoldenFileBytes_NoAccountID(t *testing.T) {
	p := setTestPath(t)

	if err := Save(AuthFile{OpenAI: &Credentials{
		Access:  "b-access",
		Refresh: "b-refresh",
		Expires: 0,
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := `{"openai":{"type":"openai_codex","access":"b-access","refresh":"b-refresh","expires":0}}`
	if got := string(data); got != want {
		t.Errorf("golden bytes mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

// TestSave_MalformedJSON_DiscardsAndOverwrites verifies that Save succeeds and
// overwrites a syntactically invalid auth.json. The corrupt content is
// discarded entirely (both providers) and the new AuthFile is written clean.
// This mirrors the "corrupted — start fresh" comment in credstore.go's Save.
func TestSave_MalformedJSON_DiscardsAndOverwrites(t *testing.T) {
	p := setTestPath(t)
	dir := filepath.Dir(p)

	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte(`{not valid json`), 0600); err != nil {
		t.Fatalf("WriteFile corrupt: %v", err)
	}

	if err := Save(AuthFile{OpenAI: &Credentials{Access: "tok", Refresh: "ref", Expires: 1}}); err != nil {
		t.Fatalf("Save on corrupt auth.json: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load after discard: %v", err)
	}
	if got.OpenAI == nil {
		t.Fatal("OpenAI is nil after discard")
	}
	if got.OpenAI.Access != "tok" {
		t.Errorf("OpenAI.Access = %q, want %q", got.OpenAI.Access, "tok")
	}
	// Corrupt file is gone — Anthropic was not in the new AuthFile so must be nil.
	if got.Anthropic != nil {
		t.Error("Anthropic non-nil after full discard — corrupt payload was not cleaned up")
	}
}

// TestSave_ConcurrentInProcess verifies that 50 goroutines calling Save
// concurrently within the same process do not corrupt auth.json. After all
// goroutines complete, Load must return a well-formed (non-nil OpenAI) result.
// Run with go test -race to verify the pathMu / mu locking is race-free.
func TestSave_ConcurrentInProcess(t *testing.T) {
	setTestPath(t)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			creds := &Credentials{
				Access:  fmt.Sprintf("access-%d", idx),
				Refresh: fmt.Sprintf("refresh-%d", idx),
				Expires: int64(idx) * 1000,
			}
			if err := Save(AuthFile{OpenAI: creds}); err != nil {
				t.Errorf("goroutine %d: Save: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	af, err := Load()
	if err != nil {
		t.Fatalf("Load after concurrent Save: %v", err)
	}
	if af.OpenAI == nil {
		t.Fatal("OpenAI is nil after concurrent Save — file was corrupted or clobbered")
	}
}
