// Tests in this file mutate package-level vars (openBrowserFn, loginBrowserStderr,
// browserLoginTimeout, tokenEndpointURL, pathFunc) and MUST NOT call t.Parallel().
package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// browserFlowTestTimeout is the per-test cap on LoginBrowser's internal callback
// wait. Kept well under Go's default test deadline so failures surface promptly.
const browserFlowTestTimeout = 5 * time.Second

// shrinkBrowserTimeout replaces browserLoginTimeout with browserFlowTestTimeout
// for the duration of the test and restores it on cleanup.
func shrinkBrowserTimeout(t *testing.T) {
	t.Helper()
	orig := browserLoginTimeout
	browserLoginTimeout = browserFlowTestTimeout
	t.Cleanup(func() { browserLoginTimeout = orig })
}

// shrinkBrowserTimeoutTo sets browserLoginTimeout to d for the duration of the
// test. Use this when a test needs to exercise the internal timeout branch and
// needs a tighter cap than browserFlowTestTimeout.
func shrinkBrowserTimeoutTo(t *testing.T, d time.Duration) {
	t.Helper()
	orig := browserLoginTimeout
	browserLoginTimeout = d
	t.Cleanup(func() { browserLoginTimeout = orig })
}

// captureBrowserStderr replaces loginBrowserStderr with a fresh buffer for the
// duration of the test and returns the buffer for assertions.
func captureBrowserStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	orig := loginBrowserStderr
	loginBrowserStderr = buf
	t.Cleanup(func() { loginBrowserStderr = orig })
	return buf
}

// overrideOpenBrowser replaces openBrowserFn with fn for the duration of the
// test and restores the original on cleanup.
func overrideOpenBrowser(t *testing.T, fn func(string) error) {
	t.Helper()
	orig := openBrowserFn
	openBrowserFn = fn
	t.Cleanup(func() { openBrowserFn = orig })
}

// redirectBrowserCredstore redirects pathFunc to a temp dir so LoginBrowser
// does not write to the real auth.json during tests. Call this in every test
// that invokes LoginBrowser — the redirect is cheap and guards against future
// refactors that move Save earlier in the flow.
func redirectBrowserCredstore(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "auth.json")
	pathMu.Lock()
	orig := pathFunc
	pathFunc = func() (string, error) { return p, nil }
	pathMu.Unlock()
	t.Cleanup(func() {
		pathMu.Lock()
		pathFunc = orig
		pathMu.Unlock()
	})
}

// fireCallback sends a GET to the local callback server with the supplied code
// and state. StartCallbackServer binds the listener synchronously before
// returning, so no sleep is required — the kernel's TCP accept queue buffers the
// connection until the server goroutine calls Accept. Parameters are
// URL-encoded via url.Values so values containing +, /, or = are safe.
func fireCallback(code, state string) {
	q := url.Values{"code": {code}, "state": {state}}
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d%s?%s", OAuthPort, callbackPath, q.Encode())
	resp, err := http.Get(callbackURL) //nolint:noctx
	if err == nil {
		resp.Body.Close() //nolint:errcheck
	}
}

// TestLoginBrowser_HappyPath runs the full browser OAuth flow end-to-end with a
// fake token server and a stub openBrowserFn that fires the callback
// asynchronously. Verifies that LoginBrowser returns Credentials that match the
// fake token response and that openBrowserFn was called exactly once.
func TestLoginBrowser_HappyPath(t *testing.T) {
	redirectBrowserCredstore(t)
	shrinkBrowserTimeout(t)
	captureBrowserStderr(t)

	fixture := tokenResponse{
		AccessToken:  "at-browser",
		RefreshToken: "rt-browser",
		// Minimal two-segment JWT — ExtractAccountID tolerates an unrecognised
		// payload and returns ""; LoginBrowser does not require a non-empty AccountID.
		IDToken:   "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEyMyJ9.sig",
		ExpiresIn: 3600,
	}
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token endpoint: method = %q, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("token endpoint: ParseForm: %v", err)
		}
		if gt := r.PostFormValue("grant_type"); gt != "authorization_code" {
			t.Errorf("token endpoint: grant_type = %q, want authorization_code", gt)
		}
		if ru := r.PostFormValue("redirect_uri"); ru != RedirectURI {
			t.Errorf("token endpoint: redirect_uri = %q, want %q", ru, RedirectURI)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fixture) //nolint:errcheck
	}))
	t.Cleanup(tokenSrv.Close)
	withTestEndpoint(t, tokenSrv.URL)

	// Stub: parse the state from the authorize URL and fire the callback with it.
	openBrowserCalled := false
	overrideOpenBrowser(t, func(authorizeURL string) error {
		openBrowserCalled = true
		parsed, err := url.Parse(authorizeURL)
		if err != nil {
			return err
		}
		state := parsed.Query().Get("state")
		go fireCallback("browser-auth-code", state)
		return nil
	})

	creds, err := LoginBrowser(context.Background())
	if err != nil {
		t.Fatalf("LoginBrowser: %v", err)
	}
	if !openBrowserCalled {
		t.Error("openBrowserFn was not called (LoginBrowser must open the browser)")
	}
	if creds.Access != fixture.AccessToken {
		t.Errorf("Access = %q, want %q", creds.Access, fixture.AccessToken)
	}
	if creds.Refresh != fixture.RefreshToken {
		t.Errorf("Refresh = %q, want %q", creds.Refresh, fixture.RefreshToken)
	}
}

