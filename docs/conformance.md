# Conformance checks for portal developers

`obx-conformance` checks a running portal against the Open Brand Exchange specification. Each test
has an id from the spec's [conformance.md](https://github.com/openbrandexchange/spec/blob/main/docs/conformance.md).

## Running it

```sh
go build -o obx-conformance ./cmd/obx-conformance        # or download it from a release
obx-conformance all --url http://localhost:8080 --setup-code <code>
```

`all` on a fresh portal is the reference run: it completes the first-run setup, imports the MAISON NOOR
fixtures and runs every profile. Against a portal that is already set up, pass `--admin-user` and
`--admin-password`, or only API keys (`--client id:secret`, `--second-client id:secret`,
`--no-original-client id:secret`) and an access key (`--token obxi_…`) for the protocol and ingest
profiles; tests that need what you did not give are skipped, not failed, and the run is then
INCOMPLETE (see [Verdicts](#verdicts-and-exit-codes)). Several profiles run together when joined
by commas: `obx-conformance protocol,ingest …` (P-FEED-5 reads the ingest replay, so a node with
the Ingest API needs both in one run to be CONFORMANT for the protocol).

### Verdicts and exit codes

A run ends in one of the spec's three verdicts (the spec's conformance.md, "Three verdicts"):

| Verdict | When | Exit code |
|---|---|---|
| `CONFORMANT` | every REQUIRED test of the profiles ran and passed | 0 |
| `INCOMPLETE` | no REQUIRED test failed, but at least one was skipped (a credential, a second API key, an operator action or a profile was not given). The report lists the skipped ids and what each needs | 2 |
| `NOT CONFORMANT` | at least one REQUIRED test failed | 1 |

Exit code 2 is also used when the suite cannot run at all (a usage error, or the run could not be
prepared: then `setup_error` is set in the JSON report). A skipped REQUIRED test is never a pass.
Optional tests may be skipped or fail without changing the verdict; a test that is REQUIRED only
"when the operator offers the action" (P-FEED-7) is reported with `required: false` when the
operator does not offer it. Tests of an optional interface the portal does not offer have the
status `n/a`, which is not a skip: P-FEED-5 and the whole ingest profile when `/.well-known/obx`
has no capability `ingest`, P-ACC-1 without `access_requests`, P-EXP-1, P-HOOK-1 and P-USE-1
without their capability. A run with `--only` never says CONFORMANT (its verdict is INCOMPLETE,
`partial: true`), but exits 0 when every selected REQUIRED test ran and passed.

The JSON report (`--json`) has a top-level `verdict` and `not_applicable` count, and per test
`status` (`pass`, `fail`, `skip`, `n/a`), `required` and, for a skip, `needs`.

## Portals without the Ingest or Node Admin API

The standard is what retailers' software reads: the **protocol** profile. How a portal gets its data
and how a brand manages its retailers is up to the portal, so it needs neither the Ingest API nor the
Node Admin API to be conformant. Such a portal:

1. loads `fixtures/maison-noor` from the spec (company "MAISON NOOR Group", brands `maison-noor` and
   `noor-atelier`) with its own import, into a test account;
2. runs the protocol profile with an **operator** that does the brand-side steps the tests need
   while they run.

```sh
obx-conformance protocol --url https://portal.example --operator-hook ./obx-operator.sh
obx-conformance protocol --url https://portal.example --operator-prompt     # do each step by hand
```

The hook is run once per step with the action name as its argument and the step as JSON on stdin
(`{"action": …, "params": {…}, "result": […], "hint": "…"}`). It prints a JSON object with the
`result` keys on stdout, or nothing, and exits 0; any other exit code fails the test. Brands are
external ids (`maison-noor`, `noor-atelier`) or `all`; products are fixture external ids.

| Action | Params | Result |
|---|---|---|
| `create_api_key` | `name`, `brands`, `originals` (`true`/`false`) | `client_id`, `client_secret` |
| `revoke_api_key` | `client_id` | |
| `approve_access_request` | `request_id`, `retailer_name` | |
| `set_grant` | `client_id`, `brands` | |
| `update_product` | `external_id`, `field` (`season`), `value` | |
| `unpublish_product`, `publish_product` | `external_id` | |
| `rotate_client_secret` | `client_id` | `client_secret` (the new secret) |
| `simulate_spec_upgrade` (optional) | | `simulated` (`true`) |

`rotate_client_secret` is for P-AUTH-2: give the API key a new secret (rotate, do not revoke) and
print `{"client_secret": "obxs_…"}`. Access tokens issued with the old secret must then answer
`401 invalid_token`, not `access_revoked`. An operator that does not offer it (no result, Enter at
the prompt, or `conformance.ErrActionNotSupported`) makes P-AUTH-2 skip after its revocation part,
so the run is INCOMPLETE. `unpublish_product` and `publish_product` of MN-1006 are also how P-CAT-4
checks the feed's delete cascade and the upserts after publishing again.

`simulate_spec_upgrade` is for P-FEED-7 (spec protocol.md §6.4): do to the portal what an upgrade
from spec 0.2 to 0.3 does to its cursors, so every cursor issued so far answers
`410 cursor_expired` (for example by bumping a cursor epoch that every cursor carries). Print
`{"simulated": "true"}` when done. The action is optional: a hook that prints nothing for it, a
person who presses Enter at the prompt, or a Go operator that returns
`conformance.ErrActionNotSupported` makes P-FEED-7 skip instead of fail. A hook that exits non-zero
still fails the test. Admin credentials alone do not run it (the Node Admin API has no such
call), so to cover P-FEED-7 also pass an operator; the other steps keep using the admin API.

In Go tests, set `conformance.Config.Operator` to a `conformance.OperatorFunc` that calls the
portal's own code; see `conformance/operator_test.go`. The mock node offers
`mock.Node.SimulateSpecUpgrade` and `mock.Node.RotateClientSecret`, which the tests call through
such an operator. API keys you pass with `--client`, `--second-client` and `--no-original-client`
are used instead of asking the operator for them. P-FEED-5 (the ingest replay) is `n/a` on
portals without the Ingest API.

## The tests

`obx-conformance list` prints every test id with its profile and whether it is required. The ids
and what they check are in the spec's
[conformance.md](https://github.com/openbrandexchange/spec/blob/main/docs/conformance.md). Added
for specification 0.2 (article data for fashion, shoes, bags and luggage) and changed for 0.3
(materials and care as terms with names):

| Test | Profile | Checks |
|---|---|---|
| P-AUTH-2 | protocol | no token → `401 unauthorized`, unknown token → `401 invalid_token`; a revoked API key's token → `401 access_revoked` and its secret `invalid_client`; after a **secret rotation** (admin API or operator `rotate_client_secret`) the token issued with the old secret → `401 invalid_token` (never `access_revoked`) and the new secret gets a working token. Skipped (INCOMPLETE) when neither the admin API nor the operator can rotate |
| P-CAT-2 | protocol | keyset pagination complete and stable, `ids=`; **limits clamp**: `GET /products?limit=100000` and `GET /changes?limit=100000` answer 200 with at most `limits.products_page_max` / `limits.changes_page_max` entries; `limit=0` and `limit=abc` answer `400 invalid_request` on both |
| P-CAT-4 | protocol | invisible objects → 404, never 403; then MN-1006 is unpublished (upload key or operator `unpublish_product`): 404 for it and its image, and `/changes` from a live cursor has exactly one `delete` for the product, then its variants (by `external_id`), then its images (by `color_code`, `null` first, then `position`), none with `data`, `seq` strictly increasing, nothing else for MN-1006; after `publish_product` the same objects as `upsert` in the same order, each `data` valid against its schema and equal to what `GET` returns (rendition URLs left out, they are signed per answer). Skipped (INCOMPLETE) without an upload key, admin credentials or operator |
| P-MEDIA-2 | protocol | as before, and: a signed URL with a changed signature, or with its expiry moved into the past, answers `403 forbidden`; the URL with neither signature nor token `401 unauthorized` (signature and expiry are found as query parameters whose names contain `sig` and `exp`; a node that signs otherwise gets a note) |
| P-EXP-1 | protocol (if `exports`) | as before, and `POST /exports` again with the same `Idempotency-Key` and body returns the same export id right away (while it may still run), the same key with another body → `409 idempotency_key_reused` |
| P-FEED-7 | protocol | cursors from before a spec upgrade expire (protocol.md §6.4): a live cursor of a new API key works, the operator runs `simulate_spec_upgrade`, then `/changes` with the old cursor answers `410 cursor_expired` (problem+json), and `/changes` without a cursor gives a snapshot and a new cursor that works. Skipped when the operator does not offer the action, without an operator, and in the online check |
| P-CAT-1 | protocol | MN-1001 and MN-1005 match the expected fixtures, now including the article data (materials and care with their names, `contains_animal_origin`, `features`, certifications, colour families, `item_subtype`, `strap_length_cm`) |
| P-CAT-5 | protocol | the article data round trip: MN-1008 (suitcase), MN-1009 (laptop bag), MN-1010 (Chelsea boot) and NA-2001 (imported with the 0.1 fibre key `recycled_polyester`) match the expected fixtures, with the vocabulary's names for parts, materials, qualifiers and care codes; MN-1009's outer fibre has the qualifiers `recycled` and `post_consumer`, in that order |
| P-CAT-6 | protocol | over the whole snapshot: every product and variant has the article data properties (`null` when not stated) and validates against the 0.3 schemas; no material code starting with `recycled_` or `organic_`, no 0.2 fibre properties and no single `qualifier`: every fibre has `percentage` (a number in (0, 100] or `null`) and the list `qualifiers` (`[]` when none) without a repeated code; MN-1005's part `outer` has the fibre `leather` with `percentage` `null`; every part, material and qualifier whose code is in the spec's `vocabularies/` has a name, the part `main` has the name `null`, every care code of `vocabularies/care.json` has exactly its `category` and `symbol` and a name, no name has an empty string; no empty `measurements` or `features`, `footwear_materials` with all three parts; MN-1004 and MN-1003 as listed in the spec, MN-1003's fibre with the qualifiers `organic` and `regenerative`, in that order |
| I-IDEM-1 | ingest | a repeated upsert is `unchanged` with no change entry; `POST /batch` and `POST /imports` with the same `Idempotency-Key` and body return the same answer or job; the same key with another body → `409 idempotency_key_reused`, and the product that body would create does not exist |
| I-EXT-1 | ingest | extensions and unknown members (ingest.md §3.1): `PUT` of `fixtures/examples/ProductInput.extension.json` (as MN-1007 of brand `maison-noor`) plus `unknown_member` and, in the first fibre, `unknown_nested` → `201`, never `422`, the warnings name both; the Ingest `GET`, `GET /products/{id}` and the `/changes` entry carry `x-noor-fabric-weight-gsm`, `x-noor-care-note` and the material's `x-noor-mill` and none of the unknown members; the same `PUT` → `unchanged`; `PATCH {"x-noor-fabric-weight-gsm": null}` → `updated`, one upsert, the member gone, the others kept. The same for a variant (`x-noor-bin`, `color.x-noor-pantone`) and an image (`x-noor-shoot`); the suite deletes them afterwards |
| I-MAT-1 | ingest | the draft single `qualifier`: `PUT` with fibres `qualifier: recycled`, `qualifier: organic` + `qualifiers: [regenerative]` and `qualifier: null` → `201`, never `422`, exactly one `warning_details` entry `qualifier_deprecated` (pointer `/materials/0/fibers/0/qualifier`) and a text in `warnings`; the Ingest `GET`, the protocol and `/changes` give the codes `[recycled]`, `[regenerative]`, `[]` and no `qualifier` member anywhere; the same product written with `qualifiers` only → `unchanged` |
| I-IMP-7 | ingest | `fixtures/imports/1-feature-update.csv` to `6-restore.csv`, imported in order on a portal that holds exactly the MAISON NOOR import (the suite restores it first): counts, error rows, change entries and fields as in `fixtures/imports/expected.json` (import rule 9 per feature key and measurement column); an error with `detail_prefix` (the decimal comma of `5-invalid.csv` row 5) must have a detail that starts with it |
| I-IMP-8 | ingest | `fixtures/imports/label-warnings.csv` as a dry run: no errors, nothing written (ML-8001 to ML-8009), and exactly the warnings of `label-warnings.expected.json` whose code is in its `codes` list (the label completeness codes and `material_unknown`, with `value` when given); the MAISON NOOR import has none |
| I-IMP-12 | ingest | variant identity (import-format.md §1.2.1): the files of `fixtures/imports/variant-identity.expected.json` in order: counts, error rows, change entries and the variants of ML-8101 as listed, each variant keeping its protocol id from file to file; and every MAISON NOOR variant has the external id `<style>-<color>-<size>[-<size_length>]`. The suite deletes ML-8101 afterwards |
| A-KEY-1 | portal | as before, and after a **rotation**: an access token of the old secret → `401 invalid_token`, the old secret → `invalid_client`, a token of the new secret works with a cursor from before; after a **revoke** `/grant`, `/changes` and `/products` answer `401 access_revoked` |
| S-URL-1 | security | a signed URL moved to another asset or rendition, extended or re-signed answers `403 forbidden`; stripped of its query (neither signature nor token) `401 unauthorized` (as P-MEDIA-2); unpublished: `403 forbidden` or `404 not_found` |
| I-IMP-11 | ingest | several qualifiers in the import (import-format.md §1.4). A products.csv with `outer: 70% recycled_polyester (post_consumer) (recycled), 30% cotton (organic) (regenerative) (organic); lining: 100% cotton` gives the qualifiers `[recycled, post_consumer]` and `[organic, regenerative]` through the Ingest API and the protocol (0.1 key first, order kept, repeats once); a second row with `100% cotton (organic, recycled)` is one `validation_failed` error at row 2, column `materials`, and is not written |

Optional, for portals that import Excel workbooks (the spec's
[import-format.md §5](https://github.com/openbrandexchange/spec/blob/main/docs/import-format.md#excel));
both are skipped when the portal answers a workbook with `unsupported_media_type`, as the mock does:

| Test | Profile | Checks |
|---|---|---|
| I-IMP-9 | ingest | each converting workbook of `fixtures/imports/xlsx/expected.json` sent as `products.xlsx` (or `assets.xlsx`, with the images its `send.images_manifest` names) as a dry run: the run succeeds, the error rows are exactly `import_errors` (Excel row numbers, `column`, `value`, a `detail` starting with `detail_prefix`), the conversion `warnings` are there, and nothing is written; `products-template.xlsx` and `assets-template.xlsx` imported for real are all unchanged with no change entries |
| I-IMP-10 | ingest | each workbook of `expected.json` with an `error` (macros, an external link, a zip bomb, too many rows, a renamed CSV, an encrypted file, a `DOCTYPE`, a macro part next to a broken `[Content_Types].xml`, a number out of Excel's range, no products sheet) sent as `products.xlsx`: the run fails with that `workbook_*` code in `ImportRun.error`, its results hold exactly one ImportRowError with `row` and `column` null and that code, and nothing is written. A portal that refuses the upload at once with a problem of that code passes too |

I-IMP-3 now refers to the nine MAISON NOOR products. A portal written for 0.1 or 0.2 fails P-CAT-1, P-CAT-5
and P-CAT-6 until it returns materials and care as 0.3 terms; P-DISC-1 only notes an `obx_version`
other than `0.3`.

## Reading the report

The text report is written for someone who does not read API specs:

1. **Result**: CONFORMANT, INCOMPLETE (no required check failed but some were skipped) or NOT
   CONFORMANT with the number of required checks that failed.
2. **Summary** per area: reading by retailers' software (protocol), automatic upload (ingest),
   admin and retailer screens (portal), security.
3. **What failed**, with why it matters and the details of each failure.
4. **Optional checks not met**, **Skipped required checks** (each with what it needs), other
   **Skipped** checks and **Not applicable** checks.
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
token. A read-only report is always partial: it is never a conformance claim. Its `verdict` is
`INCOMPLETE` when every online check passed (the checks that write or need brand-side steps are
skipped by design) or `NOT CONFORMANT` when a required check failed, never `CONFORMANT`; its exit
code keeps the online meaning: 0 when no required online check failed, 1 when one did, 2 when the
check could not run. `--stream` prints one
JSON line per finished test (`{"type":"result",…}`) and then `{"type":"report",…}`.

`obx-conformance list` shows per test what the online check needs: `anonymous`, `api_key` (one API
key; fixture parts are skipped on other data) or `fixtures` (an API key of a test account with the
MAISON NOOR test data); `-` means not checked online. Without the MAISON NOOR data, P-MEDIA-2 and P-MEDIA-3
check the first images of the catalog (web, square and thumb; originals are not downloaded).

`obx-conformance check-server [--addr :8080]` serves the same check over HTTP for the website's
container: `POST /check` with `{"url": "https://portal.example", "client": {"id": "…", "secret":
"…"}}` answers the JSON report, or with `Accept: application/x-ndjson` the `--stream` lines. It
refuses IP addresses, ports other than 443 and host names that resolve to a non-public address
before the check starts, runs at most `--max-concurrent` checks (default 2) and logs only metadata
(host, duration, counts), never the key. The deployment is in `deploy/checker`.

### The public demo portal

`obx-conformance mock --read-only --client id:secret` serves the mock with the MAISON NOOR data as a
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
Markdown report in the job summary and fails the job when a required check fails or was skipped
(INCOMPLETE, exit code 2). Outputs: `passed`,
`failed` and `report` (the JSON file).

## The mock portal

`obx-conformance mock` runs an in-memory portal with the MAISON NOOR fixtures and prints test accounts
and keys; `--fresh` starts empty with a setup code. It passes every check and is the reference for
behaviour the spec leaves open (see [NOTES.md](../NOTES.md)). By default it allows webhook and import
targets on loopback and private addresses (for local receivers); link-local and cloud-metadata
addresses are always refused, and the address it connects to is checked on every connection.
