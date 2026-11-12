# Publish to Open Brand Exchange (GitHub Action)

Publishes your product data (`products.csv`, optional `assets.csv`) and images
to your Open Brand Exchange portal with the [`obx`](../README.md) command. Only
new or changed images are uploaded, so running it on every push or every night
costs nothing when nothing changed.

## Before you start

1. In your portal, create an access key: **Automatic upload → Access keys →
   New access key**. Give it a name such as "GitHub export".
2. In your GitHub repository: **Settings → Secrets and variables → Actions**,
   add two repository secrets:
   - `OBX_URL`: the address of your portal, for example `https://brands.kestrel.example`
   - `OBX_TOKEN`: the access key (starts with `obxi_`)

## Examples

Publish the `export` folder whenever it changes, every night, and on demand:

```yaml
# .github/workflows/publish.yml
name: Publish product content
on:
  push:
    branches: [main]
    paths: ["export/**"]
  schedule:
    - cron: "30 2 * * *"
  workflow_dispatch:

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: openbrandexchange/obx-go/push-action@v0
        with:
          url: ${{ secrets.OBX_URL }}
          token: ${{ secrets.OBX_TOKEN }}
          folder: export
```

Check a pull request without changing anything in the portal:

```yaml
on: pull_request
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: openbrandexchange/obx-go/push-action@v0
        with:
          url: ${{ secrets.OBX_URL }}
          token: ${{ secrets.OBX_TOKEN }}
          folder: export
          dry-run: true
```

Separate files, a mirror of the whole catalog, Dutch messages and a pinned
obx version:

```yaml
      - uses: openbrandexchange/obx-go/push-action@v0
        with:
          url: ${{ secrets.OBX_URL }}
          token: ${{ secrets.OBX_TOKEN }}
          csv: exports/products.csv
          assets-csv: exports/assets.csv
          images: exports/images
          mirror: true          # also take offline what the file no longer contains
          lang: nl
          version: v0.2.1
```

Keep going when some rows have problems, and use the result in a later step:

```yaml
      - id: obx
        uses: openbrandexchange/obx-go/push-action@v0
        with:
          url: ${{ secrets.OBX_URL }}
          token: ${{ secrets.OBX_TOKEN }}
          folder: export
          fail-on-problems: false
      - if: steps.obx.outputs.exit-code == '2'
        run: echo "Some rows need attention; see the job summary."
```

## Inputs

| Input | Required | Default | What it does |
|---|---|---|---|
| `url` | yes | | Address of your portal. |
| `token` | yes | | Access key from your portal (starts with `obxi_`). |
| `folder` | | | Folder with `products.csv`, an optional `assets.csv` and the images (subfolders are fine). |
| `csv` | | | Path to `products.csv`, instead of or next to `folder`. |
| `assets-csv` | | | Path to an optional `assets.csv` (rights, AI information, alt text per image). |
| `images` | | | Folder or zip with images named `{style}_{colour}_{position}.jpg`. |
| `package` | | | One zip with `products.csv`, `assets.csv` and images. |
| `mirror` | | `false` | Also take offline the products of these brands that the file no longer contains. Refuses to take more than half offline. |
| `allow-mass-unpublish` | | `false` | With `mirror`: allow taking more than half offline. |
| `dry-run` | | `false` | Only check and report; upload and change nothing. |
| `language` | | portal's first language | Language of columns without a language suffix. |
| `fail-on-problems` | | `true` | Fail the step when some rows or images had problems (exit code 2). |
| `lang` | | `en` | Language of the log and the job summary (any of the 25 languages, for example `nl`, `de`, `fr`). |
| `version` | | `latest` | obx version: `latest` or a release tag such as `v0.2.1`. Pin a tag for reproducible runs. |

## Outputs

| Output | |
|---|---|
| `exit-code` | `0` all good, `2` done, but some rows or images had problems (listed in the job summary), `1` failed. |

## How it works

The action runs on Linux, macOS and Windows runners (x64 and ARM64). It
downloads the obx release archive for the runner, checks it against the
release's `checksums.txt` and refuses it when they differ, then runs
`obx push` and writes the result to the job summary. `scripts/test-push-action.sh`
in this repository runs both steps against a local release folder and the mock
portal, including a tampered archive.

Referencing `@v0` needs a `v0` tag that follows the latest 0.x release (the
maintainers move it after each release). Until the first release is published,
reference a commit instead.
