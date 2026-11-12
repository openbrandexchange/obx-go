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

## Portals without the Ingest or Node Admin API

The standard is what retailers' software reads: the **protocol** profile. How a portal gets its data
and how a brand manages its retailers is up to the portal, so it needs neither the Ingest API nor the
Node Admin API to be conformant. Such a portal:

1. loads `fixtures/kestrel` from the spec (company "Kestrel Outfitters", brands `kestrel` and
   `kestrel-trail`) with its own import, into a test account;
2. runs the protocol profile with an **operator** that does the brand-side steps the tests need
   while they run.

```sh
obx-conformance protocol --url https://portal.example --operator-hook ./obx-operator.sh
obx-conformance protocol --url https://portal.example --operator-prompt     # do each step by hand
```

The hook is run once per step with the action name as its argument and the step as JSON on stdin
(`{"action": …, "params": {…}, "result": […], "hint": "…"}`). It prints a JSON object with the
`result` keys on stdout, or nothing, and exits 0; any other exit code fails the test. Brands are
external ids (`kestrel`, `kestrel-trail`) or `all`; products are fixture external ids.

| Action | Params | Result |
|---|---|---|
| `create_api_key` | `name`, `brands`, `originals` (`true`/`false`) | `client_id`, `client_secret` |
| `revoke_api_key` | `client_id` | |
| `approve_access_request` | `request_id`, `retailer_name` | |
| `set_grant` | `client_id`, `brands` | |
| `update_product` | `external_id`, `field` (`season`), `value` | |
| `unpublish_product`, `publish_product` | `external_id` | |

In Go tests, set `conformance.Config.Operator` to a `conformance.OperatorFunc` that calls the
portal's own code; see `conformance/operator_test.go`. API keys you pass with `--client`,
`--second-client` and `--no-original-client` are used instead of asking the operator for them.
P-FEED-5 (the ingest replay) is skipped on such portals.

## The tests

`obx-conformance list` prints every test id with its profile and whether it is required. The ids
and what they check are in the spec's
[conformance.md](https://github.com/openbrandexchange/spec/blob/main/docs/conformance.md). Added
for specification 0.2 (article data for fashion, shoes, bags and luggage):

| Test | Profile | Checks |
|---|---|---|
| P-CAT-1 | protocol | KO-1001 and KO-1005 match the expected fixtures, now including the 0.2 article data (`contains_animal_origin`, `features`, certifications, colour families, `item_subtype`, `strap_length_cm`) |
| P-CAT-5 | protocol | the article data round trip: KO-1008 (suitcase), KO-1009 (laptop bag), KO-1010 (Chelsea boot) and KT-2001 (imported with the 0.1 fibre key `recycled_polyester`) match the expected fixtures |
| P-CAT-6 | protocol | over the whole snapshot: every product and variant has the 0.2 properties (`null` when not stated) and validates against the 0.2 schemas; no 0.1 fibre key, no fibre flag `false`, no empty `measurements` or `features`, `footwear_materials` with all three parts; KO-1004 and KO-1003 as listed in the spec |
| I-IMP-7 | ingest | `fixtures/imports/1-feature-update.csv` to `6-restore.csv`, imported in order on a portal that holds exactly the Kestrel import (the suite restores it first): counts, error rows, change entries and fields as in `fixtures/imports/expected.json` (import rule 9 per feature key and measurement column) |
| I-IMP-8 | ingest | `fixtures/imports/label-warnings.csv` as a dry run: no errors, nothing written, and exactly the label completeness warnings of `label-warnings.expected.json`; the Kestrel import has none |

I-IMP-3 now refers to the nine Kestrel products. A portal written for 0.1 fails P-CAT-1, P-CAT-5
and P-CAT-6 until it stores and returns the new fields; P-DISC-1 only notes an `obx_version` other
than `0.2`.

## Reading the report

The text report is written for someone who does not read API specs:

1. **Result**: CONFORMANT, or NOT CONFORMANT with the number of required checks that failed.
2. **Summary** per area: reading by retailers' software (protocol), automatic upload (ingest),
   admin and retailer screens (portal), security.
3. **What failed**, with why it matters and the details of each failure.
4. **Optional checks not met** and **Skipped** (usually a missing credential or switch).
5. **All checks** by id, and the preparation steps.

After a full pass the report says how to get the result confirmed for free ("Compatible with Open
Brand Exchange") and where to read about the paid listing as official partner:
<https://openbrandexchange.com/en/partners/>.

`--format markdown` gives the same as a table for an issue or a pull request, `--json` the complete
report for machines, and `obx-conformance report report.json` shows a saved JSON report again. A run
with `--only` is marked partial and never says CONFORMANT.

## The online check

`obx-conformance check --url https://portal.example [--client id:secret] [--json|--stream]` is the
read-only check that the compatibility checker on openbrandexchange.com runs against a portal on the
internet. It runs only the protocol tests, and only the parts that do not write or need a brand-side
step; it never creates API keys, access requests, exports, webhooks or usage events. Every
connection is checked against loopback, private, link-local (cloud metadata), CGNAT, multicast and
reserved addresses (0/8, 198.18/15, 240/4) and the NAT64 and 6to4 prefixes; only https is used,
redirects are limited to 3, requests are paced at one per 100 ms and a run is capped at 800 requests
and 64 MiB. A 429 with a Retry-After longer than 2 seconds stops the run: the remaining tests are
skipped as rate limited instead of the check waiting. Secrets can be given as
`OBX_CHECK_CLIENT=id:secret` instead of a flag, and are redacted from the report together with any
token. A read-only report is always partial: it is never a conformance claim. `--stream` prints one
JSON line per finished test (`{"type":"result",…}`) and then `{"type":"report",…}`.

`obx-conformance list` shows per test what the online check needs: `anonymous`, `api_key` (one API
key; fixture parts are skipped on other data) or `fixtures` (an API key of a test account with the
Kestrel test data); `-` means not checked online. Without the Kestrel data, P-MEDIA-2 and P-MEDIA-3
check the first images of the catalog (web, square and thumb; originals are not downloaded).

`obx-conformance check-server [--addr :8080]` serves the same check over HTTP for the website's
container: `POST /check` with `{"url": "https://portal.example", "client": {"id": "…", "secret":
"…"}}` answers the JSON report, or with `Accept: application/x-ndjson` the `--stream` lines. It
refuses IP addresses, ports other than 443 and host names that resolve to a non-public address
before the check starts, runs at most `--max-concurrent` checks (default 2) and logs only metadata
(host, duration, counts), never the key. The deployment is in `deploy/checker`.

### The public demo portal

`obx-conformance mock --read-only --client id:secret` serves the mock with the Kestrel data as a
public demo: only the protocol's reads are answered (GET, the token request and the GTIN lookup);
access requests, exports, usage events and webhooks answer 403, the ingest API, the admin API and the
portal 404. The data lives in memory, so a restart resets it. Its API key is published on purpose: it
can only read the demo data. Every online-runnable check passes against it.

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

## The mock portal

`obx-conformance mock` runs an in-memory portal with the Kestrel fixtures and prints test accounts
and keys; `--fresh` starts empty with a setup code. It passes every check and is the reference for
behaviour the spec leaves open (see [NOTES.md](../NOTES.md)). By default it allows webhook and import
targets on loopback and private addresses (for local receivers); link-local and cloud-metadata
addresses are always refused, and the address it connects to is checked on every connection.
