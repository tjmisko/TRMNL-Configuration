# Events

Today's calendar events for the TRMNL dashboard, blended from multiple sources.

## How it fits together

```
update                 →  EVENTS=$(events/fetch)            →  trmnl.json {.events}
                                  │
events/fetch (aggregator)         │  runs every executable in sources/,
                                  │  merges, filters to today, sorts
        ┌─────────────────────────┴─────────────────────────┐
sources/recurring (bash+jq)                       sources/luma (Go binary)
   Notes repo weekly events                          Luma .ics feed
   $NOTES_DIRECTORY/$NOTES_EVENTS_SUBDIR             $LUMA_ICS_URL
```

`events/fetch` is the only place that knows the time window ("today"). Each
source adapter is a standalone executable that prints a JSON **array** of
normalized event objects and exits 0 even on failure (empty array). This keeps
the dashboard resilient: a broken or unconfigured source never breaks the rest.

## Normalized event schema

Every adapter emits objects with this shape:

| field     | type            | notes                                           |
|-----------|-----------------|-------------------------------------------------|
| `title`   | string          | event name (the only text shown on-screen)      |
| `start`   | string \| null  | local 24-hour `"HH:MM"`; `null` for all-day     |
| `end`     | string \| null  | local 24-hour `"HH:MM"`; optional               |
| `all_day` | bool            | true → rendered as "All day"                    |
| `message` | string \| null  | optional note; indented sub-line under the event|
| `date`    | string          | local `"YYYY-MM-DD"` the occurrence falls on    |
| `sort`    | string          | `"YYYY-MM-DDTHH:MM"` ordering key                |
| `source`  | string          | provenance (`recurring`, `luma`, …); internal   |

`date`, `sort`, and `source` are used only by the aggregator — the device
renders **time + title**, plus the optional `message` as an indented line
beneath it. Sources that have no message may omit the field. Overlapping events
are kept on purpose (conflicts are allowed); nothing is de-duplicated.

## Configuration (.env)

```sh
LUMA_ICS_URL=""                       # Luma "Add to Calendar" .ics URL (blank → no Luma events)
NOTES_DIRECTORY="/path/to/vault"      # Notes vault scanned for recurring-event notes (shared with tasks)
EVENTS_TZ="America/Los_Angeles"       # zone used to resolve "today" and localize times
```

## Recurring-event notes

The `recurring` source scans the **entire** `$NOTES_DIRECTORY` (the same vault
the tasks source uses — no dedicated subdir). A note is treated as a recurring
event when its frontmatter is tagged with **both** `event` and `recurring`:

```markdown
---
start: 20:00          # 24-hour local time (omit for an all-day event)
end:   23:30          # optional
weekday: Monday       # full or 3-letter; also accepts a CSV list (Mon, Thu)
message: Bring your ID # optional; rendered as an indented sub-line
title: Shades         # optional; defaults to the note's filename
tags:
  - event
  - recurring
---
Notes body is ignored.
```

The note is shown only on days matching `weekday`. Weekday matching is
case-insensitive and accepts full (`Monday`) or 3-letter (`Mon`) names; multiple
days via `weekday: Mon, Thu`. `tags` may be a YAML list (as above), an inline
`[event, recurring]` array, or a CSV. The title defaults to the filename.

## Adding a new source

Drop a new executable into `events/sources/` that prints the normalized array
and exits 0. That's it — the aggregator discovers it automatically.

- **Quick/script source** → bash + jq (see `sources/recurring`).
- **Network/parsing-heavy source** (another ICS feed, an API) → a Go binary
  built into `events/sources/<name>` (see `luma/`, mirroring `BART/`). Add its
  build step to `setup` and its output path to `.gitignore`.

Read any per-source config from the environment; the aggregator exports
`NOTES_DIRECTORY`, `NOTES_EVENTS_SUBDIR`, `LUMA_ICS_URL`, `EVENTS_TZ`, and
`EVENTS_TODAY` (use `EVENTS_TODAY` so every adapter agrees on the date even
across a midnight boundary).
