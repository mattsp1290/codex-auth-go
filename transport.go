package codexauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// Version is the User-Agent version string.
// Declared as a var so it can be overridden via -ldflags -X.
// TODO: replace with internal/version.Version() once that package exists.
var Version = "0.0.0-dev"

const (
	// refreshSkewMargin is how far ahead of token expiry we trigger a refresh.
	// Matches the 60-second margin used by opencode.
	refreshSkewMargin = 60 * time.Second

	// refreshTimeout is the hard deadline on the detached refresh context.
	// The caller's context is intentionally NOT used so a caller cancel cannot
	// abort a mid-flight disk write.
	refreshTimeout = 15 * time.Second
)

// codexTransport is an http.RoundTripper that injects Codex OAuth bearer tokens
// into outgoing requests. It refreshes stale credentials on demand, collapsing
// concurrent refreshes into a single network round-trip via a singleflight group.
//
// Every outbound request is rewritten to the Codex Responses-API endpoint
// (default chatgpt.com/backend-api/codex/responses; overridable via
// Options.Endpoint) and decorated with the OAuth bearer token and Codex-specific
// headers (see RoundTrip and decorateHeaders).
type codexTransport struct {
	base http.RoundTripper
	sf   singleflight.Group

	// endpoint, when non-nil, overrides the default Codex Responses-API URL that
	// every request is rewritten to. Nil → defaultCodexURL. The scheme is taken
	// from the override (a loopback test fake is cleartext http), so the previous
	// hard-coded "https" no longer applies under override.
	endpoint *url.URL

	// mu protects creds.
	mu    sync.RWMutex
	creds Credentials

	// now is injectable for tests; production code passes time.Now.
	now func() time.Time

	// saveCreds persists a refreshed credential set to disk.
	// Production code wires credstore.Save (or a thin adapter).
	// ensureFresh calls saveCreds BEFORE updating in-memory state so a
	// disk-write failure does not leave an unsaved token in memory.
	saveCreds func(Credentials) error

	// wipeCreds removes the persisted credential entry from disk.
	// Called when the refresh token is revoked (invalid_grant) so that the
	// next process start surfaces "not logged in" rather than looping on a
	// dead refresh token. May be nil (in which case on-disk state is not wiped).
	wipeCreds func() error

	// loadCreds reads the credential entry from disk.
	// Used on invalid_grant to check whether another process has already
	// refreshed (multi-process coordination). May be nil; if nil the
	// cross-process check is skipped.
	loadCreds func() (Credentials, error)

	// sessionID is a per-process UUID v4 sent as the session_id request header.
	// Generated once at construction and reused for every request — opencode
	// sends a single session id per CLI invocation, not per request.
	sessionID string

	// appName is the User-Agent application prefix. Empty preserves the legacy
	// package-level wrapper value, "advisor".
	appName string

	logger *slog.Logger

	// safeRefreshErrors is enabled only for the high-level catalog API. It
	// prevents token-endpoint response content from escaping through errors or
	// logs while leaving HTTPClient's existing behavior unchanged.
	safeRefreshErrors bool
}

// newCodexTransport constructs a codexTransport with required fields validated
// and defaults applied. saveCreds must be non-nil. A fresh session_id UUID is
// generated here and reused for every outbound request.
func newCodexTransport(creds Credentials, saveCreds func(Credentials) error) *codexTransport {
	return newCodexTransportForApp("advisor", creds, saveCreds)
}

func newCodexTransportForApp(appName string, creds Credentials, saveCreds func(Credentials) error) *codexTransport {
	return newCodexTransportForClient(appName, slog.Default(), creds, saveCreds)
}

