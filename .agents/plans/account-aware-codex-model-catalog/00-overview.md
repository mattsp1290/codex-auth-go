# Account-aware Codex model catalog

Status: Ready

Planning only. No implementation described by this plan has occurred.

## Application context

```json
{
  "application_context": {
    "has_active_users": false,
    "backward_compatibility_required": false,
    "feature_flags": "not-applicable",
    "confirmation_digest": "f489a9b464dca8ceb9e3c01e1c15a43dfb82339326505f1ca7d25dd682f210ef",
    "confirmed_at": "2026-09-04T16:27:26Z"
  }
}
```

The user confirmed that the library has no active users or external consumers and that backward compatibility is not required. Feature flags are therefore not applicable. The request nevertheless makes preservation of the existing `HTTPClient`, login, status, and `IsCodexAllowed` behavior an explicit acceptance condition, so this plan treats those surfaces as regression constraints.

## Change classification

- Change type: additive public library API with an internal authenticated-route extension.
- Affected areas: Go patch-toolchain readiness, endpoint derivation, authenticated HTTP-client construction, catalog wire decoding, public Go types, error classification, hermetic tests, package documentation, changelog, release tagging, and the external consumer-unblock record.
- Security boundary: the package continues to own credential loading, token refresh, account headers, redirects, and endpoint safety. Callers receive catalog data, never credentials or a general-purpose bearer-authenticated transport.

## Requested outcome

Add a documented `Client.ListModels` API that retrieves the authenticated account's current Codex picker catalog from the dedicated models route. It returns the canonical slug, display metadata, ascending picker priority, optional default reasoning effort, and ordered supported reasoning-effort options for every picker-visible model.

### Measurable success criteria

1. A consumer can call proposed `Client.ListModels(ctx, clientVersion)` and receive proposed `[]ModelCatalogEntry` without reading credential files or decoding the private backend schema.
2. The outbound request is exactly `GET <configured-codex-provider-base>/models?client_version=<url-encoded-value>` and carries the same bearer, `ChatGPT-Account-Id`, `originator`, `session_id`, and `User-Agent` behavior as Responses requests.
3. The method returns only wire entries whose `visibility` is `list`, stable-sorted by ascending `priority`, and preserves each entry's reasoning-effort order.
4. Unknown response object fields and unknown non-empty reasoning-effort strings decode successfully.
5. Invalid required fields, malformed JSON, trailing JSON, non-2xx responses, refresh failures, and canceled requests return errors and logs that contain no credential or response-body content from any network exchange reachable through `ListModels`.
6. `Options.Endpoint` and `Options.CredentialPath` remain valid hermetic seams for the catalog request, including stale-token refresh.
7. Existing public behavior remains unchanged and every repository quality gate passes.
8. The implementation is committed and pushed, a verified release tag or full commit is named, and the matching external response record unblocks `eino-tui`.
9. The known baseline `govulncheck` failure is removed by pinning a secure Go 1.26 patch in both module and CI configuration, and GitHub Actions succeeds for the exact commit before it is tagged.

## Scope

- Add proposed public catalog result and reasoning-option types.
- Update the repository and CI from vulnerable Go 1.26.3 to proposed Go 1.26.7, or a newer secure 1.26 patch verified at implementation time.
- Add a proposed high-level `Client.ListModels` method with explicit client-version input.
- Serialize catalog calls made through one `Client` so concurrent stale calls cannot independently rotate the same refresh token.
- Derive a models endpoint from the existing Responses endpoint configuration without making `HTTPClient` route-mutable.
- Reuse the current credential, refresh, header, redirect, and cleartext-loopback protections.
- Decode only the catalog fields required by the consumer.
- Filter picker visibility inside the package and expose stable priority order.
- Add hermetic behavioral, failure, cancellation, refresh, and regression tests.
- Document freshness, account dependence, caching guidance, and errors.
- Prepare the changelog, release pin, and external completion response.

## Non-goals

- Do not change Bubble Tea, `eino-tui`, Eino resolver, run-snapshot, persistence, or provider-selection code.
- Do not expose raw OAuth credentials, stored credential records, a raw response body, or an unrestricted authenticated transport.
- Do not mirror the full upstream Rust schema, pricing data, fallback routing, non-Codex providers, or the general OpenAI model catalog.
- Do not add a package-level deprecated wrapper for `ListModels`.
- Do not add an in-package catalog cache, ETag persistence, retry policy, or background refresh worker.
- Do not make `IsCodexAllowed` consult the live catalog or use it to discard catalog entries.

## Constraints

