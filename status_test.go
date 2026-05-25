package codexauth

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestClientStatusNotLoggedIn(t *testing.T) {
	root := t.TempDir()
	c := NewClient(Options{AppName: "status-empty"})
	c.pathFunc = func() (string, error) {
		return filepath.Join(root, "status-empty", "auth.json"), nil
	}

	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.LoggedIn {
		t.Fatal("LoggedIn = true, want false")
	}
	if st.ConfigPath == "" {
		t.Fatal("ConfigPath is empty")
	}
}

func TestClientStatusLoggedIn(t *testing.T) {
	root := t.TempDir()
	c := NewClient(Options{AppName: "status-logged-in"})
	c.pathFunc = func() (string, error) {
		return filepath.Join(root, "status-logged-in", "auth.json"), nil
	}

	expires := time.Now().Add(2 * time.Hour).UnixMilli()
	if err := c.save(AuthFile{OpenAI: &Credentials{
		Access:    "access",
		Refresh:   "refresh",
		Expires:   expires,
		AccountID: "acct-123",
	}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.LoggedIn {
		t.Fatal("LoggedIn = false, want true")
	}
	if st.Stale {
		t.Fatal("Stale = true, want false")
	}
	if st.AccountID != "acct-123" {
		t.Fatalf("AccountID = %q, want acct-123", st.AccountID)
	}
	if st.ExpiresAt.UnixMilli() != expires {
		t.Fatalf("ExpiresAt = %d, want %d", st.ExpiresAt.UnixMilli(), expires)
	}
}

func TestClientStatusStale(t *testing.T) {
	root := t.TempDir()
	c := NewClient(Options{AppName: "status-stale"})
	c.pathFunc = func() (string, error) {
		return filepath.Join(root, "status-stale", "auth.json"), nil
	}

	if err := c.save(AuthFile{OpenAI: &Credentials{
		Access:  "access",
		Refresh: "refresh",
		Expires: time.Now().Add(30 * time.Second).UnixMilli(),
	}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.LoggedIn {
		t.Fatal("LoggedIn = false, want true")
	}
	if !st.Stale {
		t.Fatal("Stale = false, want true")
	}
}
