# TRMNL Plugin Editor

Edit the GooseTRM TRMNL plugin template and configuration, preview locally, and push to the device.

## Trigger

User invokes `/trmnl-edit` with a description of the desired change (or a raw Liquid/HTML snippet).

## Process

### 1. Understand the current state

Read these files to understand the current template and available data:

- `plugin/src/full.liquid` — current display template
- `trmnl.json` — current data payload (know what variables exist and their shapes)
- `plugin/src/settings.yml` — plugin definition (polling URL and headers, custom fields, refresh interval)
- `plugin/.trmnlp.yml` — dev server config, including local custom-field values

If the change involves data fields, also read the relevant fetch scripts (`*/fetch`) to understand the full data shape.

### 2. Research TRMNL docs (use a subagent)

Before making template edits, spawn an Explore subagent to research the relevant TRMNL documentation. The subagent should:

- Read `docs/trmnlp-workflow.md` for the local design system quick reference
- If the local reference is insufficient, fetch these URLs for current docs:
  - `https://trmnl.com/framework/docs` — design system component docs
  - `https://trmnl.com/framework/docs/layout` — layout classes
  - `https://trmnl.com/framework/docs/table` — table classes
  - `https://trmnl.com/framework/docs/title` — typography
  - `https://docs.trmnl.com/go/private-plugins/templates` — templating guide
- Return the specific classes, patterns, and Liquid syntax needed for the requested change

### 3. Edit the template

Apply the changes to `plugin/src/full.liquid` (or other config files if explicitly requested).

**Rules:**
- Only edit `full.liquid` unless the user explicitly asks to modify other files
- Never modify `settings.yml` fields `id`, `polling_url`, `polling_headers`, `custom_fields`, or `name` without explicit ask
- **Never put a secret in `settings.yml`** — it is tracked and the repo is public. The polling token is a `plugin_token` custom field whose value lives on trmnl.com; the file only carries `{{ plugin_token }}`. `{{ env.X }}` does not expand there either (it is parsed as plain YAML) — `.trmnlp.yml` is the only file where that works
- Never touch data pipeline files (`update`, `*/fetch` scripts, BART Go code)
- Never create git commits or branches — this is a deploy-to-device workflow
- Use TRMNL Design System classes, not custom CSS (the device loads `plugins.css`)
- The display is 800x480px, 2-bit grayscale e-ink — no color, no animations

### 4. Start the dev server

Run the dev server in the background for preview:

```sh
./trmnlp serve
```

Run it from the repo root — the wrapper resolves the plugin directory itself.

Tell the user to open `http://localhost:4567` to preview changes. The server hot-reloads on file save.

### 5. Iterate

If the user wants adjustments, edit `full.liquid` and let the dev server hot-reload. Repeat until satisfied.

### 6. Push (on confirmation only)

When the user is ready, ask for explicit confirmation before pushing:

```sh
./trmnlp push
```

After a successful push, open the TRMNL dashboard in the browser:

```sh
xdg-open https://trmnl.com/dashboard
```

### 7. Stop the dev server

After pushing (or when the user is done), stop the background dev server process.

## Design System Quick Reference

The device is 800x480px, 2-bit grayscale e-ink.

### Layout
- `layout` — one per view, wraps all content
- `layout--row` / `layout--col` — direction
- `layout--center` / `layout--left` / `layout--right` — alignment
- `columns` + `column` — zero-config column grid
- `flex` / `flex--row` / `flex--col` — flexible containers

### Typography
- `title` (26px), `title--small` (16px), `title--large` (30px), `title--xlarge` (35px)
- `label` (16px), `label--small` (16px alt font), `label--large` (21px)
- `description` (16px), `description--large` (16px alt font)
- `value` (38px) + size scale from `value--xxsmall` to `value--peta`

### Tables
- `table` + `table--small` / `table--large` / `table--indexed`
- Headers: `<th><span class="title title--small">...</span></th>`
- Cells: `<td><span class="label">...</span></td>`
- `data-table-limit="true"` for overflow handling

### Footer
```html
<div class="title_bar">
  <img class="image" src="">
  <span class="title">Main</span>
  <span class="instance">Secondary</span>
</div>
```

### Other
- `divider`, `richtext`, `item`, `progress`, `chart`

## Available Data Variables

**`trmnl.json` in the repo root is the ground truth** — read it. It is regenerated
by `./update` and always matches what the device will actually receive.

The annotated table of every variable, with types and shapes, lives in
`docs/trmnlp-workflow.md` under "Available Data Variables". Read that for the
semantics (which fields can be null, what `bart_status: error` means, how
`events` is split into sections). It is not duplicated here, because a second
copy is a copy that goes stale — this one did, and silently omitted `events`,
`bart_status`, `weather.uv`, `weather.berkeley_marina` and `rain_alert` for
several releases.

Access arrays: `{{ bart | slice: 0 }}`, nested objects: `{{ weather.sf.high }}`.

## Refreshing data (only if asked)

```sh
./update
```
