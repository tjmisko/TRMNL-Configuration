# TRMNL Plugin Development Workflow

Reference guide for editing, previewing, and deploying the GooseTRM TRMNL plugin.

## Architecture Overview

```
fetch scripts ─┐
               ├─▶ update ─▶ trmnl.json ─▶ retend.app (hosted)
BART/fetch ────┘                                │
                                                ▼
                                    TRMNL device polls endpoint
                                                │
                                                ▼
                              plugin/src/full.liquid renders data
```

**Local dev flow** (`./trmnlp serve`):

```
                  trmnl.json
                      │
  python3 -m http.server :9473
                      │
       host.docker.internal:9473  ◄── --add-host bridge (Linux)
                      │
              Docker (trmnlp)
                      │
              localhost:4567 ─▶ browser preview
```

The plugin uses a **polling strategy**: the TRMNL device fetches `trmnl.json` from `https://retend.app/trmnl.json` every 15 minutes, then renders the data through `full.liquid`. During local development, `./trmnlp serve` temporarily redirects polling to a local HTTP server so the preview reflects your current `trmnl.json`.

## The polling token

`trmnl.json` is your calendar, your task list, and your commute, served over the
public internet. nginx gates it on a shared secret and returns **404** — not
401 — to any request without a matching `X-Plugin-Token` header, so the endpoint
does not advertise itself.

The secret is **not in this repo**, and must not be put back into it. It lives in
two places:

| Where | What holds it | How to change it |
|-------|---------------|------------------|
| TRMNL | the plugin's `plugin_token` custom field | trmnl.com → plugin → Plugin token |
| Server | the nginx gate on `retend.app` | edit the site config, `nginx -t`, reload |

What this repo ships is a *reference* to it. `settings.yml` declares the field and
interpolates it into the header:

```yaml
polling_headers: content-type=application/json&x-plugin-token={{ plugin_token }}
custom_fields:
- keyname: plugin_token
  field_type: password
  name: Plugin token
```

TRMNL expands `{{ plugin_token }}` server-side when it polls. Because the file
holds only the reference, it is tracked normally.

`.trmnlp.yml` supplies the same field locally from `$TRMNL_PLUGIN_TOKEN`, which
is the *only* file where trmnlp expands `{{ env }}` — `settings.yml` is parsed as
plain YAML, so an interpolation there would upload literally. `./trmnlp serve`
repoints polling at a local copy of `trmnl.json` and never sends the header, so
local development works with the variable unset.

**Rotating.** Change the nginx side first, then the custom field — the reverse
order breaks every poll until nginx catches up. One missed refresh is the worst
case either way. Verify with:

```sh
curl -so /dev/null -w '%{http_code}\n' https://retend.app/trmnl.json                    # want 404
curl -so /dev/null -w '%{http_code}\n' -H "x-plugin-token: $TOKEN" https://retend.app/trmnl.json  # want 200
```

Rotation is the only real revocation. A token that has been committed stays
readable in history forever, so replacing the value is what closes the exposure —
rewriting history is not required and is not worth a force-push on a public repo.

## Key Files

| File | Purpose | Edit frequency |
|------|---------|----------------|
| `plugin/src/full.liquid` | Display template (Liquid + TRMNL Design System) | Often |
| `plugin/src/settings.yml` | Plugin definition: polling URL and headers, custom fields, refresh interval, `id`, name | Rarely |
| `plugin/.trmnlp.yml` | Local dev server config: watched paths, custom-field values, variable overrides | Rarely |
| `trmnl.json` | Generated data payload (read-only reference) | Never (auto-generated) |

## Available Data Variables

These variables are available in `full.liquid` via the polled `trmnl.json`:

