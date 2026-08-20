#!/usr/bin/env bash
# Tests for the tagging, sectioning, ordering, and stage-toggle behavior of
# events/fetch.
#
#   events/tags_test.sh
#
# The aggregator is driven against a scratch pipeline — fixture sources, fixture
# filters, fixture tags.conf/sources.conf — so nothing here touches the real
# vault, the real calendar feeds, or the network.
# >/dev/null guards against a set CDPATH making `cd` echo the path.
SCRIPT_DIR="$(cd "$(dirname "$0")" >/dev/null 2>&1 && pwd)"
FETCH="$SCRIPT_DIR/fetch"

FIXTURE=$(mktemp -d)
trap 'rm -rf "$FIXTURE"' EXIT
mkdir -p "$FIXTURE/sources" "$FIXTURE/filters"

TODAY=$(TZ=America/Los_Angeles date +%Y-%m-%d)
failures=0

# ── Fixture pipeline ────────────────────────────────────────────────

# source_note emits what sources/recurring emits for a tagged vault note.
cat >|"$FIXTURE/sources/recurring" <<EOF
#!/usr/bin/env bash
jq -n --arg d "\$EVENTS_TODAY" '[
  {title: "East Bay Fusion", start: "20:00", "end": "00:00", all_day: false,
   tags: ["dance"], date: \$d, sort: (\$d + "T20:00"), source: "recurring"},
  {title: "Cal Sailing Club @ Berkeley Marina", start: "13:00", "end": "16:00",
   all_day: false, tags: ["sailing"], date: \$d, sort: (\$d + "T13:00"),
   source: "recurring"},
  {title: "Rent due", start: null, "end": null, all_day: true, tags: [],
   date: \$d, sort: (\$d + "T00:00"), source: "recurring"}
]'
EOF

# The Commons feed tags nothing itself; the calendar it came from is the tag.
cat >|"$FIXTURE/sources/ics" <<EOF
#!/usr/bin/env bash
jq -n --arg d "\$EVENTS_TODAY" '[
  {title: "Tea Ceremony", start: "09:00", "end": "10:00", all_day: false,
   date: \$d, sort: (\$d + "T09:00"), source: "the-commons"},
  {title: "Board Game Night", start: "19:00", "end": "22:00", all_day: false,
   date: \$d, sort: (\$d + "T19:00"), source: "the-commons"},
  {title: "Dentist", start: "08:00", "end": "09:00", all_day: false,
   date: \$d, sort: (\$d + "T08:00"), source: "gcal"},
  {title: "Cal Sailing Work Party", start: "10:00", "end": "12:00",
   all_day: false, date: \$d, sort: (\$d + "T10:00"), source: "gcal"}
]'
EOF

# A filter that stamps every event it sees, so we can prove filters run at all,
# run after tagging, and can be switched off.
cat >|"$FIXTURE/filters/stamp" <<'EOF'
#!/usr/bin/env bash
jq 'map(.title = (.tag + "/" + .title))'
EOF

cat >|"$FIXTURE/filters/broken" <<'EOF'
#!/usr/bin/env bash
echo 'not json at all'
EOF

