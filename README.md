# codex-auth-go

Go library for authenticating against the ChatGPT subscription-backed Codex
endpoint and producing an `*http.Client` that can call:

```text
https://chatgpt.com/backend-api/codex/responses
```

## Install

```sh
go get github.com/mattsp1290/codex-auth-go
```

## Quickstart

```go
ctx := context.Background()

auth := codexauth.NewClient(codexauth.Options{
	AppName: "my-agent",
})

if _, err := auth.Status(ctx); err != nil {
	log.Fatal(err)
}

httpClient, err := auth.HTTPClient(ctx)
if errors.Is(err, codexauth.ErrNotLoggedIn) {
	if _, err := auth.Login(ctx, false); err != nil {
		log.Fatal(err)
	}
	httpClient, err = auth.HTTPClient(ctx)
}
if err != nil {
	log.Fatal(err)
}

req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexauth.CodexEndpoint, body)
if err != nil {
	log.Fatal(err)
}
resp, err := httpClient.Do(req)
if err != nil {
	log.Fatal(err)
}
defer resp.Body.Close()
```

`HTTPClient` rewrites outbound requests to the Codex Responses endpoint,
injects the bearer token, refreshes tokens before expiry, and preserves the
Codex-specific `originator` and `session_id` header casing.

## Account-aware model catalog

`Client.ListModels` fetches the current model picker catalog for the
authenticated ChatGPT account without exposing credentials or the backend wire
schema:

```go
models, err := auth.ListModels(ctx, "your-catalog-compatibility-value")
if errors.Is(err, codexauth.ErrNotLoggedIn) {
	log.Fatal("not logged in")
}
if err != nil {
	log.Fatal(err)
}
for _, model := range models {
	fmt.Printf("%s: %s\n", model.Slug, model.DisplayName)
}
```

The caller supplies `clientVersion` as an opaque, non-secret Codex catalog
compatibility value. The library passes the exact value through as the
`client_version` query parameter; it does not assume that an application or Go
module version has the backend's intended meaning.

Results depend on the authenticated account and the server's current catalog.
Only picker-visible entries are returned, stable-sorted by ascending server
priority. Each model's reasoning options stay in server order, and effort names
are open strings so future values remain usable.

Every call performs a fresh catalog request plus any token refresh required for
stale credentials; the library does not cache catalog data. A caller may cache
within one UI session, but should refetch after login or account changes and
when the user explicitly refreshes. Catalog calls through one `Client` are
serialized to protect rotating refresh tokens. Calls through separate clients
or processes are not coalesced.

Cancellation is prompt when credentials are fresh. If credentials are stale,
the existing detached refresh and safe persistence step may finish before the
call returns; cancellation still prevents the subsequent catalog request.
Non-success catalog errors can be inspected with `errors.As` to
`*ModelCatalogHTTPError`. Catalog and catalog-scoped refresh errors never expose
network response bodies.

## Login Flows

`Client.Login(ctx, false)` prefers the browser OAuth + PKCE flow when a browser
launcher is available. If no browser launcher can be found, it falls back to the
device flow so SSH and headless environments still work.

Use `Client.Login(ctx, true)` to force the device flow. The device flow prints a
browser URL and user code to stderr, polls until authorization succeeds or the
device code expires, exchanges the authorization code, and stores credentials.
Set `Options.DevicePrompt` to render the device URL and user code in your own
UI instead of using the default stderr prompt.

Package-level `Login`, `LoginBrowser`, `LoginDevice`, `Logout`, and
`HTTPClient` remain as deprecated advisor-compatibility wrappers. New code
should use an explicit `Client`.

## Status

`Client.Status(ctx)` is local-only. It reads the configured credential store and
does not refresh tokens, delete credentials, or make network calls.

`StatusInfo.LoggedIn` is true when a refresh token is stored. `ExpiresAt`
reports the stored access-token expiry. `Stale` is true when the stored access
token is expired or within the transport refresh margin. `AccountID` is the
account ID cached from the login response, and `ConfigPath` is the auth.json path
for the client's `AppName`.

## AppName

`Options.AppName` selects the credential directory. `NewClient(Options{})`
defaults to `codex`; set a stable app name to isolate credentials for your
program:

```go
auth := codexauth.NewClient(codexauth.Options{AppName: "local-symphony"})
```

The deprecated package-level wrappers use `advisor` intentionally so existing
advisor installations keep reading the same auth.json path without a re-login.

## Endpoint override (hermetic tests)

`Options.Endpoint` overrides the Responses-API URL the transport rewrites every
request to. Use it to point the real transport at a loopback SSE fake in tests:

```go
// srv is an httptest.Server — replace srv.URL with your fake's address.
auth := codexauth.NewClient(codexauth.Options{
    AppName:        "my-agent",
    Endpoint:       srv.URL + "/backend/responses", // e.g. http://127.0.0.1:PORT/...
    CredentialPath: fixtureAuthPath,                // path to a staged auth.json
})
httpClient, err := auth.HTTPClient(ctx)
```

`CredentialPath` overrides the `auth.json` location, so you can stage a fixture
credential file without touching `HOME`/`XDG`. The file must contain a valid
`openai` entry; a file with no `openai` entry returns `ErrNotLoggedIn`.

**Safety rail:** cleartext `http://` is permitted only for loopback hosts
(`127.0.0.0/8`, `::1`, `localhost`) so bearer tokens are never sent in
plaintext to a remote host. `HTTPClient` and `ListModels` return
`ErrInsecureEndpoint` for any other `http://` endpoint. `https://` to any host
is always allowed. `ListModels` normalizes trailing slashes, replaces the final
Responses path component with `models`, and drops endpoint query/fragment data.

**Redirect caveat:** when using a loopback `http://` endpoint, your fake server
must respond directly (e.g. `200` with the SSE body). The client hard-refuses
any non-https redirect and returns `"codexauth: refusing non-https redirect"` —
by design. A test fake that issues a `3xx` will surface this error, not a
`200`. Respond directly.

**Warning on `CredentialPath` and writes:** on any write (token refresh, `Save`,
`Logout`), the **parent directory** of `CredentialPath` is `MkdirAll`'d AND its
permissions are unconditionally overwritten to `0700` — even if the directory
already existed. Pointing `CredentialPath` at a file in a shared directory
(`/tmp`, `$HOME`) will re-permission that directory on first write. Use
`t.TempDir()` in tests or any directory you own exclusively.

## Security

JWT handling is passive. `ExtractAccountID` decodes claims from an accepted
OAuth response so the account ID can be cached, but it does not verify the JWT
signature, issuer, audience, expiry, or not-before time.

Credential writes use mode 0600 for auth.json and tighten the parent directory
to 0700 on platforms where POSIX modes apply. `Logout` performs best-effort RFC
7009 refresh-token revocation and refuses redirects for the revocation POST.

The module intentionally uses OpenAI's public Codex CLI OAuth client ID:

```text
app_EMoamEEZ73f0CkXaXp7hrann
```

Do not make `ClientID` configurable or replace it with an application-specific
value. That identifier is part of the Codex subscription OAuth flow, and
changing it breaks authentication for this endpoint.

The `Originator` header value remains `advisor` in v0.1.0 to preserve the
source package's on-the-wire behavior. Do not change it without a verified wire
trace showing a different value is accepted; a future v0.2.0 may move it to
`Options`.

The extraction plan lives in
[docs/prompts/extract-codex-auth-go.md](docs/prompts/extract-codex-auth-go.md).