| Variable | Type | Shape |
|----------|------|-------|
| `date` | string | `"Last (Fifth) Monday, 31 August 2026"` — the weekday is prefixed with which occurrence of it the date is in the month. The prefix is `First`–`Fifth`, except on the last occurrence of that weekday in the month, which reads `Last (Fourth)` or `Last (Fifth)` |
| `week` | string | `"Week 09"` |
| `day_of_year` | number | `243` — today's ordinal day, 1 on 1 January through 365 (366 in a leap year) on 31 December |
| `days_to_go` | number | `122` — days left in the year, excluding today, so it always sums with `day_of_year` to the year's length. 0 on 31 December |
| `greetings` | string | `"Greetings from retend.app"` |
| `is_sunday` | boolean | `true` when today is Sunday |
| `is_last_day_of_month` | boolean | `true` on the last calendar day of the month |
| `bart` | array | `[{"depart": "20:28", "arrive": "20:43"}, ...]`; empty only when there are genuinely no trains |
| `bart_status` | string | `"ok"`, or `"error"` when the data could not be fetched — render "Unavailable" rather than an empty list |
| `weather.sf` | object | `{"high": 62, "low": 54, "rain": true, "rain_chance": 32, "alerts": []}` |
| `weather.oakland` | object | Same shape as `weather.sf` |
| `weather.berkeley_marina` | object | `{"high": 62, "low": 57, "wind": {"dir": "W", "speed_kt": 10, "gust_kt": null, "source": "observed"}}` |
| `weather.uv` | object | `{"max": 7, "band": "High", "peak": "14:00"}`; `null` when the UV lookup fails. Only `max` is rendered, on the Weather heading line |
| `birthdays` | array | List of people with birthdays today |
| `tasks` | array | Tasks due today |
| `events` | array | Today's events as dashboard **sections**, `[{heading, tagged, events}]`, blended from `events/sources/*` and split by `events/tags.conf` (see `events/README.md`) |
| `checklists.sunday` | array | Sunday checklist items |
| `checklists.end_of_month` | array | End-of-month checklist items |
| `rain_alert.active` | boolean | `true` if rain is forecast in either city |
| `rain_alert.chance` | number | Max rain chance across SF and Oakland |
| `rain_alert.city` | string | City with the higher rain chance (`"San Francisco"` or `"Oakland"`) |
| `rain_alert.alerts` | array | Deduplicated union of weather alerts from both cities |

Access nested values with dot notation: `{{ weather.sf.high }}`.
Access array elements with the `slice` filter: `{{ bart | slice: 0 }}`.

## Development Commands

### 1. Start the dev server

```sh
./trmnlp serve
```

Opens `http://localhost:4567` with hot-reload. Edits to files in `plugin/src/` and `.trmnlp.yml` auto-refresh the preview.

Under the hood, the wrapper:

1. Starts `python3 -m http.server 9473` in the background, serving `trmnl.json` from the repo root.
2. Rewrites `polling_url` in `plugin/src/settings.yml` to `http://host.docker.internal:9473/trmnl.json` so the Docker container can reach the host.
3. Launches the `trmnlp` Docker container with `--add-host host.docker.internal:host-gateway` (required on Linux; macOS resolves this automatically).
4. On exit, a `trap` restores the original `polling_url` and kills the HTTP server.

> **Unclean shutdown caveat:** If the process is killed with `kill -9` or otherwise bypasses the trap, `settings.yml` will be left pointing at the local URL. Restore it with:
> ```sh
> git checkout plugin/src/settings.yml
> ```

### 2. Edit the template

Edit `plugin/src/full.liquid`. The dev server reloads automatically.

### 3. Push to TRMNL

```sh
./trmnlp push
```

Uploads the plugin to the TRMNL web service. Requires prior authentication.

The push command runs with `docker run -it` and prompts for confirmation, so it requires an interactive TTY. For scripted/non-interactive use:

```sh
echo "y" | docker run -i \
  --volume ~/.config/trmnlp:/root/.config/trmnlp \
  --volume ./plugin:/plugin \
  trmnl/trmnlp push
```

