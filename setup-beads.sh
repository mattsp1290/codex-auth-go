#!/bin/bash
# Project: codex-auth-go (extract from advisor/internal/advisor/codexauth)
# Generated: 2026-05-25
# Source plan: docs/prompts/extract-codex-auth-go.md (sections referenced
#              as §N below correspond to that document).
#
# Run once from the repo root. Beads creates duplicates on re-run; re-run
# requires a manual cleanup via `bd delete` first.

set -e

if ! command -v bd >/dev/null 2>&1; then
  echo "error: 'bd' (beads) CLI not found on PATH" >&2
  exit 1
fi

if [ ! -d ".beads" ]; then
  bd init
fi

echo "Creating codex-auth-go extraction beads..."

# =========================================================================
# Phase 1 — PR1: Repo skeleton (§8.a, §10 PR1)
# Linux-only CI per §6; per-OS source still ships and is kept honest by
# GOOS=darwin/windows go vet cross-compile steps.
# =========================================================================

GOMOD_INIT=$(bd create "go mod init github.com/mattsp1290/codex-auth-go" \
  -d "Run 'go mod init github.com/mattsp1290/codex-auth-go' at repo root. Go directive 1.25.5 to match advisor (~/git/advisor/go.mod). Commit go.mod with no dependencies yet — they arrive with the source lift in PR2." \
  -p 0 -l prep --silent)

BRANCH_CHECK=$(bd create "Verify default branch is 'main' not 'master'" \
  -d "Per §7. Run 'git symbolic-ref refs/remotes/origin/HEAD' (or check on GitHub). If default is 'master', rename via 'git branch -m main' locally + 'gh repo edit --default-branch main'. Required before tagging v0.1.0 since the lift plan and CI assume main." \
  -p 1 -l prep --silent)

GITIGNORE=$(bd create "Add .gitignore covering Go build artifacts and editor files" \
  -d "Standard Go .gitignore: bin/, *.test, coverage.out, .idea/, .vscode/, *.swp, .DS_Store. Mirror advisor's .gitignore where reasonable." \
  -p 2 -l prep --silent)
bd dep add $GITIGNORE $GOMOD_INIT

LICENSE_VERIFY=$(bd create "Verify existing LICENSE is MIT (2026 Matt Spurlin)" \
  -d "Per §7 the LICENSE is already in place at repo root. Cat the file to confirm it is MIT and the copyright line is correct. If not, fix before PR1 merges. No new code." \
  -p 3 -l prep --silent)

GOLANGCI=$(bd create "Add .golangci.yml mirroring advisor's lint config" \
  -d "Per §8.a. Copy advisor's lint config (errcheck, govet, ineffassign, gofmt, gocyclo at a sane threshold). Verify with 'golangci-lint run' on the empty package — should be silent." \
  -p 1 -l prep --silent)
bd dep add $GOLANGCI $GOMOD_INIT

CI_WORKFLOW=$(bd create "Add Linux-only .github/workflows/ci.yml with build, test, vet, lint, govulncheck, and cross-compile vet" \
  -d "Per §6 and §8.a. Single job on ubuntu-latest with steps: setup-go 1.25.5, go build ./..., go test -race ./..., go vet ./..., golangci-lint run, govulncheck ./..., GOOS=darwin go vet ./..., GOOS=windows go vet ./.... The cross-compile vet steps keep browser_darwin.go, browser_windows.go, credstore_path_darwin.go, and credstore_path_windows.go honest. No matrix entries for macos or windows runners by design." \
  -p 0 -l prep --silent)
bd dep add $CI_WORKFLOW $GOMOD_INIT
bd dep add $CI_WORKFLOW $GOLANGCI

README_SKELETON=$(bd create "Flesh out README.md skeleton: scope, install, links to plan" \
  -d "Per §8.a. Replace the current one-line README. Sections: project description (extracted from advisor's codexauth package), 'install' (go get placeholder, expanded in PR3), 'scope and non-goals' (cite §1 of the plan), 'security notes' (passive JWT decode per §5; do not modify ClientID). The runnable usage example is added in PR3 once the Client API exists." \
  -p 1 -l prep --silent)
bd dep add $README_SKELETON $GOMOD_INIT

