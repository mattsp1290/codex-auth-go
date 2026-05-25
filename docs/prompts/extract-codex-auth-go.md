# Extract `codex-auth-go` from `advisor/internal/advisor/codexauth/`

Planning document for lifting the in-tree `codexauth` package from
`~/git/advisor` into a standalone Go module at
`github.com/mattsp1290/codex-auth-go`. This is a plan, not a scaffold.

Facts in this document were verified against
`/Users/punk1290/git/advisor/internal/advisor/codexauth/` on 2026-05-25
(40 files). When this document disagrees with the in-tree source, the
in-tree source wins.

---

## 1. Goals and non-goals

### Goals

- Publish a focused, eino-free Go module that lets a Go process
  authenticate against an OpenAI ChatGPT Plus/Pro/Teams subscription
  and obtain an `*http.Client` that calls
  `https://chatgpt.com/backend-api/codex/responses` with a refreshing
  bearer token.
- Lift the package verbatim where possible. The design (single-flight
  refresh, three-branch `invalid_grant` recovery, flock-gated atomic
  credstore, case-preserved `originator`/`session_id` headers) is
  hard-won per ADR-0008 and not up for renegotiation in the lift.
- Provide a stable, minimal public API surface that both
  `~/git/advisor` and `~/git/local-symphony` can consume.
- Parameterise the `"advisor"` string leaking into the on-disk config
  directory name so the module is reusable.
- Preserve full test parity. Every `_test.go` except the eino-coupled
  `chatmodel_test.go` ships with the lift.

### Non-goals

- No eino ChatModel wrapper. `chatmodel.go` and `chatmodel_test.go`
  import `github.com/cloudwego/eino/components/model` and
  `github.com/cloudwego/eino/schema` (`chatmodel.go:16-17`). They move
  to a separate `eino-providers/openaicodex` module per research
  doc 05 Option B; transitionally they live in advisor's own tree (§8.f).
- No provider-classification logic (`classifyCodexError` at
  `openai_codex.go:96`) in v0.1.0; deferred to v0.2.0 (§8.g).
- No CLI, daemon, JSON-RPC server, or credential broker. Library only.
- No JWT signature verification. `jwt.go:ExtractAccountID` is a
  passive claims consumer today; the lift preserves that.

---

## 2. Module layout: file disposition

Forty source files live in
`~/git/advisor/internal/advisor/codexauth/`. The disposition is below;
"ship" means copy into the new module unchanged, "ship with edits"
means small touch-ups described in later sections, "stay" means leave
behind in `advisor`.

Non-test files:

- `authorize.go` — ship.
- `browser.go` — ship.
- `browser_darwin.go` — ship.
- `browser_linux.go` — ship.
- `browser_windows.go` — ship.
- `browser_other.go` — ship.
- `callback.go` — ship with edits. The error string at line 33
  hard-codes the substring `"is another advisor instance running?"`;
  rephrase to drop `advisor`.
- `chatmodel.go` — stay. Imports `cloudwego/eino`; goes to
  `eino-providers/openaicodex` eventually, lives in `advisor` in the
  meantime (§8.f).
- `constants.go` — ship with edits. The `Originator` constant value
  `"advisor"` (line 29) is a header-value decision flagged in §4.
- `credstore.go` — ship with edits. The literal `"advisor"` directory
  name at line 60 becomes `Options.AppName` (§4).
- `exchange.go` — ship.
- `jwt.go` — ship.
- `login_browser.go` — ship.
- `login_device.go` — ship.
- `models.go` — ship. `IsCodexAllowed` is callable from both
  consumers; the version regex and explicit allow-list have nothing
  advisor-specific in them.
- `pkce.go` — ship.
- `public.go` — ship with edits. Re-shaped per §3 (new `Client` type,
  preserved package-level functions as deprecated wrappers).
- `token_http.go` — ship. Owns `AuthError`.
- `transport.go` — ship with edits. The `advisorVersion` var
  (line 19) and the literal `"advisor/"+advisorVersion` User-Agent
  prefix (line 266) are parameterised via `Options.AppName` and a
  per-module `Version` var (§4).

Test files:

