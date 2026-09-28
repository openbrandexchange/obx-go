# Automating uploads

Every way of running `obx` reads the portal address and the access key from `OBX_URL` and
`OBX_TOKEN`, so the same command works in cron, CI and containers. Create one access key per system
(*Automatic upload → Access keys → New access key* in the portal), so you can switch one off
without affecting the others. The spec's [automation recipes](https://github.com/openbrandexchange/spec/blob/main/docs/automation.md)
cover n8n, Make and Zapier.

## A folder your PIM or ERP writes to: `obx watch`

**systemd** (Linux): [examples/systemd/obx-watch.service](../examples/systemd/obx-watch.service).

```sh
sudo install -m 0755 obx /usr/local/bin/obx
sudo mkdir -p /etc/obx && sudo sh -c 'echo "OBX_TOKEN=obxi_…" > /etc/obx/token.env' && sudo chmod 600 /etc/obx/token.env
sudo cp examples/systemd/obx-watch.service /etc/systemd/system/
sudo systemctl edit obx-watch            # set OBX_URL and the folder
sudo systemctl enable --now obx-watch
journalctl -u obx-watch -f               # one line per publish, with the result
```

The unit runs as a dynamic user with a read-only view of the export folder, restarts after a
failure and keeps its state in `/var/lib/obx`.

**Docker Compose**: [examples/docker-compose.yml](../examples/docker-compose.yml). The image
`ghcr.io/openbrandexchange/obx` runs as an unprivileged user; mount the export folder read-only and
a volume on `/home/obx/.obx` for the state. `docker compose logs -f obx-watch` shows the log.

**Windows**: run `obx watch D:\Export` as a scheduled task at start-up, or use `obx push` nightly (below).

## A nightly job: `obx push`

[examples/cron.txt](../examples/cron.txt) has a crontab line and the Windows Task Scheduler
commands. Use the exit code: `0` all good, `2` published with problems (listed in the log), `1`
failed. A nightly run costs nothing when nothing changed.

## Exports in Git: the GitHub Action

See [push-action/README.md](../push-action/README.md): publish on every change to the export folder
and every night, check pull requests with `dry-run: true`, and read the result in the job summary.

## Other CI systems

GitLab CI, Bitbucket Pipelines, Azure DevOps or Jenkins: download obx from the releases page (or use
the container image) and run `obx push`:

```yaml
# .gitlab-ci.yml
publish:
  image:
    name: ghcr.io/openbrandexchange/obx:v0
    entrypoint: [""]          # the image's entrypoint is obx; GitLab needs a shell
  script: ["obx push export"]
  variables: { OBX_URL: "https://brands.kestrel.example" }   # OBX_TOKEN as a masked CI variable
```

## Good to know

- **Rate limits.** When the portal asks obx to slow down (429), obx waits as long as the portal
  asks and says so in the log.
- **Big catalogs.** Only changed images are sent; the first run of a large catalog takes as long as
  the upload of all images, later runs seconds. Interrupted runs continue where they stopped.
- **Mirror safely.** Use `--mirror` only when the export always contains the whole catalog of the
  brands in it; the portal refuses to take more than half offline unless you allow it.
- **Keys.** Rotate or switch off a key in the portal when a system is retired; `obx logout` only
  forgets it on one computer.
