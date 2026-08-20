# Events

Today's calendar events for the TRMNL dashboard, blended from multiple sources.

## How it fits together

```
update  →  EVENTS=$(events/fetch)  →  trmnl.json {.events}
                    │
       ┌────────────┴───────────┐
sources/recurring (bash+jq)   sources/ics (Go binary)      ← produce
  vault weekly/monthly notes    N .ics feeds (Luma, Google…)
  $NOTES_DIRECTORY              events/feeds.conf
       └────────────┬───────────┘
                    │  merge → today only → ignore.conf → tag (tags.conf)
                    ↓
          filters/tides (Go binary)                        ← rewrite
            clamps `sailing` events to Cal Sailing's live hours
                    │
                    ↓  sort by tag, then time → de-duplicate → split into sections
```

`events/fetch` is the only place that knows the time window ("today"). The
pipeline has two kinds of stage, each discovered by dropping an executable into
a directory:

| stage        | lives in         | contract                                        |
|--------------|------------------|-------------------------------------------------|
| **source**   | `events/sources/`| prints a JSON **array** of normalized events     |
| **filter**   | `events/filters/`| reads that array on **stdin**, prints one back   |

Both exit 0 even on failure, and both are resilient by design: a source that
breaks contributes an empty array, a filter that breaks hands back the events it
was given. A broken or unconfigured stage never takes the rest down with it.