- `authorize_test.go` — ship.
- `browser_test.go` — ship.
- `browser_linux_test.go` — ship; runs on the Linux CI runner (§6).
- `callback_test.go` — ship.
- `chatmodel_test.go` — stay. Pairs with `chatmodel.go`.
- `credstore_flock_test.go` — ship.
- `credstore_path_darwin_test.go` — ship; compiles under
  `GOOS=darwin go vet` only — not executed in CI (§6 tradeoff).
- `credstore_path_linux_test.go` — ship; runs on the Linux CI runner.
- `credstore_path_windows_test.go` — ship; compiles under
  `GOOS=windows go vet` only — not executed in CI (§6 tradeoff).
- `credstore_test.go` — ship.
- `exchange_test.go` — ship.
- `jwt_test.go` — ship.
- `login_browser_test.go` — ship.
- `login_device_test.go` — ship.
- `models_test.go` — ship. Verified to have no advisor-specific
  imports.
- `otel_denylist_test.go` — ship. Verified to have no
  advisor-specific imports. Critical guardrail that no credential
  string ends up in an OTel attribute.
- `pkce_test.go` — ship.
- `public_test.go` — ship. Verified to have no advisor-specific
  imports.
- `token_http_test.go` — ship.
- `transport_singleflight_test.go` — ship.
- `transport_test.go` — ship.

No file in the source package imports any `github.com/mattsp1290/...`
path; this was verified by `grep`. The lift's import-path rewrite is
therefore confined to the `package codexauth` declarations and any
internal symbol cross-references; there is no advisor-internal package
to detangle.

External dependencies that come with the package:
`github.com/google/uuid` (transport.go), `github.com/gofrs/flock`
(credstore.go), `golang.org/x/sync/singleflight` (transport.go), plus
`golang.org/x/sys` indirectly via flock. Shipping tests also pull in
OpenTelemetry packages via `otel_denylist_test.go`:
`go.opentelemetry.io/otel`, `go.opentelemetry.io/otel/sdk/trace`, and
`go.opentelemetry.io/otel/sdk/trace/tracetest`.

---

## 3. Public API surface

The lifted module exposes a `Client` type so that two consumers (or two
goroutines in one consumer) can hold credentials with different
`AppName`s without colliding on a package-level singleton. Today
`codexauth` has no `Client` type — every operation is a package-level
function backed by package-level state (`pathFunc`, `mu`, the global
flock).

Implementation requirement: do not implement `Client` by temporarily
mutating package globals such as `pathFunc`. A `Client` owns a
credstore/path resolver and its own in-process mutex; client methods
must call that store directly. Cross-process safety remains the
per-file flock. Package-level compatibility functions may use one
legacy singleton client, but two explicit clients with different
`AppName`s must be race-free when used concurrently.

Proposed types and signatures (descriptive only; bodies omitted):

- `type Options struct { AppName string; Logger *slog.Logger }`
- `func NewClient(opts Options) *Client`
- `func (c *Client) Login(ctx context.Context, forceDevice bool) (Credentials, error)`
- `func (c *Client) LoginBrowser(ctx context.Context) (Credentials, error)`
- `func (c *Client) LoginDevice(ctx context.Context) (Credentials, error)`
- `func (c *Client) Logout(ctx context.Context) error`
- `func (c *Client) HTTPClient(ctx context.Context) (*http.Client, error)`
- `func (c *Client) Status(ctx context.Context) (StatusInfo, error)` — new in v0.1.0
- `type StatusInfo struct { LoggedIn bool; ExpiresAt time.Time; AccountID string; ConfigPath string }`

`Options` deliberately does not expose `CallbackPort` in v0.1.0.
OpenAI's registered redirect URI is locked to
`http://localhost:1455/auth/callback` per `constants.go:22`, and the
current source hard-codes the same redirect URI into authorize and
exchange calls. A non-1455 listener is therefore a test seam, not a
production escape hatch for users who already have a process bound to
1455. Keep any alternate-port support unexported until there is a
verified OAuth path that accepts a different redirect URI.

`Options.Logger` defaults to `slog.Default()` when nil. The package
must never log credential values; the existing `Credentials.LogValue`
implementation in `exchange.go:33-40` already redacts them and that
guarantee must be preserved. Logger use is for warnings and structured
diagnostic events only. User-facing device-flow instructions continue
to go to stderr in v0.1.0 through the existing writer seam; a real
prompt callback remains a v0.2.0 decision.

