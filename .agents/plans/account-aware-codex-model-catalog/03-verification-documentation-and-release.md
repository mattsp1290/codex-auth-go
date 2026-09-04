# Verification, documentation, and release

## Goal and prerequisite state

Prove the public behavior through real package wiring, document the freshness contract, ship an immutable consumer pin, and write the requested coordination response.

Prerequisite: work packages 01 and 02 are complete with their focused tests green.

## Exact change surface

### Tests

- `catalog_test.go` (new, repository root) — primary route, header, decoding, filtering, ordering, failure, concurrency, cancellation, redirect, refresh, and leak-safety coverage.
- `catalog_external_test.go` (new, optional, repository root) or `example_test.go` (existing) — exported API compilation from package `codexauth_test` if the primary tests use package-internal fixtures.
- `client_endpoint_test.go` (existing, optional insertion point) — endpoint derivation regression cases if keeping them beside existing endpoint safety tests is clearer.
- `public_test.go`, `transport_test.go`, `transport_singleflight_test.go`, `status_test.go`, `login_browser_test.go`, and `login_device_test.go` (existing verification surfaces; no catalog-specific edits required unless a reusable private test helper moves).

### Baseline toolchain

- `go.mod` (existing) — update `go 1.26.3` to proposed `go 1.26.7`, or the then-current secure Go 1.26 patch.
- `.github/workflows/ci.yml` (existing) — pin `actions/setup-go` to the same patch version.
- Do not add a vulnerability exception. Verify the two standard-library findings from the pinned baseline disappear and record any new finding separately.

### Documentation

- `README.md` (existing) — add an “Account-aware model catalog” section with a short `NewClient` plus `ListModels` example and these rules:
  - results depend on the authenticated account and current server catalog;
  - only picker-visible models are returned in priority order;
  - custom reasoning strings can appear;
  - the method performs one fresh catalog request plus any required token refresh and the library does not cache;
  - callers may cache within one UI session but should refetch after login/account changes and on explicit refresh;
  - the caller supplies an opaque, non-secret catalog compatibility value; the library passes it through and does not claim that an application or module semver has intrinsically correct backend semantics;
  - calls through one `Client` are serialized for safe rotating-token refresh, but distinct clients/processes are not coalesced;
  - fresh-token cancellation is prompt, while cancellation during the existing detached refresh may return after safe refresh persistence and must prevent the subsequent catalog request;
  - catalog errors and catalog-scoped refresh errors never expose response bodies.
- `doc.go` (existing) — update the package overview so it mentions the high-level catalog API while retaining the threat-model and transport scope statements.
- `client.go` (existing) — update exported `Options.Endpoint` and `Client`/`ListModels` concurrency GoDoc to cover catalog validation, sibling-route derivation, query/fragment dropping, and the per-client gate.
- `endpoint.go` (existing) — update exported `ErrInsecureEndpoint` GoDoc to include `ListModels`.
- `example_test.go` (existing, optional) — add a compile-only catalog example if README and exported GoDoc are insufficient for `go doc` discoverability.
- `CHANGELOG.md` (existing) — add the public method/types, picker filtering/ordering, reuse of existing auth/endpoint seams, and no-cache freshness policy under `[Unreleased]`; move the entry into the actual release section when the version is finalized.

### Release and coordination

- Git tag `v0.4.0` (proposed, new release reference) — use only if still unused when the release package begins.
- External response record (new, outside repository): `${HOME}/.agents/projects/codex-auth-go/responses/2026-09-04-account-aware-codex-model-catalog.md`.
  - Resolve `${HOME}/.agents/projects/codex-auth-go` from the invoking user's home directory.
  - Use the request filename unchanged.
  - State the exact public method and result types, client-version and freshness rules, visibility/ordering behavior, error contract, quality gates run, immutable full commit, and verified tag.
  - State that the `eino-tui` milestone remains blocked if the release pin cannot be fetched or the contract differs from this plan.
  - Do not include tokens, credential fixtures, raw response bodies, or session identifiers.

