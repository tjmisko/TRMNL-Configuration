# Events

Today's calendar events for the TRMNL dashboard, blended from multiple sources.

## How it fits together

```
update                 →  EVENTS=$(events/fetch)            →  trmnl.json {.events}
                                  │
events/fetch (aggregator)         │  runs every executable in sources/,
                                  │  merges, filters to today, sorts
        ┌─────────────────────────┴─────────────────────────┐
sources/recurring (bash+jq)                       sources/ics (Go binary)
   Notes repo weekly events                          N .ics feeds (Luma, Google…)
   $NOTES_DIRECTORY                                  events/feeds.conf
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
beneath it. Sources that have no message may omit the field. Genuine conflicts
(different events at overlapping times) are kept on purpose; only **exact
duplicates** — same title and same start, e.g. one event cross-posted to two
calendars — are collapsed to a single entry by the aggregator.

## Configuration

`.env` (general):

```sh
NOTES_DIRECTORY="/path/to/vault"      # Notes vault scanned for recurring-event notes (shared with tasks)
EVENTS_TZ="America/Los_Angeles"       # zone used to resolve "today" and localize times (Pacific w/ DST)
```

Calendar feeds live in their own file, **`events/feeds.conf`** (gitignored;
`setup` seeds it from `events/feeds.conf.example`). One feed per line,
`<label>  <url>`; `#` comments and blank lines ignored:

```sh
luma-commons   https://api.lu.ma/ics/get?u=...        # Luma: Subscribe / "Add to Calendar"
luma-personal  https://api.lu.ma/ics/get?u=...
gcal           https://calendar.google.com/.../basic.ics  # Google: Settings → Secret iCal address
```

`<label>` only tags the event's internal `source` field (never shown). A line
with just a URL gets a label derived from its host (`luma`/`gcal`/…). The legacy
`LUMA_ICS_URL` env var, if set, is still honored as one extra feed labeled `luma`.

### Recurring events (RRULE)

The `ics` adapter expands recurring feed events to today's occurrence. Supported:
`FREQ` DAILY/WEEKLY/MONTHLY/YEARLY with `INTERVAL`, `BYDAY` (including ordinals
like `2MO`/`-1SU` for MONTHLY), `BYMONTHDAY`, `BYMONTH`, `UNTIL`, `COUNT`, and
`EXDATE`. `STATUS:CANCELLED` events are dropped; a `RECURRENCE-ID` instance is
emitted on its own date and suppresses that date on its master series (by `UID`).
All-day spans use an exclusive `DTEND`, so a multi-day event shows on every day
it covers. Times stay anchored to the original wall-clock across DST.
Not yet handled: `DURATION` (uses `DTEND`), `BYSETPOS`, `WKST` other than Monday.

### Hiding events (ignore.conf)

To drop events you never want on the dashboard, list **title globs** in
**`events/ignore.conf`** (gitignored; `setup` seeds it from
`events/ignore.conf.example`). The aggregator drops an event if its title
matches **any** line (OR), case-insensitively — across every source, before
sorting and de-duplication:

```sh
HOLD:*            # placeholder events
*members only*    # anything members-only
Daily Standup*    # a recurring series you skip
```

`*` matches any run of characters, `?` a single character; everything else
(`:`, `(`, `.`, …) is matched literally. `#` comments and blank lines are
ignored. The path is overridable with `$EVENTS_IGNORE_FILE`.

## Recurring-event notes

The `recurring` source scans the **entire** `$NOTES_DIRECTORY` (the same vault
the tasks source uses — no dedicated subdir). A note is treated as a recurring
event when its frontmatter is tagged with **both** `event` and `recurring`:

```markdown
---
start: 18:00            # 24-hour local time (omit for an all-day event)
end:   19:30            # optional
weekday: Tuesday        # full or 3-letter; also accepts a CSV list (Mon, Thu)
message: Bring your copy # optional; rendered as an indented sub-line
title: Book Club        # optional; defaults to the note's filename
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

- **Another ICS feed** → just add a line to `events/feeds.conf`; the `ics`
  adapter already handles any RFC 5545 feed.
- **Quick/script source** → bash + jq (see `sources/recurring`).
- **Network/parsing-heavy source** (a non-ICS API) → a Go binary built into
  `events/sources/<name>` (see `ics/`, mirroring `BART/`). Add its build step to
  `setup` and its output path to `.gitignore`.

### Cal Sailing Club lesson window (`csc`)

`events/csc/` (Go, stdlib only) emits a single event titled
**"Cal Sailing Club @ Berkeley Marina"** for `$EVENTS_TODAY` showing **when you can
actually take a Beginning Sailing Lesson today** — the *intersection* of two
inputs:

1. **Live club hours.** Scrapes the published open/close schedule
   (`csc-openclose-times?view=month`, server-rendered). Rows are parsed by their
   NOAA `bdate`, and 12-hour times (incl. the literal `Noon`) are converted to
   24-hour. Open AND closed days are recorded so "club shut" is distinct from
   "scrape failed". A successful fetch caches the whole visible month to
   `events/sources/.csc-cache.json` and is reused when the site is unreachable.

2. **Lesson windows** from **`events/csc-lessons.conf`** (gitignored; `setup`
   seeds it from `events/csc-lessons.conf.example`). One line per weekday:
   `<weekday> <start> <end> [<dst_start> <dst_end>]` (24-hour). The optional DST
   pair applies while Daylight Saving Time is in effect (Pacific zone, via
   `time.IsDST`). A weekday with no entry has no lessons.

The shown time is `[max(open, lesson_start), min(close, lesson_end)]` per window.
If tides push the club open past a window (e.g. a Saturday opening at 1:30 PM vs a
10 AM–1 PM lesson), that day is suppressed. A non-lesson day, a closed club, an
empty overlap, or any error yields `[]`. If the lessons config is missing, it
degrades to showing the raw club hours. Overridable via `$CSC_URL`,
`$CSC_CACHE_FILE`, `$CSC_LESSONS_FILE`.

Read any per-source config from the environment; the aggregator exports
`NOTES_DIRECTORY`, `NOTES_EVENTS_SUBDIR`, `LUMA_ICS_URL`, `EVENTS_FEEDS_FILE`,
`EVENTS_TZ`, and `EVENTS_TODAY` (use `EVENTS_TODAY` so every adapter agrees on
the date even across a midnight boundary).