`Status` is local-only. It reads the configured credstore and returns
`LoggedIn=true` when an OpenAI entry with a refresh token exists, even
if the access token is expired. It must not perform network refreshes
or delete credentials. `ExpiresAt` reflects the stored access-token
expiry; callers that need a fresh token should call `HTTPClient` and
let the transport refresh on demand.

The package today returns these exported error types and sentinels:
`ErrNotLoggedIn` (`public.go:17`), `*AuthError` (`token_http.go:40`),
`*DeviceError` (`login_device.go:43`), `*PortInUseError`
(`callback.go:27`), and `ErrNoBrowser` (`browser.go:14`). All five
ship unchanged. `ErrNotLoggedIn` keeps its current message
`"codexauth: not logged in"` to avoid breaking any caller that matched
on the string.

Other exported v0.1.0 symbols from the lifted package must be
accounted for explicitly before tagging. The literal lift currently
exports constants plus helpers/types including `BuildAuthorizeURL`,
`StartCallbackServer`, `ErrStateMismatch`,
`ErrAuthorizationDenied`, `Credentials`, `AuthFile`, `Path`, `Load`,
`Save`, `Delete`, `ExchangeCode`, `RefreshToken`,
`ExtractAccountID`, `GenerateVerifier`, `ChallengeFromVerifier`, and
`GenerateState`. PR3 may deprecate or document package-level storage
helpers, but it must not accidentally remove a symbol that PR2
exported without a deliberate API decision.

### Backward-compat shim

The plan retains every existing package-level function as a deprecated
thin wrapper over `NewClient(Options{AppName: "advisor"})`. This
legacy default is intentionally different from
`NewClient(Options{})`, which resolves to `"codex"` (§4.1). The
`"advisor"` wrapper default is what lets existing advisor
installations keep finding `.../advisor/auth.json` after the import
swap. This is a deliberate advisor-first deviation from the older Eino
shared-repo notes that sketched `"codex"` as the wrapper default. The
wrappers must preserve their current signatures verbatim:

- `func Login(ctx context.Context, forceDevice bool) (Credentials, error)`
- `func LoginBrowser(ctx context.Context) (Credentials, error)`
- `func LoginDevice(ctx context.Context) (Credentials, error)`
- `func Logout(ctx context.Context) error`
- `func HTTPClient(_ context.Context) (*http.Client, error)`

In particular, `LoginBrowser` and `LoginDevice` return
`(Credentials, error)` today (`login_browser.go:39`,
`login_device.go:91-ish`) and the wrappers must keep that signature —
not `error` alone.

### Promotion of advisor-side error types

The Eino shared-repo notes place two sentinels and one helper in the
shared auth/provider extraction scope:

- `ErrAuthPlanNotIncluded` and `ErrAuthQuotaExceeded` from
  `~/git/advisor/internal/advisor/factory_seam.go:67,73`.
- `classifyCodexError` from `~/git/advisor/internal/advisor/openai_codex.go:96`.

Decision: keep them out of v0.1.0 only if the interim provider story
is explicit. Until v0.2.0, advisor keeps its local classification
helper and any future `eino-providers/openaicodex` module must either
duplicate the mapping locally or knowingly ship without shared
`errors.Is` classification for `usage_not_included` and
`insufficient_quota`. If `openaicodex` is developed before v0.2.0,
reconsider promoting these in v0.1.0 instead of creating throwaway
duplication.

A third sentinel, `ErrAuthNotLoggedIn` at `factory_seam.go:61`, also
exists in advisor today. The plan is to NOT promote it: advisor uses
it as the user-facing classification wrapper that wraps the raw
`codexauth.ErrNotLoggedIn` (see `openai_codex.go:37-41` where both
errors are joined). Two equivalent sentinels in the same module would
just confuse callers. The module keeps a single `ErrNotLoggedIn`
sentinel and advisor keeps its own classification wrapper. This is
recorded as a deliberate decision so a future implementer does not
"helpfully" promote it too.

---

## 4. The `"advisor"` literal lives in four places

