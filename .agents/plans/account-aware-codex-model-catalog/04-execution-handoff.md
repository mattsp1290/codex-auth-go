# Execution handoff

## Starting state

- Plan status: Ready.
- Implementation has not occurred.
- Repository baseline: `88b8094bf7ae69674777e2c82e79ac030ac39e0c` (`v0.3.0`) when planned.
- User-confirmed context: no active users, no backward-compatibility requirement, feature flags not applicable.
- Explicit request constraint: preserve current `HTTPClient`, login, status, and `IsCodexAllowed` behavior despite the broader context answer.
- Tracker: implementation should use a new or appropriately scoped `bd` issue; planning issue `codex-auth-go-dt7` covers only this specification.

## Dependency-ordered work packages

### WP0 — Secure green baseline

Goal: remove the known standard-library vulnerability failure before catalog code obscures the baseline.

Changes:

- `go.mod`: update proposed Go version from 1.26.3 to 1.26.7, or the then-current secure 1.26 patch.
- `.github/workflows/ci.yml`: pin `actions/setup-go` to the identical patch.

Prerequisites: confirm the chosen Go patch is an official release and is not affected by GO-2026-5039 or GO-2026-5037.

Parallelization: none with feature edits; establish a green baseline first.

Verification:

```sh
go version
go build ./...
go test -race ./...
go vet ./...
golangci-lint run
govulncheck ./...
GOOS=darwin go vet ./...
GOOS=windows go vet ./...
```

Acceptance:

- `go.mod` and CI pin the same secure Go 1.26 patch.
- The full baseline gate passes without exclusions.
- No catalog implementation is used to explain or suppress a baseline failure.

### WP1 — Fixed authenticated models route

Goal: derive the sibling models URL and prove the existing transport reaches it with all authentication and safety behavior intact.

Changes:

- `endpoint.go`: add proposed internal `modelsEndpoint`.
- `client.go`: add the proposed lazily initialized, context-aware per-client catalog gate and update `Options.Endpoint` GoDoc.
- `transport.go`: add proposed catalog-scoped safe refresh error/log behavior, disabled for ordinary `HTTPClient` transports.
- `catalog.go` (new): add the private request setup needed to call existing `httpClientForApp` at the derived endpoint.
- `catalog_test.go` (new) and optionally `client_endpoint_test.go`: add route, query, header, endpoint-rail, cancellation, and Responses-regression tests.

Prerequisites: baseline suite green; no implementation API decision beyond [00-overview.md](00-overview.md) changed.

Parallelization: endpoint table tests and request integration fixtures may be prepared together, but do not finalize catalog request tests before the derivation contract is stable.

Verification:

```sh
go test ./... -run 'Test(ListModels|ModelsEndpoint|HTTPClient|RoundTrip)'
```

Acceptance:

- Default, root, nested, and trailing-slash loopback overrides hit the exact sibling `/models` path.
- `client_version` is present exactly once and safely encoded.
- Real transport code injects bearer/account/Codex headers.
- Catalog requests cannot turn `HTTPClient` into a general authenticated route.
- Context and endpoint sentinel identities are preserved.
- Concurrent stale calls through one `Client` cannot rotate the same refresh token independently.
- Refresh error descriptions and raw bodies cannot escape through catalog errors or logs.
- A pre-canceled call performs no refresh or catalog I/O, cancellation wins any later refresh-error race, and every acquired gate is released with an immediate `defer`.

### WP2 — Picker-ready public contract

Goal: expose the typed catalog result and safely translate the narrow wire response.

Changes:

- `catalog.go` (new): add proposed `ModelCatalogEntry`, `ReasoningEffortOption`, `ModelCatalogHTTPError`, `Client.ListModels`, private wire types, a private response lifecycle helper, validation, filtering, stable ordering, and response lifecycle logic.
- `catalog_test.go` (new): add success, optional-field, visibility, ordering, forward-compatibility, malformed-response, non-2xx, body-close, and leak-canary coverage.
- `catalog_external_test.go` (new, optional) or `example_test.go`: prove public consumption if internal-package tests do not.

