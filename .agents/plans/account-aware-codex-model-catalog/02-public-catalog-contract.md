# Public catalog contract and decoding

## Goal and prerequisite state

Expose the smallest picker-ready typed result and convert the private backend response into it without leaking unrelated wire fields.

Prerequisite: the dedicated authenticated route from [01-endpoint-and-authentication.md](01-endpoint-and-authentication.md) exists and has black-box route/header coverage.

## Repository and protocol evidence

- `public.go` exposes behavior through `Client` methods and exported sentinel or typed errors.
- `models.go` currently contains static `IsCodexAllowed` policy. The live account catalog is a separate authority and must not be filtered through this static helper.
- Pinned upstream `ModelInfo` uses optional description/default effort, open reasoning-effort strings, `visibility`, and signed integer `priority`.
- Pinned upstream manager uses ascending priority. Go's stable sort can preserve response order for equal priorities.
- Go's JSON decoder ignores unknown object fields by default but fills missing scalar fields with zero values. Private wire fields must retain presence information where absence is malformed.

## Proposed public API

Add these proposed exported symbols to new repository-root `catalog.go`:

```go
type ModelCatalogEntry struct {
    Slug                     string
    DisplayName              string
    Description              *string
    Priority                 int
    DefaultReasoningEffort   *string
    SupportedReasoningEfforts []ReasoningEffortOption
}

type ReasoningEffortOption struct {
    Effort      string
    Description string
}

type ModelCatalogHTTPError struct {
    StatusCode int
}

func (c *Client) ListModels(ctx context.Context, clientVersion string) ([]ModelCatalogEntry, error)
```

Names are proposed but should remain stable once implementation begins. If Go formatting or lint requires alignment changes, preserve semantics rather than whitespace.

### Public semantics

- `Slug` is the exact canonical `slug` supplied by the authenticated catalog.
- `DisplayName` is the picker label.
- `Description` is nil when the server omits or nulls `description`; a pointer to an empty string preserves an explicitly empty description.
- `Priority` is the server value and the returned slice is stable-sorted ascending by this field.
- `DefaultReasoningEffort` is nil when unreported. Otherwise it contains the exact non-empty wire string.
- `SupportedReasoningEfforts` preserves wire order and contains exact non-empty effort strings plus descriptions.
- `ModelCatalogHTTPError` exposes only `StatusCode`, implements `error`, and supports `errors.As`. Its text states the operation and HTTP status without including the response body, headers, URL credentials, or account details.
- `ListModels` makes one catalog request per call, plus any token-refresh request required by existing stale-credential behavior. It does not cache or return ETag metadata.

## Private wire model

In `catalog.go`, add private proposed wire types with JSON tags only for:

- top-level `models`;
- model `slug`, `display_name`, `description`, `default_reasoning_level`, `supported_reasoning_levels`, `visibility`, and `priority`;
- effort `effort` and `description`.

Use pointers for required scalars or slices when zero is a valid value or missing/null must be distinguished. The converter must reject:

- a missing or null top-level `models` property;
- missing, null, or empty `slug` and `display_name`;
- missing/null `visibility` or `priority`;
- missing/null `supported_reasoning_levels`;
- any option with a missing, null, or empty `effort` or a missing/null description;
- an explicitly present but empty `default_reasoning_level`; and
- trailing non-whitespace JSON after the response object.

An empty top-level models array and an empty supported-effort array are valid. Unknown model fields, unknown top-level fields, and unknown non-empty reasoning-effort strings are valid.

Do not expose or decode instruction text, context windows, capability flags, upgrade metadata, service tiers, or other upstream fields.

Add proposed internal constant `maxModelCatalogResponseBytes = 8 << 20`. This bounds decompressed response data because Go's default transport transparently decompresses gzip before `resp.Body` is read. Read through an `io.LimitReader` capped at the limit plus one byte, reject over-limit content with a fixed error, and do not include content or sizes derived from untrusted headers in the error. The 8 MiB cap leaves substantial room for ignored upstream instruction metadata while preventing unbounded allocation; revise it only with measured, sanitized legitimate-payload evidence.

## Filtering and ordering

Convert only entries whose exact wire visibility is `list`. Treat `hide`, `none`, or a future unknown visibility value as non-picker entries and omit them without error.

After validation and conversion, use a stable ascending priority sort. Equal-priority entries retain server order. Preserve each model's supported reasoning-effort order exactly; do not sort efforts alphabetically or by a local enum.

