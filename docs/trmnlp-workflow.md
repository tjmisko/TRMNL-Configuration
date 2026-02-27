# TRMNL Plugin Development Workflow

Reference guide for editing, previewing, and deploying the GooseTRM TRMNL plugin.

## Architecture Overview

```
fetch scripts ─┐
               ├─▶ update ─▶ trmnl.json ─▶ retend.app (hosted)
BART binary ───┘                                │
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

## Key Files

| File | Purpose | Edit frequency |
|------|---------|----------------|
| `plugin/src/full.liquid` | Display template (Liquid + TRMNL Design System) | Often |
| `plugin/src/settings.yml` | Plugin metadata: polling URL, refresh interval, name | Rarely |
| `plugin/.trmnlp.yml` | Local dev server config: watched paths, variable overrides | Rarely |
| `trmnl.json` | Generated data payload (read-only reference) | Never (auto-generated) |

## Available Data Variables

These variables are available in `full.liquid` via the polled `trmnl.json`:

| Variable | Type | Shape |
|----------|------|-------|
| `date` | string | `"Monday, 23 February 2026"` |
| `week` | string | `"Week 09"` |
| `greetings` | string | `"Greetings from retend.app"` |
| `is_sunday` | boolean | `true` when today is Sunday |
| `is_last_day_of_month` | boolean | `true` on the last calendar day of the month |
| `bart` | array | `[{"depart": "20:28", "arrive": "20:43"}, ...]` |
| `weather.sf` | object | `{"high": 62, "low": 54, "rain": true, "rain_chance": 32, "alerts": []}` |
| `weather.oakland` | object | Same shape as `weather.sf` |
| `birthdays` | array | List of people with birthdays today |
| `tasks` | array | Tasks due today |
| `events` | array | Calendar event strings (currently hardcoded to `[]`) |
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
polling_headers: content-type=application/json
polling_body: ''
refresh_interval: 15        # minutes: 15 | 60 | 360 | 720 | 1440
no_screen_padding: 'no'     # 'yes' | 'no'
dark_mode: 'no'             # 'yes' | 'no'
id: 243914                  # plugin ID (do not change)
name: TRMNL Smartscreen     # display name
```

## .trmnlp.yml Reference

```yaml
watch:                      # paths to watch for hot-reload
  - src
  - .trmnlp.yml
custom_fields: {}           # override settings.yml custom fields
variables:
  trmnl: {}                 # override trmnl.* Liquid variables
```

Environment variables available via `{{ env.VAR_NAME }}` interpolation in this file only.
