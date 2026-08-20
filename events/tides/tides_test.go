package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseClock(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Noon", "12:00", true},
		{"noon", "12:00", true},
		{"Midnight", "00:00", true},
		{"9:00 AM", "09:00", true},
		{"12:02 PM", "12:02", true},
		{"7:56 PM", "19:56", true},
		{"12:30 AM", "00:30", true},
		{"1:05 PM", "13:05", true},
		{"", "", false},
		{"sunrise", "", false},
		{"25:00 PM", "", false},
	}
	for _, c := range cases {
		got, ok := parseClock(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseClock(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseWindow(t *testing.T) {
	open, close, ok := parseWindow("12:02 PM to 7:56 PM")
	if !ok || open != "12:02" || close != "19:56" {
		t.Fatalf("parseWindow late-open = (%q,%q,%v)", open, close, ok)
	}
	open, close, ok = parseWindow("Noon to 8:05 PM")
	if !ok || open != "12:00" || close != "20:05" {
		t.Fatalf("parseWindow noon = (%q,%q,%v)", open, close, ok)
	}
	if _, _, ok := parseWindow("Closed"); ok {
		t.Fatal("parseWindow(Closed) should not parse")
	}
}

func TestParseMonthFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "month.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := parseMonth(body)

	want := map[string]dayInfo{
		"2026-06-01": {Open: "12:00", Close: "19:55"}, // Noon to 7:55 PM
		"2026-06-03": {Open: "12:02", Close: "19:56"}, // Late Open 12:02 PM to 7:56 PM (today, appears twice in page)
		"2026-06-13": {Open: "09:00", Close: "20:02"}, // 9:00 AM to 8:02 PM
		"2026-06-30": {Open: "12:00", Close: "20:05"}, // Noon to 8:05 PM
	}
	for date, w := range want {
		if got[date] != w {
			t.Errorf("parseMonth[%s] = %+v, want %+v", date, got[date], w)
		}
	}
	// June is fully open: a record for every calendar day, none flagged closed.
	if len(got) != 30 {
		t.Errorf("parseMonth produced %d days, want 30", len(got))
	}
}

func TestParseMonthClosedDayRecorded(t *testing.T) {
	const mixed = `
<tr class="tidedata"><td class="tidedatadate nopadding"><a href="https://x?id=9414816&bdate=20261214"> Sun Dec 14</a></td>
<td class="nopadding timelinecell"><span class="tideok" style="width:100%;">Noon to 4:51 PM</span></td></tr>
<tr class="tidedata"><td class="tidedatadate nopadding"><a href="https://x?id=9414816&bdate=20261215"> Mon Dec 15</a></td>
<td class="nopadding timelinecell"><span class="tideclosed" style="width:100%;">Closed</span></td></tr>`
	got := parseMonth([]byte(mixed))
	if got["2026-12-15"] != (dayInfo{Closed: true}) {
		t.Errorf("closed day 2026-12-15 = %+v, want {Closed:true}", got["2026-12-15"])
	}
	if got["2026-12-14"] != (dayInfo{Open: "12:00", Close: "16:51"}) {
		t.Errorf("open day 2026-12-14 = %+v, want 12:00/16:51", got["2026-12-14"])
	}
}

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"13:00", 780, true},
		{"9:30", 570, true},
		{"00:00", 0, true},
		{"23:59", 1439, true},
		{"24:00", 0, false},
		{"13:60", 0, false},
		{"1300", 0, false},
	}
	for _, c := range cases {
		got, ok := parseHHMM(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseHHMM(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCacheRoundTripAndMerge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CSC_CACHE_FILE", filepath.Join(dir, "cache.json"))

	first := map[string]dayInfo{"2026-06-01": {Open: "12:00", Close: "19:55"}, "2026-12-15": {Closed: true}}
	saveCache(first)
	if got := loadCache(); !reflect.DeepEqual(got, first) {
		t.Fatalf("loadCache = %+v, want %+v", got, first)
	}

	// A later fetch of a new month must not drop the cached prior month.
	live := map[string]dayInfo{"2026-07-01": {Open: "12:00", Close: "20:30"}}
	merged := mergeCache(live)
	if merged["2026-06-01"] != (dayInfo{Open: "12:00", Close: "19:55"}) || merged["2026-07-01"] != (dayInfo{Open: "12:00", Close: "20:30"}) {
		t.Fatalf("mergeCache lost a key: %+v", merged)
	}
}

// ── Clamping sailing events ─────────────────────────────────────────

// juneHours is a week of real-shaped Cal Sailing days: a normal Monday, a
// Saturday the tide holds shut until the afternoon, and a fully closed day.
func juneHours() map[string]dayInfo {
	return map[string]dayInfo{
		"2026-06-01": {Open: "12:00", Close: "19:55"}, // Mon, opens at noon
		"2026-06-04": {Open: "09:00", Close: "20:02"}, // Thu, open all day
		"2026-06-06": {Open: "13:30", Close: "19:58"}, // Sat, late tide
		"2026-06-08": {Closed: true},                  // Mon, club shut
	}
}

// sailingEvent is one lesson note as the recurring adapter emits it.
func sailingEvent(date, start, end string) event {
	return event{
		"title": "Cal Sailing Club @ Berkeley Marina", "start": start, "end": end,
		"all_day": false, "tags": []any{"sailing"}, "date": date,
		"sort": date + "T" + start, "source": "recurring",
	}
}

func TestClampNarrowsToClubOpening(t *testing.T) {
	// Monday 1-5 PM against a club open noon-7:55 PM: the lesson fits, untouched.
	got, ok := clamp(sailingEvent("2026-06-01", "13:00", "17:00"), juneHours())
	if !ok || got["start"] != "13:00" || got["end"] != "17:00" {
		t.Fatalf("in-window Monday = %+v, ok=%v; want 13:00-17:00 kept", got, ok)
	}
	// A 10 AM start against a noon opening is pushed to noon, sort key included.
	got, ok = clamp(sailingEvent("2026-06-01", "10:00", "17:00"), juneHours())
	if !ok || got["start"] != "12:00" || got["end"] != "17:00" {
		t.Fatalf("early Monday = %+v, ok=%v; want 12:00-17:00", got, ok)
	}
	if got["sort"] != "2026-06-01T12:00" {
		t.Errorf("sort key = %v, want 2026-06-01T12:00", got["sort"])
	}
	// A club closing before the lesson ends trims the end.
	got, ok = clamp(sailingEvent("2026-06-04", "13:00", "21:00"), juneHours())
	if !ok || got["end"] != "20:02" {
		t.Fatalf("late-ending Thursday = %+v, ok=%v; want end 20:02", got, ok)
	}
}

func TestClampDropsEventTheTideSwallows(t *testing.T) {
	// Saturday 10 AM-1 PM, but the tide holds the club shut until 1:30 PM.
	if _, ok := clamp(sailingEvent("2026-06-06", "10:00", "13:00"), juneHours()); ok {
		t.Error("late-open Saturday should drop the lesson, not show it")
	}
	// A day the club never opens at all.
	if _, ok := clamp(sailingEvent("2026-06-08", "13:00", "16:00"), juneHours()); ok {
		t.Error("closed Monday should drop the lesson")
	}
}

func TestClampKeepsNominalTimesWhenHoursUnknown(t *testing.T) {
	// Site down and nothing cached for the date: nominal beats nothing.
	got, ok := clamp(sailingEvent("2026-06-02", "13:00", "16:00"), juneHours())
	if !ok || got["start"] != "13:00" || got["end"] != "16:00" {
		t.Fatalf("unknown-hours event = %+v, ok=%v; want it passed through", got, ok)
	}
}

func TestClampOpenEndedAndAllDayEvents(t *testing.T) {
	// No end: clamp the start, stay open-ended.
	e := sailingEvent("2026-06-01", "10:00", "")
	delete(e, "end")
	got, ok := clamp(e, juneHours())
	if !ok || got["start"] != "12:00" || got["end"] != nil {
		t.Fatalf("open-ended event = %+v, ok=%v; want 12:00 and no end", got, ok)
	}
	// An all-day sailing note has no window of its own, so it adopts the club's.
	allDay := event{"title": "Work Party", "all_day": true, "tags": []any{"sailing"},
		"date": "2026-06-04", "sort": "2026-06-04T00:00", "source": "recurring"}
	got, ok = clamp(allDay, juneHours())
	if !ok || got["start"] != "09:00" || got["end"] != "20:02" || got["all_day"] != false {
		t.Fatalf("all-day event = %+v, ok=%v; want 09:00-20:02", got, ok)
	}
}

func TestClampAllLeavesUntaggedEventsAlone(t *testing.T) {
	in := []byte(`[{"title":"Dentist","start":"13:00","end":"14:00","all_day":false,` +
		`"tags":["health"],"date":"2026-06-06","sort":"2026-06-06T13:00","source":"gcal"}]`)
	var got []event
	if err := json.Unmarshal(clampAll(in), &got); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if len(got) != 1 || got[0]["start"] != "13:00" {
		t.Fatalf("untagged event = %+v, want it untouched", got)
	}
}

func TestClampAllPassesGarbageThrough(t *testing.T) {
	// A filter that cannot parse its input must not empty the dashboard.
	in := []byte(`{"not":"an array"}`)
	if got := clampAll(in); string(got) != string(in) {
		t.Errorf("clampAll(garbage) = %s, want the input verbatim", got)
	}
}

func TestHasTagIsCaseInsensitive(t *testing.T) {
	if !hasTag(event{"tags": []any{"Event", " SAILING "}}, "sailing") {
		t.Error("hasTag should match regardless of case and surrounding space")
	}
	if hasTag(event{"tags": []any{"dance"}}, "sailing") {
		t.Error("hasTag matched an unrelated tag")
	}
	if hasTag(event{}, "sailing") {
		t.Error("hasTag matched an event with no tags at all")
	}
}
