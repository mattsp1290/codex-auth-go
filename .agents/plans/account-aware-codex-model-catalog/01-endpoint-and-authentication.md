# Endpoint and authentication reuse

## Goal and prerequisite state

Create an internal, fixed models-route client that inherits every current authentication and transport safety rule. This work starts from the pinned `v0.3.0` code and must not change the public behavior of `Client.HTTPClient`.

Prerequisite: [00-overview.md](00-overview.md) decisions are accepted and the Go patch-toolchain readiness gate below is green before catalog code edits.

## Repository evidence

- `endpoint.go`: `parseEndpoint` already validates absolute HTTP(S) URLs and rejects non-loopback cleartext endpoints.
- `public.go`: `httpClientForApp` owns credential loading and constructs a fully wired `codexTransport`.
- `transport.go`: `codexTransport.endpoint` fixes the rewrite destination while `RoundTrip` preserves the caller's query and applies refreshed bearer and account headers.
- `client_endpoint_test.go`: loopback endpoint and credential-path tests demonstrate the correct black-box testing pattern.
- `transport_test.go`: header, clone, redirect, default-route, and refresh lifecycle assertions define behavior that must remain unchanged.

## Exact change surface

### Baseline readiness: `go.mod` and `.github/workflows/ci.yml` (existing)

Before feature edits, change both Go 1.26.3 pins to proposed Go 1.26.7, or the latest secure Go 1.26 patch confirmed from the official Go release index at implementation time. Keep the module directive and `actions/setup-go` input identical. Do not broaden this readiness change into a Go 1.27 migration.

Run the baseline build, race test, vet, lint, `govulncheck`, and cross-platform vet commands before catalog code. If the secure patch introduces a non-catalog failure, resolve it as readiness work or stop and file a blocking Beads issue; do not weaken `govulncheck` or claim the baseline is green.

### `endpoint.go` (existing)

Add proposed internal symbol:

- `func modelsEndpoint(rawResponsesEndpoint string) (string, error)` — parse the configured Responses endpoint through existing `parseEndpoint`, select `defaultCodexURL` when the result is nil, copy the URL, remove trailing path separators, replace the final non-empty path segment with `models`, clear user info, `RawPath`, `RawQuery`, `ForceQuery`, and `Fragment`, and return an absolute string. A root or empty path becomes `/models`.

Endpoint examples:

| Responses configuration | Derived models target |
|---|---|
| empty / default | `https://chatgpt.com/backend-api/codex/models` |
| `http://127.0.0.1:PORT/backend/responses` | `http://127.0.0.1:PORT/backend/models` |
| `http://127.0.0.1:PORT/backend/responses/` | `http://127.0.0.1:PORT/backend/models` |
| `http://127.0.0.1:PORT` | `http://127.0.0.1:PORT/models` |
| `http://127.0.0.1:PORT/` | `http://127.0.0.1:PORT/models` |
| `https://example.test/custom/response-name?ignored=1#ignored` | `https://example.test/custom/models` |

Normalize trailing separators before using `path.Dir` plus `path.Join`, or use equivalent URL-path logic. Do not use filesystem path resolution. Preserve scheme and host exactly as validated. Do not carry endpoint user info into the request. Define repeated separators deterministically and cover them in tests; a valid endpoint spelling must not silently become a descendant `responses/models` route.

### `client.go` (existing)

Add a proposed context-aware per-client catalog gate, initialized by `NewClient`. A buffered channel of capacity one is the preferred mechanism because acquisition can `select` against `ctx.Done()`. Add a proposed `sync.Once`-backed private initializer so a zero-value `Client` cannot block forever on a nil gate. Make acquisition return a release function and require `ListModels` to defer that release immediately after successful acquisition. The gate serializes the full `ListModels` operation for one `Client`, including transport construction and refresh, without retaining a credential-bearing transport after the method returns.

