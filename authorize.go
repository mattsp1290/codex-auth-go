package codexauth

import "net/url"

// BuildAuthorizeURL constructs the OAuth2 authorization URL for the Codex
// subscription browser flow. All fixed parameters (client_id, redirect_uri,
// scope, originator, etc.) are sourced from the package constants so that
// the call site only supplies the per-flow values: the PKCE code challenge
// and the CSRF state token.
//
// The returned URL is ready to open in a browser; the user will be directed
// to auth.openai.com to approve the Codex subscription scope.
//
// Required params and their sources:
//   - client_id, redirect_uri, scope, code_challenge_method → package constants
//   - id_token_add_organizations, codex_cli_simplified_flow → hardcoded "true"
//   - originator → hardcoded "advisor"
//   - code_challenge, state → caller-supplied
func BuildAuthorizeURL(challenge, state string) string {
	q := url.Values{}
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", Scope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", PKCEMethod)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", Originator)
	q.Set("state", state)
	// Parse Issuer so that url.URL handles path joining; avoids double-slash
	// if Issuer ever gains a trailing slash.
	u, _ := url.Parse(Issuer) // Issuer is a compile-time constant — never fails
	u.Path = "/oauth/authorize"
	u.RawQuery = q.Encode()
	return u.String()
}
