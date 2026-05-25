package codexauth

import (
	"context"
	"log/slog"
	"net/url"
	"time"
)

// Credentials is part of the public v0.1.0 API surface. It holds the OAuth
// token set returned after a successful authorization-code exchange or token
// refresh.
//
// Expires is an absolute Unix epoch in milliseconds — not a relative
// duration — so callers can compare it directly against time.Now().UnixMilli().
//
// AccountID is not populated by ExchangeCode or RefreshToken; callers
// should invoke ExtractAccountID(creds.IDToken) separately and store the
// result before the ID token is discarded.
type Credentials struct {
	Access    string `json:"-"`
	Refresh   string `json:"-"`
	IDToken   string `json:"-"`
	Expires   int64  `json:"-"` // absolute epoch milliseconds when Access expires
	AccountID string `json:"-"` // populated by caller via ExtractAccountID, not here
}

// String implements fmt.Stringer. Returns a redacted sentinel so that
// fmt.Sprintf("%v", creds) and log lines using %v cannot leak token values.
func (c Credentials) String() string { return "Credentials{[redacted]}" }

// LogValue implements slog.LogValuer. It returns a redacted representation so
// that slog.Info("creds", "c", creds) never emits token values into logs.
func (c Credentials) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("hasAccess", c.Access != ""),
		slog.Bool("hasRefresh", c.Refresh != ""),
		slog.Bool("hasIDToken", c.IDToken != ""),
		slog.Int64("expires", c.Expires),
		slog.String("accountID", c.AccountID),
	)
}

// tokenEndpointURL is a variable (not a constant) so that tests can redirect
// requests to an httptest server without introducing a constructor or option.
var tokenEndpointURL = Issuer + "/oauth/token"

// ExchangeCode trades an authorization code (plus PKCE code verifier) for a
// token set. The redirectURI must exactly match the redirect_uri sent during
// the /authorize request; a mismatch will cause the server to reject the
// exchange with invalid_grant.
//
// Returns *AuthError if the server responds with an OAuth error. The
// Credentials.AccountID field is always empty; populate it via
// ExtractAccountID after calling this function.
func ExchangeCode(ctx context.Context, code, verifier, redirectURI string) (Credentials, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	tr, err := postForm(ctx, tokenEndpointURL, form)
	if err != nil {
		return Credentials{}, err
	}
	return credentialsFromResponse(tr), nil
}

// RefreshToken exchanges a refresh token for a new token set. The refresh
// token rotates on every successful call — the returned Credentials.Refresh
// must be persisted immediately; the old token is invalidated by the server.
//
// Returns *AuthError if the server responds with an OAuth error. The most
// common error is "invalid_grant" (refresh token revoked, expired after 90
// days of inactivity, or user signed out from all sessions).
func RefreshToken(ctx context.Context, refreshTok string) (Credentials, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {refreshTok},
		"scope":         {Scope},
	}
	tr, err := postForm(ctx, tokenEndpointURL, form)
	if err != nil {
		return Credentials{}, err
	}
	creds := credentialsFromResponse(tr)
	if creds.Refresh == "" {
		// Server did not rotate the refresh token (RFC 6749 §6 allows this).
		// Preserve the caller's input so credential-store callers do not
		// overwrite a valid persisted refresh token with an empty string.
		creds.Refresh = refreshTok
	}
	return creds, nil
}

// credentialsFromResponse converts a *tokenResponse into Credentials,
// computing the absolute Expires timestamp from the relative ExpiresIn value.
// If ExpiresIn is zero or negative, it defaults to 3600 s (1 hour) per the
// OpenAI convention observed in the Day-1 probe.
func credentialsFromResponse(tr *tokenResponse) Credentials {
	expiresIn := tr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return Credentials{
		Access:  tr.AccessToken,
		Refresh: tr.RefreshToken,
		IDToken: tr.IDToken,
		Expires: time.Now().UnixMilli() + int64(expiresIn)*1000,
	}
}