# =========================================================================
# Phase 2 — PR2: Verbatim lift of advisor/internal/advisor/codexauth (§8.b)
# 19 source files + 20 test files. Excludes chatmodel.go / chatmodel_test.go
# (eino-coupled; §1 non-goal, §8.f).
# =========================================================================

LIFT_CONSTANTS_MODELS_JWT=$(bd create "Lift constants.go, models.go, jwt.go (+ tests) verbatim" \
  -d "Leaf-node lift. Copy from ~/git/advisor/internal/advisor/codexauth/: constants.go, models.go (with models_test.go), jwt.go (with jwt_test.go). No edits except the package declaration (already 'codexauth'). These three files have no advisor-specific imports per §2's grep verification. Confirm 'go vet ./...' is clean after the copy." \
  -p 0 -l lift --silent)
bd dep add $LIFT_CONSTANTS_MODELS_JWT $CI_WORKFLOW

LIFT_PKCE_AUTHORIZE=$(bd create "Lift pkce.go + authorize.go (+ tests) verbatim" \
  -d "Copy pkce.go, pkce_test.go, authorize.go, authorize_test.go. PKCE S256 code challenge generation and the authorize-URL builder. No edits. Verify pkce_test (which tests the verifier-challenge roundtrip) passes after the lift." \
  -p 1 -l lift --silent)
bd dep add $LIFT_PKCE_AUTHORIZE $LIFT_CONSTANTS_MODELS_JWT

LIFT_TOKEN_ENDPOINT=$(bd create "Lift exchange.go + token_http.go (+ tests) verbatim" \
  -d "Copy exchange.go (with exchange_test.go) and token_http.go (with token_http_test.go). token_http.go owns the exported *AuthError type per §3. exchange.go calls the /oauth/token endpoint and decodes the response. No edits. The jwt.ExtractAccountID call site stays unchanged." \
  -p 1 -l lift --silent)
bd dep add $LIFT_TOKEN_ENDPOINT $LIFT_CONSTANTS_MODELS_JWT

LIFT_BROWSER=$(bd create "Lift browser opener cluster (browser.go + 4 OS files + browser_test.go + browser_linux_test.go)" \
  -d "Copy browser.go, browser_darwin.go (//go:build darwin), browser_linux.go (//go:build linux), browser_windows.go (//go:build windows), browser_other.go (//go:build !darwin && !linux && !windows), browser_test.go, browser_linux_test.go. The per-OS files use stdlib build constraints. browser_linux_test runs on the Linux CI runner. Darwin and Windows variants only compile under cross-compile vet per §6 — accepted tradeoff." \
  -p 1 -l lift --silent)
bd dep add $LIFT_BROWSER $CI_WORKFLOW

LIFT_CALLBACK=$(bd create "Lift callback.go + callback_test.go with one error-string edit" \
  -d "Copy callback.go and callback_test.go. EDIT per §2: the error string at callback.go:33 contains the substring 'is another advisor instance running?' — rephrase to drop 'advisor' so the module reads cleanly outside an advisor context (e.g., 'is another codex-auth client running on this port?'). Update callback_test.go to match the new string if it asserts on it. *PortInUseError public type ships unchanged per §3." \
  -p 1 -l lift --silent)
bd dep add $LIFT_CALLBACK $LIFT_BROWSER

LIFT_CREDSTORE=$(bd create "Lift credstore.go + 5 test files (credstore_test, credstore_flock_test, credstore_path_{darwin,linux,windows}_test)" \
  -d "Copy credstore.go and: credstore_test.go, credstore_flock_test.go, credstore_path_darwin_test.go, credstore_path_linux_test.go, credstore_path_windows_test.go. Note: this lift is VERBATIM — the 'advisor' literal at credstore.go:60 STAYS in this PR. The AppName parameterization happens in PR3 (§4.1). The darwin/windows path tests will compile under cross-compile vet but not execute (§6 tradeoff documented in §2). Verify credstore_flock_test exercises the lock-acquisition path on the Linux runner." \
  -p 0 -l lift --silent)
bd dep add $LIFT_CREDSTORE $CI_WORKFLOW

LIFT_LOGIN_BROWSER=$(bd create "Lift login_browser.go + login_browser_test.go verbatim" \
  -d "Copy login_browser.go and login_browser_test.go. The function orchestrates: callback listener bind → authorize URL with PKCE → browser open → callback wait → token exchange → credstore write. Depends on every upstream lift bead (pkce, authorize, callback, exchange, token_http, credstore, browser). No edits." \
  -p 1 -l lift --silent)
