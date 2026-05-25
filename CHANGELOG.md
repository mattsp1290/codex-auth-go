# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.1.0] - Unreleased

### Added

- Extracted `codexauth` from `advisor/internal/advisor/codexauth`, preserving
  source behavior verbatim where possible.
- Introduced `Client`, `Options`, `Options.AppName`, and `Status` /
  `StatusInfo` for explicit per-consumer credential stores.
- Kept deprecated package-level `Login`, `LoginBrowser`, `LoginDevice`,
  `Logout`, and `HTTPClient` functions as advisor-compatible wrappers.
- Added Linux-only CI with build, race test, native vet, lint, govulncheck, and
  cross-compile vet for darwin and windows.
