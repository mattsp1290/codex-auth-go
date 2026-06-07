# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.3.0] - 2026-06-06

### Added

- Added `Options.Endpoint` to override the Responses-API URL the transport
  rewrites every request to. Empty preserves the default
  (`https://chatgpt.com/backend-api/codex/responses`). A loopback-only
  cleartext `http://` safety rail prevents bearer tokens from being sent in
  plaintext to non-loopback hosts; `https://` to any host is allowed.
  New exported sentinel: `ErrInsecureEndpoint`.

  **Security note:** the safety rail guards only cleartext. An `https://`
  override sends the real bearer token to whatever host is configured, with no
  allowlist or certificate pinning. This is a deliberate design decision.

- Added `Options.CredentialPath` to override the `auth.json` location used by
  a `Client`. Empty preserves the default
  (`os.UserConfigDir()/<AppName>/auth.json`). Useful for staging fixture
  credentials in tests without touching `HOME`/`XDG`.

  **Warning on write-side effects:** on any write (real refresh, `Save`,
  `Logout`) the **parent directory** of `CredentialPath` is `MkdirAll`'d AND
  its permissions are unconditionally overwritten to `0700` — even if the
  directory already existed. Never point `CredentialPath` at a file inside a
  shared directory (`/tmp`, `$HOME`): the first write will re-permission that
  directory. Use a dedicated directory (e.g. `t.TempDir()` in tests).

### Notes

- Additive and non-breaking. With both fields empty, behavior is byte-identical
  to v0.2.0 (verified against the existing transport and public test assertions:
  host, scheme, path, `Authorization`, `originator`, `session_id`, query
  preservation).

## [0.2.0] - 2026-05-26

### Added

- Added `ClassifyCodexError` to map Codex API plan and quota error codes to
  exported sentinels.
- Added `Options.DevicePrompt` so callers can render device-flow user-code
  prompts in their own UI.

## [0.1.1] - 2026-05-26

### Added

- Extracted `codexauth` from `advisor/internal/advisor/codexauth`, preserving
  source behavior verbatim where possible.
- Introduced `Client`, `Options`, `Options.AppName`, and `Status` /
  `StatusInfo` for explicit per-consumer credential stores.
- Kept deprecated package-level `Login`, `LoginBrowser`, `LoginDevice`,
  `Logout`, and `HTTPClient` functions as advisor-compatible wrappers.
- Added Linux-only CI with build, race test, native vet, lint, govulncheck, and
  cross-compile vet for darwin and windows.