bd dep add $LIFT_LOGIN_BROWSER $LIFT_PKCE_AUTHORIZE
bd dep add $LIFT_LOGIN_BROWSER $LIFT_CALLBACK
bd dep add $LIFT_LOGIN_BROWSER $LIFT_TOKEN_ENDPOINT
bd dep add $LIFT_LOGIN_BROWSER $LIFT_CREDSTORE

LIFT_LOGIN_DEVICE=$(bd create "Lift login_device.go + login_device_test.go verbatim" \
  -d "Copy login_device.go and login_device_test.go. RFC 8628 device-authorization flow with deviceSafetyMargin polling and stderr user-code prompt. The exported *DeviceError type ships unchanged per §3. No edits. The PR3 device-prompt callback (§3 deferred to v0.2.0 per §7) is NOT in scope here." \
  -p 1 -l lift --silent)
bd dep add $LIFT_LOGIN_DEVICE $LIFT_TOKEN_ENDPOINT
bd dep add $LIFT_LOGIN_DEVICE $LIFT_CREDSTORE

LIFT_TRANSPORT=$(bd create "Lift transport.go + transport_test.go + transport_singleflight_test.go verbatim" \
  -d "Copy transport.go and both test files. The codexTransport handles: URL rewrite to chatgpt.com/backend-api/codex/responses, bearer injection, 60s-skew proactive refresh, three-branch invalid_grant recovery (transport.go:140-174), case-preserved originator + session_id headers, singleflight dedup of concurrent refreshes. transport_singleflight_test exercises the coalescing race; CI must run 'go test -race' to make this real. The advisorVersion var and 'advisor/' User-Agent prefix STAY in this PR per §2 — parameterization happens in PR3 (§4.3)." \
  -p 0 -l lift --silent)
bd dep add $LIFT_TRANSPORT $LIFT_TOKEN_ENDPOINT
bd dep add $LIFT_TRANSPORT $LIFT_CREDSTORE

LIFT_PUBLIC=$(bd create "Lift public.go + public_test.go verbatim (package-level Login/Logout/HTTPClient remain)" \
  -d "Copy public.go and public_test.go. The package-level Login(ctx, forceDevice), LoginBrowser, LoginDevice, Logout, and HTTPClient functions, plus ErrNotLoggedIn, ship unchanged. The Client type is NOT introduced here — that happens in PR3 (§3, §8.c). RFC 7009 revocation in Logout retains the http.ErrUseLastResponse no-redirect guard per the security note in §6." \
  -p 0 -l lift --silent)
bd dep add $LIFT_PUBLIC $LIFT_LOGIN_BROWSER
bd dep add $LIFT_PUBLIC $LIFT_LOGIN_DEVICE
bd dep add $LIFT_PUBLIC $LIFT_TRANSPORT

LIFT_OTEL_DENYLIST=$(bd create "Lift otel_denylist_test.go (credential-leak guardrail)" \
  -d "Copy otel_denylist_test.go. Per §6 this is the most important non-obvious test to preserve: it enforces that no credential string ever lands in an OpenTelemetry attribute. The guardrail makes the package safe to embed in observability-rich applications. Must run on the Linux CI runner and must remain green." \
  -p 1 -l lift --silent)
bd dep add $LIFT_OTEL_DENYLIST $LIFT_TRANSPORT
bd dep add $LIFT_OTEL_DENYLIST $LIFT_PUBLIC

LIFT_GO_TEST_GREEN=$(bd create "Verify 'go test -race ./...' is green after the full lift" \
  -d "Run 'go test -race ./...' from the repo root after all lift beads close. Every shipping test (~20 files) must pass. Tag any flake or failure as a blocker before PR2 merges. Per §6 the singleflight race test specifically requires -race to be meaningful." \
  -p 0 -l lift --silent)
bd dep add $LIFT_GO_TEST_GREEN $LIFT_PUBLIC
bd dep add $LIFT_GO_TEST_GREEN $LIFT_OTEL_DENYLIST

