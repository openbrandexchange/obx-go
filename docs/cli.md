# The obx command

`obx` publishes a brand's product data and images to its Open Brand Exchange portal, and keeps a
local copy of a brand's catalog for retailers (`obx pull`). This page is the reference; the
[README](../README.md) has the quickstart.

## Getting help

```sh
obx help                 # every command
obx help push            # one command: what it does, its options, examples
obx push --help          # the same
obx version
```

A wrong option or command says what is wrong and which help to read. Errors are written for people:
"the address brands.kestrel.exmple does not exist. Check it for typing mistakes." rather than a Go
error. With `--json` (`push`, `info`, `status`, `config`) the output is for scripts.

## Languages

`obx` speaks the 25 languages of the portal's screens: bg cs da de el en es et fi fr ga hr hu it lt
lv mt nb nl pl pt ro sk sl sv. It picks the language from, in this order:

1. `OBX_LANG` (for example `OBX_LANG=nl`),
2. `LC_ALL`, `LC_MESSAGES`, `LANG` and `LANGUAGE` (as set by Linux and macOS terminals, for
   example `nl_NL.UTF-8`),
3. on Windows, the display language of the account,
4. otherwise English.

The words are the portal's: *access key* (under *Automatic upload → Access keys*), *API key for your
software*, *EAN*. Problems that the portal reports keep the portal's own (English) detail after the
translated reason.

## Where obx keeps things

| | Linux | macOS | Windows |
|---|---|---|---|
| settings (`config.json`: portal address and access key per portal, mode 0600) | `~/.config/obx/` (or `$XDG_CONFIG_HOME/obx/`) | `~/Library/Application Support/obx/` | `%AppData%\obx\` |
| cache (image version hashes, open uploads, watch state) | `~/.cache/obx/` (or `$XDG_CACHE_HOME/obx/`) | `~/Library/Caches/obx/` | `%LocalAppData%\obx\` |

`OBX_CONFIG_DIR=/some/folder` puts both in one folder (the container image uses `/home/obx/.obx`;
mount a volume there to keep the cache). `obx config` shows the folders, the connected portals
(keys shortened) and the language in use; `obx config path` prints only the settings file.

Deleting the cache is always safe: the next push asks the portal again which images it has, and an
interrupted upload starts from the first part.

## Environment variables

| Variable | |
|---|---|
| `OBX_URL` | portal address, instead of `obx login` or `--url` |
| `OBX_TOKEN` | access key (starts with `obxi_`), instead of `obx login` or `--token` |
| `OBX_CLIENT_ID`, `OBX_CLIENT_SECRET` | API key for your software, for `obx pull` |
| `OBX_PASSWORD` | password for `obx login --email` in scripts (otherwise it is asked, hidden) |
| `OBX_LANG` | language of the messages |
| `OBX_CONFIG_DIR` | one folder for settings and cache |

Flags win over environment variables, and environment variables over what `obx login` stored.

## Shell completion

```sh
obx completion bash > /etc/bash_completion.d/obx                 # or: source <(obx completion bash) in ~/.bashrc
obx completion zsh > "${fpath[1]}/_obx"                          # then start a new shell
obx completion fish > ~/.config/fish/completions/obx.fish
obx completion powershell >> $PROFILE
```

The Homebrew formula and the release archives (`completions/`) include the bash, zsh and fish scripts.

## obx push

```sh
obx push ./export                                   # a folder: products.csv, optional assets.csv, images anywhere below it
obx push --csv products.csv --images ./images       # separate files (images: a folder or a zip)
obx push --package export.zip                       # one zip with everything
obx push ./export --dry-run                         # check only
obx push ./export --mirror                          # also take offline what the file no longer contains
```

What happens:

1. obx reads the files and lists the images (a name that appears twice is reported; the first one
   is used).
2. For every image it computes the sha256 and asks the portal whether it has it. Only new or changed
   images are uploaded; images larger than the portal's single-upload limit go in parts
   (resumable). When the portal does not make web, square and thumbnail versions itself, obx makes
   them and uploads them too; their hashes are cached, so unchanged images are never processed twice.
3. It starts one import with the CSV files and the list of images, waits for the portal to process
   it (with a progress bar) and prints a summary: rows and images, how many articles and images are
   new, updated, unchanged or taken offline, and every problem with file, row and column.

On a terminal the progress bar shows percentage, uploaded bytes and the time left; in a log file or
CI it prints a line every few seconds. `--parallel 8` checks and uploads more images at the same
time (default 4, at most 16).

**Dry run.** `--dry-run` uploads nothing and changes nothing. obx hashes the images, asks the portal
which it already has, and asks the portal to check the import without writing. The summary shows
the same counts and problems as a real run, plus how many images a real run would upload.

**Interrupted uploads.** Parts that reached the portal stay there. Run the same command again: obx
remembers the open upload (in the cache folder) and sends only the missing parts.

**Mirror.** `--mirror` takes offline the products of the brands in the file that the file no longer
contains (their images and article data disappear for retailers; nothing is deleted). When that
would be more than half of those products, the portal refuses and nothing changes, so an empty or
truncated export cannot wipe the catalog. `--allow-mass-unpublish` allows it on purpose.

**Exit codes**: `0` all good, `2` done, but some rows or images had problems (listed; the rest was
published), `1` failed (nothing or not everything was published; the message says why).

## obx watch

```sh
obx watch /data/outbox
```

Publishes whenever the folder changes, after it has been quiet for `--settle` (default 10 s), so
half-written exports are never sent. It checks every `--interval` (default 5 s), logs one line per
event with a timestamp, and remembers what it published last (in the cache folder), so a restart
does not publish the same files again. A failed push is retried on the next change. `--once`
publishes once if anything changed and stops (for cron). Stop it with Ctrl-C or `SIGTERM`; it
finishes cleanly. See [automation.md](automation.md) for running it as a service.

## obx status, info, login, logout

- `obx status` lists the recent uploads made with this access key; `obx status --job <id>` lists
  every problem of one upload.
- `obx info` checks the address and key and shows brands, content languages and limits.
- `obx login <portal>` stores an access key for this computer (paste one, or sign in with e-mail and
  password and obx creates a key named "obx on <computer> (<date>)"). The password is never stored.
- `obx logout` forgets the key on this computer; switch the key off in the portal to stop it
  working everywhere.

## obx pull (retailers)

```sh
obx pull https://brands.kestrel.example --out ./kestrel --renditions web,original --follow
```

Keeps a folder in sync with a brand's change feed, using the API key for your software that the
brand gave you (`--client-id`/`--client-secret` or `OBX_CLIENT_ID`/`OBX_CLIENT_SECRET`). Products,
variants and images are written as JSON under `products/`, `variants/` and `assets/`; image files
under `images/<style>/<version>/`, each checked against its sha256. What the brand takes offline
is removed. The position in the feed is kept in `.obx/cursor`, so a run after an interruption
continues where it stopped. `--follow` keeps running and checks every `--interval` (default 60 s).
