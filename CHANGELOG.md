# Changelog

All notable changes to obx-go are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/): until 1.0.0, a minor version may
change the Go API; the `obx` command line, its exit codes and the file formats
it reads stay compatible within a minor version.

## [0.1.0] - 2026-11-12

First release, for Open Brand Exchange specification 0.1.0.

### Added

- **Conformance for nodes without the Ingest or Node Admin API.** The OBX
  standard is the read side; how a node gets its data is its own business. A
  node loads `fixtures/kestrel` with its own import, and an operator does the
  few brand-side steps the protocol tests need: `--operator-hook <program>`,
  `--operator-prompt`, or `conformance.Config.Operator` in Go tests.
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
  what failed and why it matters), `--format markdown`, `--json` and
  `report` to show a saved JSON report; also runs the mock portal.
- **`conformance-action`**: the conformance check as a GitHub Action for
  portal repositories, with the report in the job summary.
- The mock portal's change log is incremental, so large test catalogs import
  in seconds (10,000 products, 60,000 EANs: `obx push` in about 1.4 s).
- `obx-interop` and `scripts/interop.sh`: the same automation against every
  portal edition, with a normalised comparison.
- Release tooling: GoReleaser archives for Linux, macOS and Windows (amd64,
  arm64), sha256 checksums, SPDX SBOMs, a multi-arch container image
  `ghcr.io/openbrandexchange/obx` and a Homebrew formula template.

### Also in this release

#### Changed

- `obx push --csv products.csv` also sends the `assets.csv` in the same folder when
  `--assets-csv` is not given, as a folder or package push already did. Before, the
  embargo dates, rights and AI labels in it silently fell back to the portal defaults.
- `obx status` labels a `--dry-run` upload as a dry run; it read like an upload that had
  changed the catalog.
- The mock node follows import rule 8 of the specification: an import no longer takes
  away the alt text, rights, embargo or AI label of an image it does not state them for.
  New conformance check I-IMP-5 tests this on every portal.
- The mock node follows import rule 9: a column that a products.csv does not have leaves that
  field of an existing product or variant as it is (texts per language). New conformance check
  I-IMP-6. `canonicalcsv.ProductsFile.Present` says which columns a file has.
- Portal download zips hold an `assets.csv` (spec import-format.md §2.3): the mock writes it,
  `canonicalcsv.WriteDownloadAssets` builds it, and conformance A-PORT-1 requires one row per image
  with the embargo and AI label. The mock's background download names its article data `data.csv`.
- The shared filename test vectors: `obx-conformance` and the mock read image names as
  the specification's `fixtures/expected/image-filenames.json` says.

[0.1.0]: https://github.com/openbrandexchange/obx-go/releases/tag/v0.1.0
