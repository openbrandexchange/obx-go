# Implementation notes

Choices made in obx-go where the specification (v0.1.0-draft, commit in `specdata/SPEC_COMMIT`) is silent, ambiguous or contradicts itself. Each is the simplest reading; the ones marked **spec** should be settled in the spec repository.

## Protocol and ingest behaviour (mock node and conformance suite)

1. **spec: GTIN lookup echoes the trimmed input.** `fixtures/expected/gtin-lookup.json` expects `"input": "2000 200101031"` for the request value `" 2000 200101031"`, so `input` is the input with leading/trailing whitespace removed (inner spaces kept). protocol.md §7 only says inputs are trimmed before matching.
2. **spec: header problems have `row: null`.** import-format.md §1.1 says "header = row 0", but `ImportRowError.row` has `minimum: 1`. Problems of the header or of a whole file (unknown column, missing `style_number` column, unreadable image) are reported with `row: null` and the column name.
3. **spec: read-time omission makes batch replays differ from step-by-step replays.** Upserts of objects that are no longer visible when `/changes` is read are omitted (protocol.md §6.2). I-REPLAY-2 therefore compares the batch's entries with `expected-changes.json` minus the upserts of objects that are not visible after the whole batch; A-CAT-1 reads the feed after every step.
4. **spec: P-FEED-5 is checked by I-REPLAY-1.** The protocol test runs after the ingest replay and repeats its feed findings; without the ingest profile it is skipped.
5. **Development switch and link-local addresses.** With `OBX_ALLOW_PRIVATE_URLS=true` (mock: `AllowPrivateURLs`) loopback and private addresses and plain `http` are allowed, but link-local addresses (including cloud metadata `169.254.169.254` and `fe80::/10`) are always refused. This lets P-HOOK-1 test delivery to a local receiver and still check SSRF protection. A-SRC-1 checks `https://127.0.0.1/…` only when the node refused a loopback webhook in P-HOOK-1 (strict mode); it always checks the metadata address.
6. **Re-importing the same original keeps its renditions.** On a node without an image library, an import (or PUT) that sends only `files.original` with the sha256 the asset already has keeps the stored web/square/thumb and answers `unchanged` (import-format.md §4.6: an unchanged image costs no change entry). A *new* original without renditions keeps serving the previous files until renditions for the new original arrive, as for method 1 in ingest.md §4.1.
7. **Asset external id without a colour** (an `assets.csv` row with a product and an empty `color_code`): `{style_number}_{position}`.
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

18. **spec: extra commands.** docs/automation.md lists `push`, `watch`, `status` and `info`. obx-go adds `login`/`logout` (store an upload key per portal in the user's config directory, file mode 0600) and `pull` (retailers mirror a catalog through the protocol). `obx login` can also sign in with the admin e-mail and password and create an upload key (scopes read, write, delete) through the Node Admin API, so a brand never has to copy a key by hand.
19. `obx push <folder>` treats a folder like an unpacked package: `products.csv` (or the only CSV in the folder), optional `assets.csv`, and every image below it. Two images with the same file name: the first wins and the second is reported.
20. **CLI texts are English.** The admin UI and e-mails are translated into 25 languages; the CLI is a technical tool used from scripts and CI, and its messages avoid protocol jargon ("upload key", "portal", "EAN") instead. Translating it is an open question.
21. `obx push` sends images as an `images_manifest` of blobs it uploaded (HEAD first, then PUT or resumable upload), never inside the import request, so unchanged images cost nothing. Rendition hashes are cached in the user cache directory; open multipart uploads are remembered there and resumed.

## The conformance command

22. **spec: flags.** docs/conformance.md shows `--second-client …`; obx-conformance uses `--second-client-id/--second-client-secret` and `--no-original-client-id/--no-original-client-secret`, accepts `--base` as an alias of `--url`, and can create every credential itself from `--admin-user/--admin-password` or `--setup-code`.
23. Without admin credentials, tests that need the admin API (revoking a client in P-AUTH-2, approving in P-ACC-1, P-FEED-6, the portal profile) are skipped, not failed. `--rate-limit-test` provokes a 429 (P-RATE-1); otherwise only the headers are checked. Every other raw request honours a 429 with its Retry-After (up to 5 times), so a whole `all` run passes against a node with a lowered limit (node-go: `OBX_TEST_RATE_LIMIT=1000`).
24. The suite assumes a node that holds exactly the Kestrel fixtures (plus what the suite itself creates and removes). The ingest tests clean up the brands and products they create; objects created by the portal tests (test retailers, users, keys) stay.