A single grep verified that the literal `"advisor"` (or `advisorVersion`)
appears in four production sites in the source package; the plan
addresses each independently rather than treating them as one decision.

### 4.1 Config directory name (`credstore.go:60`)

`pathFunc` returns `filepath.Join(dir, "advisor", "auth.json")`. This
controls whether credentials live under
`~/Library/Application Support/advisor/auth.json` or
`.../codex/auth.json`.

Decision: parameterise via `Options.AppName`. Empty `AppName` defaults
to `"codex"` and emits a single warn-level log via `Options.Logger`
(panic is too rude; silent degrade is invisible). The deprecated
package-level wrappers must default `AppName` to `"advisor"` (NOT
`"codex"`) so existing advisor installations keep finding their
`auth.json` at the same path bit-for-bit. New consumers calling
`NewClient(Options{})` get `"codex"`. Both defaults coexist; only the
deprecated wrappers freeze the legacy value.

### 4.2 `Originator` header (`constants.go:29`)

`Originator = "advisor"` is sent as the `originator` HTTP header on
every Codex request (`transport.go:265`). Two things are true at
once:

- The header value is observed by OpenAI's edge and may matter to
  their abuse-detection or routing logic. Changing it for advisor's
  existing users could quietly break them.
- Hardcoding `"advisor"` in a module that is no longer the advisor
  package is a smell.

Decision: keep `Originator` as a package-level constant for v0.1.0
and leave the value at `"advisor"`. This is verbatim-preservation of
on-the-wire behaviour — any change is in scope for v0.2.0 or later
once we can compare wire captures between the two consumers. Document
in the README that the header value is the preserved
advisor/codexauth wire value for v0.1.0 and that consumers MUST NOT
change it unless they have a reason and a wire trace to back it. (This
is the same logic as §5 applies to `ClientID`.) The constant stays
exported so a v0.2.0 may move it to `Options` if needed.

### 4.3 User-Agent prefix (`transport.go:19`, `transport.go:266`)

`advisorVersion` and the literal `"advisor/"+advisorVersion+" ..."`
build a User-Agent of the form `advisor/0.0.0-dev (darwin arm64)`.

Decision: rename the variable to `Version` (still package-level,
still settable via `-ldflags -X`), keep the default value
`"0.0.0-dev"`, and build the User-Agent as
`AppName+"/"+Version+" ("+GOOS+" "+GOARCH+")"` using the
`Client`-resolved `AppName`. The deprecated wrappers continue to emit
the `advisor/<version>` shape because their `AppName` is `"advisor"`
(per §4.1). New consumers see `codex/0.0.0-dev (darwin arm64)` by
default and can override either field.

### 4.4 Callback error text (`callback.go:33`)

`PortInUseError.Error` currently includes the phrase
`"is another advisor instance running?"`.

Decision: rephrase to drop `advisor`, for example
`"is another instance already running?"`. This is user-facing text
only and does not participate in credential path compatibility or wire
compatibility.

---

## 5. The OpenAI OAuth client ID is non-negotiable

`constants.go:13` defines
`ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"`. This is OpenAI's public
client identifier for the Codex CLI OAuth application. It is what
unlocks the `chatgpt.com/backend-api/codex/responses` endpoint for a
ChatGPT subscription. There is no "your own client ID" path; OpenAI
has not exposed a way to register a third-party app against the Codex
subscription audience.

Decision: do NOT parameterise `ClientID`. Keep it as an exported
constant so callers can read it (useful for debugging and logs) but
not override it. The package README must say plainly: this is the
Codex CLI client ID, every consumer of the ChatGPT subscription
endpoint must use it, and changing it will break authentication. A
similar note belongs at the top of `constants.go`, where one already
exists (`"All constants are verbatim from the registered OpenAI OAuth
application. Do not modify them"`); preserve and strengthen it.

---

## 6. Test parity strategy

Every shipping test file (twenty of them, listed in §2) must compile
and pass under the new module path. The plan:

- The new module's import path is `github.com/mattsp1290/codex-auth-go`.
  Every test currently in `package codexauth` continues to live in
  `package codexauth` after the lift; this is a black-box-only-where-
  needed convention. No test file in the source package imports any
  `github.com/mattsp1290/...` path (verified by grep), so the only
  edit required to most test files is package-declaration consistency
  and any `// +build` constraints.
