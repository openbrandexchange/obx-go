# Online compatibility check: deployment

The online check on openbrandexchange.com (`/<lang>/check/`) runs the unchanged
`obx-conformance check-server` in a Cloudflare Container. Everything here is deployed to the
Open Brand Exchange Cloudflare account (`CLOUDFLARE_ACCOUNT_ID=34a316f1061b80c0053fffe17c0413c6`).

```
browser ── /api/check (Pages Function, website) ──service binding──▶ obx-checker Worker
                                                                     ├─ Turnstile siteverify
                                                                     ├─ Limiter (Durable Object, EU)
                                                                     ├─ CheckerContainer (lite, EU, max 2)
                                                                     └─ R2 obx-check-reports (EU, 30 days)
```

| Piece | What | Where |
| --- | --- | --- |
| `obx-checker` | Worker + `CheckerContainer` (this folder) | no public URL: only the website's `CHECKER` service binding |
| `obx-demo` | Worker + `DemoContainer` (`demo/`) | <https://obx-demo.richardlindhout96.workers.dev> |
| `obx-check-reports` | R2 bucket, EU jurisdiction, lifecycle rule `expire-30d` on `reports/` | shareable results |

## Limits

- Per visitor: 5 checks per hour (HMAC of the IP address, kept for an hour).
- Per portal: one check at a time, 10 minutes cooldown, 10 per day (the demo portal has no
  cooldown or daily cap).
- In total: 200 checks per day, 2 at the same time (one per `lite` container instance,
  `max_instances: 2`).
- Per check (container flags): 800 requests, 32 MiB, one request per 100 ms, 3 minutes; https on
  port 443 to public addresses only; a 429 with a long Retry-After stops the check.
- Turnstile (widget "openbrandexchange.com compatibility check", managed) is verified in the Worker.
  The site key is public and lives in the website; the secret is a Worker secret.

The limits are `vars` in `wrangler.jsonc`. Logs (Workers observability and the container's
stderr) carry the portal host, duration and counts only: never the client id, secret or tokens.
The secret is redacted from the report by the check and once more by the Worker.

## Deploy

```sh
npm ci
CLOUDFLARE_ACCOUNT_ID=34a316f1061b80c0053fffe17c0413c6 npx wrangler deploy                      # obx-checker
CLOUDFLARE_ACCOUNT_ID=34a316f1061b80c0053fffe17c0413c6 npx wrangler deploy -c demo/wrangler.jsonc # obx-demo
```

Docker must run (wrangler builds the images from the repository root). Without Docker,
`sh push-image.sh checker|demo` cross-compiles the binary, pushes the same image to
`registry.cloudflare.com` with crane and prints its reference; put that reference as `image` in a
copy of the config (`.wrangler-deploy-*.jsonc`, ignored by git, without `image_build_context`) and
deploy with `-c`. The first deploy (2026-11-12) went that way.

Secrets of `obx-checker`,
set once with `npx wrangler secret put <NAME>`:

- `TURNSTILE_SECRET`: the widget's secret key.
- `IP_SALT`: a random string for the IP HMAC.
- `OPERATOR_TOKEN` (optional): requests with `X-OBX-Operator: <token>` skip Turnstile (not the
  limits), for scripted tests by the owner. Remove with `npx wrangler secret delete OPERATOR_TOKEN`.

The website binds the Worker in its `wrangler.toml` (`[[services]] binding = "CHECKER"`).

## The demo portal

`obx-demo` serves `obx-conformance mock --read-only` with the Kestrel Outfitters test data
(spec `fixtures/kestrel`). Only the protocol's reads are answered; writes answer 403, the ingest
and admin APIs 404. The data is in memory: a restart (the container sleeps after 15 minutes
without requests) resets it. Its API key is published on purpose; it can only read the demo:

- Portal: `https://obx-demo.richardlindhout96.workers.dev`
- Client id: `obxc_demo-kestrel`
- Client secret: `obxs_demo-read-only-public`

With this key every check that can run online passes; P-AUTH-3 and P-FEED-4 need a second key
and are not checked online. A cold start takes a few seconds while the fixtures load.