The external response is part of implementation delivery requested by the upstream coordination record. It is not a repository plan artifact and must not be created during planning.

## Integration test matrix

Use `httptest.Server` plus `Options.CredentialPath` so all network behavior is hermetic.

| Scenario | Credential state | Server behavior | Required observation |
|---|---|---|---|
| Successful list | fresh access token with account ID | valid mixed-visibility JSON | exact route/query/headers; visible sorted typed result |
| Additive schema | fresh | valid JSON with unknown fields and custom effort | decode succeeds; custom value preserved |
| Body limit | fresh | just-below-limit JSON, then oversized uncompressed and gzip-compressed bodies | valid body succeeds; overflow returns fixed safe error after decompression; body closes |
| Non-2xx | fresh | 401, 403, 429, and 5xx with unique body canary | typed status error; body and credentials absent from error/log |
| Malformed body | fresh | syntax error, wrong type, or required field absent | safe decode/validation error; body not echoed |
| Cancellation | fresh | handler blocks until request context closes | prompt return; `errors.Is(context.Canceled)` |
| Deadline | fresh | handler exceeds caller deadline | `errors.Is(context.DeadlineExceeded)` |
| Refresh | stale access token with refresh token/account ID | token endpoint returns fresh token, then catalog succeeds | one refresh; persisted token; catalog uses fresh bearer and retained account ID |
| Concurrent refresh | same `Client`, stale credentials, concurrent calls | token endpoint blocks until callers overlap, then succeeds | one token request; all calls succeed or individually cancel; no credential deletion |
| Refresh failure safety | stale credentials | OAuth JSON error with description, then non-JSON body in a separate case | only safe code/sentinel escapes; body and credential canaries absent from error/log |
| Unknown OAuth code | stale credentials | OAuth JSON uses a canary as `error` | `ErrRefreshFailed`; raw code absent from error/log |
| Cancel during refresh | stale credentials | refresh blocks, caller cancels, refresh then succeeds | persisted refresh completes; zero catalog calls; eventual `context.Canceled` identity |
| Pre-canceled stale request | stale credentials | both token and catalog servers record calls | zero token/catalog calls; immediate context identity |
| Cancel plus refresh failure | stale credentials | caller cancels while refresh later fails as OAuth JSON, non-JSON, or timeout | context error wins after detached refresh; no catalog call; bodies absent |
| No credentials | missing OpenAI entry | catalog server must see zero calls | `errors.Is(ErrNotLoggedIn)` |
| Endpoint rail | fresh | non-loopback cleartext URL | `errors.Is(ErrInsecureEndpoint)`; zero calls |
| Redirect | fresh | models route returns 302 to second server | target sees zero calls and no bearer; original status is classified; body closes |
| Gate recovery | same `Client` | each post-acquisition failure followed by valid response | second call acquires promptly and succeeds for every failure class |
| Regression | fresh | catalog call followed by Responses request | catalog hits `/models`; `HTTPClient` still hits `/responses` |

The stale-refresh test mutates `tokenEndpointURL`; serialize it and restore global state with `t.Cleanup`.

## Verification commands

Run focused tests during implementation:

```sh
go test ./... -run 'Test(ListModels|ModelsEndpoint)'
go test ./... -run 'TestHTTPClient|TestRoundTrip|TestEnsureFresh'
```

Run repository and CI-equivalent gates from the repository root before release:

```sh
gofmt -w <changed-go-files>
go build ./...
go test -race ./...
go vet ./...
golangci-lint run
govulncheck ./...
GOOS=darwin go vet ./...
GOOS=windows go vet ./...
```

Use the pinned tool versions from `.github/workflows/ci.yml` when local tools differ. Inspect any `govulncheck` finding before deciding whether it blocks release; do not suppress it through this feature.

## Client-version protocol check

Before freezing documentation:

