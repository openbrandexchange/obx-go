# Changelog

All notable changes to obx-go are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/): until 1.0.0, a minor version may
change the Go API; the `obx` command line, its exit codes and the file formats
it reads stay compatible within a minor version.

## [Unreleased]

## [0.1.0] - unreleased

First release, for Open Brand Exchange specification 0.1.0.

### Added

- **`obx` command** for brands: `login` (paste an access key, or sign in and
  let obx create one), `push` (products.csv, assets.csv and images, from a
  folder, separate files or one zip), `watch` (publish whatever lands in a
  folder), `status`, `info`, `config`, `logout`, `completion`
  (bash, zsh, fish, PowerShell), and `pull` for retailers (keep a local copy
  of a brand's catalog and images, every file checked against its sha256).
- `obx push` uploads only new or changed images, in parts that resume after
  an interruption, makes web, square and thumbnail versions for portals that
  need them, and has `--dry-run` (uploads and changes nothing), `--mirror` with
  a guard against mass unpublishing, `--json` and exit codes 0 / 2 / 1.
- Help for every command, messages in 25 languages (bg cs da de el en es et
  fi fr ga hr hu it lt lv mt nb nl pl pt ro sk sl sv) with the project's
  glossary, plain-language errors and progress bars.
- **GitHub Action** `openbrandexchange/obx-go/push-action` for brands, with
  checksum-verified download of obx and a job summary.
- **Go SDK**: `obx` (types, error codes, GTIN helpers), `client` (OBX
  Protocol: OAuth2, change feed with cursor store, products, GTIN lookup,
  verified downloads, exports, webhooks, access requests, usage), `ingest`
  (Ingest API: writes with ETags, batches, blob dedupe, resumable uploads,
  imports, jobs), `canonicalcsv`, `renditions` (pure-Go image versions),
  `webhook` (signatures), `nodeadmin` (Node Admin + Portal API) and `mock`
  (in-memory portal with the Kestrel fixtures and edge-case knobs).
- **`obx-conformance`**: protocol, ingest, portal and security profiles
  against any portal, with a report for people (verdict, summary per area,
  what failed and why it matters), `--format markdown` and `--json`; also runs
  the mock portal.
- `obx-interop` and `scripts/interop.sh`: the same automation against every
  portal edition, with a normalised comparison.
- Release tooling: GoReleaser archives for Linux, macOS and Windows (amd64,
  arm64), sha256 checksums, SPDX SBOMs, a multi-arch container image
  `ghcr.io/openbrandexchange/obx` and a Homebrew formula template.

[Unreleased]: https://github.com/openbrandexchange/obx-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/openbrandexchange/obx-go/releases/tag/v0.1.0