Document the concurrency boundary: one `Client` prevents duplicate stale refreshes among its `ListModels` calls. Distinct `Client` instances and processes retain the repository's existing invalid-grant recovery behavior and are not newly coalesced.

Update the exported `Options.Endpoint` comment so it states that both `HTTPClient` and `ListModels` validate it lazily, that `ListModels` derives the sibling models route after trailing-slash normalization, and that embedded query/fragment content is dropped for catalog requests.

### `catalog.go` (new; repository-root package insertion point)

The proposed `Client.ListModels` implementation checks `ctx.Err()`, acquires the per-client catalog gate with a context-aware `select`, defers release immediately, rechecks `ctx.Err()`, calls `modelsEndpoint(c.endpoint)`, then passes the derived fixed URL to existing `httpClientForApp` with `c.appName`, `c.logger`, `c.load`, `c.save`, and `c.delete`. This reuses:

- `ErrNotLoggedIn` behavior;
- credential-path overrides;
- stale-token refresh and persistence;
- invalid-grant cross-process recovery and credential deletion;
- bearer overwrite;
- `originator`, `session_id`, `User-Agent`, and optional `ChatGPT-Account-Id` headers;
- the loopback cleartext rail; and
- redirect refusal and cross-host authorization stripping.

Enable proposed catalog-scoped safe refresh failures on the returned internal transport before sending the request. Do not change default `HTTPClient` refresh error behavior.

Construct the catalog `GET` request with `http.NewRequestWithContext`. Set its query through `url.Values.Set("client_version", clientVersion)` so reserved characters are encoded once. Set `Accept: application/json`. Do not set, copy, return, or log authorization data.

The request URL given to the fixed transport may be the derived models URL itself. The transport remains authoritative for the final destination and header injection.

### `transport.go` (existing)

Add proposed internal catalog-safe refresh behavior to `codexTransport`, disabled by default and enabled only for the `ListModels` transport:

- retain raw refresh errors internally long enough to execute invalid-grant recovery;
- replace a returned `AuthError` with a new safe `AuthError` containing `Code` but no `Description` only when `Code` is in a closed safe set (`invalid_grant`, `invalid_client`, `invalid_request`, `unauthorized_client`, `unsupported_grant_type`, `invalid_scope`, or `temporarily_unavailable`);
- map every unknown, empty, malformed, or attacker-controlled OAuth code to `ErrRefreshFailed` without logging the raw value;
- replace any other remote refresh failure that can contain response content with `ErrRefreshFailed` and a fixed operation prefix;
- log invalid-grant branch metadata plus a safe OAuth code, never the raw error or description; and
- leave local persistence/load errors inspectable when their error text cannot contain network response content.

Add focused tests proving the flag is off for ordinary `HTTPClient` transports and on for `ListModels`. Existing public behavior is otherwise unchanged.

### `endpoint.go` exported error documentation (existing)

Update the `ErrInsecureEndpoint` comment to say it can be returned by `HTTPClient` or `ListModels`.

### `public.go` (existing; no behavior change expected)

Prefer no changes beyond comments if `catalog.go` can reuse `httpClientForApp` unchanged. If implementation reveals unavoidable duplication, extract a proposed internal helper that accepts an already validated fixed endpoint, then keep `httpClientForApp` as a behavior-identical wrapper. Do not add a public route selector and do not let caller request URLs bypass the fixed endpoint.

## Intended behavior and invariants

1. `HTTPClient` continues to rewrite all caller requests to its configured Responses URL.
2. `ListModels` creates a separate short-lived client whose transport rewrites only to the derived models URL after acquiring the per-client gate.
3. The models request is `GET`, has no body, and contains exactly one `client_version` value supplied by the caller.
4. An embedded query or fragment in `Options.Endpoint` never reaches the models request.
5. A cleartext non-loopback endpoint fails before credentials are loaded into a request or any network call starts.
6. Context cancellation propagates through gate acquisition and the catalog HTTP exchange. Check it before and immediately after gate acquisition. A stale-token refresh retains the existing detached persistence semantics, so cancellation during refresh may return after refresh finishes; after `Do` returns, caller cancellation takes precedence over a concurrent refresh error, the catalog request must not be sent, and `errors.Is` must match the caller cancellation.
7. Catalog construction does not mutate `Client`, credential state, the caller's context, or any existing `*http.Client`.

