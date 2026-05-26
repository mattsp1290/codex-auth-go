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
