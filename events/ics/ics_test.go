package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return loc
}

// 2026-06-01 is a Monday, so 06-03 is a Wednesday — used throughout below.

func TestParseDT_TZIDConvertsToTarget(t *testing.T) {
	loc := mustLoc(t)
	got := parseDT("20260603T120000", map[string]string{"TZID": "America/New_York"}, loc)
	if !got.ok || got.allDay {
		t.Fatalf("expected ok timed event, got %+v", got)
	}
	if h := got.t.Format("15:04"); h != "09:00" {
		t.Errorf("NYC noon should be 09:00 in LA, got %s", h)
	}
}

func TestParseDT_AllDay(t *testing.T) {
	loc := mustLoc(t)
	got := parseDT("20260603", map[string]string{"VALUE": "DATE"}, loc)
	if !got.ok || !got.allDay {
		t.Fatalf("expected all-day, got %+v", got)
	}
}

func TestParseDT_UTCZulu(t *testing.T) {
	loc := mustLoc(t)
	got := parseDT("20260603T200000Z", nil, loc)
	if h := got.t.Format("15:04"); h != "13:00" {
		t.Errorf("20:00Z should be 13:00 PDT, got %s", h)
	}
}

func TestUnfoldJoinsContinuations(t *testing.T) {
	lines := unfold("SUMMARY:Long ti\n tle\r\nDTSTART:1")
	if len(lines) != 2 || lines[0] != "SUMMARY:Long title" {
		t.Fatalf("unfold failed: %#v", lines)
	}
}

