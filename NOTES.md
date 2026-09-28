# Implementation notes

Choices made in obx-go where the specification (v0.1.0-draft, commit in `specdata/SPEC_COMMIT`) is silent, ambiguous or contradicts itself. Each is the simplest reading; the ones marked **spec** should be settled in the spec repository.

## Protocol and ingest behaviour (mock node and conformance suite)

1. **spec: GTIN lookup echoes the trimmed input.** `fixtures/expected/gtin-lookup.json` expects `"input": "2000 200101031"` for the request value `" 2000 200101031"`, so `input` is the input with leading/trailing whitespace removed (inner spaces kept). protocol.md §7 only says inputs are trimmed before matching.
2. **spec: header problems have `row: null`.** import-format.md §1.1 says "header = row 0", but `ImportRowError.row` has `minimum: 1`. Problems of the header or of a whole file (unknown column, missing `style_number` column, unreadable image) are reported with `row: null` and the column name.
3. **spec: read-time omission makes batch replays differ from step-by-step replays.** Upserts of objects that are no longer visible when `/changes` is read are omitted (protocol.md §6.2). I-REPLAY-2 therefore compares the batch's entries with `expected-changes.json` minus the upserts of objects that are not visible after the whole batch; A-CAT-1 reads the feed after every step.
4. **spec: P-FEED-5 is checked by I-REPLAY-1.** The protocol test runs after the ingest replay and repeats its feed findings; without the ingest profile it is skipped.
5. **Development switch and link-local addresses.** With `OBX_ALLOW_PRIVATE_URLS=true` (mock: `AllowPrivateURLs`) loopback and private addresses and plain `http` are allowed, but link-local addresses (including cloud metadata `169.254.169.254` and `fe80::/10`) are always refused. This lets P-HOOK-1 test delivery to a local receiver and still check SSRF protection. The mock checks the resolved address again on every connection it makes (`internal/safedial`: a dialer `Control` function, proxies ignored), so the checked address is the one connected to and DNS rebinding cannot reach a refused address; every node follows the same rule. A-SRC-1 checks `https://127.0.0.1/…` only when the node refused a loopback webhook in P-HOOK-1 (strict mode); it always checks the metadata address.
6. **Re-importing the same original keeps its renditions.** On a node without an image library, an import (or PUT) that sends only `files.original` with the sha256 the asset already has keeps the stored web/square/thumb and answers `unchanged` (import-format.md §4.6: an unchanged image costs no change entry). A *new* original without renditions keeps serving the previous files until renditions for the new original arrive, as for method 1 in ingest.md §4.1.
7. **Images of every colour: `{style}_ALL_{position}`.** The colour part `ALL` (any case) of an image name is reserved and means "no colour" (`color_code: null`, the image belongs to every colour), for example `KO-1001_ALL_4_detail.jpg`. The asset external id of such an image, and of an `assets.csv` row with a product and an empty `color_code`, is `{style_number}_ALL_{position}`, the same as the file name, as in the admin UI, node-ts and node-php.
8. **Variant external id without GTIN**: `{style}-{color}-{size}` with `/` in the size replaced by `-` (external ids may not contain `/`), or `{style}-{color}` without a size.
9. **Mirror scope**: "more than half" is counted against the *published* products of the brands that appear in the file.
10. **Import job status**: an import that cannot run at all (unreadable package, zip slip, refused mass unpublish) finishes as a `failed` job whose `import_run.error` carries the code (`import_invalid`, `mass_unpublish_refused`); it also appears as a file-level ImportRowError. The conformance suite accepts either place.
11. **Rotated upload keys**: the old secret is unknown afterwards and gets `401 invalid_token`; a revoked key gets `401 access_revoked`. The conformance suite accepts any 401 after rotation.
12. **Download statistics**: API rendition downloads, exports and usage events are counted per retailer; admin previews in the portal and anonymous signed-URL downloads are not.
13. **Exports** are built synchronously by the mock (the first status read is `succeeded`); zip entries are stored without compression (images are already compressed).
14. **Signed URLs** (mock): `?expires=<unix>&sig=<hmac(GET, path, expires)>`, valid one hour, not bound to a client.

## Renditions (`renditions` package)

15. Pixels are treated as sRGB; embedded ICC profiles are ignored (derived renditions only; originals are never touched).
16. The JPEG EXIF orientation of the original is applied to web/square/thumb so they display upright.
17. `thumb` is scaled from `web` (identical to scaling the original for every image ≤ 2000 px; slightly faster for bigger ones). Quality: web/square 86, thumb 82.

## The `obx` CLI