Prerequisites: WP1 complete.

Parallelization: decoding unit cases may be written while WP1 lands if they call a private pure conversion helper. Public black-box tests wait for WP1.

Verification:

```sh
go test ./... -run 'TestListModels'
go test -race ./...
```

Acceptance:

- Returned entries contain every requested field and only picker-visible models.
- Priority sorting is stable and reasoning-option order is untouched.
- Additive fields and custom efforts succeed.
- Missing required fields and trailing JSON fail without echoing data.
- Decompressed responses larger than 8 MiB fail with a fixed safe error; just-below-limit responses succeed.
- Non-2xx errors expose status only through `ModelCatalogHTTPError`.
- `ErrNotLoggedIn`, `ErrInsecureEndpoint`, cancellation, deadlines, safe `ErrRefreshFailed`, and sanitized OAuth codes remain inspectable.
- Tracking-body unit tests prove closure independently of black-box transport tests.

### WP3 — Refresh and regression hardening

Goal: prove the catalog path inherits credential refresh and does not alter established public behavior.

Changes:

- `catalog_test.go` (new): add stale-token refresh with persistent fixture, refreshed bearer, retained account ID, one token request, concurrent calls, cancellation during refresh, redirect refusal, safe refresh failures, and exact catalog request assertions.
- Existing regression test files: edit only if a narrowly reusable helper must move; preserve current assertions.

Prerequisites: WP2 complete.

Parallelization: none with another test that mutates `tokenEndpointURL`.

Verification:

```sh
go test ./... -run 'TestListModels.*(Refresh|NotLoggedIn|Cancel|Deadline)'
go test ./... -run 'Test(Status|Login|HTTPClient|IsCodexAllowed|RoundTrip|EnsureFresh)'
```

Acceptance:

- Stale credentials refresh once and catalog uses the new bearer plus retained account ID.
- Concurrent stale catalog calls through one `Client` cause one rotation and do not delete credentials.
- Cancellation during refresh may wait for persistence, then returns the caller cancellation without issuing the catalog request.
- OAuth descriptions, arbitrary OAuth error codes, and non-JSON refresh-body canaries are absent from catalog errors and logs.
- A catalog redirect target receives neither a request nor a bearer token.
- Credential fixtures remain isolated under `t.TempDir()`.
- Existing login, status, HTTP client, static model admission, and transport tests pass unchanged in meaning.
- Error and log canaries prove that credentials and response bodies are absent.
- Every failure class is followed by a successful same-client call to prove gate recovery.

### WP4 — Documentation and release preparation

Goal: make the contract discoverable and prepare consistent release metadata.

Changes:

- `README.md`: catalog usage, freshness, cache/refetch, account-dependence, ordering, custom efforts, client-version, and safe errors.
- `doc.go`: high-level catalog scope.
- `client.go` and `endpoint.go`: exported option and sentinel documentation for catalog behavior.
- `example_test.go` (optional): compile-only example.
- `CHANGELOG.md`: proposed `v0.4.0` entry, subject to the release gate.

Prerequisites: public names and semantics stable after WP3.

Parallelization: README prose can be drafted during late WP3; changelog version must wait for tag availability check.

Verification:

```sh
go test ./...
go doc . Client.ListModels
```

Acceptance:

- GoDoc and README describe the same signature and semantics as tests.
- Documentation states no package cache and gives explicit refetch triggers.
- Documentation does not imply catalog entries are globally available or permanent.
- Documentation treats `clientVersion` as an opaque consumer-owned pass-through value and describes the stale-refresh cancellation delay.

### WP5 — Quality gates, immutable pin, and unblock response

Goal: publish a reproducible revision and accurately notify the blocked consumer.

Changes:

- Git commit and proposed tag `v0.4.0` (new immutable release metadata).
- `${HOME}/.agents/projects/codex-auth-go/responses/2026-09-04-account-aware-codex-model-catalog.md` (new external response record, created only after remote verification).
- Beads implementation issue state and remote Beads data.