1. Reinspect the current OpenAI Codex models client and its `client_version` producer. Record whether it still sends the whole workspace package version and whether the server contract documents additional constraints.
2. Keep `Client.ListModels` input explicit unless new authoritative evidence supports a safe library-owned derivation.
3. Assign selection of the exact non-secret value to the `eino-tui` consumer in the external response. Do not invent or silently derive that value in this repository.
4. If a maintainer performs an optional live smoke, record only route/status, result count, the chosen non-secret value, and whether required fields are populated. Do not print catalog bodies, headers, tokens, account IDs, or session IDs.

This check documents the pass-through boundary without turning an unknown consumer-side value into a blocker for the supported library contract.

## Release procedure and stop/go gates

1. Fetch remote refs and confirm proposed `v0.4.0` is unused locally and remotely. If occupied, select the next additive semantic version before finalizing changelog text.
2. Complete the client-version protocol check and keep the exact value consumer-owned unless authoritative evidence changes the contract.
3. Confirm `git diff` contains only intended catalog implementation, toolchain readiness, documentation, tests, changelog, and plan files. Preserve unrelated `.agents` content.
4. Commit the implementation and plan with a focused message.
5. Run `git pull --rebase`. Resolve any conflict without dropping tests or plan constraints.
6. Run the complete focused and CI-equivalent verification commands on the post-rebase tree. Record the exact full `git rev-parse HEAD` only after every gate passes.
7. Close the implementation Beads issue, run `bd dolt push`, push the unchanged tested commit, and confirm local HEAD equals the remote branch head. If closing the issue changes a tracked repository file or any source/plan file changes after step 6, recommit as needed, rerun the complete gates, capture the new SHA, push again, and push Beads data again.
8. Locate the GitHub Actions run for the exact pushed SHA and wait for it to complete successfully:

   ```sh
   gh run list --repo mattsp1290/codex-auth-go --commit <full-sha> --workflow CI --json databaseId,headSha,status,conclusion,url
   gh run watch <run-id> --repo mattsp1290/codex-auth-go --exit-status
   ```

   Verify the returned `headSha` equals the recorded SHA. Stop if no exact-SHA run appears or the run fails.
9. Tag that exact pushed, locally tested, and CI-tested commit, push the tag, fetch remote refs, and verify `git rev-parse <tag>^{commit}` equals the recorded full commit.
10. Confirm the tag is resolvable from the remote rather than only from local refs.
11. Prune stale remote-tracking branches, confirm no stash was created or left behind, and run final `git status` and upstream-divergence checks.
12. Write the external response record only after steps 1–11 succeed.
13. Re-read the response pin and public signature against the tagged source.

Stop if any gate fails. Do not write a completion response that implies `eino-tui` is unblocked when the commit or tag is not remotely usable.

## Acceptance criteria

- Every request acceptance item maps to a passing named hermetic test or an explicit quality gate.
- The full suite exercises the real credential-backed transport, not a replacement `httpDoer` that bypasses headers and refresh.
- README and GoDoc are consistent about visibility, order, freshness, caching, client version, cancellation, and error safety.
- The external response accurately assigns the opaque `clientVersion` choice to the consumer and does not imply an unverified version namespace.
- GitHub Actions succeeded for the exact pushed SHA before tagging.
- Changelog and release tag identify the same version.
- The external response names the exact pushed commit and verified tag and describes the usable contract.
- No repository file claims implementation or release completion before those actions actually occur.

## Dependencies, risks, and exclusions

- This work follows implementation packages 01 and 02; documentation can begin after the public signature stabilizes, but release steps are strictly last.
- Do not run a live account request as an automated quality gate. Hermetic tests are authoritative for CI; any optional manual smoke must not log catalog bodies or credentials and must not be described as validating every possible caller version.
- Do not modify `eino-tui` or its `go.mod` in this repository work.
- Do not move or retag a published version. Ship a patch release for post-tag corrections.