- OS-specific files in the source:
  - `browser_linux.go` and its companion `browser_linux_test.go`.
  - `browser_darwin.go`, `browser_windows.go`, `browser_other.go`.
  - `credstore_path_darwin_test.go`, `credstore_path_linux_test.go`,
    `credstore_path_windows_test.go`.

  GitHub Actions runs **Linux-only** (`ubuntu-latest`). The matrix is
  intentionally narrow: per-project decision to keep CI cost and
  surface small, and the file resolved by `os.UserConfigDir()` is
  exercised by the path-resolver tests under a fake-env seam rather
  than a real runner. To keep the per-OS source files (the `_darwin`,
  `_windows`, and `_other` build-constrained variants) from
  bit-rotting silently, the same Linux job runs `GOOS=darwin go vet
  ./...` and `GOOS=windows go vet ./...` cross-compile steps after
  the native build. These steps cost ~10s of runner time and
  guarantee the package still compiles on every supported OS even
  though no native test runs.

  Known tradeoff: `credstore_path_darwin_test.go` and
  `credstore_path_windows_test.go` execute only their compiled bodies
  under the cross-compile vet — they are not *run*. The Linux-tagged
  `browser_linux_test.go` and `credstore_path_linux_test.go` do run on
  the Linux runner. The in-process behaviour of the darwin/windows
  credstore path resolver is therefore unverified in CI. The
  `_test.go` files still ship with the module so a contributor on a
  native machine (or a future CI matrix expansion) picks them up
  immediately.
- The `otel_denylist_test.go` guardrail is the most important non-
  obvious test to preserve. It enforces that no credential string ever
  lands in an OpenTelemetry attribute; that invariant is what makes
  the package safe to embed in observability-rich applications. The
  v0.1.0 CI must run it.
- The `transport_singleflight_test.go` race-and-coalescing test
  ships unchanged. Run `go test -race` in the CI matrix so the test
  actually exercises the singleflight ordering it claims to.

No substantial test rewiring is anticipated beyond import paths, but
PR1 must add the OpenTelemetry test dependencies needed by
`otel_denylist_test.go`.

---

## 7. Versioning and release plan

- **v0.1.0**: lifted code with all twenty shipping test files
  passing. `Options.AppName` parameter added with `"codex"` default
  for `NewClient` and `"advisor"` for the deprecated wrappers.
  Deprecated package-level wrappers in place. `Status()` shipped per
  §3 because it is small and provides immediate value to
  local-symphony. No promotion of advisor's error classification
  helpers yet.
- **v0.2.0**: promote `ErrAuthPlanNotIncluded`,
  `ErrAuthQuotaExceeded`, and the `classifyCodexError` helper from
  advisor. Optionally accept a device-code prompt callback in
  `Options` so callers can render the user-code in their own UI
  instead of stderr (today `login_device.go` defines `deviceStderr`
  near line 33 and writes the device-code prompt around line 120).
- **v1.0.0**: tag only after at least one month of advisor AND
  local-symphony both running the relevant shared modules in their
  main branches with no API churn. For local-symphony, `codex-auth-go`
  alone is only sufficient for login/status plumbing; using Codex as a
  model backend also requires the future Eino provider work called out
  in §8.h. v1.0.0 is the public-API-ossification line; before it,
  breaking changes are allowed by semver convention but should be
  rare.
- Branch convention: `main`. The existing
  `/Users/punk1290/git/codex-auth-go/.git` repo (already present per
  `ls`) needs verification that its default branch is `main` and not
  `master`; flag for the implementer to check on PR1.
- License: the repo already ships with an MIT `LICENSE` file dated
  2026 Matt Spurlin (`/Users/punk1290/git/codex-auth-go/LICENSE`).
  This decision is therefore CLOSED; there is no Phase 0 TODO for it.
  Advisor itself does not yet ship a LICENSE in the same place; the
  fact that the new module's MIT covers only the new repo is worth
  noting in the README but does not block the lift.

---

## 8. Advisor migration plan

A staged sequence. Each step is committable on its own and the next
step does not break until it lands.

