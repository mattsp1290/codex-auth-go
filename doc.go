// Package codexauth authenticates ChatGPT subscription-backed Codex clients.
//
// The package provides a focused Go library for obtaining an *http.Client that
// calls the Codex Responses endpoint with a refreshing bearer token and for
// fetching the authenticated account's picker-visible catalog through
// [Client.ListModels]. It is not a CLI, daemon, JSON-RPC server, credential
// broker, Eino ChatModel wrapper, or provider-classification layer.
//
// JWT handling is intentionally passive. ExtractAccountID decodes claims so the
// account identifier can be cached with credentials, but it does not verify the
// JWT signature, issuer, audience, expiry, not-before time, or any other trust
// boundary. Treat decoded claims as local metadata from an OAuth response that
// has already been accepted, not as standalone proof of identity.
//
// The credential store writes auth.json with mode 0600 and tightens its parent
// directory to 0700 on platforms where POSIX modes apply. Logout performs
// best-effort RFC 7009 refresh-token revocation before deleting local OpenAI
// credentials, and the revocation HTTP client refuses redirects so a token POST
// body is not replayed to another target.
//
// The transport refreshes tokens 60 seconds before expiry and coalesces
// concurrent refresh attempts with singleflight. Client.ListModels additionally
// serializes its complete operation per Client so separate short-lived
// transports cannot rotate the same stale refresh token concurrently. It does
// not retry ordinary 401 responses as a refresh signal; refresh and
// invalid_grant recovery are handled through the explicit token-refresh path.
//
// See README.md for tutorials and migration examples.
package codexauth
