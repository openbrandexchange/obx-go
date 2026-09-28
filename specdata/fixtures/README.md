# Fixtures

Everything here is **fictional**: "Kestrel Outfitters" is not a real company, the GTINs use the GS1 restricted-circulation prefix `200` (never assigned to trade items), and the images are generated flat placeholders. Regenerate with `python3 fixtures/generate.py` (Python 3 standard library only). The committed files are canonical: PNG bytes (and therefore hashes) can differ with another zlib version, so do not regenerate casually.

| Path | What |
|---|---|
| `kestrel/products.csv` | canonical import CSV: 2 brands (Kestrel, Kestrel Trail), 8 products, 40 variants with valid EAN-13s, materials, care codes, `nl` + `en` text |
| `kestrel/assets.csv` | overrides: one AI-generated model image (`KO-1003_RST_3_model.png`, `ai_generated: true`) and one embargoed image (`KO-1002_BLK_2_back.png`, embargo until 2030-01-01) |
| `kestrel/images/*.png` | 27 placeholder images following `{style}_{color}_{position}_{qualifier}.png`; `KO-1002_OLV_1_front.png` is 2400×3000 to exercise downscaling, the rest 1200×1500 |
| `kestrel/images.json` | sha256, bytes, dimensions of every original |
| `expected/*.json` | expected protocol objects after importing `kestrel/` into a fresh node (placeholders: see [docs/conformance.md](../docs/conformance.md#fixture-conventions)) |
| `ingest/script.ndjson` | 18 ingest operations: upserts, an idempotent repeat, an image replacement, a position conflict, an asset unpublish, an invalid GTIN, a product unpublish + republish, deletes |
| `ingest/expected-results.json` | expected `result` (and `obx_error`) per operation |
| `ingest/expected-changes.json` | expected live `/changes` entries per operation (`after_op`), in order |
| `ingest/files/` | the images the script references by sha256 (upload them as blobs first) |
| `examples/*.json` | one or more valid example documents per schema (file name prefix = schema name) and a webhook signature test vector |

## Replaying the ingest script

1. Fresh node, setup done (first brand `kestrel`), a retailer API client without brand restriction.
2. Read `/changes` without cursor until `phase` is `live`; keep the cursor.
3. Upload every file in `ingest/files/` as a blob (`PUT /obx/v1/ingest/blobs/{sha256}`).
4. For each line of `script.ndjson` (index `i`): send it with the matching endpoint (`upsert` → PUT, `patch`/`publish`/`unpublish` → PATCH with merge patch, `delete` → DELETE). On nodes whose `IngestInfo.renditions.server_generated` is false, add client-generated `web`/`square`/`thumb` blobs to `data.files` first. Compare with `expected-results.json[i]`. If the operation is an asset upsert that did not fail, poll `GET /obx/v1/ingest/assets/{external_id}` until `visibility` is not `pending_files` (max 60 s). Then read `/changes` from the cursor and compare the entries (`type`, `op`, `external_id`, order) with the `expected-changes.json` entries whose `after_op` is `i`.