### 8.a Scaffold the new module

In `~/git/codex-auth-go/` (already exists with a LICENSE, a README,
and an empty `.gitignore`):

- `go mod init github.com/mattsp1290/codex-auth-go`.
- Add `.golangci.yml` mirroring advisor's lint config (errcheck,
  govet, ineffassign, gofmt, gocyclo at a sane threshold).
- Add `.github/workflows/ci.yml` with a Linux-only `ubuntu-latest`
  job per §6: `go build ./...`, `go test -race ./...`, `go vet ./...`,
  `golangci-lint run`, `govulncheck ./...`, plus the
  `GOOS=darwin go vet ./...` and `GOOS=windows go vet ./...`
  cross-compile steps that keep the per-OS source files honest.
- Add the runtime dependencies from §2 and the OpenTelemetry test
  dependencies required by `otel_denylist_test.go`.
- Expand the README to a usage example using `Client`, plus the
  non-negotiables from §5.

### 8.b Lift the codexauth source verbatim

Copy every file listed under "ship" in §2 into the module root
(flat package; no internal subdirs needed yet). Do NOT copy
`chatmodel.go` or `chatmodel_test.go`. All twenty shipping `_test.go`
files come along. Verify `go test ./...` passes on macOS locally
before pushing the branch.

### 8.c Introduce `Options.AppName` and the `Client` type

Refactor the package-level functions to thin wrappers per §3. Touch
`credstore.go`, `transport.go`, `callback.go`, and `constants.go` per
§4. The `credstore.go` work is a real per-client store refactor, not a
temporary override of `pathFunc`. Keep the public sentinels exported
and explicitly decide the status of every other exported symbol listed
in §3 before tagging. Tests should compile unchanged because they
call the package-level wrappers; if any test calls the new `Client`
methods, it does so in a new test file added in this commit. Add the
new `Status` and `StatusInfo` types here, with the local-only semantics
from §3.

### 8.d Tag v0.1.0

After 8.c lands, tag `v0.1.0` on `main` in
`github.com/mattsp1290/codex-auth-go`.

### 8.e Wire advisor to consume v0.1.0

In `~/git/advisor`:

- Add `github.com/mattsp1290/codex-auth-go v0.1.0` to `go.mod`.
- Delete the entire `~/git/advisor/internal/advisor/codexauth/`
  directory EXCEPT `chatmodel.go` and `chatmodel_test.go` (see §8.f).
- Update every advisor import of
  `internal/advisor/codexauth` to
  `github.com/mattsp1290/codex-auth-go`; this includes CLI
  login/logout code, model listing/config paths, factory tests,
  OpenAI Codex tests, and integration smoke tests, not only
  `openai_codex.go`.
- Fix the existing advisor OAuth-mode provider gate in the same PR or
  a prerequisite PR: `AuthModeOAuth` must be able to build the OpenAI
  provider without requiring `OPENAI_API_KEY`. `AvailableModels`
  already advertises the OAuth path, but the factory path still
  consults `keys.KeyFor("openai")`; the migration is not complete
  until provider construction matches the advertised auth mode.
- Because the new module's deprecated package-level wrappers default
  `AppName` to `"advisor"` (§4.1), the on-disk path
  `...Application Support/advisor/auth.json` is preserved bit-for-bit.
  No user re-login is required.
- Run advisor's full test suite and update any golden/test
  expectations affected by the package move or callback error wording.

### 8.f Re-home chatmodel.go transitionally

Move `chatmodel.go` and `chatmodel_test.go` to a new advisor-owned
package — proposed location
`~/git/advisor/internal/advisor/codexchatmodel/`. The file imports
`cloudwego/eino` and is the only file in the original package that
does; it cannot live in `codex-auth-go` per §1's non-goal. This is a
transitional home pending the existence of
`eino-providers/openaicodex` as a third module.

### 8.g v0.2.0 lift of classification helpers

Once advisor has run on `codex-auth-go v0.1.0` for a release cycle,
lift `ErrAuthPlanNotIncluded`, `ErrAuthQuotaExceeded`, and
`classifyCodexError` from `factory_seam.go` and `openai_codex.go`
into the module, tag v0.2.0, and update advisor to import them from
there. Leave `ErrAuthNotLoggedIn` in advisor per §3's note.