## Error paths

- Whitespace-only client version (`strings.TrimSpace(clientVersion) == ""`): return a local validation error before gate acquisition, credential loading, or network I/O. Preserve every other input exactly before URL encoding.
- Invalid endpoint: propagate the existing wrapped parse error or `ErrInsecureEndpoint` behavior.
- No OpenAI credential entry: preserve `errors.Is(err, ErrNotLoggedIn)`.
- Refresh failure: preserve invalid-grant recovery and credential-wipe behavior, but for catalog calls expose only safe `AuthError.Code`, `ErrRefreshFailed`, and local persistence/load causes. Do not expose OAuth descriptions or raw token-response snippets.
- Request cancellation/deadline: wrap without breaking `errors.Is(err, context.Canceled)` or `errors.Is(err, context.DeadlineExceeded)`.
- Transport failure: wrap with the catalog operation name but never include headers or response content.

## Tests and observable acceptance

Add endpoint-derivation table tests in proposed `catalog_test.go` or existing `client_endpoint_test.go`:

- default endpoint produces `/backend-api/codex/models`;
- nested loopback override replaces the last path component;
- loopback server root produces `/models`;
- embedded query and fragment are removed;
- `/backend/responses/`, `/`, empty paths, and repeated/trailing separators follow the defined normalization;
- cleartext non-loopback returns `ErrInsecureEndpoint`;
- invalid or relative URL preserves current endpoint validation errors.

Add black-box request tests through `NewClient(...).ListModels(...)` that assert:

- exact method, scheme, host, path, and one decoded `client_version` value;
- encoded client versions do not inject a second query parameter;
- `Accept`, bearer, account ID, `originator`, `session_id`, and application/version `User-Agent` headers are present;
- caller cancellation terminates a blocked catalog server and retains `errors.Is(context.Canceled)`;
- a caller canceled while waiting for another catalog call exits without waiting for the gate;
- a pre-canceled stale call makes zero token and catalog requests even if gate acquisition is also ready;
- cancellation during a stale refresh allows refresh persistence to finish, sends no catalog request, and returns an error matching the caller cancellation;
- cancellation racing with OAuth JSON failure, non-JSON failure, or refresh timeout returns the caller context error after the detached refresh ends;
- concurrent stale calls through one `Client` make exactly one token request, all callers succeed or honor their own cancellation, and credentials are not deleted;
- a models handler returning `302` does not cause the redirect target to receive a request or bearer token, and the original response is classified and closed;
- `HTTPClient` still targets the configured Responses path before and after a catalog call.

Add gate-release recovery cases: after endpoint validation failure, missing credentials, refresh failure, transport failure, non-2xx, decode failure, and cancellation, a second valid call on the same `Client` must acquire promptly and complete. These cases prove every post-acquisition return executes the deferred release.

Acceptance: no test-only bearer injection path is introduced. The test server must observe headers produced by the real `codexTransport`.

## Dependencies, risks, and exclusions

- This work precedes public decoding because the public method needs the dedicated client.
- Tests that replace package-level `tokenEndpointURL` must not call `t.Parallel`.
- The per-client gate intentionally serializes catalog calls. It does not promise coalesced results or cross-client/cross-process refresh serialization.
- Do not change refresh timeouts, retry semantics, session-ID lifetime, or redirect policy.
- Do not export `modelsEndpoint`, a models URL constant, a route enum, or a new endpoint option.
- Do not make the existing transport select a destination from the incoming caller URL.
