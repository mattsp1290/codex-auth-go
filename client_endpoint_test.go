package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// farFuture returns a Unix millisecond timestamp 48 hours from now.
func farFuture() int64 {
	return time.Now().Add(48 * time.Hour).UnixMilli()
}

// writeFixtureAuth writes a valid auth.json with the given credentials into a
// dedicated temp directory and returns the full file path. The directory is
// pre-created so loadWithPath can find the file.
func writeFixtureAuth(t *testing.T, creds Credentials) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	disk := diskAuthFile{
		OpenAI: &storedCreds{
			Type:      "openai_codex",
			Access:    creds.Access,
			Refresh:   creds.Refresh,
			Expires:   creds.Expires,
			AccountID: creds.AccountID,
		},
	}
	data, err := json.Marshal(disk)
	if err != nil {
		t.Fatalf("writeFixtureAuth: marshal: %v", err)
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatalf("writeFixtureAuth: write: %v", err)
	}
	return p
}

// ── A. Endpoint override end-to-end (acceptance #2) ──────────────────────────

// TestHTTPClient_EndpointOverride verifies that Options.Endpoint redirects the
// transport to the override URL while still injecting Authorization, originator,
// session_id, and ChatGPT-Account-Id.
//
// We swap tr.base for a captureTransport (like the existing TestHTTPClient_URLRewrite)
// so we inspect the outgoing request _before_ it hits the wire — the request at
// that point still has URL.Scheme and URL.Host set by RoundTrip.
func TestHTTPClient_EndpointOverride(t *testing.T) {
	creds := Credentials{
		Access:    "fake-access",
		AccountID: "acct-123",
		Expires:   farFuture(),
	}
	path := writeFixtureAuth(t, creds)

	c := NewClient(Options{
		AppName:        "advisor",
		Endpoint:       "http://127.0.0.1:1/backend/responses",
		CredentialPath: path,
	})
	hc, err := c.HTTPClient(context.Background())
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}

	// Replace the base transport with a captureTransport so no real network
	// call is made and we can inspect the outgoing request's URL and headers.
	cap := &captureTransport{}
	hc.Transport.(*codexTransport).base = cap

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	if _, err := hc.Do(req); err != nil {
		t.Fatalf("hc.Do: %v", err)
	}

	got := cap.last()
	if got == nil {
		t.Fatal("base transport was not called")
		return
	}
	if got.URL.Scheme != "http" {
		t.Errorf("URL.Scheme = %q, want http", got.URL.Scheme)
	}
	if got.URL.Host != "127.0.0.1:1" {
		t.Errorf("URL.Host = %q, want 127.0.0.1:1", got.URL.Host)
	}
	if got.URL.Path != "/backend/responses" {
		t.Errorf("URL.Path = %q, want /backend/responses", got.URL.Path)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer fake-access" {
		t.Errorf("Authorization = %q, want \"Bearer fake-access\"", auth)
	}
	if orig := got.Header["originator"]; len(orig) == 0 || orig[0] != Originator { //nolint:staticcheck
		t.Errorf("originator = %v, want %q", orig, Originator)
	}
	if sid := got.Header["session_id"]; len(sid) == 0 || sid[0] == "" { //nolint:staticcheck
		t.Errorf("session_id header missing or empty; got %v", sid)
	}
	if acct := got.Header.Get("ChatGPT-Account-Id"); acct != "acct-123" {
		t.Errorf("ChatGPT-Account-Id = %q, want acct-123", acct)
	}
}

// TestHTTPClient_EndpointOverride_QueryContract verifies the two-part query-string
// guarantee documented in Options.Endpoint: (a) any query embedded in the Endpoint
// URL itself is silently dropped; (b) the caller's own query string is preserved.
func TestHTTPClient_EndpointOverride_QueryContract(t *testing.T) {
	path := writeFixtureAuth(t, Credentials{Access: "fake-access", AccountID: "acct-123", Expires: farFuture()})

	c := NewClient(Options{
		AppName:        "advisor",
		Endpoint:       "http://127.0.0.1:1/backend/responses?dropme=1", // embedded query must be dropped
		CredentialPath: path,
	})
	hc, err := c.HTTPClient(context.Background())
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	cap := &captureTransport{}
	hc.Transport.(*codexTransport).base = cap

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses?keep=2", nil)
	if _, err := hc.Do(req); err != nil {
		t.Fatalf("hc.Do: %v", err)
	}
	got := cap.last()
	if got == nil {
		t.Fatal("base transport was not called")
		return
	}
	if got.URL.RawQuery != "keep=2" {
		t.Errorf("RawQuery = %q, want %q (caller query preserved, endpoint query dropped)",
			got.URL.RawQuery, "keep=2")
	}
}