18. **spec: extra commands.** docs/automation.md lists `push`, `watch`, `status` and `info`. obx-go adds `login`/`logout` (store an access key per portal in the user's config directory, file mode 0600), `pull` (retailers mirror a catalog through the protocol), `config` (where settings live) and `completion` (bash, zsh, fish, PowerShell). `obx login` can also sign in with the admin e-mail and password and create an access key (scopes read, write, delete) through the Node Admin API, so a brand never has to copy a key by hand.
19. `obx push <folder>` treats a folder like an unpacked package: `products.csv` (or the only CSV in the folder), optional `assets.csv`, and every image below it. Two images with the same file name: the first wins and the second is reported.
20. **CLI texts in 25 languages.** Every message of `obx` is registered in Go with an id and its English text (`cmd/obx/*.go`, `register(...)`) and translated in `cmd/obx/locales/<lang>.json` for the admin UI's languages. The language comes from `OBX_LANG`, then `LC_ALL`/`LC_MESSAGES`/`LANG`/`LANGUAGE`, then (Windows) the display language, else English. Wording follows the glossary: "access key" under "Automatic upload → Access keys" (the admin UI's words, not "upload key" or "token"), "API key for your software", "EAN"; texts avoid plural grammar ("images: 3"). Tests fail when a language misses a message, changes a `{placeholder}`, drops a command, option or `EAN`, or uses a protocol word. The 21 languages other than nl/de/fr/en are machine-quality and need a native review (Irish and Maltese first), as for the admin UI. Problems reported by the portal keep the portal's (English) detail after obx's translated reason. `obx-conformance` stays English: it is for people building a portal.
21. **`obx push --dry-run` uploads nothing.** It hashes every image, asks the portal which it already has (`HEAD /blobs`), and sends a dry-run import whose manifest names every image. A portal cannot know the bytes it was not sent, so `blob_missing`/`files_missing` problems for exactly those images are dropped and reported as "would upload". **spec:** the mock treats a dry-run manifest entry whose blob is missing as present (placeholder, removed after the run), so its counts equal a real run; nodes that report `blob_missing` instead still work with obx but count those images' assets as not created. ingest.md §5 should say which a dry run does.
22. **Where obx keeps things:** settings in `os.UserConfigDir()/obx/config.json` (Linux `~/.config/obx`, macOS `~/Library/Application Support/obx`, Windows `%AppData%\obx`), cache (rendition hashes, open uploads, watch state) in `os.UserCacheDir()/obx`; `OBX_CONFIG_DIR` puts both in one folder (the container image uses `/home/obx/.obx`). `obx config` shows them.
23. `obx push` sends images as an `images_manifest` of blobs it uploaded (HEAD first, then PUT or resumable upload), never inside the import request, so unchanged images cost nothing. Rendition hashes are cached in the user cache directory; open multipart uploads are remembered there and resumed.

## The conformance command

24. **spec: flags.** docs/conformance.md shows `--second-client …`; obx-conformance takes `--second-client id:secret` (also `--client`, `--no-original-client`) as well as the separate `--second-client-id/--second-client-secret` and `--no-original-client-id/--no-original-client-secret`, accepts `--base` as an alias of `--url`, and can create every credential itself from `--admin-user/--admin-password` or `--setup-code`.
25. Without admin credentials, tests that need the admin API (revoking a client in P-AUTH-2, approving in P-ACC-1, P-FEED-6, the portal profile) are skipped, not failed. `--rate-limit-test` provokes a 429 (P-RATE-1); otherwise only the headers are checked. Every other raw request honours a 429 with its Retry-After (up to 5 times), so a whole `all` run passes against a node with a lowered limit (node-go: `OBX_TEST_RATE_LIMIT=1000`).
26. The suite assumes a node that holds exactly the Kestrel fixtures (plus what the suite itself creates and removes). The ingest tests clean up the brands and products they create; objects created by the portal tests (test retailers, users, keys) stay.
27. **Report for people.** The text report opens with the verdict, a summary per area in plain words ("Reading by retailers' software", "Automatic upload", "Admin and retailer screens", "Security"), then what failed with why it matters, optional checks not met, skipped checks, and every check. `--format markdown` gives a table for issues and CI step summaries; `--json` (or `--format json`) the machine-readable report. A run with `--only` is marked `partial` and never says CONFORMANT.

## Performance

28. **The mock's change log is incremental.** A write marks the records it stored or removed (and the variants and assets of a changed product); a commit compares only those. Brand changes, settings changes, a restored dry run and rights that may have expired still compare every record. Before this, every write compared the whole catalog, so importing 2,000 products took 4 minutes; now 0.2 s. `TestIncrementalCommitEqualsFullCommit` proves that both ways write the same log.
29. **Load test** (`OBX_LOAD=1 go test -run TestLoad -v ./cmd/obx/`): 10,000 products with 60,000 variants (each with an EAN) against the mock, measured on an Apple M-series laptop under load, 2026-09-28:

    | step | time |
    |---|---|
    | `obx push` (first, 60,000 rows) | 1.4 s |
    | `obx push` again (nothing changed) | 1.1 s |
    | `obx pull` snapshot (70,000 objects, one JSON file each) | 18 s |
    | `obx pull` again (no changes) | 2 ms |

    `obx pull` time is almost all file system work (one file per object, written atomically); the CLI side of `push` is CSV parsing and planning (60,000 rows in about 0.1 s). Image uploads are bounded by the network: only new or changed images are sent, `--parallel` of them at a time. The rendition cache is written behind (at most every 2 s), not per image. The node side (catalog browse, `/changes` paging, EAN lookup at this size) is measured in each portal edition's own load test.
