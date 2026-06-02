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
| `date`    | string          | local `"YYYY-MM-DD"` the occurrence falls on    |
| `sort`    | string          | `"YYYY-MM-DDTHH:MM"` ordering key                |
| `source`  | string          | provenance (`recurring`, `luma`, …); internal   |

`date`, `sort`, and `source` are used only by the aggregator — the device
renders just **time + title**. Overlapping events are kept on purpose
(conflicts are allowed); nothing is de-duplicated.

## Configuration (.env)

```sh
LUMA_ICS_URL=""                       # Luma "Add to Calendar" .ics URL (blank → no Luma events)
NOTES_EVENTS_SUBDIR="events"          # subdir of NOTES_DIRECTORY holding recurring-event notes
EVENTS_TZ="America/Los_Angeles"       # zone used to resolve "today" and localize times
```

## Recurring-event notes

Put one Markdown note per recurring event under
`$NOTES_DIRECTORY/$NOTES_EVENTS_SUBDIR` with simple frontmatter:

```markdown
---
start: "18:00"        # 24-hour local time (omit for an all-day marker)
end:   "20:00"        # optional
day:   Tuesday        # weekday; also accepts `days: [Tue, Thu]`
title: Climbing Club  # optional; defaults to the filename
---
Notes body is ignored.
```

Weekday matching is case-insensitive and accepts full (`Tuesday`) or 3-letter
(`Tue`) names. Multiple weekdays via `days: [Mon, Thu]` or `day: Mon, Thu`.

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