Every stage can be switched off individually in **`events/sources.conf`** (see
[Turning sources on and off](#turning-sources-on-and-off)).

## Normalized event schema

Every adapter emits objects with this shape:

| field     | type            | set by   | notes                                  |
|-----------|-----------------|----------|----------------------------------------|
| `title`   | string          | source   | event name                             |
| `start`   | string \| null  | source   | local 24-hour `"HH:MM"`; `null` for all-day |
| `end`     | string \| null  | source   | local 24-hour `"HH:MM"`; optional      |
| `all_day` | bool            | source   | true → rendered as "All day"           |
| `message` | string \| null  | source   | optional note; indented sub-line       |
| `tags`    | \[string]       | source   | the event's own tags, lowercase; optional |
| `date`    | string          | source   | local `"YYYY-MM-DD"` the occurrence falls on |
| `sort`    | string          | source   | `"YYYY-MM-DDTHH:MM"` ordering key       |
| `source`  | string          | source   | provenance (`recurring`, `the-commons`, …) |
| `tag`     | string          | **aggregator** | the tag it was classified under: `Commons`, `Dance`, `Sailing`, `Other` |

The device renders **title + time**, plus the optional `message` as an indented
line beneath, and `tag` as a bold label at the head of the row in a `tagged`
section. `tags`, `date`, `sort`, and `source` are inputs to the aggregator
rather than screen content; sources that have none of a given field may simply
omit it.

Genuine conflicts (different events at overlapping times) are kept on purpose;
only **exact duplicates** — same title and same start, e.g. one event
cross-posted to two calendars — are collapsed to a single entry.

### What the aggregator returns

Stages speak flat arrays among themselves, but `events/fetch` prints the day
already split into the dashboard's **sections** — see
[Tags and sections](#tags-and-sections):

```json
[ { "heading": "Dance",  "tagged": false, "events": [ … ] },
  { "heading": "Events", "tagged": true,  "events": [ … ] } ]
```

| field     | type       | notes                                                   |
|-----------|------------|---------------------------------------------------------|
| `heading` | string     | the section heading, i.e. the tag name (or `Events`)     |
| `tagged`  | bool       | whether rows carry their `tag` as a label — true only for the shared `Events` section |
| `events`  | \[event]   | in order; never empty, since empty sections are omitted  |

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

`<label>` names the event's `source` field. It is not printed on the device, but
it is what a `source:` rule in [`tags.conf`](#tags-and-sections) matches on — labelling a feed
`the-commons` is how everything on that calendar comes out tagged COMMONS. A
line with just a URL gets a label derived from its host (`luma`/`gcal`/…). The
legacy `LUMA_ICS_URL` env var, if set, is still honored as one extra feed
labeled `luma`.

Two more config files round out the pipeline, both gitignored and both seeded by
`setup` from their `.example`: **`events/tags.conf`** decides how events are
[tagged and ordered](#tags-and-sections), and **`events/sources.conf`**
[switches stages on and off](#turning-sources-on-and-off).

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

## Tags and sections

Every event is classified under exactly one **tag**, and each tag is presented
one of two ways: as a bold label at the head of its row inside the shared
**Events** section, or — if the tag is declared a `heading` — as a **section of
its own**, with its own heading and no label on its rows, the way Birthdays sits
in the column. The day therefore reads as a small stack of named blocks rather
than one undifferentiated column.

The vocabulary, the precedence, the presentation, and the on-screen order all
live in **`events/tags.conf`** (gitignored; `setup` seeds it from
`events/tags.conf.example`). One rule or declaration per line, `<Tag>
<something>`:

```sh
Dance     heading                # its own section, above Events (declared first)
Dance     tag:dance              # notes whose frontmatter says `dance`

Commons   source:the-commons     # everything on The Commons' calendar
Sailing   tag:sailing
Sailing   title:*Cal Sailing*    # ...and a net for sailing off a calendar
```

### Rules: `<Tag>  <kind>:<glob>`

`<kind>` is where the tag comes from — the three provenances the dashboard
supports:

| kind      | matches against                | provenance                        |
|-----------|--------------------------------|-----------------------------------|
| `tag:`    | the event's own `tags` array   | **frontmatter** of a vault note   |
| `source:` | the event's `source` field     | **the calendar** it arrived on: an adapter name (`recurring`) or a feed label from `feeds.conf` (`the-commons`) |
| `title:`  | the event's title              | the **schedule** that named it — the catch-all for feeds that tag nothing |

Rules are evaluated top to bottom and the **first match wins**, so file order is
precedence: put the specific rule above the general one. An event matching no
rule is tagged `Other`.

Globs work as they do in `ignore.conf`: `*` matches any run of characters, `?` a
single character, everything else is literal, and matching is case-insensitive.
Because a title may contain spaces, everything after the first whitespace run is
the matcher (`Sailing  title:Cal Sailing*` works).

### Declarations: `<Tag>  heading`

A `heading` line gives that tag a section of its own instead of a label on each
row. It never matches an event — it only declares presentation, and (by being an
appearance of the tag) where that tag sits in the order.

There is nothing else to change: no template edit, no new key in `trmnl.json`.
Promoting `Sailing` to its own section is one word.

### Order

On-screen order is the order tags first appear in the file, declarations
included, with `Other` last unless the file places it itself.

- A **heading** tag's section sits where its tag first appears.
- The shared **Events** section sits where the first non-heading tag appears.

So the example above puts Dance above Events, because `Dance heading` is the
first line; moving that block below `Commons` moves the section below Events.
Within a section, events stay in tag order, then all-day first, then time.

A section with nothing in it today is omitted entirely, the way Birthdays is.

### Degrading

Trailing `#` comments and blank lines are ignored, and a malformed line is
skipped with a warning rather than taking the file down. The path is overridable
with `$EVENTS_TAGS_FILE`.

A missing `tags.conf` is not fatal — every event simply comes out `Other` in one
Events section, in plain time order, and the aggregator says so on stderr.

## Turning sources on and off

**`events/sources.conf`** (gitignored; `setup` seeds it from
`events/sources.conf.example`) switches individual pipeline stages — sources and
filters alike — without deleting anything:

```sh
recurring  on    # weekly/monthly notes from the vault
ics        on    # .ics calendar feeds
tides      off   # stop reconciling sailing times with the club's hours
```

The name is the executable's filename in `sources/` or `filters/`. `on` also
accepts `yes`/`true`/`1`/`enabled` and `off` accepts `no`/`false`/`0`/`disabled`,
case-insensitively. **A stage that isn't listed defaults to on**, so dropping a
new adapter into `sources/` works without touching this file — and a name here
that matches no stage warns on stderr rather than silently doing nothing. The
path is overridable with `$EVENTS_SOURCES_FILE`.


## Recurring-event notes

The `recurring` source scans the **entire** `$NOTES_DIRECTORY` (the same vault
the tasks source uses — no dedicated subdir). A note is treated as a recurring
event when its frontmatter is tagged with **both** `event` and `recurring`:

```markdown
---
start: 18:00            # 24-hour local time (omit for an all-day event)
end:   19:30            # optional
start_dst: 18:00        # optional; replaces start while DST is in effect
end_dst:   20:30        # optional; replaces end while DST is in effect
weekday: Tuesday        # full or 3-letter; also accepts a CSV list (Mon, Thu)
week: 2                 # optional; 2nd Tuesday of the month. Omit = every week.
message: Bring your copy # optional; rendered as an indented sub-line
title: Book Club        # optional; defaults to the note's filename
tags:
  - event
  - recurring
  - dance               # any further tag rides along for events/tags.conf
---
Notes body is ignored.
```

The note is shown only on days matching `weekday`. Weekday matching is
case-insensitive and accepts full (`Monday`) or 3-letter (`Mon`) names; multiple
days via `weekday: Mon, Thu`. `tags` may be a YAML list (as above), an inline
`[event, recurring]` array, or a CSV. The title defaults to the filename.

Every tag past the two structural ones (`event`, `recurring`) is carried through
to the emitted event's `tags` array — this is the **frontmatter provenance** the
`tag:` rules in [`tags.conf`](#tags-and-sections) match on. Tagging a note `dance` is all it
takes to file it under DANCE on the device.

#### Seasonal windows (`start_dst` / `end_dst`)

A series that shifts with Daylight Saving Time can carry a second window; each
side overrides independently, so a note may shift only its end:

```markdown
start: 13:00            # Pacific Standard Time
end:   16:00
start_dst: 13:00        # ...and an hour later all summer
end_dst:   17:00
```

Whether DST is in effect is worked out from `$EVENTS_TZ` itself rather than a
hardcoded North American rule: DST is on when the day's UTC offset exceeds the
year's standard offset. A zone without DST never triggers the override.

#### Monthly series (`week`)

Add `week` to narrow a note from every week to specific occurrences of its
`weekday` within the month — "second Saturday of the month" and friends:

```markdown
weekday: Saturday
week: 1, 3              # 1st and 3rd Saturday
```

`week` counts occurrences of that weekday, so `week: 2` is the *second Saturday*
(days 8–14), not "the Saturday of the second calendar week". That matches the
ordinal `BYDAY` semantics of RRULE (`2SA`), so the same series reads the same
whether it comes from a note or from an `.ics` feed.

Accepted tokens, comma- or space-separated, case-insensitive: `1`–`5`,
`1st`–`5th`, `first`–`fifth`, and `last` (or `-1`) for the final such weekday of
the month — which is the 4th in a short month and the 5th in a long one. A note
whose `week` is entirely unrecognized shows on **no** day and warns on stderr, so
a typo hides the event rather than firing it on the wrong weeks. `weeks:` works
as an alias, mirroring `weekday:`/`weekdays:`.

Notes ready to copy into the vault live in **`events/vault-notes/`**, which is
also the fixture directory for `events/recurring_test.sh` (`./events/recurring_test.sh`
asserts the exact set of titles emitted on ~20 dates). The tests drive the
adapter through `$EVENTS_TODAY`, which now also determines the weekday and the
week-of-month — a caller-supplied `NOTES_DIRECTORY`/`EVENTS_TZ`/`EVENTS_TODAY`
takes precedence over `.env`.

## Sailing times and the tide (`filters/tides`)

Cal Sailing sits on Berkeley Marina, whose shallow basin empties at low tide, so
the club's open and close times move day to day with the water instead of
following a timetable. That splits the sailing event across two homes, and the
split is the point:

- **The schedule lives in the notes vault.** Three notes tagged `event`,
  `recurring`, `sailing` — Monday, Thursday, Saturday — hold the *nominal*
  lesson windows, exactly like every other recurring note. Copies ready for the
  vault are in [`events/vault-notes/sailing/`](vault-notes/). Change when you
  sail by editing a note; no code, no redeploy.
- **The water lives in this repo.** `events/tides/` (Go, stdlib only) builds to
  `events/filters/tides`, which rewrites those notes' times to what the tide
  actually allows.

For every event tagged `sailing` (override the tag with `$TIDES_TAG`) the filter
intersects the note's window with the club's real hours for that date:

```
shown = [ max(club_open, note_start), min(club_close, note_end) ]
```

so a Saturday 10 AM–1 PM lesson on a day the club can only open at 10:22 shows
as **10:22–13:00**, and a day the tide holds the club shut past 1 PM drops off
the dashboard entirely rather than advertising hours you cannot sail. A day the
club never opens drops too. An all-day sailing event has no window of its own to
narrow, so it simply adopts the club's hours.

Club hours are scraped from the published open/close schedule
(`csc-openclose-times?view=month`, server-rendered). Rows are parsed by their
NOAA `bdate`, and 12-hour times (incl. the literal `Noon`) are converted to
24-hour. Open **and** closed days are recorded, so "club shut" stays distinct
from "scrape failed". A successful fetch caches the whole visible month to
`events/filters/.csc-cache.json` and is reused when the site is unreachable.

Being a filter rather than a source changes the failure mode for the better: if
the scrape fails and nothing is cached for the date, the lesson still appears at
its nominal time with a note on stderr — degraded, not missing. Overridable via
`$CSC_URL`, `$CSC_CACHE_FILE`, `$TIDES_TAG`.

## Adding a new source

Drop a new executable into `events/sources/` that prints the normalized array
and exits 0. That's it — the aggregator discovers it automatically.

- **Another ICS feed** → just add a line to `events/feeds.conf`; the `ics`
  adapter already handles any RFC 5545 feed.
- **Quick/script source** → bash + jq (see `sources/recurring`).
- **Network/parsing-heavy source** (a non-ICS API) → a Go binary built into
  `events/sources/<name>` (see `ics/`, mirroring `BART/`). Add its build step to
  `setup` and its output path to `.gitignore`.
- **Not a new source at all** — something that *changes* events already in the
  list → a filter instead (see [Adding a new filter](#adding-a-new-filter)).

Add the new stage to `events/sources.conf.example` too, so it is switchable
alongside the others.

## Adding a new filter

Drop an executable into `events/filters/` that reads the event array on stdin
and prints one on stdout. Filters run **after** tagging, so `tag` and `tags` are
available to match on, and before sorting, so a filter may freely rewrite times.

A filter must never empty the dashboard: on any error, print the input back
unchanged (the aggregator also keeps the previous list if a filter's output
isn't a JSON array). Add its build step to `setup`, its output path to
`.gitignore`, and a line to `events/sources.conf.example`.

Read any per-stage config from the environment; the aggregator exports
`NOTES_DIRECTORY`, `NOTES_EVENTS_SUBDIR`, `LUMA_ICS_URL`, `EVENTS_FEEDS_FILE`,
`EVENTS_TZ`, and `EVENTS_TODAY` (use `EVENTS_TODAY` so every stage agrees on the
date even across a midnight boundary).

## Tests

```sh
events/recurring_test.sh    # vault notes: weekday, week-of-month, DST windows
events/tags_test.sh         # tagging, ordering, stage toggles, degraded configs
(cd events/tides && go test ./...)
(cd events/ics   && go test ./...)
```

`tags_test.sh` drives `events/fetch` against a scratch pipeline — fixture
sources, fixture filters, fixture configs — through `$EVENTS_SOURCES_DIR`,
`$EVENTS_FILTERS_DIR`, `$EVENTS_TAGS_FILE`, `$EVENTS_IGNORE_FILE`, and
`$EVENTS_SOURCES_FILE`, so it touches neither the vault nor the network.
