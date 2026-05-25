// Tests in this file mutate package-level vars (browserAvailableCheck,
// loginBrowserFn, loginDeviceFn, logoutHTTPClient, logoutStderr, pathFunc) and
// MUST NOT call t.Parallel().
package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// httpDoerFunc adapts a func to the httpDoer interface.
type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// overrideBrowserCheck replaces browserAvailableCheck for the test duration.
func overrideBrowserCheck(t *testing.T, fn func() error) {
	t.Helper()
	orig := browserAvailableCheck
	browserAvailableCheck = fn
	t.Cleanup(func() { browserAvailableCheck = orig })
}

// stubLoginBrowser replaces loginBrowserFn with a stub returning fixedCreds/fixedErr.
func stubLoginBrowser(t *testing.T, fixedCreds Credentials, fixedErr error) {
	t.Helper()
	orig := loginBrowserFn
	loginBrowserFn = func(context.Context) (Credentials, error) { return fixedCreds, fixedErr }
	t.Cleanup(func() { loginBrowserFn = orig })
}

// stubLoginDevice replaces loginDeviceFn with a stub returning fixedCreds/fixedErr.
func stubLoginDevice(t *testing.T, fixedCreds Credentials, fixedErr error) {
	t.Helper()
	orig := loginDeviceFn
	loginDeviceFn = func(context.Context) (Credentials, error) { return fixedCreds, fixedErr }
	t.Cleanup(func() { loginDeviceFn = orig })
}

// overrideLogoutClient replaces logoutHTTPClient for the test duration.
func overrideLogoutClient(t *testing.T, fn httpDoerFunc) {
	t.Helper()
	orig := logoutHTTPClient
	logoutHTTPClient = fn
	t.Cleanup(func() { logoutHTTPClient = orig })
}

// captureLogoutStderr replaces logoutStderr with a fresh buffer and returns it.
func captureLogoutStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	orig := logoutStderr
	logoutStderr = buf
	t.Cleanup(func() { logoutStderr = orig })
	return buf
}

// saveCreds writes creds as the OpenAI entry. Call redirectBrowserCredstore first.
func saveCreds(t *testing.T, creds Credentials) {
	t.Helper()
	if err := Save(AuthFile{OpenAI: &creds}); err != nil {
		t.Fatalf("saveCreds: %v", err)
	}
}

// freshCreds returns a non-expired Credentials set for use in tests.
func freshCreds() Credentials {
	return Credentials{
		Access:  "my-access-token",
		Refresh: "my-refresh-token",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}
}

// ── Login tests ───────────────────────────────────────────────────────────────

// TestLogin_ForceDevice verifies that forceDevice=true always routes to
// LoginDevice, even when a browser is nominally available.
func TestLogin_ForceDevice(t *testing.T) {
	deviceCreds := Credentials{Access: "device-at", Refresh: "rt", Expires: time.Now().Add(time.Hour).UnixMilli()}
	stubLoginDevice(t, deviceCreds, nil)
	stubLoginBrowser(t, Credentials{Access: "should-not-be-returned"}, nil)
	overrideBrowserCheck(t, func() error { return nil }) // browser "available"

	got, err := Login(context.Background(), true)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.Access != deviceCreds.Access {
		t.Errorf("Access = %q, want %q (device flow must be used)", got.Access, deviceCreds.Access)
	}
}

// TestLogin_BrowserAvailable verifies that forceDevice=false with a browser
// present routes to LoginBrowser.
func TestLogin_BrowserAvailable(t *testing.T) {
	browserCreds := Credentials{Access: "browser-at", Refresh: "rt", Expires: time.Now().Add(time.Hour).UnixMilli()}
	stubLoginBrowser(t, browserCreds, nil)
	stubLoginDevice(t, Credentials{Access: "should-not-be-returned"}, nil)
	overrideBrowserCheck(t, func() error { return nil })

	got, err := Login(context.Background(), false)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.Access != browserCreds.Access {
		t.Errorf("Access = %q, want %q (browser flow must be used)", got.Access, browserCreds.Access)
	}
}

// TestLogin_NoBrowser verifies that forceDevice=false with no browser available
// falls back to LoginDevice instead of erroring.
func TestLogin_NoBrowser(t *testing.T) {
	deviceCreds := Credentials{Access: "device-fallback-at", Refresh: "rt", Expires: time.Now().Add(time.Hour).UnixMilli()}
	stubLoginDevice(t, deviceCreds, nil)
	stubLoginBrowser(t, Credentials{Access: "should-not-be-returned"}, nil)
	overrideBrowserCheck(t, func() error { return ErrNoBrowser })

	got, err := Login(context.Background(), false)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.Access != deviceCreds.Access {
		t.Errorf("Access = %q, want %q (device fallback must be used)", got.Access, deviceCreds.Access)
	}
}

// ── HTTPClient tests ──────────────────────────────────────────────────────────