func TestDeriveLabel(t *testing.T) {
	cases := map[string]string{
		"https://api.lu.ma/ics/get?u=abc":                       "luma",
		"https://calendar.google.com/calendar/ical/x/basic.ics": "gcal",
		"https://www.example.com/feed.ics":                      "example.com",
	}
	for url, want := range cases {
		if got := deriveLabel(url); got != want {
			t.Errorf("deriveLabel(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestParseRRuleFields(t *testing.T) {
	loc := mustLoc(t)
	r, ok := parseRRule("FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;COUNT=10", loc)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if r.freq != "WEEKLY" || r.interval != 2 || r.count != 10 || len(r.byday) != 2 {
		t.Fatalf("unexpected rrule: %+v", r)
	}
}

func TestParseByDayOrdinal(t *testing.T) {
	bd, ok := parseByDay("-1SU")
	if !ok || bd.ord != -1 || bd.wd != time.Sunday {
		t.Fatalf("got %+v ok=%v", bd, ok)
	}
}

func TestOccursOn_WeeklyDefaultWeekday(t *testing.T) {
	loc := mustLoc(t)
	d := func(day int) time.Time { return time.Date(2026, 6, day, 0, 0, 0, 0, loc) }
	r, _ := parseRRule("FREQ=WEEKLY", loc)
	if !occursOn(r, d(3), d(10), nil) { // +7 days, same weekday
		t.Error("weekly should recur 7 days later")
	}
	if occursOn(r, d(3), d(4), nil) { // next day, different weekday
		t.Error("weekly should not recur on a different weekday")
	}
}

func TestOccursOn_WeeklyInterval(t *testing.T) {
	loc := mustLoc(t)
	d := func(day int) time.Time { return time.Date(2026, 6, day, 0, 0, 0, 0, loc) }
	r, _ := parseRRule("FREQ=WEEKLY;INTERVAL=2", loc)
	if occursOn(r, d(3), d(10), nil) { // 1 week later — off cycle
		t.Error("biweekly should skip the in-between week")
	}
	if !occursOn(r, d(3), d(17), nil) { // 2 weeks later
		t.Error("biweekly should hit 2 weeks later")
	}
}

func TestOccursOn_WeeklyByDay(t *testing.T) {
	loc := mustLoc(t)
	d := func(day int) time.Time { return time.Date(2026, 6, day, 0, 0, 0, 0, loc) }
	r, _ := parseRRule("FREQ=WEEKLY;BYDAY=MO,WE", loc)
	if !occursOn(r, d(1), d(3), nil) { // Mon start, Wed same week
		t.Error("expected Wednesday occurrence")
	}
	if occursOn(r, d(1), d(2), nil) { // Tuesday
		t.Error("Tuesday is not in BYDAY")
	}
	if !occursOn(r, d(1), d(8), nil) { // Mon the next week
		t.Error("expected following Monday")
	}
}

func TestOccursOn_DailyInterval(t *testing.T) {
	loc := mustLoc(t)
	d := func(day int) time.Time { return time.Date(2026, 6, day, 0, 0, 0, 0, loc) }
	r, _ := parseRRule("FREQ=DAILY;INTERVAL=3", loc)
	if !occursOn(r, d(1), d(4), nil) {
		t.Error("every-3-days should hit day 4")
	}
	if occursOn(r, d(1), d(3), nil) {
		t.Error("every-3-days should skip day 3")
	}
}

func TestOccursOn_MonthlyNthWeekday(t *testing.T) {
	loc := mustLoc(t)
	r, _ := parseRRule("FREQ=MONTHLY;BYDAY=2WE", loc) // 2nd Wednesday
	start := time.Date(2026, 6, 10, 0, 0, 0, 0, loc)  // 2nd Wed of June
	if !occursOn(r, start, time.Date(2026, 7, 8, 0, 0, 0, 0, loc), nil) {
		t.Error("expected 2nd Wednesday of July (the 8th)")
	}
	if occursOn(r, start, time.Date(2026, 7, 15, 0, 0, 0, 0, loc), nil) {
		t.Error("the 15th is the 3rd Wednesday, not the 2nd")
	}
}

func TestOccursOn_MonthlyByMonthDayNegative(t *testing.T) {
	loc := mustLoc(t)
	r, _ := parseRRule("FREQ=MONTHLY;BYMONTHDAY=-1", loc)
	start := time.Date(2026, 6, 30, 0, 0, 0, 0, loc)
	if !occursOn(r, start, time.Date(2026, 7, 31, 0, 0, 0, 0, loc), nil) {
		t.Error("expected last day of July")
	}
	if occursOn(r, start, time.Date(2026, 7, 30, 0, 0, 0, 0, loc), nil) {
		t.Error("the 30th is not the last day of July")
	}
}

func TestOccursOn_Yearly(t *testing.T) {
	loc := mustLoc(t)
	r, _ := parseRRule("FREQ=YEARLY", loc)
	start := time.Date(2026, 3, 15, 0, 0, 0, 0, loc)
	if !occursOn(r, start, time.Date(2028, 3, 15, 0, 0, 0, 0, loc), nil) {
		t.Error("expected yearly recurrence")
	}
	if occursOn(r, start, time.Date(2028, 3, 16, 0, 0, 0, 0, loc), nil) {
		t.Error("wrong day of month")
	}
}

func TestOccursOn_Until(t *testing.T) {
	loc := mustLoc(t)
	d := func(day int) time.Time { return time.Date(2026, 6, day, 0, 0, 0, 0, loc) }
	r, _ := parseRRule("FREQ=WEEKLY;UNTIL=20260608T235959Z", loc)
	if !occursOn(r, d(3), d(3), nil) {
		t.Error("start date is before UNTIL")
	}
	if occursOn(r, d(3), d(10), nil) {
		t.Error("June 10 is past UNTIL (June 8)")
	}
}

func TestOccursOn_Count(t *testing.T) {
	loc := mustLoc(t)
	d := func(day int) time.Time { return time.Date(2026, 6, day, 0, 0, 0, 0, loc) }
	r, _ := parseRRule("FREQ=DAILY;COUNT=3", loc) // 06-01, 06-02, 06-03
	if !occursOn(r, d(1), d(3), nil) {
		t.Error("3rd occurrence is within COUNT=3")
	}
	if occursOn(r, d(1), d(4), nil) {
		t.Error("4th occurrence exceeds COUNT=3")
	}
}

func TestOccursOn_Exdate(t *testing.T) {
	loc := mustLoc(t)
	d := func(day int) time.Time { return time.Date(2026, 6, day, 0, 0, 0, 0, loc) }
	r, _ := parseRRule("FREQ=WEEKLY", loc)
	excluded := map[string]bool{"2026-06-10": true}
	if occursOn(r, d(3), d(10), excluded) {
		t.Error("excluded date should not occur")
	}
}

func TestExpand_SingleTimedToday(t *testing.T) {
	loc := mustLoc(t)
	today := time.Date(2026, 6, 3, 0, 0, 0, 0, loc)
	cal := "BEGIN:VEVENT\nUID:1\nSUMMARY:Lunch\n" +
		"DTSTART;TZID=America/Los_Angeles:20260603T123000\n" +
		"DTEND;TZID=America/Los_Angeles:20260603T133000\nEND:VEVENT\n"
	evs := expand(parseCalendar(cal, loc), "gcal", today, loc)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if evs[0].Start == nil || *evs[0].Start != "12:30" || evs[0].End == nil || *evs[0].End != "13:30" {
		t.Errorf("bad times: %+v", evs[0])
	}
	if evs[0].Source != "gcal" || evs[0].Date != "2026-06-03" {
		t.Errorf("bad metadata: %+v", evs[0])
	}
}

func TestExpand_AllDayMultiDaySpan(t *testing.T) {
	loc := mustLoc(t)
	cal := "BEGIN:VEVENT\nUID:trip\nSUMMARY:Trip\n" +
		"DTSTART;VALUE=DATE:20260602\nDTEND;VALUE=DATE:20260605\nEND:VEVENT\n" // exclusive end
	for _, day := range []int{2, 3, 4} {
		today := time.Date(2026, 6, day, 0, 0, 0, 0, loc)
		evs := expand(parseCalendar(cal, loc), "gcal", today, loc)
		if len(evs) != 1 || !evs[0].AllDay {
			t.Errorf("day %d: expected 1 all-day event, got %+v", day, evs)
		}
	}
	today := time.Date(2026, 6, 5, 0, 0, 0, 0, loc) // end is exclusive
	if evs := expand(parseCalendar(cal, loc), "gcal", today, loc); len(evs) != 0 {
		t.Errorf("end date is exclusive, expected 0 events, got %+v", evs)
	}
}

func TestExpand_RecurringWeeklyToday(t *testing.T) {
	loc := mustLoc(t)
	today := time.Date(2026, 6, 17, 0, 0, 0, 0, loc) // a Wednesday
	cal := "BEGIN:VEVENT\nUID:standup\nSUMMARY:Standup\n" +
		"DTSTART;TZID=America/Los_Angeles:20260603T090000\n" +
		"DTEND;TZID=America/Los_Angeles:20260603T091500\n" +
		"RRULE:FREQ=WEEKLY\nEND:VEVENT\n"
	evs := expand(parseCalendar(cal, loc), "gcal", today, loc)
	if len(evs) != 1 || evs[0].Start == nil || *evs[0].Start != "09:00" || evs[0].Date != "2026-06-17" {
		t.Fatalf("expected recurring occurrence at 09:00 on 06-17, got %+v", evs)
	}
}

func TestExpand_RecurrenceIdOverrideSuppressesMaster(t *testing.T) {
	loc := mustLoc(t)
	today := time.Date(2026, 6, 17, 0, 0, 0, 0, loc)
	// Master weekly Wednesday at 09:00; the 06-17 instance moved to 11:00.
	cal := "BEGIN:VEVENT\nUID:standup\nSUMMARY:Standup\n" +
		"DTSTART;TZID=America/Los_Angeles:20260603T090000\nRRULE:FREQ=WEEKLY\nEND:VEVENT\n" +
		"BEGIN:VEVENT\nUID:standup\nSUMMARY:Standup (moved)\n" +
		"RECURRENCE-ID;TZID=America/Los_Angeles:20260617T090000\n" +
		"DTSTART;TZID=America/Los_Angeles:20260617T110000\nEND:VEVENT\n"
	evs := expand(parseCalendar(cal, loc), "gcal", today, loc)
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 event (override only), got %d: %+v", len(evs), evs)
	}
	if evs[0].Start == nil || *evs[0].Start != "11:00" {
		t.Errorf("expected the moved 11:00 instance, got %+v", evs[0])
	}
}

func TestExpand_CancelledDropped(t *testing.T) {
	loc := mustLoc(t)
	today := time.Date(2026, 6, 3, 0, 0, 0, 0, loc)
	cal := "BEGIN:VEVENT\nUID:1\nSUMMARY:Gone\nSTATUS:CANCELLED\n" +
		"DTSTART;TZID=America/Los_Angeles:20260603T090000\nEND:VEVENT\n"
	if evs := expand(parseCalendar(cal, loc), "gcal", today, loc); len(evs) != 0 {
		t.Errorf("cancelled event should be dropped, got %+v", evs)
	}
}

func TestLoadFeeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feeds.conf")
	content := "# comment\n\n" +
		"luma-sf   https://api.lu.ma/ics/get?u=1\n" +
		"https://calendar.google.com/calendar/ical/x/basic.ics\n" +
		"luma-sf   https://api.lu.ma/ics/get?u=1\n" // duplicate URL, should dedupe
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENTS_FEEDS_FILE", path)
	t.Setenv("LUMA_ICS_URL", "https://api.lu.ma/ics/get?u=legacy")

	feeds := loadFeeds()
	if len(feeds) != 3 {
		t.Fatalf("expected 3 feeds (deduped + legacy), got %d: %+v", len(feeds), feeds)
	}
	if feeds[0].label != "luma-sf" || feeds[1].label != "gcal" || feeds[2].label != "luma" {
		t.Errorf("unexpected labels: %+v", feeds)
	}
}
