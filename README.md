# obx-go

Tools for [Open Brand Exchange](https://openbrandexchange.com), the free, open way for **brands** to deliver their final product content (images with role, view, order, rights and AI provenance, plus article data per size with EAN, materials and care) to their retailers.

| | For whom | What it does |
|---|---|---|
| **`obx`** command | brands | publish a spreadsheet and a folder of images to your portal, once or automatically (cron, GitHub Actions, a watched folder); in 25 languages |
| **[`push-action`](push-action/README.md)** | brands | the same as a GitHub Action |
| **`obx pull`** | retailers and developers | keep a local copy of a brand's catalog and images up to date |
| **Go SDK** | developers | packages for the OBX Protocol (read side), the Ingest API (write side), the canonical CSV format and image versions |
| **`obx-conformance`** | people building a portal | checks a portal against the specification, with a report anyone can read; also runs an in-memory **mock portal** |
| **[`conformance-action`](conformance-action/action.yml)** | portal developers | the conformance check as a GitHub Action, report in the job summary |

It works with every edition of the portal (Portal for Cloudflare, Portal for Google Cloud & Docker, Portal for web hosting) and with any other conforming implementation.

## Quickstart for brands

1. **Install** `obx`, one of:
   - download the file for your computer from the [releases page](https://github.com/openbrandexchange/obx-go/releases), unpack it and put `obx` (Windows: `obx.exe`) somewhere on your PATH; check it against `checksums.txt`;
   - Homebrew (macOS, Linux): `brew install openbrandexchange/tap/obx`;
   - Docker: `docker run --rm ghcr.io/openbrandexchange/obx:v0 help`;
   - with Go 1.26+: `go install github.com/openbrandexchange/obx-go/cmd/obx@latest`.
2. **Connect** this computer to your portal. Either paste an access key (in your portal: *Automatic upload → Access keys → New access key*), or press Enter and sign in with your admin e-mail and password; `obx` then creates a key for you:
   ```sh
   obx login https://brands.kestrel.example
   ```
3. **Check** a folder with `products.csv` (one row per size and colour) and images named `{style}_{colour}_{position}.jpg` without changing anything, then **publish** it:
   ```sh
   obx push ./export --dry-run
   obx push ./export
   ```
   `obx` checks every image, uploads only what is new or changed, makes the web and thumbnail versions when your portal needs that, and shows a summary with any rows that need attention. Running it again costs nothing when nothing changed.

`obx` speaks your computer's language (or set `OBX_LANG=nl`, `de`, `fr`, …). `obx help` lists every command; `obx <command> --help` explains one with examples.

The file format is the [canonical import format](https://github.com/openbrandexchange/spec/blob/main/docs/import-format.md): the same CSV and image names work in the portal's upload screen, in scheduled imports and in `obx`.

```
export/
  products.csv        style_number, name:nl, name:en, gtin (EAN), color_code, size, materials, care, …
  assets.csv          optional: rights, AI information or alt text per image
  images/
    KO-1001_NVY_1_front.jpg
    KO-1001_NVY_2_back.jpg
    KO-1001_ALL_4_detail.jpg     an image of every colour
    KO-1003_RST_3_model.png
```

## Commands

```
obx login <portal>          connect this computer (stores the access key, readable only by you)
obx info                    check the connection: brands, languages, limits
obx push [<folder>] [--csv products.csv] [--assets-csv assets.csv] [--images <folder|zip>] [--package export.zip]
         [--mirror] [--allow-mass-unpublish] [--dry-run] [--language nl] [--no-wait] [--json]
obx watch <folder> [--interval 5s] [--settle 10s] [--once]
obx status [--job <id>]     recent uploads and their problems
obx pull <portal> --out <folder> [--renditions web,thumb] [--follow]
obx config [path]           where obx keeps its settings on this computer
obx completion <bash|zsh|fish|powershell>
obx logout | version | help [<command>]
```

- `--dry-run` uploads and changes nothing: you see the counts, the problems and how many images a real run would upload.
- `--mirror` also takes offline the products of the brands in the file that the file no longer contains. It refuses to take more than half offline unless you add `--allow-mass-unpublish`, so a broken export never empties your catalog.
- Large images are uploaded in parts; an interrupted push (Ctrl-C, a lost connection) continues with the missing parts on the next run.
- Exit codes: `0` everything worked, `2` done, but some rows or images had problems (they are listed), `1` failed.

Everything about the command line, including where settings live on Linux, macOS and Windows, shell completion and the environment variables: **[docs/cli.md](docs/cli.md)**.

## Automating it

| You have… | Do this |
|---|---|
| a folder where your PIM/ERP/DAM drops exports | `obx watch /data/outbox` as a service: [systemd](examples/systemd/obx-watch.service), [Docker Compose](examples/docker-compose.yml) |
| a server that runs nightly jobs | one [cron line](examples/cron.txt) (or Windows Task Scheduler) running `obx push` |
| exports in a Git repository | the [GitHub Action](push-action/README.md) |
| n8n, Make or Zapier | plain HTTP steps, see the spec's [automation recipes](https://github.com/openbrandexchange/spec/blob/main/docs/automation.md) |

All commands read `OBX_URL` (your portal address) and `OBX_TOKEN` (the access key) from the environment, so the same line works in cron, CI and containers. Details, logs and restarts: **[docs/automation.md](docs/automation.md)**.

```yaml
# .github/workflows/publish.yml
- uses: openbrandexchange/obx-go/push-action@v0
  with:
    url: ${{ secrets.OBX_URL }}
    token: ${{ secrets.OBX_TOKEN }}
    folder: export
```

## For retailers: `obx pull`

With an API key for your software from the brand (client id and secret):

```sh
OBX_CLIENT_ID=obxc_… OBX_CLIENT_SECRET=obxs_… obx pull https://brands.kestrel.example --out ./kestrel --renditions web
```

This keeps `./kestrel` in sync: `products/`, `variants/`, `assets/` as JSON, and images under `images/<style>/<version>/`. Every file is checked against its sha256. Run it again (or with `--follow`) to pick up only what changed; things the brand took offline are removed. Without `--renditions` it downloads the web version of every image; add `original` for the full-size files when the brand allows it.

## For developers: the Go SDK

```sh
go get github.com/openbrandexchange/obx-go
```

| Package | |
|---|---|
| `obx` | types generated from the spec's JSON Schemas (exact JSON tags), error codes, GTIN helpers, `LocalizedText.Pick` (the language fallback every OBX UI uses: your language → English → first available) |
| `client` | OBX Protocol client: discovery, OAuth2 client credentials with token caching, change feed with a cursor store (`Sync` handles snapshots, grant resyncs and expired cursors), products, GTIN lookup, rendition downloads with sha256 verification, exports, webhooks, access requests, usage |
| `ingest` | Ingest API client: upserts/patches/deletes with ETags, batches (sync and async), blob dedupe, resumable multipart uploads with progress, imports and jobs; `Preparer` hashes, uploads and (when needed) renders images |
| `canonicalcsv` | parser/validator for `products.csv`, `assets.csv` and image names, import planning, and the CSV export writer |
| `renditions` | pure-Go web (≤ 2000 px), square and thumb (≤ 600 px) JPEG renditions, for portals without an image library |
| `webhook` | HMAC-SHA256 signature signing and verification (`OBX-Webhook-Signature`) |
| `nodeadmin` | client for the Node Admin + Portal API used by the shared admin UI |
| `mock` | in-memory portal with the Kestrel Outfitters fixtures and edge-case knobs |

Every client has timeouts on its HTTP calls and retries 429 answers (after `Retry-After`) and, for idempotent requests, 5xx answers and network errors with backoff.

Mirror a brand's catalog:

```go
c, _ := client.New(client.Config{BaseURL: "https://brands.kestrel.example", ClientID: id, ClientSecret: secret})
err := c.Sync(ctx, client.FileCursorStore("cursor.txt"), client.SyncOptions{Follow: true}, func(ctx context.Context, ev client.Event) error {
	switch ev.Kind {
	case client.EventChange: // ev.Change.Op is upsert or delete; ev.Change.Data is the current object
	case client.EventSnapshotComplete: // remove local objects not seen since EventSnapshotStarted
	}
	return nil
})
```

Publish from a PIM connector:

```go
ic, _ := ingest.New("https://brands.kestrel.example", os.Getenv("OBX_TOKEN"))
res, err := ic.PutProduct(ctx, "KO-1001", obx.ProductInput{StyleNumber: "KO-1001", Name: obx.LocalizedText{"en": "Merino Crew Sweater"}}, nil)
// res.Result is created, updated or unchanged; unchanged costs nothing downstream.
```

Verify a webhook:

```go
http.Handle("/obx", webhook.Handler(func(ev *obx.WebhookEvent) error { /* fetch /changes */ return nil }, secret))
```

### The mock portal

`mock` is an in-memory node that speaks the OBX Protocol, the Ingest API and the Node Admin + Portal API, preloaded with the spec fixtures. It passes the full conformance suite, and has knobs for the cases consumers must survive: duplicate change entries, expired cursors, revoked access, changed bytes, 429 and 5xx answers, and a node without an image library.

```go
n := mock.New(mock.Options{Fixtures: true})
srv := httptest.NewServer(n)
creds := n.Credentials()      // API keys, access key, admin and retailer logins
n.SetDuplicateChanges(true)   // at-least-once delivery
n.ExpireCursors()             // next /changes call answers 410 cursor_expired
n.RevokeClient(creds.Full.ClientID)
n.SetCorruptDownloads(true)   // bytes no longer match their sha256
n.Force429(2, 1)              // two 429s with Retry-After: 1
```

Or run it: `obx-conformance mock` (prints test credentials), `obx-conformance mock --fresh` for an empty node that still needs setup.

## For people building a portal: conformance

```sh
obx-conformance all --url http://localhost:8080 --setup-code <code>        # fresh node: setup, fixtures, every profile
obx-conformance protocol --url https://node.example --admin-user admin@brand.example --admin-password …
obx-conformance protocol --url https://node.example --client obxc_…:obxs_…
obx-conformance protocol --url https://node.example --operator-hook ./obx-operator.sh   # node without admin or ingest API
obx-conformance all --url … --setup-code … --format markdown > report.md    # for an issue or pull request
obx-conformance all --url … --setup-code … --json > report.json             # for machines
obx-conformance report report.json                                          # show a saved report again
obx-conformance list                                                         # all test ids
```

The report starts with the verdict ("CONFORMANT" or "NOT CONFORMANT"), a summary per area in plain words, and what failed with why it matters; then every check by test id (see the spec's [conformance.md](https://github.com/openbrandexchange/spec/blob/main/docs/conformance.md)). Profiles: `protocol` (read side, normative), `ingest` (write side, normative), `portal` (Node Admin + Portal API used by the shared admin UI), `security` (the observable parts of the spec's security.md: isolation, sessions, signed URLs, SSRF spellings, CORS only on discovery, upload limits, CSV and link injection; `--rate-limit-test` adds login and token brute force) and `all`. Exit code `0` = every required test passed, `1` = a required test failed, `2` = the suite could not run. With admin credentials or a setup code the suite creates its own retailers, API keys and access keys. To test webhook delivery to a local receiver, run the node with `OBX_ALLOW_PRIVATE_URLS=true`.

In a portal's CI, use the [conformance action](conformance-action/action.yml) or build `obx-conformance` from a checkout of this repository: **[docs/conformance.md](docs/conformance.md)**.

### Interop: do all portals answer the same?

`scripts/interop.sh` runs the same automation against node-go, node-php and node-ts (sibling checkouts; needs Go, Docker and Bun): first-run setup, `obx push` of the Kestrel fixtures, `obx pull`, then `obx-interop trace`, which records the change feed, every product and the sha256 of every file and replays the spec's ingest script step by step, and finally an incremental and a fresh `obx pull`. Node ids, timestamps, cursors and signed URLs are replaced by stable labels such as `product:KO-1001` before `obx-interop compare` lists every difference. `NODES=go,ts` runs a subset.

The spec's schemas and fixtures are embedded (`specdata/`, refreshed with `scripts/sync-spec.sh ../spec`); `--fixtures` points at another copy.

## Building and releasing

Go 1.26 or newer.

```sh
go test ./...                     # unit, HTTP and CLI tests; includes a full conformance run against the mock
golangci-lint run ./...           # lint (CI also runs shellcheck, actionlint, govulncheck and the licence check)
go build ./cmd/obx ./cmd/obx-conformance ./cmd/obx-interop
docker build -t obx .             # the container image from source
goreleaser release --snapshot --clean   # release archives, checksums, SBOMs and images in dist/, nothing published
```

Releases are made by pushing a version tag; see [CONTRIBUTING.md](CONTRIBUTING.md#releases) and [CHANGELOG.md](CHANGELOG.md). Implementation notes and the choices made where the spec is open: [NOTES.md](NOTES.md). Security reports: [SECURITY.md](SECURITY.md).

## License

Apache License 2.0, see [LICENSE](LICENSE) and [NOTICE](NOTICE). The embedded specification schemas and fixtures in `specdata/` are CC-BY-4.0.

Open Brand Exchange Foundation (in formation) · hello@openbrandexchange.com
