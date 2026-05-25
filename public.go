package codexauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrNotLoggedIn is returned by HTTPClient when no OpenAI credentials are stored.
// Callers should check with errors.Is and prompt the user to run Login.
var ErrNotLoggedIn = errors.New("codexauth: not logged in")

// ErrPlanNotIncluded marks Codex API responses where the authenticated account
// does not have Codex subscription access.
var ErrPlanNotIncluded = errors.New("codexauth: plan not included")

// ErrQuotaExceeded marks Codex API responses where the authenticated account
// has exhausted its Codex quota.
var ErrQuotaExceeded = errors.New("codexauth: quota exceeded")

// ErrRefreshFailed marks failures while refreshing stored Codex credentials.
var ErrRefreshFailed = errors.New("codexauth: refresh failed")

// httpDoer is the minimal interface satisfied by *http.Client, allowing tests
// to replace logoutHTTPClient without importing net/http/httptest.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var (
	// browserAvailableCheck probes whether a browser launcher is available on
	// this host without actually opening one. Any non-nil return causes Login
	// to fall back to the device flow. Tests replace this to control routing
	// without relying on OS binary or display-server availability.
	browserAvailableCheck = canOpenBrowserNative

	// loginBrowserFn and loginDeviceFn are vars so Login tests can stub the
	// inner flows without spinning up a full OAuth server stack.
	loginBrowserFn = func(ctx context.Context) (Credentials, error) { return loginBrowser(ctx, Save) }
	loginDeviceFn  = func(ctx context.Context) (Credentials, error) { return loginDevice(ctx, Save) }

	// logoutHTTPClient performs the RFC 7009 revocation POST. CheckRedirect is
	// set to refuse all redirects so the refresh token in the POST body cannot
	// be replayed to a redirect target. Tests replace this to capture the
	// request without hitting the network.
	logoutHTTPClient httpDoer = newLogoutHTTPClient()

	// logoutStderr receives warning messages when revocation fails. Tests
	// replace this to assert the warning without polluting test output.
	logoutStderr io.Writer = os.Stderr
)

func legacyClient() *Client {
	c := NewClient(Options{AppName: "advisor"})
	c.browserAvailableCheck = browserAvailableCheck
	c.loginBrowserFn = loginBrowserFn
	c.loginDeviceFn = loginDeviceFn
	c.logoutHTTPClient = logoutHTTPClient
	c.logoutStderr = logoutStderr
	c.pathFunc = getPath
	return c
}

// Login initiates an OAuth flow and returns fresh credentials.
//
// If forceDevice is true, the device-authorization flow is used regardless of
// browser availability. Otherwise, Login probes for a browser launcher via
// browserAvailableCheck: any non-nil return causes Login to fall back to the
// device flow. When a browser is available, the PKCE browser flow is used.
//
// Deprecated: use NewClient(Options{AppName: "advisor"}).Login instead.
func Login(ctx context.Context, forceDevice bool) (Credentials, error) {
	return legacyClient().Login(ctx, forceDevice)
}

// Login initiates an OAuth flow for this client and returns fresh credentials.
func (c *Client) Login(ctx context.Context, forceDevice bool) (Credentials, error) {
	if forceDevice || c.browserAvailableCheck() != nil {
		return c.loginDeviceFn(ctx)
	}
	return c.loginBrowserFn(ctx)
}

// Logout revokes the stored refresh token (RFC 7009 best-effort) and then
// deletes the on-disk OpenAI credentials. Network errors during revocation are
// logged to logoutStderr but do not prevent the local delete. Idempotent:
// returns nil when no credentials are stored.
//
// Deprecated: use NewClient(Options{AppName: "advisor"}).Logout instead.
func Logout(ctx context.Context) error {
	return legacyClient().Logout(ctx)
}

// Logout revokes this client's stored refresh token and deletes local
// credentials.
func (c *Client) Logout(ctx context.Context) error {
	return logout(ctx, c.load, c.delete, c.logoutHTTPClient, c.logoutStderr)
}

func logout(
	ctx context.Context,
	loadCreds func() (AuthFile, error),
	deleteCreds func(string) error,
	revokeClient httpDoer,
	stderr io.Writer,
) error {
	af, err := loadCreds()
	if err != nil {
		return fmt.Errorf("codexauth: logout: %w", err)
	}
	if af.OpenAI == nil {
		return nil
	}

	// Best-effort RFC 7009 revocation. Skip when no refresh token is stored
	// (corrupted / partial credstore entry) — nothing useful to revoke.
	if af.OpenAI.Refresh != "" {
		revokeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		revokeURL := Issuer + "/oauth/revoke"
		form := url.Values{
			"token":           {af.OpenAI.Refresh},
			"token_type_hint": {"refresh_token"},
			"client_id":       {ClientID},
		}
		req, reqErr := http.NewRequestWithContext(revokeCtx, http.MethodPost, revokeURL,
			strings.NewReader(form.Encode()))
		if reqErr != nil {
			_, _ = fmt.Fprintf(stderr,
				"codexauth: revoke failed, deleting local credentials anyway: %v\n", reqErr)
		} else {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			resp, doErr := revokeClient.Do(req)
			if doErr != nil {
				_, _ = fmt.Fprintf(stderr,
					"codexauth: revoke failed, deleting local credentials anyway: %v\n", doErr)
			} else {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode >= 300 {
					_, _ = fmt.Fprintf(stderr,
						"codexauth: revoke returned %d, deleting local credentials anyway\n",
						resp.StatusCode)
				}
			}
		}
	}

	return deleteCreds("openai")
}

// HTTPClient returns an *http.Client pre-configured to route requests through
// the Codex Responses API with automatic OAuth bearer-token injection and
// refresh. The transport is seeded from the OpenAI credentials currently on
// disk at the time of the call. Subsequent cross-process token rotations are
// detected lazily via the invalid_grant recovery path in codexTransport.
//
// ctx is reserved for future use (e.g. tracing the credential load). All
// per-request context propagation happens via the individual request's context.
//
// Returns ErrNotLoggedIn (sentinel) if no credentials are stored. Callers
// should errors.Is against it and prompt the user to run Login.
//
// Deprecated: use NewClient(Options{AppName: "advisor"}).HTTPClient instead.
func HTTPClient(_ context.Context) (*http.Client, error) {
	return legacyClient().HTTPClient(context.Background())
}

// HTTPClient returns an *http.Client pre-configured for this client's stored
// Codex credentials.
func (c *Client) HTTPClient(_ context.Context) (*http.Client, error) {
	return httpClientForApp(c.appName, c.logger, c.load, c.save, c.delete)
}

func httpClientForApp(
	appName string,
	logger *slog.Logger,
	loadCreds func() (AuthFile, error),
	saveCreds func(AuthFile) error,
	deleteCreds func(string) error,
) (*http.Client, error) {
	af, err := loadCreds()
	if err != nil {
		return nil, fmt.Errorf("codexauth: HTTPClient: %w", err)
	}
	if af.OpenAI == nil {
		return nil, ErrNotLoggedIn
	}
	creds := *af.OpenAI

	tr := newCodexTransportForClient(appName, logger, creds, func(c Credentials) error {
		return saveCreds(AuthFile{OpenAI: &c})
	})
	tr.wipeCreds = func() error { return deleteCreds("openai") }
	tr.loadCreds = func() (Credentials, error) {
		loaded, loadErr := loadCreds()
		if loadErr != nil || loaded.OpenAI == nil {
			return Credentials{}, loadErr
		}
		return *loaded.OpenAI, nil
	}
	tr.base = http.DefaultTransport

	return makeHTTPClient(tr), nil
}