Do not check `supported_in_api` and do not call `IsCodexAllowed`. ChatGPT-account catalogs may expose picker-eligible models that are not API-key eligible, and the server catalog is the requested authority.

## Response lifecycle and errors

Add a proposed private response helper in `catalog.go`, such as `decodeModelCatalogResponse(*http.Response) ([]ModelCatalogEntry, error)`. It owns status classification, decoding, validation, filtering, sorting, and body closure. Unit-test it with a tracking `io.ReadCloser`; keep separate black-box `Client.ListModels` tests through the real `codexTransport` for routing and authentication.

1. Close every non-nil response body on every path.
2. Accept every 2xx status, not only 200, unless pinned/live evidence proves a stricter contract during implementation.
3. For non-2xx, return `*ModelCatalogHTTPError` based on status alone. Do not decode, log, quote, or wrap body content.
4. Read at most 8 MiB plus one detection byte after transport decompression, then decode one JSON object and require EOF apart from whitespace.
5. Wrap syntax/type/validation errors with a stable `codexauth: list models:` operation prefix. Do not reproduce the offending body or raw field value in the error.
6. Preserve `errors.Is` for caller context, `ErrNotLoggedIn`, `ErrInsecureEndpoint`, and safe `ErrRefreshFailed`; preserve `errors.As` to a sanitized allowlisted `AuthError` code when the refresh response was a valid recognized OAuth error. Do not unwrap a raw error whose `Error()` can reveal response content.
7. Return a newly allocated result. Do not retain the response buffer or expose private wire structs.

## Tests and observable acceptance

Add proposed `catalog_test.go` under the repository root with hermetic tests for:

- successful decoding of all public fields;
- nil versus explicitly empty optional descriptions;
- absent versus present default reasoning effort;
- stable ascending model priority including equal-priority response order;
- exact reasoning-option order;
- visibility filtering for `list`, `hide`, `none`, and an unknown future value;
- custom effort values such as `focused`;
- additive top-level, model, and effort fields;
- empty catalog and empty supported-effort list;
- malformed JSON, wrong JSON types, missing/null required fields, empty required strings, and trailing JSON;
- successful bodies just below the 8 MiB limit, uncompressed and transparently compressed bodies above it, and cancellation while reading;
- every representative non-2xx status class, verified through `errors.As` to `*ModelCatalogHTTPError` and exact `StatusCode`;
- an error-body canary and credential canaries absent from returned error strings and captured structured logs;
- OAuth JSON `error_description`, arbitrary OAuth-code, and non-JSON token-body canaries absent from returned errors and logs on stale-refresh failures, while allowlisted `AuthError.Code` or `ErrRefreshFailed` identity remains inspectable;
- response bodies closed on success, decode failure, and non-2xx paths;
- `ErrNotLoggedIn` preserved with an empty credential fixture;
- caller cancellation and deadline identity preserved;
- cancellation during detached stale refresh completes safe persistence, skips the catalog request, and eventually returns the caller cancellation identity;
- concurrent stale calls through the same `Client` issue one refresh and do not wipe valid credentials; and
- a catalog redirect is not followed and never delivers bearer credentials to its target.
- every failure after gate acquisition releases the gate, proven by a subsequent successful call on the same `Client`.

Add an external-package compile/use test in proposed `catalog_external_test.go` only if needed to prove the exported surface can be consumed without private types. Existing `example_test.go` may instead gain a compile-only `ExampleClient_ListModels` with no `Output:` directive.

## Compatibility requirements

- Do not rename, remove, or reinterpret existing exported values or methods.
- Keep `IsCodexAllowed` and its tests byte-for-byte behavior-compatible; the catalog contract does not replace it automatically.
- Keep package-level deprecated wrappers unchanged and do not add a package-level `ListModels` wrapper.
- Keep stored credentials and `Options` layout behavior unchanged.
- Keep default `HTTPClient` refresh behavior unchanged; catalog-specific refresh redaction is opt-in on its private transport.

## Dependencies, risks, and exclusions

- Depends on the authenticated models target in work package 01.
- Calls on one `Client` are serialized by design. Do not claim result coalescing or serialization across distinct clients/processes.
- Strict presence validation can reject backend payloads that the full Codex client tolerates through defaults. Validate only fields required to build the promised public result; do not require unrelated wire fields.
- Pointer-valued public optional fields distinguish absence from explicit empty strings but require callers to copy pointed-to values if they retain them independently. The implementation must allocate values owned by the result.
- Do not add an interface, pagination, cache policy type, visibility enum, or general schema version to the public surface.
