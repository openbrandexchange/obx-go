# Contributing

Thank you for helping. This repository holds the `obx` command, the GitHub Actions, the Go SDK and
the conformance suite of Open Brand Exchange. Everyone taking part follows the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- Bugs and ideas: open an issue with the templates. Security problems: see [SECURITY.md](SECURITY.md),
  never a public issue.
- Protocol changes start in [openbrandexchange/spec](https://github.com/openbrandexchange/spec); the
  SDK, the mock portal and the conformance suite follow the spec (`scripts/sync-spec.sh ../spec`
  refreshes the embedded schemas and fixtures).
- Choices where the spec is open are written down in [NOTES.md](NOTES.md).

## Development

Go 1.26 or newer (the `toolchain` line in go.mod picks the patch release).

```sh
gofmt -l . && go vet ./... && golangci-lint run ./...   # format and lint
go test ./...                                          # unit, HTTP, CLI and conformance tests
go test -race -timeout 45m ./...                       # the same with the race detector (slow)
scripts/test-push-action.sh                            # the GitHub Action against a local release
scripts/interop.sh                                     # all portal editions (needs Docker and Bun)
goreleaser release --snapshot --clean                  # release archives and images in dist/
```

- Tests test behaviour: run the command or call the API against the mock portal and check the
  output and the portal's state, never source text.
- CLI messages are registered in Go with an English text (`register(...)` in `cmd/obx`) and
  translated in `cmd/obx/locales/<lang>.json` for all 25 languages; `TestLocalesComplete` fails when
  one is missing. Use the words of the
  [glossary](https://github.com/openbrandexchange/spec/blob/main/docs/glossary.md): "access key",
  "automatic upload", "API key for your software", "EAN"; never "token", "ingest" or "GTIN".
- Code, comments, commit messages and docs are English.
- Keep dependencies few; every module must have an Apache-2.0-compatible licence (CI runs
  `go-licenses check`).

## Developer Certificate of Origin

Contributions are accepted under the [Developer Certificate of Origin 1.1](https://developercertificate.org/).
Sign off every commit to say you wrote it or have the right to submit it under this repository's
licence (Apache-2.0):

```sh
git commit -s -m "Explain the change"
```

This adds `Signed-off-by: Your Name <you@example.com>`. You keep the copyright in your contribution.
There is no contributor licence agreement.

## Pull requests

- One logical change per pull request, with tests and docs updated.
- CI must be green: format, vet, golangci-lint, shellcheck, actionlint, tests on Linux and macOS
  (with the race detector), locales, conformance of the mock through the conformance action, the
  push action, release builds, container image, govulncheck and the licence check.
- User-visible changes get a line in `CHANGELOG.md` under "Unreleased".

## Releases

Maintainers only. Versions follow [Semantic Versioning](https://semver.org/). Move the entries under
"Unreleased" in `CHANGELOG.md` to a new version with today's date, then push a tag `vX.Y.Z`; the
release workflow publishes a draft release (archives, checksums, SBOMs, container image, Homebrew
formula) that a maintainer reviews and publishes. Afterwards move the `v0` tag to the new release so
`uses: openbrandexchange/obx-go/push-action@v0` picks it up, and copy `obx.rb` from the release
into the tap repository.