> **`trmnlp pull` goes the other way, and it does not merge.** It overwrites
> `src/` with whatever is on TRMNL's servers — including `full.liquid`. If the
> server's copy is older than yours (it usually is, since the server only changes
> when you push), pulling silently reverts your markup to the last pushed
> version. `git checkout` gets it back, but there is no prompt beyond a generic
> "local plugin files will be overwritten".
>
> Pull is for recovering settings edited through the web UI, and for that it is
> worth doing into a scratch directory first:
>
> ```sh
> mkdir -p /tmp/pullcheck/src && cp plugin/src/settings.yml /tmp/pullcheck/src/
> echo y | docker run --rm -i \
>   --volume ~/.config/trmnlp:/root/.config/trmnlp \
>   --volume /tmp/pullcheck:/plugin trmnl/trmnlp pull
> diff plugin/src/settings.yml /tmp/pullcheck/src/settings.yml
> ```

### 4. Authenticate (one-time)

```sh
./trmnlp login
```

Saves API key to `~/.config/trmnlp/config.yml`.

### 5. Refresh data (optional)

```sh
./update
```

Re-runs all fetch scripts and regenerates `trmnl.json`.

## Troubleshooting

### BART shows "Unavailable" or no trains

`BART/fetch` prints an empty array only when BART genuinely has no qualifying
trains. Anything else exits non-zero and `update` sets `bart_status` to
`error`, so run it directly to see why:

```bash
./BART/fetch            # orchestrates refresh + retry
./BART/bart -v          # the binary alone, with diagnostics
```

`bart` exit codes:

| Code | Meaning |
|------|---------|
| 0 | Success. An empty array means there really are no trains. |
| 2 | Static GTFS schedule missing, unreadable, or empty. |
| 3 | Realtime feed unreachable or unparseable (already retried 3×). |
| 4 | Static schedule resolves none of the realtime trips — it is out of date. |

On 2 and 4, `BART/fetch` re-downloads the schedule and retries once.

The static schedule lives in `BART/bart_gtfs/` and is **not** committed — BART
reissues it every few months and regenerates every `trip_id` when it does, so
a stale copy resolves nothing. `BART/refresh-gtfs` downloads it;
`--if-stale` only hits the network when the data is missing or past its last
service day in `calendar.txt`. Note that `feed_info.txt` carries a much more
conservative `feed_end_date` than `calendar.txt` and is not a reliable
staleness signal.

Because BART can regenerate trip IDs while `calendar.txt` still looks current,
the date check is only a cheap pre-filter. The authoritative signal is the
share of realtime trips the static data resolves, which `bart` measures on
every run (`-v` prints it).

```bash
./BART/refresh-gtfs     # force a download
(cd BART && go test ./...)   # unit tests for parsing and selection
```

### Port 4567 already allocated

The dev server binds to port 4567. If a previous container is still running:

```sh
docker ps -a --filter "publish=4567"
docker rm -f <container-id>
```

### `settings.yml` left with local URL after unclean shutdown

If `./trmnlp serve` was killed without cleanup (e.g. `kill -9`, terminal crash):

```sh
git checkout plugin/src/settings.yml
```

## Layout Notes

The `.column` class applies `gap: 10px` between all direct children. When building sections with a label above a list, wrap the label and its content in a single `<div>` so the gap appears between sections rather than between the label and its items:

```html
<!-- Good: gap applies between the two wrapper divs -->
<div class="column">
  <div>
    <div class="label">Section A</div>
    <div>...items...</div>
  </div>
  <div>
    <div class="label">Section B</div>
    <div>...items...</div>
  </div>
</div>

<!-- Bad: gap pushes the label away from its items -->
<div class="column">
  <div class="label">Section A</div>
  <div>...items...</div>
  <div class="label">Section B</div>
  <div>...items...</div>
</div>
```

## TRMNL Design System Quick Reference

The TRMNL device is an 800x480 pixel, 2-bit grayscale e-ink display. All styling uses the TRMNL Design System CSS classes.

Required assets (loaded by the dev server automatically):
- CSS: `https://trmnl.com/css/latest/plugins.css`
- JS: `https://trmnl.com/js/latest/plugins.js`

### Layout

```html
<div class="layout">         <!-- exactly one per view -->
  <div class="columns">      <!-- zero-config column grid -->
    <div class="column">...</div>
    <div class="column">...</div>
  </div>
</div>
```