LIFT_CROSSCOMPILE_GREEN=$(bd create "Verify 'GOOS=darwin go vet ./...' and 'GOOS=windows go vet ./...' are green" \
  -d "Per §6. Run both cross-compile vet steps locally before merging PR2. They are also CI gates in .github/workflows/ci.yml. Each must complete with zero diagnostics — this is the only mechanism keeping the darwin and windows source variants from bit-rotting silently." \
  -p 0 -l lift --silent)
bd dep add $LIFT_CROSSCOMPILE_GREEN $LIFT_GO_TEST_GREEN

# =========================================================================
# Phase 3 — PR3: Client + Options + AppName + Status (§3, §4, §8.c)
# =========================================================================

API_OPTIONS=$(bd create "Define Options struct (AppName, CallbackPort, Logger) and exported Credentials/StatusInfo types" \
  -d "Per §3. Add a new file (proposed: client.go) declaring: type Options struct { AppName string; CallbackPort int; Logger *slog.Logger }, type StatusInfo struct { LoggedIn bool; ExpiresAt time.Time; AccountID string; ConfigPath string }. Credentials already exists in exchange.go — re-export through a doc comment on the type stating it is part of the public v0.1.0 surface. Options.CallbackPort defaults to OAuthPort (1455) when zero. Options.Logger defaults to slog.Default() when nil; flag this in the open-question gate per §11." \
  -p 0 -l api --silent)
bd dep add $API_OPTIONS $LIFT_CROSSCOMPILE_GREEN

API_CLIENT_TYPE=$(bd create "Introduce Client struct in client.go with NewClient(opts Options) *Client" \
  -d "Per §3 and §8.c. Define type Client { /* unexported fields: appName, callbackPort, logger, http client */ } and NewClient(opts Options) *Client. The internal fields replace the package-level singletons (browserAvailableCheck, loginBrowserFn, loginDeviceFn, logoutHTTPClient, logoutStderr in public.go). Empty AppName defaults to 'codex' per §4.1 and emits a single warn-level slog event. Tests inject via Options or via small with* test helpers in a new codexauth_internal_test.go." \
  -p 0 -l api --silent)
bd dep add $API_CLIENT_TYPE $API_OPTIONS

API_CREDSTORE_APPNAME=$(bd create "Parameterize credstore.go directory name via Options.AppName (§4.1)" \
  -d "Change credstore.go:60 pathFunc from filepath.Join(dir, 'advisor', 'auth.json') to filepath.Join(dir, c.appName, 'auth.json'). The pathFunc becomes a method on *Client or accepts the resolved AppName via a struct field; the package-level wrappers pre-supply 'advisor' (see API_PUBLIC_WRAPPERS). Add tests: (a) two Client instances with different AppName produce two distinct auth.json paths, (b) an empty AppName defaults to 'codex' and emits the warn-level log exactly once per process." \
  -p 0 -l api --silent)
bd dep add $API_CREDSTORE_APPNAME $API_CLIENT_TYPE

API_TRANSPORT_USERAGENT=$(bd create "Parameterize transport.go User-Agent via Options.AppName + package Version var (§4.3)" \
  -d "Rename transport.go:19 advisorVersion to Version (package-level var, default '0.0.0-dev', settable via -ldflags -X). Build User-Agent at transport.go:266 as appName + '/' + Version + ' (' + runtime.GOOS + ' ' + runtime.GOARCH + ')' where appName flows in from the Client's resolved AppName. Deprecated wrappers (see API_PUBLIC_WRAPPERS) continue to emit 'advisor/<version>'. New consumers see 'codex/<version>' by default. Update transport_test if it asserts the literal." \
  -p 0 -l api --silent)
bd dep add $API_TRANSPORT_USERAGENT $API_CLIENT_TYPE

API_ORIGINATOR_DECISION=$(bd create "Document the Originator-stays-constant decision in constants.go and README (§4.2)" \
  -d "Per §4.2 the Originator header value stays 'advisor' for v0.1.0 — verbatim preservation of on-the-wire behaviour. Add a comment above constants.go:29 stating that this is the literal the Codex CLI sends, consumers MUST NOT change it without a wire trace, and v0.2.0 may move it to Options. Add a parallel note to README. No code change to the constant value." \
  -p 2 -l api --silent)
bd dep add $API_ORIGINATOR_DECISION $API_CLIENT_TYPE

