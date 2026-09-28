# Conformance checks for portal developers

`obx-conformance` checks a running portal against the Open Brand Exchange specification. Each test
has an id from the spec's [conformance.md](https://github.com/openbrandexchange/spec/blob/main/docs/conformance.md).

## Running it

```sh
go build -o obx-conformance ./cmd/obx-conformance        # or download it from a release
obx-conformance all --url http://localhost:8080 --setup-code <code>
```

`all` on a fresh portal is the reference run: it completes the first-run setup, imports the Kestrel
fixtures and runs every profile. Against a portal that is already set up, pass `--admin-user` and
`--admin-password`, or only API keys (`--client id:secret`, `--second-client id:secret`,
`--no-original-client id:secret`) and an access key (`--token obxi_…`) for the protocol and ingest
profiles; tests that need what you did not give are skipped, not failed.

## Reading the report

The text report is written for someone who does not read API specs:

1. **Result**: CONFORMANT, or NOT CONFORMANT with the number of required checks that failed.
2. **Summary** per area: reading by retailers' software (protocol), automatic upload (ingest),
   admin and retailer screens (portal), security.
3. **What failed**, with why it matters and the details of each failure.
4. **Optional checks not met** and **Skipped** (usually a missing credential or switch).
5. **All checks** by id, and the preparation steps.

`--format markdown` gives the same as a table for an issue or a pull request, `--json` the complete
report for machines, and `obx-conformance report report.json` shows a saved JSON report again. A run
with `--only` is marked partial and never says CONFORMANT.

## In CI

In this repository's own CI the mock portal is checked through the conformance action. A portal
repository can do the same:

```yaml
- name: Start the portal
  run: ./start-my-portal.sh &                # listens on :8080, fresh, setup code "ci-code"
- uses: openbrandexchange/obx-go/conformance-action@main
  with:
    url: http://127.0.0.1:8080
    setup-code: ci-code
```

The action builds `obx-conformance` from its own checkout, runs the profile (default `all`), puts the
Markdown report in the job summary and fails the job when a required check fails. Outputs: `passed`,
`failed` and `report` (the JSON file).

**While the repositories are private**, another repository can only use this action, or check out
obx-go, when access is granted. Either allow it once for the organisation (obx-go → Settings →
Actions → General → Access: "Accessible from repositories in the openbrandexchange organization"),
or add a secret `OBX_READ_TOKEN` (a fine-grained token with read access to obx-go's contents) to the
portal repository and check out obx-go with it:

```yaml
- uses: actions/checkout@v4
  with:
    repository: openbrandexchange/obx-go
    path: obx-go
    token: ${{ secrets.OBX_READ_TOKEN }}
- run: (cd obx-go && go build -o "$RUNNER_TEMP/obx-conformance" ./cmd/obx-conformance)
- run: '"$RUNNER_TEMP/obx-conformance" all --url http://127.0.0.1:8080 --setup-code ci-code'
```

Once the repositories are public, neither is needed.

## The mock portal

`obx-conformance mock` runs an in-memory portal with the Kestrel fixtures and prints test accounts
and keys; `--fresh` starts empty with a setup code. It passes every check and is the reference for
behaviour the spec leaves open (see [NOTES.md](../NOTES.md)). By default it allows webhook and import
targets on loopback and private addresses (for local receivers); link-local and cloud-metadata
addresses are always refused, and the address it connects to is checked on every connection.
