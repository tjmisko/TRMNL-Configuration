# Vault notes

Ready-to-copy recurring-event notes for the `recurring` source adapter. Nothing
here is read by the dashboard — the adapter scans `$NOTES_DIRECTORY`, not this
directory. These files live in the repo so the schedule is reviewable in git;
copy the ones you want into your vault:

```sh
cp -n events/vault-notes/dance-socials/*.md "$NOTES_DIRECTORY/"
cp -n events/vault-notes/sailing/*.md       "$NOTES_DIRECTORY/"
```

They double as the fixture for `events/recurring_test.sh`, which runs against
this directory by default. Editing a note here changes what the tests assert.

## dance-socials

Transcribed from the "dance socials" schedule. The **weekly** rows already in
the vault (Shades @ Polish Club, Mission City Swing @ Russian Center, The
Breakaway @ Veterans Memorial Building) are deliberately **not** duplicated
here; only the notes the vault was missing are included.

| note                     | when                     | time        |
|--------------------------|--------------------------|-------------|
| East Bay Fusion          | every Tuesday            | 20:00–00:00 |
| CI Jam @ Finnish Hall    | every Thursday           | 20:00–23:00 |
| Circle Left              | 1st Saturday             | 19:00–22:00 |
| Bal Haus                 | 1st Saturday             | 20:00–00:00 |
| Mission Fusion           | 1st **and** 3rd Saturday | 21:00–00:00 |
| Microfusion              | 2nd Saturday             | 21:00–00:00 |
| Breakaway Blues          | 3rd Saturday             | 21:00–23:00 |
| Starry Plough            | 2nd Friday               | 19:00–23:00 |
| Down to Dance            | 4th Sunday               | 15:00–18:00 |

Two readings of the source schedule are worth knowing about:

- **"microfusion (9-12PM)"** is taken as 9 PM–12 AM, matching the other fusion
  socials rather than a literal 9 AM–12 PM.
- **Mission City Swing** already in the vault starts at 20:00, but the schedule
  lists it at 9 PM–12 AM. The vault note is left alone — fix whichever is wrong.

## sailing

The Cal Sailing Club Beginning Sailing Lesson days. These replace the old
`events/csc-lessons.conf`: the schedule now lives in the vault like every other
recurring series, so changing when you sail is a note edit rather than a config
edit and a rebuild.

| note                        | when            | nominal time | in DST      |
|-----------------------------|-----------------|--------------|-------------|
| Cal Sailing Monday Lesson   | every Monday    | 13:00–16:00  | 13:00–17:00 |
| Cal Sailing Thursday Lesson | every Thursday  | 13:00–16:00  | 13:00–17:00 |
| Cal Sailing Saturday Lesson | every Saturday  | 10:00–13:00  | unchanged   |

All three share the title **Cal Sailing Club @ Berkeley Marina** and are tagged
`sailing`, which is what earns them the SAILING tag on the device and what
`events/filters/tides` looks for.

**The times above are nominal.** Berkeley Marina empties at low tide, so the
club's real open and close times move daily; `events/filters/tides` narrows each
window to the hours the club is actually open, and drops the event outright on a
day the tide swallows the window whole. Keep these notes describing when the
*lessons* are scheduled and let the filter worry about the water — see
[Sailing times and the tide](../README.md#sailing-times-and-the-tide-filterstides).