// TestLoginBrowser_PortBusy is the trip-wire test: pre-binding port OAuthPort
// must cause LoginBrowser to return *PortInUseError WITHOUT ever calling
// openBrowserFn. This enforces the invariant that the callback listener binds
// BEFORE the browser opens — opening first would create a window where the user
// completes the OAuth flow before the server is ready to accept the callback.
func TestLoginBrowser_PortBusy(t *testing.T) {
	redirectBrowserCredstore(t)

	blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", OAuthPort))
	if err != nil {
		t.Skipf("cannot pre-bind 127.0.0.1:%d: %v", OAuthPort, err)
	}
	t.Cleanup(func() { blocker.Close() }) //nolint:errcheck

	calls := 0
	overrideOpenBrowser(t, func(string) error { calls++; return nil })

	_, loginErr := LoginBrowser(context.Background())
	if loginErr == nil {
		t.Fatal("LoginBrowser returned nil error with port already bound")
	}
	var pie *PortInUseError
	if !errors.As(loginErr, &pie) {
		t.Errorf("error = %T (%v), want *PortInUseError", loginErr, loginErr)
	}
	if calls != 0 {
		t.Errorf("openBrowserFn called %d time(s), want 0 (trip-wire: browser must not open before listener binds)", calls)
	}
}

// TestLoginBrowser_StateMismatch verifies that a callback carrying the wrong
// state parameter causes LoginBrowser to return ErrStateMismatch.
func TestLoginBrowser_StateMismatch(t *testing.T) {
	redirectBrowserCredstore(t)
	shrinkBrowserTimeout(t)
	captureBrowserStderr(t)

	// Stub fires the callback with a state value that will never match.
	overrideOpenBrowser(t, func(string) error {
		go fireCallback("some-code", "WRONG-STATE-VALUE")
		return nil
	})

	_, err := LoginBrowser(context.Background())
	if err == nil {
		t.Fatal("LoginBrowser returned nil error on state mismatch")
	}
	if !errors.Is(err, ErrStateMismatch) {
		t.Errorf("error = %v, want error wrapping ErrStateMismatch", err)
	}
}

// TestLoginBrowser_CtxCancel verifies that cancelling the caller's context
// before the OAuth callback arrives causes LoginBrowser to return ctx.Err()
// (context.Canceled), not the internal browserLoginTimeout error.
func TestLoginBrowser_CtxCancel(t *testing.T) {
	redirectBrowserCredstore(t)
	shrinkBrowserTimeout(t)
	captureBrowserStderr(t)

	ctx, cancel := context.WithCancel(context.Background())

	// Stub: do not fire a callback; cancel the caller's context after a short delay.
	overrideOpenBrowser(t, func(string) error {
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
		return nil
	})

	_, err := LoginBrowser(ctx)
	if err == nil {
		t.Fatal("LoginBrowser returned nil error after caller ctx cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// TestLoginBrowser_NoBrowserFallback verifies that when openBrowserFn returns
// an error (e.g. ErrNoBrowser on headless hosts), LoginBrowser falls back to
// printing the authorize URL to loginBrowserStderr and continues waiting for the
// callback. The overall flow still succeeds when the callback arrives.
func TestLoginBrowser_NoBrowserFallback(t *testing.T) {
	redirectBrowserCredstore(t)
	shrinkBrowserTimeout(t)
	stderr := captureBrowserStderr(t)

	fixture := tokenResponse{
		AccessToken:  "at-fallback",
		RefreshToken: "rt-fallback",
		IDToken:      "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEyMyJ9.sig",
		ExpiresIn:    3600,
	}
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fixture) //nolint:errcheck
	}))
	t.Cleanup(tokenSrv.Close)
	withTestEndpoint(t, tokenSrv.URL)

	// Stub returns ErrNoBrowser (headless host) but still fires the callback
	// so the flow can complete.
	overrideOpenBrowser(t, func(authorizeURL string) error {
		parsed, _ := url.Parse(authorizeURL)
		go fireCallback("code-after-fallback", parsed.Query().Get("state"))
		return ErrNoBrowser
	})

	_, err := LoginBrowser(context.Background())
	if err != nil {
		t.Fatalf("LoginBrowser: %v", err)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("Open this URL to log in:")) {
		t.Errorf("stderr missing fallback prompt; got: %q", stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("/oauth/authorize?")) {
		t.Errorf("stderr missing authorize URL fragment; got: %q", stderr.String())
	}
}

// TestLoginBrowser_InternalTimeout verifies that when browserLoginTimeout elapses
// without the caller's context being cancelled, LoginBrowser returns a wrapped
// "timed out waiting for OAuth callback" error — not context.DeadlineExceeded.
// The production code explicitly constructs this message when ctx.Err() is nil
// (outer context healthy, only the inner cap fired). If someone refactors that
// branch to return innerCtx.Err(), callers using errors.Is(err, context.DeadlineExceeded)
// would silently behave differently; this test catches that regression.
func TestLoginBrowser_InternalTimeout(t *testing.T) {
	redirectBrowserCredstore(t)
	shrinkBrowserTimeoutTo(t, 100*time.Millisecond)
	captureBrowserStderr(t)

	// Browser "opens" but no callback ever fires.
	overrideOpenBrowser(t, func(string) error { return nil })

	_, err := LoginBrowser(context.Background())
	if err == nil {
		t.Fatal("LoginBrowser returned nil error after internal timeout")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v; internal timeout must not propagate innerCtx.Err()", err)
	}
	if !strings.Contains(err.Error(), "timed out waiting for OAuth callback") {
		t.Errorf("error = %q, want substring %q", err.Error(), "timed out waiting for OAuth callback")
	}
}