Prerequisites: WP0–WP4 complete; all focused tests green; current upstream client-version semantics rechecked; the external response is prepared to assign exact value selection to `eino-tui`.

Parallelization: none. Remote version check, commit, pull/rebase, final gates, issue closure, push, exact-SHA GitHub Actions success, tag verification, cleanup, and response writing are strictly ordered.

Verification:

```sh
go build ./...
go test -race ./...
go vet ./...
golangci-lint run
govulncheck ./...
GOOS=darwin go vet ./...
GOOS=windows go vet ./...
git status --short --branch
git rev-parse HEAD
gh run list --repo mattsp1290/codex-auth-go --commit <full-sha> --workflow CI --json databaseId,headSha,status,conclusion,url
gh run watch <run-id> --repo mattsp1290/codex-auth-go --exit-status
git rev-parse v0.4.0^{commit}
git ls-remote --tags origin refs/tags/v0.4.0 refs/tags/v0.4.0^{}
```

If the release version changes, substitute the selected version consistently in the last two commands, changelog, and response.

Acceptance:

- All quality commands pass on the exact commit that is pushed.
- The complete gates run after the final rebase and after any later tree change.
- The exact pushed SHA's GitHub Actions CI run succeeds before tagging.
- `git status` reports the branch up to date with origin after the repository close protocol.
- The implementation issue is closed and its final state is pushed to Beads before delivery.
- Stashes are empty and stale remote-tracking branches are pruned before the final status check.
- The verified remote tag resolves to the intended full commit.
- The response record names the actual API, verified tag, full commit, and tests run.
- The response does not claim unblock before the pin is remotely usable.

## Integration and regression gates

1. **Route gate:** `/models` and `/responses` remain separate fixed targets derived from one safe endpoint configuration.
2. **Authentication gate:** all catalog network tests traverse `codexTransport`; no fake bypasses refresh or header decoration.
3. **Schema gate:** narrow decoding tolerates additive fields and custom effort strings while rejecting absent required picker fields.
4. **Safety gate:** error/log tests contain neither credential canaries nor non-2xx/malformed body canaries.
5. **Lifecycle gate:** response bodies close and cancellation identities survive wrapping.
6. **Concurrency gate:** one `Client` serializes catalog calls context-safely and concurrent stale calls rotate once without credential deletion.
7. **Compatibility gate:** existing `HTTPClient`, login, status, `IsCodexAllowed`, endpoint, redirect, and singleflight tests remain green.
8. **Protocol boundary:** current upstream semantics are rechecked, and the response leaves the opaque `client_version` choice with the consumer unless authoritative evidence supports derivation.
9. **Baseline and delivery gate:** the secure Go patch passes locally, final feature gates run post-rebase, exact-SHA GitHub Actions succeeds, and remote commit/tag verification precedes the external response.

## Final definition of done

- Proposed public symbols exist with documented, tested semantics.
- The live-account catalog request uses the exact dedicated route and required query.
- Returned data is picker-visible, priority-ordered, reasoning-order-preserving, and forward-compatible for additive fields and custom effort strings.
- Credentials, raw bodies, and unrestricted authenticated transports remain private.
- Hermetic tests cover every acceptance scenario named in the request.
- All repository and CI-equivalent gates pass.
- Implementation changes are committed and pushed.
- A verified remote tag or full commit is available for `eino-tui`.
- The external response record accurately identifies that pin and usable contract.
- The implementation Beads issue is closed and Beads data is pushed.

## Deferred and follow-up work

- `eino-tui` consumption, selector UI, resolver reconstruction, and run-snapshot changes remain in the consumer repository.
- Cross-process or persistent catalog caching remains deferred until a consumer demonstrates a need.
- ETag conditional requests, background refresh, pricing, service tiers, input modalities, and the full upstream schema remain out of scope.
- Replacing `IsCodexAllowed` with catalog-based validation is a consumer migration decision, not automatic library behavior.
