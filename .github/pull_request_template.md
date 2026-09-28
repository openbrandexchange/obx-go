## What and why

<!-- One logical change. Link the issue if there is one. -->

## Checks

- [ ] `gofmt -l .` is empty, `go vet ./...`, `golangci-lint run ./...` and `go test ./...` pass
- [ ] Tests cover the new behaviour (CLI in, output and portal state out; never source text)
- [ ] New or changed CLI messages are in all 25 locales (`go test -run TestLocalesComplete ./cmd/obx/`) and follow the glossary
- [ ] Docs, NOTES.md and `CHANGELOG.md` ("Unreleased") updated for user-visible changes
- [ ] Every commit is signed off (`git commit -s`, Developer Certificate of Origin)