// ── B. Safety rail (acceptance #3) ───────────────────────────────────────────

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
		wantNil bool // nil url expected (empty input)
		errIs   error
	}{
		{name: "empty", input: "", wantNil: true},
		{name: "https any host", input: "https://chatgpt.com/x"},
		{name: "https evil host", input: "https://evil.example/x"},
		{name: "http loopback ipv4", input: "http://127.0.0.1:9/x"},
		{name: "http loopback ipv6", input: "http://[::1]:9/x"},
		{name: "http localhost", input: "http://localhost:9/x"},
		{name: "http non-loopback", input: "http://evil.example/x", wantErr: true, errIs: ErrInsecureEndpoint},
		{name: "ftp scheme", input: "ftp://host/x", wantErr: true},
		{name: "relative path", input: "relative/path", wantErr: true},
		{name: "http 0.0.0.0 rejected", input: "http://0.0.0.0:9/x", wantErr: true, errIs: ErrInsecureEndpoint},
		// isLoopbackHost normalization: uppercase and trailing FQDN dot are accepted
		{name: "http LOCALHOST uppercase ok", input: "http://LOCALHOST:9/x"},
		{name: "http localhost trailing dot ok", input: "http://localhost.:9/x"},
		// Documented rejections: non-standard loopback encodings that bypass naive checks
		{name: "http 127.1 shorthand rejected", input: "http://127.1:9/x", wantErr: true, errIs: ErrInsecureEndpoint},
		{name: "http decimal ip rejected", input: "http://2130706433:9/x", wantErr: true, errIs: ErrInsecureEndpoint},
		{name: "http localhost substring rejected", input: "http://notlocalhost.evil/x", wantErr: true, errIs: ErrInsecureEndpoint},
		// IPv4-mapped IPv6 loopback: Go's net.ParseIP("::ffff:127.0.0.1") returns 127.0.0.1
		// and IsLoopback()=true, so this form is intentionally accepted.
		{name: "http ipv4-mapped ipv6 loopback ok", input: "http://[::ffff:127.0.0.1]:9/x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := parseEndpoint(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseEndpoint(%q): want error, got nil", tc.input)
				}
				if tc.errIs != nil && !errors.Is(err, tc.errIs) {
					t.Errorf("parseEndpoint(%q): err = %v, want errors.Is(err, %v)", tc.input, err, tc.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEndpoint(%q): unexpected error: %v", tc.input, err)
			}
			if tc.wantNil {
				if u != nil {
					t.Errorf("parseEndpoint(%q): want nil URL, got %v", tc.input, u)
				}
			} else if u == nil {
				t.Errorf("parseEndpoint(%q): want non-nil URL, got nil", tc.input)
			}
		})
	}
}

// TestHTTPClient_InsecureEndpointBlackBox verifies that HTTPClient returns
// ErrInsecureEndpoint for a cleartext non-loopback endpoint, and that this
// validation fires before credential load (so no staged auth.json is needed).
func TestHTTPClient_InsecureEndpointBlackBox(t *testing.T) {
	c := NewClient(Options{
		AppName:  "advisor",
		Endpoint: "http://evil.example/x",
	})
	_, err := c.HTTPClient(context.Background())
	if err == nil {
		t.Fatal("expected error for insecure endpoint, got nil")
	}
	if !errors.Is(err, ErrInsecureEndpoint) {
		t.Errorf("error = %v, want errors.Is(err, ErrInsecureEndpoint)", err)
	}
}

// ── C. CredentialPath (acceptance #4) ────────────────────────────────────────

func TestHTTPClient_CredentialPath(t *testing.T) {
	t.Run("valid_entry", func(t *testing.T) {
		path := writeFixtureAuth(t, Credentials{
			Access:  "a",
			Expires: farFuture(),
		})
		c := NewClient(Options{AppName: "advisor", CredentialPath: path})
		hc, err := c.HTTPClient(context.Background())
		if err != nil {
			t.Fatalf("HTTPClient with valid CredentialPath: %v", err)
		}
		if hc == nil {
			t.Fatal("HTTPClient returned nil")
		}
	})

	t.Run("no_openai_entry", func(t *testing.T) {
		dir := t.TempDir()
		empty := filepath.Join(dir, "auth.json")
		if err := os.WriteFile(empty, []byte(`{}`), 0600); err != nil {
			t.Fatalf("write empty auth: %v", err)
		}
		c := NewClient(Options{AppName: "advisor", CredentialPath: empty})
		_, err := c.HTTPClient(context.Background())
		if !errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("want ErrNotLoggedIn, got %v", err)
		}
	})
}

// ── D. No refresh on far-future expiry (acceptance #5) ───────────────────────

// TestHTTPClient_NoRefreshOnFarFuture verifies that a credential with far-future
// expiry never hits the token endpoint when using Options.Endpoint + CredentialPath.
func TestHTTPClient_NoRefreshOnFarFuture(t *testing.T) {
	// A token-endpoint fake that fails the test if called.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token endpoint called unexpectedly for far-future credentials")
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(tokenSrv.Close)
	withTestEndpoint(t, tokenSrv.URL)

	// A fake Codex endpoint that just accepts requests.
	codexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(codexSrv.Close)

	path := writeFixtureAuth(t, Credentials{
		Access:  "no-refresh-needed",
		Refresh: "would-be-used-if-refresh-fired", // present — ensures expiry is the sole reason refresh is skipped
		Expires: farFuture(),
	})
	c := NewClient(Options{
		AppName:        "advisor",
		Endpoint:       codexSrv.URL,
		CredentialPath: path,
	})
	hc, err := c.HTTPClient(context.Background())
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	if _, err := hc.Do(req); err != nil {
		t.Fatalf("hc.Do: %v", err)
	}
	// If the token endpoint fake was hit, the test already failed via t.Errorf above.
}