### 8.h Future local-symphony / Eino provider work

`codex-auth-go` is an auth module, not an Eino provider. That is
enough for local-symphony to check login status or obtain an
authenticated `*http.Client`, but it is not enough to replace
local-symphony's current Ollama backend. Today local-symphony's agent
graph requires a `model.ToolCallingChatModel`, while advisor's
`chatmodel.go` only returns `model.BaseChatModel` and explicitly does
not implement tool calling.

The later `eino-providers/openaicodex` module must therefore absorb
more than the raw `chatmodel.go` experiment. It needs the injected
client/provider adapter behavior currently represented in advisor's
`openai_codex.go`, a decision on tool-calling support, local-symphony
configuration shape, health-check semantics, fallback behavior, and
snapshot/status integration. Until that module exists, do not treat
local-symphony as covered by the v0.1.0 `codex-auth-go` extraction
except for optional auth/status plumbing.

---

## 9. Risk register

- **Codex endpoint contract drift.** The chatgpt.com endpoints are
  not a stable public API. Mitigation: a `make smoke` target the
  implementer runs by hand against a real subscription before tagging
  any release. CI itself runs only unit and contract tests.
- **Token rotation behaviour change.** Today OpenAI rotates refresh
  tokens on every `/oauth/token` call; the three-branch
  `invalid_grant` recovery at `transport.go:140-174` is designed
  around that. If OpenAI changes rotation, the "another process
  refreshed" branch will misclassify and may thrash or wipe good
  credentials. Mitigation: log a structured event whenever the
  three-branch logic fires so a behaviour change shows up in
  production telemetry before users notice.
- **CI runs Linux-only by design.** The Windows credstore path
  resolution (`%AppData%`) and the darwin path resolution
  (`~/Library/Application Support/...`) are validated by
  `GOOS=darwin go vet` / `GOOS=windows go vet` cross-compile steps
  and by path-resolver unit tests under a fake-env seam, not by a
  real runner. Risk: a bug only reproducible at runtime on a native
  darwin or windows host slips through. Mitigation: ship the
  `_darwin_test.go` and `_windows_test.go` files so any contributor
  on a native machine catches the regression locally, and revisit
  whether to add native runners once the module has ≥1 non-advisor
  consumer reporting a real production issue.
- **Public-API ossification.** Once anyone outside the org pins
  `v0.1.0`, breaking changes hurt. Hence the deliberately small §3
  surface; additions before v1.0.0 go through a one-week review.
- **Accidental API churn from PR2 to PR3.** A literal lift exports
  low-level helpers beyond the preferred `Client` surface. Mitigation:
  inventory the exported symbols in PR2 and explicitly mark each as
  supported, deprecated, or intentionally removed before the v0.1.0
  tag.
- **`Originator` header re-use.** Two products sending
  `originator: advisor` may confuse OpenAI-side incident response.
  Accepted for v0.1.0 to avoid on-the-wire change; revisit in v0.2.0
  per §4.2.
- **local-symphony expectation mismatch.** `codex-auth-go` does not
  provide an Eino `ToolCallingChatModel`, so it cannot by itself run
  local-symphony's current agent graph. Mitigation: keep v0.1.0 scoped
  to auth/status and track Codex-as-model support in the future
  `eino-providers/openaicodex` plan (§8.h).
- **Multi-process credstore contention.** The flock at
  `credstore.go:78-103` has not been stress-tested with two distinct
  consumer apps writing to the same `auth.json`. Separate `AppName`s
  avoid this by construction — each consumer writes to its own file —
  which is one motivation for parameterising the directory name in
  §4.1.

---

## 10. First-PR breakdown

The first five PRs in chronological order. Each one is independently
mergeable; the human reviewer should be able to read each diff in
under ten minutes.

- **PR1 — skeleton.** `go mod init github.com/mattsp1290/codex-auth-go`,
  flesh out the existing README, add `.golangci.yml`, add the
  Linux-only GitHub Actions workflow per §6 (ubuntu-latest with
  `go test -race`, lint, `govulncheck`, plus `GOOS=darwin/windows
  go vet` cross-compile steps). No `codexauth` source yet. Verify
  the existing repo's default branch is `main` not `master` (§7);
  fix if necessary.