API_CLIENT_METHODS=$(bd create "Add Client.Login / LoginBrowser / LoginDevice / Logout / HTTPClient methods" \
  -d "Per §3. Each method delegates to the existing lifted internals using the Client's resolved appName, callbackPort, and logger. Signatures verbatim from §3. Inside each method, the global vars at public.go:25-50 become struct fields with the same defaults. Tests confirm a Client and a deprecated wrapper produce identical behaviour when AppName='advisor' (path bit-for-bit per §8.e)." \
  -p 0 -l api --silent)
bd dep add $API_CLIENT_METHODS $API_CREDSTORE_APPNAME
bd dep add $API_CLIENT_METHODS $API_TRANSPORT_USERAGENT

API_PUBLIC_WRAPPERS=$(bd create "Refactor public.go package-level functions into deprecated wrappers over NewClient(Options{AppName:\"advisor\"})" \
  -d "Per §3 backward-compat shim and §8.c. Keep Login(ctx, forceDevice), LoginBrowser(ctx), LoginDevice(ctx), Logout(ctx), HTTPClient(_) as exported package-level functions with verbatim existing signatures. Add // Deprecated: use NewClient(Options{AppName:\"advisor\"}) instead. Per §4.1 the wrappers must default AppName to 'advisor' so existing advisor installations keep finding auth.json at the same on-disk path bit-for-bit. ErrNotLoggedIn message ('codexauth: not logged in') unchanged per §3." \
  -p 0 -l api --silent)
bd dep add $API_PUBLIC_WRAPPERS $API_CLIENT_METHODS

API_STATUS=$(bd create "Implement Client.Status(ctx) (StatusInfo, error) — new in v0.1.0 (§3)" \
  -d "Per §3 and §7. Add Client.Status(ctx) that returns StatusInfo{LoggedIn, ExpiresAt, AccountID, ConfigPath}. AccountID comes from the cached ID-token claims via jwt.ExtractAccountID. ConfigPath comes from the credstore path resolver for the Client's AppName. LoggedIn false when credstore returns ErrNotLoggedIn. Stale (within 60s of expiry) — decision: surface as a separate field on StatusInfo, NOT a method. Dedicated test file: status_test.go with logged-in, not-logged-in, and stale-token cases." \
  -p 1 -l api --silent)
bd dep add $API_STATUS $API_PUBLIC_WRAPPERS

API_TEST_APPNAME_PARITY=$(bd create "Test: deprecated wrapper + NewClient(Options{AppName:\"advisor\"}) hit identical on-disk path" \
  -d "Regression guard for the §8.e migration claim that advisor users do not need to re-login. The test stands up two flows — one through Login (wrapper) and one through NewClient(Options{AppName:\"advisor\"}).Login — and asserts they read and write the SAME absolute path under os.UserConfigDir(). Add as a new test in public_test.go or a new file appname_parity_test.go." \
  -p 1 -l testing --silent)
bd dep add $API_TEST_APPNAME_PARITY $API_PUBLIC_WRAPPERS

API_TEST_APPNAME_ISOLATION=$(bd create "Test: two Clients with different AppName produce two distinct credential files" \
  -d "Per §9 risk mitigation (multi-process credstore contention). Stand up two Clients with AppName='advisor' and AppName='local-symphony', each call Save(creds), and assert they wrote to two different paths and neither can read the other's creds without explicit cross-app discovery. Confirms the isolation-by-construction claim of §4.1." \
  -p 1 -l testing --silent)
bd dep add $API_TEST_APPNAME_ISOLATION $API_CREDSTORE_APPNAME

# =========================================================================
# Phase 4 — PR3 docs: README, doc.go, example_test, CHANGELOG
# =========================================================================

DOCS_README=$(bd create "Expand README with Client quickstart, browser/device flow walkthroughs, Status section" \
  -d "Per §8.a and §10 PR3. Replace the PR1 README skeleton with sections: Install (go get), Quickstart (NewClient + HTTPClient + first request to chatgpt.com/backend-api/codex/responses), Login flows (browser vs device with the forceDevice fallback rationale from §3), Status (interpreting StatusInfo), AppName guidance (per §4.1), Security (passive JWT decode per §5, file permissions, ClientID-do-not-modify per §5). End with a link to the plan at docs/prompts/extract-codex-auth-go.md." \
  -p 1 -l docs --silent)
