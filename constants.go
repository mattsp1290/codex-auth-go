// Package codexauth implements the OAuth2 + PKCE authentication flow for
// ChatGPT's Codex subscription, token storage and refresh, and the
// RoundTripper that injects bearer credentials into Responses-API requests.
//
// All constants are verbatim from the registered OpenAI OAuth application.
// Do not modify them — they are correctness anchors that tie to the live
// auth.openai.com configuration.
package codexauth

const (
	// ClientID is the OAuth application client identifier registered with
	// auth.openai.com. Used in every OAuth request.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// Issuer is the base URL of the OpenAI authorization server.
	Issuer = "https://auth.openai.com"
	// CodexEndpoint is the Responses-API endpoint for Codex inference.
	CodexEndpoint = "https://chatgpt.com/backend-api/codex/responses"
	// OAuthPort is the local callback server port used during the browser
	// OAuth flow.
	OAuthPort = 1455
	// RedirectURI is the registered OAuth redirect URI for the browser flow.
	RedirectURI = "http://localhost:1455/auth/callback"
	// Scope is the OAuth scope string requested for Codex access.
	Scope = "openid profile email offline_access"
	// PKCEMethod is the PKCE code challenge method. S256 is mandatory per
	// RFC 7636 §4.2 for public clients.
	PKCEMethod = "S256"
	// Originator is the literal value preserved from the source package's wire
	// behavior. Consumers must not change it without a verified wire trace
	// proving a different value is accepted; v0.2.0 may move it to Options.
	Originator = "advisor"
	// DeviceRedirectURI is the redirect_uri used when exchanging an authorization
	// code obtained from the device poll endpoint. The server expects this exact
	// value for codes issued via /api/accounts/deviceauth/token.
	DeviceRedirectURI = "https://auth.openai.com/deviceauth/callback"
)