func newCodexTransportForClient(appName string, logger *slog.Logger, creds Credentials, saveCreds func(Credentials) error) *codexTransport {
	if saveCreds == nil {
		panic("codexauth: newCodexTransport: saveCreds must not be nil")
	}
	if appName == "" {
		appName = "advisor"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &codexTransport{
		creds:     creds,
		saveCreds: saveCreds,
		now:       time.Now,
		sessionID: uuid.New().String(),
		appName:   appName,
		logger:    logger,
	}
}

// isValid reports whether creds are valid with at least refreshSkewMargin to spare.
func isValid(creds Credentials, now func() time.Time) bool {
	return creds.Access != "" &&
		now().Add(refreshSkewMargin).UnixMilli() < creds.Expires
}

// ensureFresh refreshes the bearer token if it will expire within refreshSkewMargin.
//
// Concurrent callers are coalesced by a singleflight group: at most one
// /oauth/token call is in flight at any time. The refresh runs under a detached
// context (context.Background + 15 s timeout) so cancelling the caller's context
// does not abort a mid-flight token write.
//
// On success the refreshed Credentials are written to disk via saveCreds and then
// stored in t.creds.
//
// On invalid_grant: if loadCreds is wired, the on-disk state is consulted first —
// another process may have already refreshed. If the disk creds are still stale,
// wipeCreds is called (if wired) to remove the dead entry, and the error is
// returned. Callers should inspect the error with errors.As → *AuthError and map
// ae.Code == "invalid_grant" to core.ErrTypeAuthInvalidGrant.
func (t *codexTransport) ensureFresh(_ context.Context) error {
	t.mu.RLock()
	valid := isValid(t.creds, t.now)
	t.mu.RUnlock()
	if valid {
		return nil
	}

	_, err, _ := t.sf.Do("refresh", func() (any, error) {
		// Double-check under write lock — a preceding singleflight batch may
		// have already refreshed by the time this closure runs.
		t.mu.Lock()
		doubleValid := isValid(t.creds, t.now)
		oldRefresh := t.creds.Refresh
		oldAccountID := t.creds.AccountID
		t.mu.Unlock()
		if doubleValid {
			return nil, nil
		}
		if oldRefresh == "" {
			return nil, fmt.Errorf("codexauth: no refresh token available")
		}

		// Use a detached context so a caller cancel cannot interrupt the
		// disk-write path at the end of this closure.
		rctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()

		fresh, err := RefreshToken(rctx, oldRefresh)
		if err != nil {
			var ae *AuthError
			if errors.As(err, &ae) && ae.Code == "invalid_grant" {
				// Refresh token revoked — check disk in case another process
				// already refreshed with a rotated refresh token.
				if t.loadCreds != nil {
					loaded, lerr := t.loadCreds()
					if lerr != nil {
						// Transient disk read error — skip wipe to preserve disk state.
						t.mu.Lock()
						t.creds = Credentials{}
						t.mu.Unlock()
						t.logInvalidGrant("load_error", false, err)
						if t.safeRefreshErrors {
							return nil, fmt.Errorf("codexauth: refresh credential load: %w", errors.Join(lerr, safeRemoteRefreshError(err)))
						}
						return nil, fmt.Errorf("codexauth: refresh token revoked (invalid_grant): %w", err)
					}
					if loaded.Access != "" && loaded.Refresh != oldRefresh {
						// Another process refreshed with a rotated token. Adopt.
						if loaded.AccountID == "" {
							loaded.AccountID = oldAccountID
						}
						t.mu.Lock()
						t.creds = loaded
						t.mu.Unlock()
						t.logInvalidGrant("rotation_race", true, err)
						return nil, nil
					}
					// Disk state is also stale (same refresh token) — fall through to wipe.
				}
				// No recovery path. Wipe the dead entry from disk so the next
				// startup surfaces "not logged in" instead of looping.
				if t.wipeCreds != nil {
					_ = t.wipeCreds()
				}
				t.mu.Lock()
				t.creds = Credentials{}
				t.mu.Unlock()
				t.logInvalidGrant("wiped_creds", false, err)
				if t.safeRefreshErrors {
					return nil, fmt.Errorf("codexauth: refresh token revoked: %w", safeRemoteRefreshError(err))
				}
				return nil, fmt.Errorf("codexauth: refresh token revoked (invalid_grant): %w", err)
			}
			if t.safeRefreshErrors {
				return nil, fmt.Errorf("codexauth: refresh: %w", safeRemoteRefreshError(err))
			}
			return nil, fmt.Errorf("codexauth: refresh: %w", err)
		}

		// Carry AccountID forward — refresh responses do not include it.
		fresh.AccountID = oldAccountID

		// Persist BEFORE updating in-memory — a disk failure must not leave an
		// unsaved token as the in-process source of truth.
		if err := t.saveCreds(fresh); err != nil {
			return nil, fmt.Errorf("codexauth: persist credentials: %w", err)
		}

		t.mu.Lock()
		t.creds = fresh
		t.mu.Unlock()

		return nil, nil
	})
	return err
}

func (t *codexTransport) logInvalidGrant(branchID string, hasCredsAfter bool, err error) {
	logger := t.logger
	if logger == nil {
		logger = slog.Default()
	}
	if t.safeRefreshErrors {
		logger.Warn("codexauth invalid_grant recovery",
			"branch_id", branchID,
			"has_creds_after", hasCredsAfter,
			"oauth_code", "invalid_grant",
		)
		return
	}
	logger.Warn("codexauth invalid_grant recovery", "branch_id", branchID, "has_creds_after", hasCredsAfter, "error", err)
}

func safeRemoteRefreshError(err error) error {
	var authErr *AuthError
	if errors.As(err, &authErr) && isSafeOAuthErrorCode(authErr.Code) {
		return &AuthError{Code: authErr.Code}
	}
	return ErrRefreshFailed
}

func isSafeOAuthErrorCode(code string) bool {
	switch code {
	case "invalid_grant", "invalid_client", "invalid_request", "unauthorized_client",
		"unsupported_grant_type", "invalid_scope", "temporarily_unavailable":
		return true
	default:
		return false
	}
}

// RoundTrip implements http.RoundTripper. For every request it:
//  1. Ensures credentials are fresh (refreshing if needed).
//  2. Clones the caller's request so the original is never mutated.
//  3. Rewrites the URL to the Codex Responses-API endpoint (defaultCodexURL
//     unless overridden via Options.Endpoint).
//  4. Injects the OAuth bearer token.
//  5. Delegates to t.base (falling back to http.DefaultTransport if nil).
//
// Calling RoundTrip twice with the same *http.Request is safe for nil-body
// requests. For requests with a body, the caller must set req.GetBody so each
// clone receives a fresh reader — Go's HTTP client does this automatically for
// retry-safe requests.
//
// RoundTrip does NOT read or buffer the response body. SSE streams pass
// through unchanged.
func (t *codexTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.ensureFresh(req.Context()); err != nil {
		return nil, err
	}
	if t.safeRefreshErrors {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
	}

	clone := req.Clone(req.Context())

	// If the caller provided GetBody, give this clone a fresh body reader so
	// consecutive RoundTrip calls on the same request each send the full body.
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("codexauth: rebuffer request body: %w", err)
		}
		clone.Body = body
	}

	// Rewrite to the Codex endpoint. Set individual URL fields (not a new *url.URL)
	// so any query string the caller provided is preserved.
	ep := t.endpoint
	if ep == nil {
		ep = defaultCodexURL
	}
	clone.URL.Scheme = ep.Scheme // from override — must NOT be forced to "https"
	clone.URL.Host = ep.Host
	clone.URL.Path = ep.Path
	clone.URL.RawPath = "" // use canonical Path
	clone.URL.Opaque = ""  // prevent opaque URIs from bypassing the rewrite
	clone.URL.User = nil   // don't carry caller credentials onward
	clone.Host = ep.Host

	// Read creds after ensureFresh so we always inject the freshest token.
	t.mu.RLock()
	access := t.creds.Access
	account := t.creds.AccountID
	t.mu.RUnlock()

	// Overwrite whatever the SDK placed in Authorization (e.g. a dummy API key).
	clone.Header.Set("Authorization", "Bearer "+access)

	t.decorateHeaders(clone, account)

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// decorateHeaders sets the four Codex-required headers on req.
//   - originator:         "advisor" (lowercase — OpenAI's edge may match exactly)
//   - User-Agent:         "<appName>/<version> (<GOOS> <GOARCH>)"
//   - session_id:         per-process UUID v4 (snake_case name is intentional; see ADR-0008)
//   - ChatGPT-Account-Id: accountID, set only when non-empty
//
// originator and session_id use direct map assignment (not Header.Set) to
// preserve the exact key case on the wire for HTTP/1.1. Header.Set would
// canonicalize them to "Originator" and "Session_id" via textproto.
func (t *codexTransport) decorateHeaders(req *http.Request, accountID string) {
	req.Header["originator"] = []string{Originator}
	appName := t.appName
	if appName == "" {
		appName = "advisor"
	}
	req.Header.Set("User-Agent", appName+"/"+Version+" ("+runtime.GOOS+" "+runtime.GOARCH+")")
	req.Header["session_id"] = []string{t.sessionID}
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
}

// makeHTTPClient wraps transport in an *http.Client configured for Codex calls.
//
// The 120 s Timeout is a hard ceiling for the entire exchange (connect + headers
// + body). Streaming callers impose a tighter deadline via their request context.
//
// CheckRedirect enforces two invariants before capping at zero followed redirects:
//  1. Non-https redirect targets are refused — any redirect to an http:// URL
//     returns an error and the original response is discarded.
//  2. Cross-host redirects have their Authorization header stripped as
//     defense-in-depth against bearer token leakage (runs after the https
//     check, before ErrUseLastResponse, so it takes effect if the cap is
//     ever relaxed).
//
// Codex never legitimately redirects, so the policy caps at zero followed
// redirects: CheckRedirect always returns http.ErrUseLastResponse (after the
// guards above) because Go's net/http always populates via with at least the
// original request before calling CheckRedirect.
func makeHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout:   120 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("codexauth: refusing non-https redirect")
			}
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			// len(via) >= 1 is always true here; ErrUseLastResponse caps at 0 followed redirects.
			return http.ErrUseLastResponse
		},
	}
}
