package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// storedCreds is the JSON wire format for a single provider's token set.
// IDToken is intentionally absent — it is never persisted.
type storedCreds struct {
	Type      string `json:"type"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"` // no omitempty — zero must round-trip
	AccountID string `json:"accountId,omitempty"`
}

// diskAuthFile mirrors the top-level structure of auth.json.
// Anthropic is kept as *json.RawMessage so it round-trips byte-for-byte.
type diskAuthFile struct {
	OpenAI    *storedCreds     `json:"openai,omitempty"`
	Anthropic *json.RawMessage `json:"anthropic,omitempty"`
}

// AuthFile is the caller-facing view of auth.json.
//
// On POSIX, auth.json is created with mode 0600 and its parent directory
// with mode 0700. On Windows, file access is governed by inherited ACLs
// from %LocalAppData%; Chmod is a best-effort approximation only.
type AuthFile struct {
	// OpenAI is nil if no OpenAI creds are stored. When non-nil, IDToken is
	// always "" — the ID token is consumed once by ExtractAccountID and is
	// never persisted.
	OpenAI    *Credentials
	Anthropic *json.RawMessage // raw bytes; never inspected by this package
}

// mu serializes all credstore operations within this process.
var mu sync.Mutex

// pathMu guards reads and writes of pathFunc. It must always be acquired
// before mu so the locking order is consistent (pathMu → mu, never mu → pathMu).
var pathMu sync.RWMutex

// pathFunc returns the canonical path to auth.json. It is a variable so
// tests can redirect to t.TempDir() without exporting a setter. All reads
// must go through getPath(); all writes must hold pathMu.Lock().
var pathFunc = func() (string, error) {
	return defaultPath("advisor")
}

// getPath reads pathFunc under pathMu.RLock so concurrent test overrides
// and production reads are race-free.
func getPath() (string, error) {
	pathMu.RLock()
	fn := pathFunc
	pathMu.RUnlock()
	return fn()
}

// flockTimeout is the maximum time to wait for a cross-process file lock.
const flockTimeout = 30 * time.Second

// acquireFlock opens a shared (read) or exclusive (write) advisory lock on
// p+".lock" with a 30-second deadline. Returns the locked Flock; the caller
// must call Unlock when done.
func acquireFlockExclusive(p string) (*flock.Flock, error) {
	lk := flock.New(p + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), flockTimeout)
	defer cancel()
	locked, err := lk.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("credstore: acquire lock on %s: %w", p+".lock", err)
	}
	if !locked {
		return nil, fmt.Errorf("credstore: lock on %s held by another process for >%s", p+".lock", flockTimeout)
	}
	return lk, nil
}

func acquireFlockShared(p string) (*flock.Flock, error) {
	lk := flock.New(p + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), flockTimeout)
	defer cancel()
	locked, err := lk.TryRLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("credstore: acquire read lock on %s: %w", p+".lock", err)
	}
	if !locked {
		return nil, fmt.Errorf("credstore: read lock on %s blocked for >%s", p+".lock", flockTimeout)
	}
	return lk, nil
}

// Path returns the canonical path to auth.json.
func Path() (string, error) { return getPath() }

// Path returns this client's canonical path to auth.json.
func (c *Client) Path() (string, error) { return c.path() }

// Load reads auth.json and returns its contents. A missing file is not an
// error; the returned AuthFile has nil fields in that case.
//
// Load acquires a shared (read) flock for cross-process safety, but does
// NOT hold the lock after returning. Callers performing a read-modify-write
// (e.g. load → refresh tokens → save) should be aware that another process
// may persist a rotation in between; the Save call's "re-read under lock +
// merge" path protects against clobbering the *other* provider's entry but
// does NOT detect that the OpenAI refresh token the caller is about to
// overwrite was already rotated by a sibling process. For concurrent-safe
// refresh, route through a single goroutine / singleflight per process.
func Load() (AuthFile, error) {
	return loadWithPath(getPath)
}

func (c *Client) load() (AuthFile, error) {
	return loadWithPath(c.path)
}

