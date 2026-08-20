# GooseTRM

A personal e-ink dashboard for a [TRMNL](https://trmnl.com/) device.

A handful of shell scripts and two Go binaries gather the day — calendar events,
tasks, weather, BART departures, checklists, birthdays — into a single
`trmnl.json`. The device polls that file every 15 minutes and renders it through
a Liquid template onto an 800×480, 2-bit grayscale display.

```
  birthday/fetch ─┐
  tasks/fetch     │
  weather/fetch   ├──▶  ./update  ──▶  trmnl.json  ──▶  retend.app
  checklists/fetch│                                          │  (nginx, token-gated)
  events/fetch    │                                          ▼
  BART/fetch     ─┘                            TRMNL device polls every 15 min
                                                             │
                                                             ▼
                                            plugin/src/full.liquid renders it
```

Two halves, and they deploy independently:

- **The data.** `./update` runs on whatever host serves `retend.app` and writes
  `trmnl.json` into the web root. Changing a fetch script means pulling and
  re-running `update` there — no plugin push involved.
- **The display.** `plugin/src/full.liquid` lives on TRMNL's servers. Changing
  the layout means `./trmnlp push` — no data deploy involved.

## Quick start

Requires `jq`, `rg`, `curl`, `go`, `unzip`, plus `docker` and `python3` for the
plugin preview.

```sh
git clone https://github.com/tjmisko/TRMNL-Configuration.git
cd TRMNL-Configuration
./setup
```

`./setup` is idempotent — run it again after any pull. It checks prerequisites,
creates `.env` (prompting for your notes vault path), downloads BART's static
GTFS schedule, builds the Go binaries, seeds every `events/*.conf` from its
`.example`, reports config a pull could not update, and finishes with a smoke
test that writes a real `trmnl.json`.

## Everyday use

**Refresh the data:**

```sh
./update            # re-runs every fetch script, rewrites trmnl.json
```

A fetch script that fails never takes the dashboard down — `update` validates
each one's JSON and substitutes an empty fallback, so a broken feed costs you
one section rather than the whole screen. BART is the deliberate exception: it
reports an outage as `bart_status: error` so the template can render
"Unavailable" instead of implying there are no trains.

**Change the display:**

```sh
./trmnlp serve      # preview at localhost:4567, hot-reloads on save
./trmnlp push       # upload the plugin to TRMNL
```

`serve` points the plugin at a local copy of `trmnl.json`, so the preview shows
your current data. Edit `plugin/src/full.liquid` and the browser updates itself.

**Authenticate (once):**

```sh
./trmnlp login      # saves an API key to ~/.config/trmnlp/config.yml
```

## Configuration

Every file below is gitignored and seeded from a committed `.example`. They hold
real feed URLs, vault paths, and personal schedule details, and this repo is
public.

| File | Holds |
|------|-------|
| `.env` | Notes vault path, project path, the polling token mirror |
| `events/feeds.conf` | Luma / Google Calendar `.ics` subscription URLs |
| `events/ignore.conf` | Title globs for events to hide |
| `events/tags.conf` | Event tags, their headings, and their order |
| `events/sources.conf` | Which pipeline stages are switched on |

Because they are gitignored, **`git pull` cannot update them**. A pull that adds
a tag or a pipeline stage brings the code but not the line that enables it, and
the dashboard keeps rendering the old thing without complaining. `./setup`
reports this drift at the end of every run; it only ever reports, since copying
an example over would clobber real config.

## The polling token

`trmnl.json` is your calendar and your commute on the public internet, so nginx
gates it on a shared secret and 404s anything without a matching
`X-Plugin-Token`.

**That secret is not in this repo and must not be put back into it.** It lives in
the plugin's `plugin_token` custom field on trmnl.com and in the nginx config on
the server. What is committed here is a reference — `{{ plugin_token }}` — which
TRMNL expands when it polls.

See [docs/trmnlp-workflow.md](docs/trmnlp-workflow.md#the-polling-token) for how
to rotate it and which order to do it in.

## Repository layout

```
update                  orchestrator — runs every fetch, writes trmnl.json
setup                   idempotent bootstrap
trmnlp                  Docker wrapper around the trmnlp CLI

birthday/fetch          ─┐
tasks/fetch              │  each prints JSON on stdout,
weather/fetch            ├─ each degrades to a safe empty value
checklists/fetch         │
events/                  │  a two-stage pipeline; see events/README.md
BART/                   ─┘  Go binary + GTFS schedule handling

plugin/src/full.liquid  the display template
plugin/src/settings.yml the plugin definition pushed to TRMNL
plugin/.trmnlp.yml      local dev-server config
```

## Further reading

| Document | Covers |
|----------|--------|
| [docs/trmnlp-workflow.md](docs/trmnlp-workflow.md) | Plugin editing, the polling token, the TRMNL design system, troubleshooting |
| [events/README.md](events/README.md) | The events pipeline: sources, filters, tags, recurring vault notes |
| [TRMNLP.md](TRMNLP.md) | Vendored upstream reference for the `trmnlp` tool itself |
