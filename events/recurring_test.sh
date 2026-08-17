#!/usr/bin/env bash
# Tests for events/sources/recurring, driven entirely through $EVENTS_TODAY.
#
#   events/recurring_test.sh              # run against events/vault-notes
#   NOTES_DIRECTORY=~/Notes events/recurring_test.sh   # run against a real vault
#
# Each case asserts the exact set of titles the adapter emits for one date, so
# a note that leaks onto the wrong week fails just as loudly as one that
# vanishes. Dates are in August 2026, whose 1st is a Saturday.
# >/dev/null guards against a set CDPATH making `cd` echo the path.
SCRIPT_DIR="$(cd "$(dirname "$0")" >/dev/null 2>&1 && pwd)"
ADAPTER="$SCRIPT_DIR/sources/recurring"

export NOTES_DIRECTORY="${NOTES_DIRECTORY:-$SCRIPT_DIR/vault-notes}"
export EVENTS_TZ="${EVENTS_TZ:-America/Los_Angeles}"

failures=0

# expect <date> <comment> <title>... — asserts the emitted titles, in order.
expect() {
  local date="$1" comment="$2"
  shift 2
  local want got
  want=$(printf '%s\n' "$@")
  got=$(EVENTS_TODAY="$date" "$ADAPTER" | jq -r '.[].title')

  if [[ "$got" == "$want" ]]; then
    printf 'ok    %s  %s\n' "$date" "$comment"
    return
  fi

  failures=$((failures + 1))
  printf 'FAIL  %s  %s\n' "$date" "$comment"
  printf '        want: %s\n' "$(printf '%s' "$want" | tr '\n' '|')"
  printf '        got:  %s\n' "$(printf '%s' "$got" | tr '\n' '|')"
}

# Weekly notes (no `week:`) show up on every occurrence of their weekday.
expect 2026-08-04 'Tue, 1st week'  'East Bay Fusion'
expect 2026-08-25 'Tue, 4th week'  'East Bay Fusion'
expect 2026-08-20 'Thu, every week' 'CI Jam @ Finnish Hall'

# Monthly notes appear only on their listed occurrence of that weekday.
expect 2026-08-01 'Sat, 1st week'  'Circle Left' 'Bal Haus' 'Mission Fusion'
expect 2026-08-08 'Sat, 2nd week'  'Microfusion'
expect 2026-08-15 'Sat, 3rd week'  'Breakaway Blues' 'Mission Fusion'
expect 2026-08-22 'Sat, 4th week — no Saturday socials'
expect 2026-08-29 'Sat, 5th week — no Saturday socials'
expect 2026-08-14 'Fri, 2nd week'  'Starry Plough'
expect 2026-08-07 'Fri, 1st week — Starry Plough is 2nd only'
expect 2026-08-23 'Sun, 4th week'  'Down to Dance'
expect 2026-08-30 'Sun, 5th week — Down to Dance is 4th only'

# A `week: 1, 3` note fires on both listed weeks and nothing between them.
expect 2026-09-05 'Sat, 1st week of a month starting Tuesday' \
  'Circle Left' 'Bal Haus' 'Mission Fusion'
expect 2026-09-19 'Sat, 3rd week of a month starting Tuesday' \
  'Breakaway Blues' 'Mission Fusion'

# February 2026 has exactly four Saturdays, so the 4th is also the last.
expect 2026-02-28 'Sat, 4th and last week of a 28-day month'

# The dance socials only exercise plain digits. Spell the rest of the accepted
# `week` vocabulary out in a scratch vault: ordinal words, `last`, and a typo.
FIXTURE_VAULT=$(mktemp -d)
trap 'rm -rf "$FIXTURE_VAULT"' EXIT

note() {
  printf -- '---\ntitle: %s\nstart: 12:00\nweekday: Saturday\nweek: %s\ntags: [event, recurring]\n---\n' \
    "$1" "$2" >"$FIXTURE_VAULT/$1.md"
}
note 'Word Ordinal'   'second'
note 'Ordinal Suffix' '3rd'
note 'Last Saturday'  'last'
note 'Negative Last'  '-1'
note 'Typo Week'      'seccond'

NOTES_DIRECTORY="$FIXTURE_VAULT"
expect 2026-08-08 'week: second matches the 2nd Saturday' 'Word Ordinal'
expect 2026-08-15 'week: 3rd matches the 3rd Saturday'    'Ordinal Suffix'
expect 2026-08-29 'last/-1 match the final Saturday (5th here)' \
  'Last Saturday' 'Negative Last'
expect 2026-02-28 'last/-1 match the final Saturday (4th here)' \
  'Last Saturday' 'Negative Last'
expect 2026-08-22 'no note fires on the 4th Saturday of a 5-Saturday month'

if (( failures )); then
  printf '\n%d test(s) failed\n' "$failures"
  exit 1
fi

printf '\nall tests passed\n'