// TestHTTPClient_ErrNotLoggedIn verifies that HTTPClient returns ErrNotLoggedIn
// when the credstore holds no OpenAI entry.
func TestHTTPClient_ErrNotLoggedIn(t *testing.T) {
	redirectBrowserCredstore(t)

	_, err := HTTPClient(context.Background())
	if err == nil {
		t.Fatal("expected ErrNotLoggedIn with empty credstore, got nil")
	}
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("error = %v, want errors.Is(err, ErrNotLoggedIn)", err)
	}
}

// TestHTTPClient_URLRewrite verifies that the returned *http.Client rewrites
// requests to the Codex Responses-API endpoint (chatgpt.com).
func TestHTTPClient_URLRewrite(t *testing.T) {
	redirectBrowserCredstore(t)
	saveCreds(t, freshCreds())

	client, err := HTTPClient(context.Background())
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}

	// Inject a captureTransport as the base so no real network call is made.
	cap := &captureTransport{}
	client.Transport.(*codexTransport).base = cap

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("client.Do: %v", err)
	}

	got := cap.last()
	if got == nil {
		t.Fatal("base transport was not called — codexTransport not wired correctly")
	}
	if got.URL.Host != "chatgpt.com" {
		t.Errorf("URL.Host = %q, want chatgpt.com", got.URL.Host)
	}
	if got.URL.Path != "/backend-api/codex/responses" {
		t.Errorf("URL.Path = %q, want /backend-api/codex/responses", got.URL.Path)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer my-access-token" {
		t.Errorf("Authorization = %q, want \"Bearer my-access-token\"", auth)
	}
}

// ── Logout tests ──────────────────────────────────────────────────────────────

// TestLogout_Empty verifies that Logout returns nil when no credentials are
// stored (idempotent).
func TestLogout_Empty(t *testing.T) {
	redirectBrowserCredstore(t)

	if err := Logout(context.Background()); err != nil {
		t.Fatalf("Logout on empty credstore: %v", err)
	}
}

// TestLogout_HappyPath verifies the full Logout flow: the revocation POST is
// sent with the correct form body and headers, the OpenAI credstore entry is
// deleted, and any pre-existing Anthropic entry is preserved.
func TestLogout_HappyPath(t *testing.T) {
	redirectBrowserCredstore(t)
	creds := freshCreds()
	creds.Refresh = "my-refresh-token"
	saveCreds(t, creds)

	// Also save an Anthropic entry to verify it survives Logout.
	anthropicRaw := json.RawMessage(`{"apiKey":"anthro-key"}`)
	if err := Save(AuthFile{Anthropic: &anthropicRaw}); err != nil {
		t.Fatalf("Save anthropic: %v", err)
	}

	var capturedMethod, capturedURL, capturedContentType, capturedBody string
	overrideLogoutClient(t, httpDoerFunc(func(r *http.Request) (*http.Response, error) {
		capturedMethod = r.Method
		capturedURL = r.URL.String()
		capturedContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}, nil
	}))

	if err := Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	// Verify revocation request shape.
	if capturedMethod != http.MethodPost {
		t.Errorf("revoke method = %q, want POST", capturedMethod)
	}
	wantURL := Issuer + "/oauth/revoke"
	if capturedURL != wantURL {
		t.Errorf("revoke URL = %q, want %q", capturedURL, wantURL)
	}
	if capturedContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", capturedContentType)
	}
	vals, _ := url.ParseQuery(capturedBody)
	if vals.Get("token") != "my-refresh-token" {
		t.Errorf("token = %q, want my-refresh-token", vals.Get("token"))
	}
	if vals.Get("token_type_hint") != "refresh_token" {
		t.Errorf("token_type_hint = %q, want refresh_token", vals.Get("token_type_hint"))
	}
	if vals.Get("client_id") != ClientID {
		t.Errorf("client_id = %q, want %q", vals.Get("client_id"), ClientID)
	}

	// OpenAI entry must be gone.
	af, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if af.OpenAI != nil {
		t.Error("OpenAI creds still present after Logout")
	}
	// Anthropic entry must be preserved.
	if af.Anthropic == nil {
		t.Error("Anthropic creds were clobbered by Logout")
	}
}

// TestLogout_RevokeNetworkError verifies that a network failure during revocation
// causes a warning on logoutStderr but does not prevent the local credential delete.
func TestLogout_RevokeNetworkError(t *testing.T) {
	redirectBrowserCredstore(t)
	saveCreds(t, freshCreds())
	stderr := captureLogoutStderr(t)

	overrideLogoutClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unreachable")
	}))

	if err := Logout(context.Background()); err != nil {
		t.Fatalf("Logout should succeed despite revoke error: %v", err)
	}

	if !bytes.Contains(stderr.Bytes(), []byte("revoke failed")) {
		t.Errorf("stderr missing revoke warning; got: %q", stderr.String())
	}

	af, err := Load()
	if err != nil {
		t.Fatalf("Load after logout: %v", err)
	}
	if af.OpenAI != nil {
		t.Error("OpenAI creds still present after Logout (network error must not block local delete)")
	}
}