bd dep add $DOCS_README $API_STATUS

DOCS_DOC_GO=$(bd create "Add doc.go with package overview, threat model, passive-JWT-decode disclaimer" \
  -d "doc.go contains the package godoc. Sections: scope (what this module is and is not — cite §1), threat model (passive JWT consumer per §5; no signature/iss/aud/exp/nbf verification — list the implications), security posture (0600 file mode, 0700 dir mode, RFC 7009 revocation, no-redirect on revoke), opinions (60s skew, no 401-retry path, single-flight refresh). Keep terse and link to README for tutorials." \
  -p 1 -l docs --silent)
bd dep add $DOCS_DOC_GO $API_STATUS

DOCS_EXAMPLE=$(bd create "Add example_test.go with a compilable NewClient + HTTPClient usage snippet" \
  -d "ExampleNewClient demonstrates: NewClient(Options{AppName: \"yourapp\"}), Status to gate, Login(ctx, false) on first run, HTTPClient(ctx) returning the configured *http.Client, then a sample request to the Codex Responses endpoint that the example does NOT execute (use // Output: nothing). The point is to compile-verify the usage shape; go test catches drift if any signature changes." \
  -p 2 -l docs --silent)
bd dep add $DOCS_EXAMPLE $API_STATUS

DOCS_CHANGELOG=$(bd create "Add CHANGELOG.md in Keep-a-Changelog format, seed v0.1.0 unreleased entry" \
  -d "Per §11 open-question this format is the recommendation. Sections: Unreleased / v0.1.0. Bullets: 'extracted from advisor/internal/advisor/codexauth (verbatim where possible)', 'introduced Client/Options/AppName/Status', 'kept deprecated package-level functions as wrappers', 'CI Linux-only with cross-compile vet for darwin/windows'. The Unreleased section becomes v0.1.0 when PR4 tags." \
  -p 2 -l docs --silent)
bd dep add $DOCS_CHANGELOG $API_STATUS

# =========================================================================
# Phase 5 — PR4: Tag v0.1.0 (§8.d)
# =========================================================================

RELEASE_V010=$(bd create "Tag v0.1.0 on main" \
  -d "Per §8.d. No code in this PR — only the CHANGELOG.md frontmatter swap (Unreleased → v0.1.0 with date) and 'git tag -a v0.1.0 -m ...'. Gated on every PR3 bead landing. After tag, push to origin with --tags so consumers can 'go get github.com/mattsp1290/codex-auth-go@v0.1.0'." \
  -p 0 -l release --silent)
bd dep add $RELEASE_V010 $API_TEST_APPNAME_PARITY
bd dep add $RELEASE_V010 $API_TEST_APPNAME_ISOLATION
bd dep add $RELEASE_V010 $DOCS_README
bd dep add $RELEASE_V010 $DOCS_DOC_GO
bd dep add $RELEASE_V010 $DOCS_EXAMPLE
bd dep add $RELEASE_V010 $DOCS_CHANGELOG
bd dep add $RELEASE_V010 $BRANCH_CHECK
bd dep add $RELEASE_V010 $LICENSE_VERIFY

# =========================================================================
# Phase 6 — PR5: Advisor consumes v0.1.0 (§8.e, §8.f)
# Executes in ~/git/advisor, not in this repo.
# =========================================================================

ADV_GOMOD=$(bd create "advisor: add 'github.com/mattsp1290/codex-auth-go v0.1.0' to go.mod" \
  -d "Per §8.e. In ~/git/advisor, 'go get github.com/mattsp1290/codex-auth-go@v0.1.0'. Verify go.sum updates and 'go mod tidy' is a no-op afterwards." \
  -p 0 -l migration --silent)
bd dep add $ADV_GOMOD $RELEASE_V010

ADV_IMPORT_SWAP=$(bd create "advisor: rewrite internal/advisor/openai_codex.go import to github.com/mattsp1290/codex-auth-go" \
  -d "Per §8.e. Change ~/git/advisor/internal/advisor/openai_codex.go:12 (and any other call sites grep finds — likely factory.go and factory_seam.go also import the package) from 'github.com/mattsp1290/advisor/internal/advisor/codexauth' to 'github.com/mattsp1290/codex-auth-go'. The deprecated wrappers preserve every existing signature so no call-site rewrites are needed." \
  -p 0 -l migration --silent)