func loadWithPath(resolvePath func() (string, error)) (AuthFile, error) {
	p, err := resolvePath()
	if err != nil {
		return AuthFile{}, err
	}
	dir := filepath.Dir(p)

	mu.Lock()
	defer mu.Unlock()

	// If the config directory doesn't exist there is nothing to load; skip
	// the flock (which requires the directory to exist for the lock file).
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		return AuthFile{}, nil
	}

	lk, err := acquireFlockShared(p)
	if err != nil {
		return AuthFile{}, err
	}
	defer lk.Unlock() //nolint:errcheck

	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return AuthFile{}, nil
	}
	if err != nil {
		return AuthFile{}, err
	}

	var disk diskAuthFile
	if err := json.Unmarshal(data, &disk); err != nil {
		return AuthFile{}, err
	}

	var af AuthFile
	if disk.OpenAI != nil {
		af.OpenAI = &Credentials{
			Access:    disk.OpenAI.Access,
			Refresh:   disk.OpenAI.Refresh,
			Expires:   disk.OpenAI.Expires,
			AccountID: disk.OpenAI.AccountID,
		}
	}
	af.Anthropic = disk.Anthropic
	return af, nil
}

// Save persists af to auth.json using the atomic write protocol:
// MkdirAll → flock(.lock) → re-read → merge → CreateTemp → Write+Sync →
// Chmod 0600 → Rename → fsync dir → Chmod dir 0700 → release flock.
//
// Fields in af that are nil are not written; existing values for those
// providers are preserved from the on-disk file (merge-don't-clobber).
func Save(af AuthFile) error {
	return saveWithPath(getPath, af)
}

func (c *Client) save(af AuthFile) error {
	return saveWithPath(c.path, af)
}

func saveWithPath(resolvePath func() (string, error), af AuthFile) error {
	p, err := resolvePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)

	mu.Lock()
	defer mu.Unlock()

	// 1. Ensure directory exists with tight permissions.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// 2. Acquire cross-process flock on the .lock sidecar file.
	lk, err := acquireFlockExclusive(p)
	if err != nil {
		return err
	}
	defer lk.Unlock() //nolint:errcheck

	// 3. Re-read under lock to merge concurrent writes from other processes.
	var disk diskAuthFile
	data, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &disk); err != nil {
			disk = diskAuthFile{} // corrupted — start fresh
		}
	}

	// 4. Apply the caller's changes (nil means "leave existing intact").
	if af.OpenAI != nil {
		disk.OpenAI = &storedCreds{
			Type:      "openai_codex",
			Access:    af.OpenAI.Access,
			Refresh:   af.OpenAI.Refresh,
			Expires:   af.OpenAI.Expires,
			AccountID: af.OpenAI.AccountID,
		}
	}
	if af.Anthropic != nil {
		disk.Anthropic = af.Anthropic
	}

	return atomicWrite(p, dir, &disk)
}

// Delete removes one provider's credentials from auth.json.
// Unrecognized provider names are silently ignored (idempotent).
// Supported values: "openai", "anthropic".
func Delete(provider string) error {
	return deleteWithPath(getPath, provider)
}

func (c *Client) delete(provider string) error {
	return deleteWithPath(c.path, provider)
}

func deleteWithPath(resolvePath func() (string, error), provider string) error {
	p, err := resolvePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)

	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	lk, err := acquireFlockExclusive(p)
	if err != nil {
		return err
	}
	defer lk.Unlock() //nolint:errcheck

	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var disk diskAuthFile
	if err := json.Unmarshal(data, &disk); err != nil {
		disk = diskAuthFile{} // corrupted — start fresh (mirrors Save behavior)
	}

	switch provider {
	case "openai":
		disk.OpenAI = nil
	case "anthropic":
		disk.Anthropic = nil
	}

	return atomicWrite(p, dir, &disk)
}

// atomicWrite marshals disk and writes it to p via CreateTemp+Sync+Rename+dirSync.
// The final file has mode 0600; dir is fsynced for rename durability, then
// tightened to 0700. Callers must hold both mu and the flock before calling.
func atomicWrite(p, dir string, disk *diskAuthFile) error {
	out, err := json.Marshal(disk)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".auth-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()        //nolint:errcheck
			os.Remove(tmpPath) //nolint:errcheck
		}
	}()

	if _, err := tmp.Write(out); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Chmod 0600 explicitly before rename: os.CreateTemp documents 0600 on
	// POSIX but callers should not rely on that across platforms. Setting it
	// before rename guarantees the final file is 0600 from the moment it
	// exists at the canonical path.
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, p); err != nil {
		return err
	}
	committed = true

	// Fsync parent dir so the rename is durable across power loss.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	// Tighten the directory — MkdirAll may have raced with a permissive umask.
	return os.Chmod(dir, 0700)
}
