# Changelog

All notable changes to obx-go are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/): until 1.0.0, a minor version may
change the Go API; the `obx` command line, its exit codes and the file formats
it reads stay compatible within a minor version.

## [Unreleased]

For Open Brand Exchange specification 0.3.0: materials and care as terms with
names (spec proposal 0004, [spec PR #3](https://github.com/openbrandexchange/spec/pull/3)).
**Breaking** (allowed before 1.0): the JSON of `materials` and `care` changes,
and only 0.3 is supported.

### Changed (breaking)

- **`obx.MaterialPart.Part`, `obx.Fiber.Material` and `obx.Fiber.Qualifier`
  are terms** (`obx.Term{Code, Name}`, `Name` a `LocalizedText` or `nil`).
  `Fiber.Fiber`, `Fiber.Recycled` and `Fiber.Organic` are gone: a fibre has
  one qualifier (`organic`, `recycled`, `regenerative` or another code),
  `nil` when it has none. JSON: `{"part": {"code", "name"}, "fibers":
  [{"material": {"code", "name"}, "percentage", "qualifier": {...} | null}]}`.
- **`obx.CareInstruction`** has `Category` (ISO 3758 group: `washing`,
  `bleaching`, `drying`, `ironing`, `professional_care`), `Symbol` (GINETEX
  notation such as `1_C_40`) and `Name`; `Text` is the brand's own extra text.
- `obx.NormalizeFiber` turns the 0.1 keys `recycled_<fibre>` /
  `organic_<fibre>` into the material with that qualifier (the key wins over a
  qualifier already set).
- `obx.Version` is `0.3` and `obx.SpecVersion` `0.3.0`; the embedded schemas
  are the 0.3 schemas at `/schemas/v0.3/`. The spec withdrew 0.1 and 0.2, so
  the frozen 0.1 schemas are no longer embedded: `specdata.SchemaBaseV01` and
  `Validator.Schema("v0.1/...")` are removed.
- `canonicalcsv` materials grammar: `<pct>% <material> [(<qualifier>)]`,
  any qualifier code (`(regenerative)`), one per fibre (two are an error);
  `(recycled)`, `(organic)` and the 0.1 keys keep working.

### Added

- **Vocabularies**: `specdata.Vocabulary()` returns the spec's
  `vocabularies/*.json` (parts, materials, qualifiers, care codes with names in
  en/nl/de/fr, and for care category and symbol) as an `obx.Vocabulary`;
  `obx.ParseVocabulary` reads another copy. `Materials.WithNames` and
  `Care.WithNames` fill names (and care category/symbol) for known codes and
  keep what was written for others, without empty strings.
- The mock portal stores and returns the vocabulary's names, categories and
  symbols for known codes (on every write path: Ingest API, imports, admin);
  writes may leave them out, so PUTting back a GET is still `unchanged`.
- **Conformance** P-CAT-6 checks the terms: no material code starting with
  `recycled_`/`organic_`, no 0.2 fibre properties, every known part, material
  and qualifier has a name (P-CAT-5 pins the vocabulary's names for the Kestrel
  products), the part `main` has the name `null`, care codes of the vocabulary
  have exactly its category and symbol and a name, and no name has an empty
  string. P-CAT-1 and P-CAT-5 match the 0.3 expected fixtures.
- **Conformance P-FEED-7** (spec protocol.md §6.4: a node that upgrades
  0.2 → 0.3 answers every earlier cursor with `410 cursor_expired`): takes a
  live cursor, asks the operator for the new optional action
  `simulate_spec_upgrade` (`conformance.ActSimulateSpecUpgrade`, result
  `simulated: true`), then checks `410 cursor_expired` for the old cursor and a
  working `/changes` without one. Skipped when the operator does not offer the
  action (`conformance.ErrActionNotSupported`, or no `simulated: true`), without
  an operator and in the online check. `mock.Node.SimulateSpecUpgrade` bumps
  the mock's cursor epoch; `TestMockPassesAll` covers it.
- `scripts/sync-spec.sh` copies `vocabularies/` too.

## [0.2.1] - 2026-11-12

An online compatibility check, the Excel import tests and fixes. Works with
Open Brand Exchange specification 0.2.0; nothing a portal or client has to do
changes.

### Added

- **Online check**: `obx-conformance check` runs the read-only part of the
  protocol profile against a portal on the internet (safe dialer, https only,
  request and byte budget, secrets redacted); `--stream` prints one JSON line
  per finished test. `obx-conformance check-server` serves it over HTTP
  (`POST /check`) for the compatibility checker on openbrandexchange.com;
  `deploy/checker` holds its Cloudflare deployment.
- `obx-conformance mock --read-only --client id:secret`: a public read-only
  demo portal with a fixed API key (`mock.Options.ReadOnly`,
  `FullClientID`/`FullClientSecret`).
- `conformance.Config.Progress` and `Result.OnlineTier`.
- **Conformance I-IMP-9 and I-IMP-10** (ingest profile, optional): the Excel
  import of the spec's `import-format.md` §5 with the workbooks of
  `fixtures/imports/xlsx/` (now vendored in `specdata`). I-IMP-9 sends each
  converting workbook as `products.xlsx` / `assets.xlsx` in a dry run and
  checks the error rows (Excel row numbers, `detail` locators) and the
  conversion warnings; the two templates must import all unchanged. I-IMP-10
  checks that each workbook to refuse fails the run with one ImportRowError
  (`row` null) of the expected `workbook_*` code and writes nothing. Both are
  skipped on a node that answers a workbook with `unsupported_media_type`.
- `obx.ErrWorkbookInvalid`, `ErrWorkbookUnsafe`, `ErrWorkbookSheetMissing`,
  `ErrWorkbookTooLarge`; `ingest.XLSXContentType`.

### Changed

- The safe dialer also refuses 0/8, 198.18/15, 240/4, NAT64 (64:ff9b::/96)
  and 6to4 (2002::/16).
- `ingest.EncodeImport` sends a `csv` / `assets_csv` part whose name ends in
  `.xlsx` with the xlsx content type.
- `canonicalcsv` normalises header names as the spec's §1.1 now says: trimmed
  of space, tab, CR and LF, each run of white space and `-` one `_`
  (`Style  -  Number` = `style_number`).
- The mock node answers spreadsheet files (`.xlsx`, `.xls`, `.ods`, …) with
  `415 unsupported_media_type`, so it skips I-IMP-9 and I-IMP-10.
- After a full conformant result, the `obx-conformance` report adds one calm
  line on how to have it confirmed for free and where partner listings are
  described; an online check that passes says that the other checks need a
  local run.

### Fixed

- The mock node compares `If-None-Match` weakly (RFC 9110 §13.1.2): behind a
  proxy that compresses responses, a client that sends back `W/"…"` gets
  `304 Not Modified` instead of the whole response again.

## [0.2.0] - 2026-11-12

For Open Brand Exchange specification 0.2.0: product data for fashion, shoes,
bags and luggage (spec proposal 0002). Additive: every new field is optional,
and clients and portals written for 0.1 keep working.

### Added

- **Article data in the `obx` types**: product `ItemSubtype`,
  `ContainsAnimalOrigin`, `FootwearMaterials`, `Features` and
  `Certifications`; variant `Measurements` and `SizeLength`; `Color.Family`;
  fibre flags `Fiber.Recycled` and `Fiber.Organic`. Protocol objects always
  carry the new properties (`null` when not stated); inside `measurements` and
  `features` only stated members are written, and the fibre flags only when
  true. `Features` has typed fields for the registry keys and keeps every
  other key as sent (`Features.Other`); `Features.Validate` checks the registry.
- `obx.NormalizeFiber` and `Materials.Normalized` turn the 0.1 fibre keys
  `recycled_<fibre>` and `organic_<fibre>` into the fibre with its flag.
- **Label completeness warnings** with stable codes
  (`fibre_composition_missing`, `animal_origin_unknown`,
  `footwear_materials_missing`, `fibre_percentages_not_100`):
  `obx.LabelWarnings`, `IngestResult.WarningDetails`. The mock portal reports
  them on product writes and per product in imports; they never block
  publishing. `obx push` lists them in the user's language.
- **`canonicalcsv`** reads and writes the 0.2 columns: `item_subtype`,
  `contains_animal_origin`, `footwear_materials`, `certifications` (with
  `(material)` / `(product)`), `feature:<key>`, `color_family`, `size_length`
  and the measurement columns (decimal comma accepted; `strap_length_cm` as
  `min-max` or one number), and the materials flags `(recycled)` /
  `(organic)`. Exports have a column for every registry feature.
  `Present.Features` says which feature columns a file has.
- The mock portal stores and serves all new fields, normalises 0.1 fibre
  keys on every write, stores an empty `features` or `measurements` object as
  `null`, and applies import rule 9 per feature key and per measurement column.
- **Conformance**: P-CAT-1 checks the 0.2 article data of KO-1001 and KO-1005;
  new P-CAT-5 (article data round trip of KO-1008, KO-1009, KO-1010, KT-2001),
  P-CAT-6 (article data shape over the whole snapshot), I-IMP-7 (the new CSV
  columns, `fixtures/imports`) and I-IMP-8 (label completeness warnings).

### Changed

- `obx.Version` is `0.2` and `obx.SpecVersion` `0.2.0`; the embedded schemas
  are the 0.2 schemas at `/schemas/v0.2/`, and the frozen 0.1 schemas are
  embedded too (`specdata.SchemaBaseV01`, `Validator.Schema("v0.1/Product")`).
- The Kestrel fixtures have 11 products, 46 variants and 35 images; the
  conformance counts follow `expected/counts.json`.
- `canonicalcsv.ParseMaterials` no longer warns when percentages do not sum
  to 100: the portal reports that as the label warning
  `fibre_percentages_not_100` for the product as stored.
- P-DISC-1 notes an `obx_version` other than `0.2` instead of failing; the
  specification does not ask for a version there.
- I-IMP-3 speaks of the nine Kestrel products; I-IMP-4 uses EANs that are not
  in the fixtures (one of them now belongs to KO-1009).

### Fixed

- The mock portal no longer panics while describing a schema `pattern`
  violation (for example a `javascript:` web address); it answers
  `422 validation_failed` with the field.

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

[Unreleased]: https://github.com/openbrandexchange/obx-go/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/openbrandexchange/obx-go/releases/tag/v0.2.1
[0.2.0]: https://github.com/openbrandexchange/obx-go/releases/tag/v0.2.0
[0.1.0]: https://github.com/openbrandexchange/obx-go/releases/tag/v0.1.0