bd dep add $ADV_IMPORT_SWAP $ADV_GOMOD

ADV_DELETE_INTERNAL=$(bd create "advisor: delete internal/advisor/codexauth/ except chatmodel.go + chatmodel_test.go" \
  -d "Per §8.e and §8.f. 'rm -rf' every file in ~/git/advisor/internal/advisor/codexauth/ EXCEPT chatmodel.go and chatmodel_test.go. Leave the directory in place to hold those two files until §8.f re-homes them. Verify advisor still builds before committing." \
  -p 0 -l migration --silent)
bd dep add $ADV_DELETE_INTERNAL $ADV_IMPORT_SWAP

ADV_REHOME_CHATMODEL=$(bd create "advisor: move chatmodel.go + chatmodel_test.go to internal/advisor/codexchatmodel/" \
  -d "Per §8.f. Move the two remaining files from internal/advisor/codexauth/ into a new package internal/advisor/codexchatmodel/. Update the package declaration to 'codexchatmodel'. The file imports github.com/cloudwego/eino/components/model + github.com/cloudwego/eino/schema, which stay as-is. Update advisor's factory wiring to reference the new package path. Transitional home pending eino-providers/openaicodex module." \
  -p 1 -l migration --silent)
bd dep add $ADV_REHOME_CHATMODEL $ADV_DELETE_INTERNAL

ADV_TEST_SUITE_GREEN=$(bd create "advisor: 'go test ./...' green after the migration" \
  -d "Per §8.e gate. Run advisor's full test suite (including integration tests if any). Expectation: every test that passed before the migration passes after. Any new failure indicates a real regression in the deprecated-wrapper shim and must block PR5." \
  -p 0 -l migration --silent)
bd dep add $ADV_TEST_SUITE_GREEN $ADV_REHOME_CHATMODEL

ADV_PATH_BIT_FOR_BIT=$(bd create "advisor: verify on-disk auth.json path is bit-for-bit unchanged (no user re-login)" \
  -d "Per §4.1 promise and §8.e. On macOS verify '~/Library/Application Support/advisor/auth.json' still resolves. On Linux verify '$XDG_CONFIG_HOME/advisor/auth.json' (or '~/.config/advisor/auth.json'). On Windows verify '%AppData%\\advisor\\auth.json'. Smoke this by reading creds with the new wrapper and confirming no second Login dialog appears. If path differs in any byte, PR5 must not merge." \
  -p 0 -l migration --silent)
bd dep add $ADV_PATH_BIT_FOR_BIT $ADV_TEST_SUITE_GREEN

# =========================================================================
# Phase 7 — Smoke + Risk Mitigations (§9)
# =========================================================================

SMOKE_MAKE_TARGET=$(bd create "Add 'make smoke' target that hits the real Codex endpoint with a configured Client" \
  -d "Per §9 risk register (Codex endpoint contract drift). Add a Makefile (or scripts/smoke.sh) that runs a tiny program: NewClient + Login (skipped if Status.LoggedIn) + Generate one request via HTTPClient + assert non-401 response. Hand-run before tagging each release; not in CI. Document in CONTRIBUTING.md once that exists." \
  -p 2 -l testing --silent)
bd dep add $SMOKE_MAKE_TARGET $API_PUBLIC_WRAPPERS

TELEMETRY_INVALID_GRANT=$(bd create "Add structured log event when the three-branch invalid_grant recovery fires" \
  -d "Per §9 risk register (Token rotation behaviour change). At transport.go:140-174 emit a structured slog event (logger from Options) for each of the three branches (rotation race / wiped creds / true invalid_grant) — log level=warn, fields=branch_id, has_creds_after, error. Lets us see a server-side rotation behaviour change in production telemetry before users notice. Keep ALL log messages free of credential material per the otel_denylist guarantee." \
  -p 2 -l impl --silent)
bd dep add $TELEMETRY_INVALID_GRANT $API_PUBLIC_WRAPPERS

# =========================================================================
# Phase 8 — v0.2.0: Error classification + device prompt callback (§7, §8.g)
# Held until advisor has run on v0.1.0 for a release cycle.
# =========================================================================