**Layout modifiers:**
- `layout--row` / `layout--col` — direction
- `layout--left` / `layout--center-x` / `layout--right` — horizontal alignment
- `layout--top` / `layout--center-y` / `layout--bottom` — vertical alignment
- `layout--center` — center both axes
- `layout--stretch` / `layout--stretch-x` / `layout--stretch-y` — fill available space

**Flex container** (within layout):
- `flex` / `flex--row` / `flex--col`

### Typography

| Class | Font | Size | Use |
|-------|------|------|-----|
| `title` | BlockKie | 26px | Section headings |
| `title--small` | NicoClean | 16px | Table headers |
| `title--large` | Inter 425 | 30px | Prominent headings |
| `title--xlarge` | Inter 400 | 35px | Large headings |
| `title--xxlarge` | Inter 375 | 40px | Hero text |
| `label` | NicoClean | 16px | Data values, content |
| `label--small` | NicoPups | 16px | Secondary data |
| `label--large` | Inter 500 | 21px | Emphasized labels |
| `label--xlarge` | Inter 475 | 26px | Large labels |
| `description` | NicoPups | 16px | Supporting text |
| `description--large` | NicoClean | 16px | Larger descriptions |
| `value` | — | 38px | Large numeric displays |
| `value--xxsmall` to `value--peta` | — | 16px–380px | Graduated number sizes |

### Tables

```html
<table class="table">
  <thead>
    <tr>
      <th><span class="title title--small">Header</span></th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td><span class="label">Cell</span></td>
    </tr>
  </tbody>
</table>
```

**Table modifiers:**
- `table--small` / `table--xsmall` — compact rows
- `table--large` — spacious rows
- `table--indexed` — numbered index column

**Attributes:**
- `data-table-limit="true"` — overflow engine with "and X more" row
- `data-clamp="1"` — single-line truncation with ellipsis

### Title Bar (footer)

```html
<div class="title_bar">
  <img class="image" src="">
  <span class="title">Main text</span>
  <span class="instance">Secondary text</span>
</div>
```

### Other Components

- `divider` — horizontal rule
- `richtext` / `richtext--large` — rich text blocks
- `item` — list item component
- `progress` — progress bar
- `chart` — chart container

## settings.yml Reference

```yaml
strategy: polling           # polling | webhook | static
polling_url: https://...    # endpoint to fetch data from
polling_verb: get           # get | post
polling_headers: content-type=application/json&x-plugin-token={{ plugin_token }}
polling_body: ''
custom_fields:              # values live on TRMNL, never in this file
- keyname: plugin_token     # referenced as {{ plugin_token }} above
  field_type: password      # see form_fields.yml for the full type list
  name: Plugin token
  optional: false
refresh_interval: 15        # minutes: 15 | 60 | 360 | 720 | 1440
no_screen_padding: 'no'     # 'yes' | 'no'
dark_mode: 'no'             # 'yes' | 'no'
id: 243914                  # plugin ID (do not change)
name: TRMNL Smartscreen     # display name
```

TRMNL interpolates custom fields into `polling_url`, `polling_headers` and
`polling_body` only. This file is parsed as plain YAML by trmnlp, so `{{ env.X }}`
does **not** work here — see `.trmnlp.yml` below for that.

Never paste a live secret into this file. It is tracked, and the repo is public.

## .trmnlp.yml Reference

```yaml
watch:                      # paths to watch for hot-reload
  - src
  - .trmnlp.yml
custom_fields:              # local values for fields declared in settings.yml
  plugin_token: "{{ env.TRMNL_PLUGIN_TOKEN }}"
variables:
  trmnl: {}                 # override trmnl.* Liquid variables
```

Environment variables are available via `{{ env.VAR_NAME }}` interpolation **in
this file only** — which is exactly what makes it the right home for a secret.
The variable comes from `.env`, which is gitignored; leave it unset and `serve`
still works, since the local dev server never sends the polling header.
