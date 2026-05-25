package codexauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

var (
	// openBrowserFn is the browser-opener seam. Tests replace it with a stub
	// so they can capture the authorize URL without launching a real browser.
	openBrowserFn = openBrowser

	// loginBrowserStderr is where the fallback "Open this URL" prompt is written
	// when openBrowserFn returns any error (ErrNoBrowser or exec failure).
	loginBrowserStderr io.Writer = os.Stderr

	// browserLoginTimeout caps the wait for the OAuth callback.
	// Declared as a var so tests can shrink it to avoid slow runs.
	browserLoginTimeout = 5 * time.Minute
)

// LoginBrowser implements the browser-based OAuth2 + PKCE authorization flow:
//
//  1. Generates a PKCE verifier, S256 challenge, and CSRF state nonce.
//  2. Starts the local callback server (MUST happen before opening the browser).
//  3. Opens the browser to the authorize URL; if no browser is available,
//     prints the URL to stderr for manual copy-paste.
//  4. Waits up to browserLoginTimeout for the OAuth callback.
//  5. Exchanges the authorization code for tokens.
//  6. Extracts the account ID and persists credentials.
//
// If port OAuthPort (1455) is already in use, LoginBrowser returns *PortInUseError
// without opening the browser — the trip-wire invariant in the test suite
// enforces that openBrowserFn is never called on an EADDRINUSE failure.
//
// Deprecated: use NewClient(Options{AppName: "advisor"}).LoginBrowser instead.
func LoginBrowser(ctx context.Context) (Credentials, error) {
	return legacyClient().LoginBrowser(ctx)
}

// LoginBrowser implements the browser-based OAuth2 + PKCE authorization flow
// for this client.
func (c *Client) LoginBrowser(ctx context.Context) (Credentials, error) {
	return loginBrowser(ctx, c.save)
}

func loginBrowser(ctx context.Context, saveCreds func(AuthFile) error) (Credentials, error) {
	verifier, err := GenerateVerifier()
	if err != nil {
		return Credentials{}, fmt.Errorf("codexauth: browser login: generate verifier: %w", err)
	}
	state, err := GenerateState()
	if err != nil {
		return Credentials{}, fmt.Errorf("codexauth: browser login: generate state: %w", err)
	}
	challenge := ChallengeFromVerifier(verifier)

	// Start the callback server BEFORE opening the browser.
	// A PortInUseError here means we cannot accept the callback — abort before
	// openBrowserFn is ever called.
	codeCh, errCh, shutdown, err := StartCallbackServer(ctx, state)
	if err != nil {
		return Credentials{}, err // *PortInUseError propagated unwrapped for errors.As
	}
	defer shutdown() // registered first → runs second (after cancel, per LIFO)

	innerCtx, cancel := context.WithTimeout(ctx, browserLoginTimeout)
	defer cancel() // registered second → runs first (LIFO)

	authorizeURL := BuildAuthorizeURL(challenge, state)
	if openErr := openBrowserFn(authorizeURL); openErr != nil {
		if !errors.Is(openErr, ErrNoBrowser) {
			// Unexpected exec error (not just "no binary") — log it so the user
			// knows why the browser didn't open.
			_, _ = fmt.Fprintf(loginBrowserStderr, "codexauth: browser open failed (%v), printing URL instead\n", openErr)
		}
		_, _ = fmt.Fprintf(loginBrowserStderr, "Open this URL to log in:\n  %s\n", authorizeURL)
	}

	var code string
	select {
	case code = <-codeCh:
		// authorization code received from the callback handler
	case err = <-errCh:
		return Credentials{}, err // ErrStateMismatch, ErrAuthorizationDenied, or network error
	case <-innerCtx.Done():
		if ctx.Err() != nil {
			// Caller cancelled their context.
			return Credentials{}, ctx.Err()
		}
		// Internal 5-minute timeout elapsed.
		return Credentials{}, fmt.Errorf("codexauth: browser login: timed out waiting for OAuth callback")
	}

	// Use the outer ctx for exchange — the 5-min cap is callback-wait-scoped only.
	creds, err := ExchangeCode(ctx, code, verifier, RedirectURI)
	if err != nil {
		return Credentials{}, fmt.Errorf("codexauth: browser login: exchange: %w", err)
	}

	creds.AccountID = ExtractAccountID(creds.IDToken)
	if err := saveCreds(AuthFile{OpenAI: &creds}); err != nil {
		return Credentials{}, fmt.Errorf("codexauth: browser login: save credentials: %w", err)
	}

	return creds, nil
}