V02_ERR_PROMOTE=$(bd create "Promote ErrAuthPlanNotIncluded + ErrAuthQuotaExceeded from advisor's factory_seam.go" \
  -d "Per §3 and §8.g. Move the two sentinels from ~/git/advisor/internal/advisor/factory_seam.go (lines 67 and 73) into codex-auth-go (rename to ErrPlanNotIncluded and ErrQuotaExceeded to drop the advisor-flavored 'Auth' prefix; this is a non-breaking ADDITION since they did not exist in the module before). Per §3 note: do NOT promote ErrAuthNotLoggedIn — advisor keeps that as its user-facing classification wrapper over codex-auth-go's ErrNotLoggedIn." \
  -p 2 -l future --silent)
bd dep add $V02_ERR_PROMOTE $ADV_PATH_BIT_FOR_BIT

V02_CLASSIFIER=$(bd create "Promote classifyCodexError helper from advisor's openai_codex.go:96" \
  -d "Per §8.g. Lift classifyCodexError (and rename to ClassifyCodexError as exported) from ~/git/advisor/internal/advisor/openai_codex.go:96 into codex-auth-go. The function maps Codex HTTP error responses to the v0.2.0 promoted sentinels. Add a dedicated classifier_test.go covering the plan-not-included + quota-exceeded + unknown branches." \
  -p 2 -l future --silent)
bd dep add $V02_CLASSIFIER $V02_ERR_PROMOTE

V02_DEVICE_PROMPT=$(bd create "Add device-prompt callback to Options so callers render user-code in their own UI" \
  -d "Per §7 and §11 open-questions. Today login_device.go:33 writes 'Open <Issuer>/codex/device and enter code: <user_code>' directly to os.Stderr. Add Options.DevicePrompt func(verificationURI, userCode string) error; default behaviour preserved when nil (stderr write). Update the Client.LoginDevice signature per §3 to take the prompt callback explicitly OR resolve it via Options — pick one in the v0.2.0 spike. Non-breaking if added via Options." \
  -p 2 -l future --silent)
bd dep add $V02_DEVICE_PROMPT $RELEASE_V010

V02_TAG=$(bd create "Tag v0.2.0 on main" \
  -d "Per §7. CHANGELOG bump; 'git tag -a v0.2.0 ...'. Held until V02_ERR_PROMOTE, V02_CLASSIFIER, and V02_DEVICE_PROMPT all close." \
  -p 2 -l release --silent)
bd dep add $V02_TAG $V02_ERR_PROMOTE
bd dep add $V02_TAG $V02_CLASSIFIER
bd dep add $V02_TAG $V02_DEVICE_PROMPT

# =========================================================================
# Phase 9 — v1.0.0: External-event-gated public-API ossification (§7)
# Both advisor AND local-symphony must have shipped on v0.1.0+ for a
# release cycle with no churn.
# =========================================================================

V1_EXTERNAL_GATE=$(bd create "Verify advisor + local-symphony have shipped on codex-auth-go for ≥1 release cycle each with no API churn" \
  -d "Per §7 v1.0.0 criterion. External-event-gated — this bead stays open until that condition is observably true. Check: (a) advisor main branch imports codex-auth-go at the current minor or newer for ≥1 month, (b) same for local-symphony, (c) no breaking-change PR opened against codex-auth-go in that window. If any condition fails, leave open and re-check next quarter." \
  -p 3 -l future --silent)
bd dep add $V1_EXTERNAL_GATE $V02_TAG

V1_TAG=$(bd create "Tag v1.0.0 — public API ossification line" \
  -d "Per §7. After v1.0.0, breaking changes to Client / Options / Credentials / StatusInfo / public error sentinels require a major bump. Internal helpers remain free to change. CHANGELOG entry calls out which API surface is now frozen." \
  -p 3 -l release --silent)
bd dep add $V1_TAG $V1_EXTERNAL_GATE

echo ""
echo "Bead graph created."
echo ""
echo "Inspect with:"
echo "  bd list                     # all beads"
echo "  bd ready                    # unblocked / ready-to-pick-up"
echo "  bd dep tree                 # dependency tree"
echo "  bd graph | dot -Tpng -o graph.png   # if graphviz installed"
echo ""
echo "Start work with:"
echo "  bd ready                    # see ready beads"
echo "  bd show <id>                # read full description"