- **PR2 — lift codexauth source.** Verbatim copy of every file marked
  "ship" in §2 (no `chatmodel.go`, no `chatmodel_test.go`).
  All twenty shipping `_test.go` files compile under the new module
  path; the ones that build on Linux (every test that is not under a
  `_darwin` or `_windows` build tag) pass on the Linux runner. The
  cross-compile vet steps confirm `_darwin_test.go` and
  `_windows_test.go` still compile under their build constraints.
  No API changes yet — this PR is a literal lift so a reviewer can
  confirm that nothing about the code changed except the directory.
- **PR3 — introduce Options.AppName, Client, and Status.** Implements
  §3 and §4 in one PR. The deprecated package-level functions are
  preserved and routed through `NewClient`, with `AppName` defaults
  set to `"advisor"` in the wrappers and `"codex"` in
  `NewClient(Options{})`. The implementation owns per-client
  credstore state and does not mutate `pathFunc` around calls. Adds a
  small test file demonstrating both code paths plus concurrent
  clients with different `AppName`s. README updated with the usage
  example.
- **PR4 — tag v0.1.0.** A no-code PR that creates the
  `v0.1.0` tag on `main` from the tip of PR3. The PR itself can be a
  CHANGELOG.md commit if a CHANGELOG is added at this point.
- **PR5 — advisor consumes v0.1.0.** A single PR in
  `~/git/advisor` that performs §8.e and §8.f. Diff is mostly a
  repo-wide import-path swap, a directory move, and the OAuth-mode
  provider-gate fix; the regression surface is small because the
  deprecated wrappers preserve every existing credential-path
  behaviour bit-for-bit.

---

## 11. Open questions for human review

- **Repo visibility.** Should
  `github.com/mattsp1290/codex-auth-go` be public from day 1, or
  private during PR1-PR3 and made public at v0.1.0 tag? Both are
  fine; the answer affects whether external eyes can race us on
  copying the OAuth flow before we tag. Recommendation: public from
  day 1 since OpenAI's Codex CLI is already open-source and there is
  no IP advantage to private.
- **CHANGELOG format.** Keep-a-Changelog? Conventional Commits and
  let `git-cliff` generate it? Decision punted to PR4.
- **`Options.Logger` semantics.** Should `nil` mean "no logging" or
  "use `slog.Default()`"? Recommendation in §3 is the latter; flag
  here so the human reviewer can object before code locks it in.
- **Device-code prompt callback timing.** §3 keeps stderr prompts in
  v0.1.0 and defers a callback to v0.2.0. If local-symphony needs to
  render device login in its own UI earlier, pull that callback into
  PR3.
- **Should `Originator` ever become an `Options` field?** §4.2
  leaves it constant for v0.1.0; this is the cleanest answer but the
  inverse (parameterise it now, default to `"codex"`) would prevent
  the §9 risk about two products both reporting as `advisor`. The
  human reviewer should choose.
- **Should error classification move in v0.1.0?** §3 allows deferral
  only if no shared `openaicodex` provider lands before v0.2.0. If
  provider work starts earlier, promote `ErrAuthPlanNotIncluded`,
  `ErrAuthQuotaExceeded`, and `classifyCodexError` immediately.
- **local-symphony scope.** Is local-symphony's near-term use only
  auth/status, or should Codex become a model backend? The latter is
  not solved by this module and belongs in `eino-providers/openaicodex`.
- **Should the package publish a CLI alongside the library?** Out of
  scope for §1 by design, but worth a note: `advisor login` and
  `advisor logout` already exist; a thin `codex-auth` CLI in the
  `cmd/` subtree of this module would let local-symphony reuse the
  exact same login UX without re-implementing it. Recommendation:
  consider in v0.2.0 once we know what local-symphony actually
  needs.
- **GitHub release artifacts.** Pure-library Go modules typically
  don't ship binaries. If a CLI is added (previous bullet), the
  release workflow becomes a separate decision.

---

The plan lives at
`/Users/punk1290/git/codex-auth-go/docs/prompts/extract-codex-auth-go.md`;
the next concrete step is PR1 (skeleton) in §10.