- Keep `HTTPClient` fixed to the configured Responses endpoint.
- Treat `Options.Endpoint` as a Responses URL and derive its sibling models URL by removing trailing path separators and replacing the final non-empty path segment with `models`.
- Preserve the current endpoint rule: only loopback hosts may use cleartext HTTP.
- Use the caller's context on the catalog request. Preserve the existing detached, 15-second token-refresh persistence behavior in `codexTransport.ensureFresh`.
- Reject `clientVersion` when `strings.TrimSpace(clientVersion) == ""`; otherwise preserve the original caller-supplied string and encode it with `net/url`. Treat it as an opaque, non-secret Codex catalog compatibility value and do not log or store it.
- Ignore additive JSON fields through Go's default decoder behavior.
- Preserve custom, non-empty reasoning-effort values instead of defining a closed enum.
- Never include any catalog or token-refresh response body, access token, refresh token, account ID, or complete credential record in an error or log emitted by `ListModels`.
- Bound the decompressed successful catalog body at proposed 8 MiB and fail safely on overflow.

## Repository findings

### Verified repository facts

- `Client` already holds the raw `Options.Endpoint`, logger, and credential-store callbacks in `client.go`; it does not currently coordinate multiple clients or transports created from the same instance.
- `Client.HTTPClient` delegates to `httpClientForApp` in `public.go`, which validates the endpoint, loads credentials, wires refresh persistence and invalid-grant recovery, then installs `codexTransport`.
- `codexTransport.RoundTrip` in `transport.go` clones requests, refreshes credentials, rewrites every URL to its configured endpoint, overwrites authorization, adds Codex headers, and delegates without reading the response body.
- `makeHTTPClient` applies a 120-second request ceiling and refuses every redirect after checking HTTPS and stripping authorization on cross-host redirects.
- `parseEndpoint` in `endpoint.go` already implements the required absolute-URL and loopback-only cleartext checks.
- `Options.Endpoint` is documented as the Responses URL. Existing hermetic tests use paths such as `/backend/responses`, while some tests use a server root.
- `Options.CredentialPath` and `writeFixtureAuth` in `client_endpoint_test.go` provide the real credential-load seam required by catalog tests.
- Token refresh is singleflight only within one `codexTransport` and is detached from caller cancellation so a refresh cannot leave an incomplete disk write. Because `httpClientForApp` creates a new transport per call, catalog calls made through one `Client` need their own context-aware serialization gate to prevent concurrent stale refreshes.
- The repository is pinned at commit `88b8094bf7ae69674777e2c82e79ac030ac39e0c`, tagged `v0.3.0`, when this plan was written.
- The GitHub Actions run for that commit failed at `govulncheck`: `.github/workflows/ci.yml` and `go.mod` pin Go 1.26.3, while [GO-2026-5039](https://pkg.go.dev/vuln/GO-2026-5039) and [GO-2026-5037](https://pkg.go.dev/vuln/GO-2026-5037) affect Go 1.26 before 1.26.4. The official Go download index listed Go 1.26.7 as the latest 1.26 patch during planning.
- The worktree already contains unrelated untracked `.agents/plans/codex-endpoint-and-credpath-options/` files. Preserve them and commit only this plan or later implementation files intentionally selected for the catalog change.

### Verified upstream facts

The request pins OpenAI Codex commit `b3f5e45cc1de8bcb09d320f3211378db285aa201` as protocol evidence.

- [`codex-api/src/endpoint/models.rs`](https://github.com/openai/codex/blob/b3f5e45cc1de8bcb09d320f3211378db285aa201/codex-rs/codex-api/src/endpoint/models.rs) builds a `GET` request at provider-relative path `models`, adds `client_version`, and decodes a top-level `models` array.
- [`protocol/src/openai_models.rs`](https://github.com/openai/codex/blob/b3f5e45cc1de8bcb09d320f3211378db285aa201/codex-rs/protocol/src/openai_models.rs) defines the relevant wire fields as `slug`, `display_name`, optional `description`, optional `default_reasoning_level`, `supported_reasoning_levels`, `visibility`, and `priority`. Reasoning-effort values are open to custom strings.
- [`models-manager/src/manager.rs`](https://github.com/openai/codex/blob/b3f5e45cc1de8bcb09d320f3211378db285aa201/codex-rs/models-manager/src/manager.rs) sorts remote models by ascending priority before building picker models.
- [`app-server/tests/suite/v2/model_list.rs`](https://github.com/openai/codex/blob/b3f5e45cc1de8bcb09d320f3211378db285aa201/codex-rs/app-server/tests/suite/v2/model_list.rs) preserves server-provided reasoning-option order and hides entries that are not picker-visible by default.

### Verified consumer facts

- `eino-tui/internal/codexmodel/config.go` currently validates static slugs with `IsCodexAllowed`.
- `eino-tui/internal/codexmodel/resolver.go` fixes reasoning effort to `medium` and constructs one immutable resolver.
- Pinned upstream `models-manager/src/lib.rs` derives `client_version` from the whole `codex-models-manager` workspace package version. `eino-tui/internal/cli/run.go` has a non-secret application `Version` candidate, but repository evidence does not prove that this unrelated version namespace has the same backend meaning.
- `eino-tui/go.mod` currently pins `codex-auth-go v0.3.0`.

## Key decisions

1. **Expose a dedicated high-level method.** Proposed `Client.ListModels` keeps authentication and routing private and avoids weakening `HTTPClient` into a general bearer transport.
2. **Accept client version per call.** `ListModels(ctx, clientVersion string)` keeps the value explicit because this repository cannot prove which consumer version namespace the backend expects. The contract treats it as an opaque pass-through value owned by the caller; the library does not claim that an application or module semver is intrinsically correct.
3. **Return picker-ready data.** The package filters `visibility == "list"`, stable-sorts ascending by priority, and does not expose visibility metadata that this consumer does not need.
4. **Preserve open string values.** Proposed reasoning effort fields are strings, so a server-added value does not require a library release.
5. **Serialize and do not cache.** Calls through one `Client` acquire a context-aware catalog gate, then make one catalog request plus any required token-refresh request. Documentation tells callers to cache for a UI session if desired and refetch after login/account changes or an explicit refresh action.

### Rejected alternatives

- Generalizing `HTTPClient` to accept arbitrary authenticated routes would expose more bearer authority than the request needs.
- Adding a new public `ModelsEndpoint` override would duplicate `Options.Endpoint` and violate the acceptance requirement that existing overrides work.
- Returning the raw upstream schema would freeze unrelated backend fields into the public Go API.
- Filtering with `IsCodexAllowed` would defeat account-aware discovery and hide future valid server slugs.
- Defining a closed reasoning-effort enum would reject future values already tolerated by the upstream protocol.
- Adding cache and ETag behavior would increase persistence and invalidation scope without being required to unblock the consumer.

## Target control and data flow

```text
eino-tui candidate catalog compatibility value
        |
        v
Client.ListModels(ctx, clientVersion)
        |
        +-- reject whitespace-only clientVersion
        +-- acquire per-Client context-aware catalog gate
        +-- defer gate release immediately and recheck ctx
        +-- derive sibling /models endpoint from Options.Endpoint or CodexEndpoint
        +-- build existing credential-backed codexTransport for that fixed endpoint
        +-- enable catalog-scoped refresh-error/log redaction
        +-- GET ?client_version=... under caller context
        |
        v
codexTransport
        +-- refresh stale credentials with existing singleflight/disk rules
        +-- inject Authorization and Codex/account headers
        +-- enforce endpoint and redirect safety
        |
        v
Codex /models response
        +-- reject non-2xx without reading body into the error
        +-- reject decompressed bodies larger than 8 MiB
        +-- decode narrow private wire structs; ignore additive fields
        +-- validate required picker fields
        +-- keep visibility=list
        +-- stable-sort priority ascending
        |
        v
[]ModelCatalogEntry (no credentials, raw body, or unrestricted client)
```

The catalog gate serializes calls made through one `Client`; it does not claim coordination across distinct `Client` values or processes. Waiting callers can exit through their own canceled context before acquiring the gate. A caller canceled during an already-started stale-token refresh may return only after the existing detached refresh finishes, but no catalog request may be sent afterward and the returned error must still match the caller's cancellation.

Cancellation takes precedence at the `ListModels` boundary: check `ctx.Err()` before gate acquisition, immediately after acquisition, and after any request error. If refresh and cancellation race, allow the existing detached refresh lifecycle to finish, then return the caller's context error instead of the refresh error.

## Compatibility, rollout, migration, and rollback

- API compatibility: the new method and types are additive. No existing signature changes.
- Stored data: no catalog data or new credential fields are persisted. Existing `auth.json` structure remains unchanged.
- Configuration: no new option is required. `Options.Endpoint` and `Options.CredentialPath` retain their existing meanings.
- Workflow: existing login, status, logout, `HTTPClient`, and `IsCodexAllowed` flows remain unchanged.
- Rollout: land implementation and tests, select the release version, synchronize/rebase, pass all local/CI gates on the final tree, push that unchanged commit, create and verify proposed tag `v0.4.0`, then write the response record with the immutable tag and full commit.
- Migration: `eino-tui` can upgrade from `v0.3.0` to the verified pin and call `ListModels` with a consumer-selected catalog compatibility value. The external response must not prescribe an unverified version namespace, and consumer code changes remain outside this repository plan.
- Rollback: before tagging, revert the catalog commit. After publishing `v0.4.0`, do not move the tag; publish a corrective patch tag and change the external response record to the verified patch pin if necessary.
- Feature flags: not applicable by user confirmation.

## Risks and mitigations

- **Endpoint derivation drift:** a malformed or unusual override path could target the wrong sibling. Centralize final-non-empty-segment replacement and test default, nested loopback, server-root, trailing/repeated separators, query, and fragment cases.
- **Schema drift:** backend additions must not break decoding. Keep private wire structs narrow and test unknown fields and custom effort strings.
- **Oversized catalog:** the upstream payload can include large ignored fields. Read at most 8 MiB plus one detection byte after HTTP decompression, reject overflow with a fixed safe error, and test compressed and uncompressed limits.
- **Silent malformed zero values:** `encoding/json` does not reject missing scalar fields. Use pointer wire fields where absence differs from a valid zero and validate required strings/slices before conversion.
- **Concurrent refresh rotation:** separate transports have separate singleflight groups. Serialize catalog calls per `Client`, make gate acquisition context-aware, and test multiple stale callers for one refresh and no credential deletion.
- **Credential disclosure:** current refresh errors can contain OAuth descriptions or body snippets. Enable catalog-scoped safe refresh errors and log attributes, and test catalog plus refresh failures with unique credential/body canaries.
- **False cancellation confidence:** refresh is intentionally detached. Test fresh-token cancellation and cancellation during stale refresh, including the documented delay and the no-catalog-request invariant.
- **Release pin race:** a response naming an unpushed commit or local-only tag does not unblock the consumer. Verify both commit reachability and the remote tag before writing the completion response.
- **Known red baseline:** main currently fails `govulncheck` on Go 1.26.3. Upgrade the module and CI to the same secure 1.26 patch before catalog work, and wait for the exact pushed SHA's Actions run before tagging.

## Assumptions

- The upstream pinned schema and route are the authoritative protocol evidence for this change.
- The configured `Options.Endpoint` continues to mean one concrete Responses URL whose final non-empty path segment can be replaced by `models`; `/` maps to `/models`.
- Ascending numeric priority is the backend picker order; equal priorities retain response order.
- Picker-visible ChatGPT-account entries need not have `supported_in_api == true`; visibility, not API-key eligibility, controls this contract.

## Unresolved decisions and gates

- No blocking product or application-context decisions remain.
- Non-blocking protocol check: before freezing documentation, recheck the current upstream `client_version` producer. If no authoritative semantics are published, retain the explicit opaque pass-through contract and make the external response assign value selection to the `eino-tui` consumer. An optional sanitized live smoke may validate a chosen value, but it must not be represented as proof that every caller version yields the same catalog.
- Stop/go readiness gate: upgrade `go.mod` and `.github/workflows/ci.yml` to the same secure Go 1.26 patch and make `govulncheck ./...` pass before feature work. Recheck the latest 1.26 patch at implementation time; use proposed 1.26.7 if it remains current.
- Non-blocking implementation check: confirm the next unused semantic version immediately before release. This plan proposes `v0.4.0` because the current release is `v0.3.0` and the change adds public API.
- Stop/go gate: select an unused release version before final changelog edits; after synchronization/rebase, do not publish a tag or response record until all local gates and the remote GitHub Actions workflow pass on the exact unchanged pushed commit.
- Stop/go gate: if the live or pinned route cannot be derived as a sibling of `Options.Endpoint`, stop and obtain an explicit endpoint contract rather than exposing a general transport.

## Document map

- [01-endpoint-and-authentication.md](01-endpoint-and-authentication.md) — derive the dedicated route and reuse the credential-refreshing transport without changing `HTTPClient`.
- [02-public-catalog-contract.md](02-public-catalog-contract.md) — define the public types, decoding, validation, filtering, ordering, and error behavior.
- [03-verification-documentation-and-release.md](03-verification-documentation-and-release.md) — specify hermetic coverage, documentation, quality gates, release pin, and consumer response.
- [04-execution-handoff.md](04-execution-handoff.md) — order implementation packages, dependencies, commands, and definition of done.