chmod +x "$FIXTURE"/sources/* "$FIXTURE"/filters/*

cat >|"$FIXTURE/tags.conf" <<'EOF'
Commons   source:the-commons
Dance     tag:dance
Sailing   tag:sailing
Sailing   title:*Cal Sailing*
EOF

: >|"$FIXTURE/ignore.conf"
: >|"$FIXTURE/sources.conf"

run_fetch() {
  EVENTS_TZ=America/Los_Angeles \
    EVENTS_SOURCES_DIR="$FIXTURE/sources" \
    EVENTS_FILTERS_DIR="$FIXTURE/filters" \
    EVENTS_TAGS_FILE="$FIXTURE/tags.conf" \
    EVENTS_IGNORE_FILE="$FIXTURE/ignore.conf" \
    EVENTS_SOURCES_FILE="$FIXTURE/sources.conf" \
    NOTES_DIRECTORY= \
    "$FETCH" 2>/dev/null
}

# check <comment> <jq filter> <expected> — asserts one projection of the output.
# The output is an array of sections, so most filters reach through
# `.[].events[]` to get at the day's events as one flat list.
check() {
  local comment="$1" filter="$2" want="$3" got
  got=$(run_fetch | jq -r "$filter")
  if [[ "$got" == "$want" ]]; then
    printf 'ok    %s\n' "$comment"
    return
  fi
  failures=$((failures + 1))
  printf 'FAIL  %s\n' "$comment"
  printf '        want: %s\n' "$(printf '%s' "$want" | tr '\n' '|')"
  printf '        got:  %s\n' "$(printf '%s' "$got" | tr '\n' '|')"
}

# ── Provenance: each of the three matcher kinds tags its own events ──

# The stamp filter rewrites titles; keep it off until the filter cases below.
printf 'stamp off\n' >|"$FIXTURE/sources.conf"

check 'the commons calendar tags its events by source' \
  '[.[].events[] | select(.source == "the-commons") | .tag] | unique | join(",")' \
  'Commons'

check 'a vault note tags its event from its own frontmatter' \
  '.[].events[] | select(.title == "East Bay Fusion") | .tag' \
  'Dance'

check 'a scheduled sailing note is tagged from its frontmatter' \
  '.[].events[] | select(.title == "Cal Sailing Club @ Berkeley Marina") | .tag' \
  'Sailing'

check 'a title rule catches a sailing event from a calendar that tags nothing' \
  '.[].events[] | select(.title == "Cal Sailing Work Party") | .tag' \
  'Sailing'

check 'an event matching no rule falls through to Other' \
  '.[].events[] | select(.title == "Dentist") | .tag' \
  'Other'

# ── Ordering: tag first (in tags.conf order), then all-day, then time ──

check 'events are grouped by tag in tags.conf order, Other last' \
  '[.[].events[].tag] | join(",")' \
  'Commons,Commons,Dance,Sailing,Sailing,Other,Other'

check 'within a tag, all-day comes first and the rest run in time order' \
  '[.[].events[] | .tag + ":" + .title] | join("|")' \
  'Commons:Tea Ceremony|Commons:Board Game Night|Dance:East Bay Fusion|Sailing:Cal Sailing Work Party|Sailing:Cal Sailing Club @ Berkeley Marina|Other:Rent due|Other:Dentist'

check 'tag_rank is scaffolding and never reaches the device' \
  '[.[].events[] | has("tag_rank")] | unique | join(",")' \
  'false'

# ── Sections ────────────────────────────────────────────────────────
# With no tag declared `heading`, the whole day is one tagged section.

check 'without a heading declaration the day is a single tagged Events section' \
  '[.[] | .heading + ":" + (.tagged | tostring)] | join(",")' \
  'Events:true'

# Declaring a tag `heading` lifts it out into a section of its own, positioned
# where the tag is declared — here first, so it sits above Events.
cat >|"$FIXTURE/tags.conf" <<'EOF'
Dance     heading
Dance     tag:dance
Commons   source:the-commons
Sailing   tag:sailing
Sailing   title:*Cal Sailing*
EOF

check 'a heading tag becomes its own section, declared first so it leads' \
  '[.[] | .heading + ":" + (.tagged | tostring)] | join(",")' \
  'Dance:false,Events:true'

check 'the heading section holds exactly its own tag' \
  '[.[] | select(.heading == "Dance") | .events[].title] | join(",")' \
  'East Bay Fusion'

check 'and those events are gone from Events' \
  '[.[] | select(.heading == "Events") | .events[].tag] | unique | sort | join(",")' \
  'Commons,Other,Sailing'

check 'the Events section still sorts all-day first, then by time' \
  '[.[] | select(.heading == "Events") | .events[].title] | join("|")' \
  'Tea Ceremony|Board Game Night|Cal Sailing Work Party|Cal Sailing Club @ Berkeley Marina|Rent due|Dentist'

# Moving the declaration moves the section: same rules, Dance declared after
# Commons, so Events now leads.
cat >|"$FIXTURE/tags.conf" <<'EOF'
Commons   source:the-commons
Dance     heading
Dance     tag:dance
Sailing   tag:sailing
Sailing   title:*Cal Sailing*
EOF

check 'section order follows declaration order' \
  '[.[].heading] | join(",")' \
  'Events,Dance'

# A heading tag that matches nothing today leaves no empty heading behind.
cat >|"$FIXTURE/tags.conf" <<'EOF'
Dance     heading
Dance     tag:dance
Curling   heading
Curling   title:*Bonspiel*
Commons   source:the-commons
EOF

check 'a section with nothing in it today is left out entirely' \
  '[.[].heading] | join(",")' \
  'Dance,Events'

# Every tag a heading: no Events section is emitted at all.
cat >|"$FIXTURE/tags.conf" <<'EOF'
Dance     heading
Dance     tag:dance
Other     heading
Other     title:*
EOF

check 'when every tag is a heading there is no Events section' \
  '[.[].heading] | join(",")' \
  'Dance,Other'

cat >|"$FIXTURE/tags.conf" <<'EOF'
Commons   source:the-commons
Dance     tag:dance
Sailing   tag:sailing
Sailing   title:*Cal Sailing*
EOF

# ── Filters ─────────────────────────────────────────────────────────

printf 'broken off\n' >|"$FIXTURE/sources.conf"
check 'a filter runs after tagging, so it can see the tag' \
  '.[].events[] | select(.title | endswith("Dentist")) | .title' \
  'Other/Dentist'

printf 'stamp off\nbroken on\n' >|"$FIXTURE/sources.conf"
check 'a filter that returns garbage leaves the events untouched' \
  '[.[].events[]] | length' \
  '7'

# ── Stage toggles (sources.conf) ────────────────────────────────────

printf 'stamp off\nics off\n' >|"$FIXTURE/sources.conf"
check 'a source switched off contributes nothing' \
  '[.[].events[].source] | unique | join(",")' \
  'recurring'

printf 'stamp off\nrecurring disabled\nics YES\n' >|"$FIXTURE/sources.conf"
check 'on/off spellings are case-insensitive and accept yes/disabled' \
  '[.[].events[].source] | unique | join(",")' \
  'gcal,the-commons'

printf 'stamp off\n' >|"$FIXTURE/sources.conf"
check 'a stage missing from sources.conf defaults to on' \
  '[.[].events[].source] | unique | join(",")' \
  'gcal,recurring,the-commons'

# ── Degraded configs ────────────────────────────────────────────────

printf 'stamp off\n' >|"$FIXTURE/sources.conf"
mv "$FIXTURE/tags.conf" "$FIXTURE/tags.conf.bak"
check 'with no tags.conf every event is Other in one Events section' \
  '[.[] | .heading + ":" + ([.events[].tag] | unique | join(","))] | join("|")' \
  'Events:Other'
mv "$FIXTURE/tags.conf.bak" "$FIXTURE/tags.conf"

cat >|"$FIXTURE/tags.conf" <<'EOF'
Dance     tag:dance
Nonsense  wat:*
Broken
Commons   source:the-commons
EOF
check 'a malformed rule is skipped without taking its file down' \
  '[.[].events[] | select(.tag != "Other") | .tag] | unique | sort | join(",")' \
  'Commons,Dance'

if ((failures)); then
  printf '\n%d test(s) failed\n' "$failures"
  exit 1
fi

printf '\nall tests passed\n'
