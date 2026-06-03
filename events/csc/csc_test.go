package main

import (
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

func TestNormalizeWeekday(t *testing.T) {
	cases := map[string]string{
		"Mon": "monday", "monday": "monday", "thursday": "thursday",
		"Thu": "thursday", "SAT": "saturday", "funday": "",
	}
	for in, want := range cases {
		if got := normalizeWeekday(in); got != want {
			t.Errorf("normalizeWeekday(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsDST(t *testing.T) {
	if !isDST("2026-06-15", "America/Los_Angeles") {
		t.Error("June 15 should be DST in Pacific")
	}
	if isDST("2026-01-15", "America/Los_Angeles") {
		t.Error("January 15 should not be DST in Pacific")
	}
}

// lessonsFixture mirrors csc-lessons.conf.example.
func lessonsFixture() map[string][]lessonDef {
	return map[string][]lessonDef{
		"monday":   {{start: 780, end: 960, dstStart: 780, dstEnd: 1020, hasDST: true}},
		"thursday": {{start: 780, end: 960, dstStart: 780, dstEnd: 1020, hasDST: true}},
		"saturday": {{start: 600, end: 780, dstStart: 600, dstEnd: 780}},
	}
}

func TestLoadLessons(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "csc-lessons.conf")
	body := "# comment\nMonday 13:00 16:00 13:00 17:00\nThu 13:00 16:00 13:00 17:00\nSaturday 10:00 13:00\n\nGarbage line\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CSC_LESSONS_FILE", path)

	got := loadLessons()
	if !reflect.DeepEqual(got, lessonsFixture()) {
		t.Errorf("loadLessons = %+v, want %+v", got, lessonsFixture())
	}
}

func TestWindowsForToday(t *testing.T) {
	lessons := lessonsFixture()
	cases := []struct {
		date string // 2026-06-01 Mon, -04 Thu, -06 Sat, -02 Tue; 2026-01-05 Mon (no DST)
		want [][2]int
	}{
		{"2026-06-01", [][2]int{{780, 1020}}}, // Monday in DST -> 1-5 PM
		{"2026-01-05", [][2]int{{780, 960}}},  // Monday, no DST -> 1-4 PM
		{"2026-06-04", [][2]int{{780, 1020}}}, // Thursday in DST -> 1-5 PM
		{"2026-06-06", [][2]int{{600, 780}}},  // Saturday -> 10 AM-1 PM (no DST variant)
		{"2026-06-02", nil},                   // Tuesday -> no lessons
	}
	for _, c := range cases {
		got := windowsForToday(c.date, "America/Los_Angeles", lessons)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("windowsForToday(%s) = %v, want %v", c.date, got, c.want)
		}
	}
}

func TestClampWindows(t *testing.T) {
	// Thursday 1-5 PM clamped to a club day open 12:36, close 19:57 -> unchanged.
	if got := clampWindows([][2]int{{780, 1020}}, toMinutes("12:36"), toMinutes("19:57")); !reflect.DeepEqual(got, [][2]int{{780, 1020}}) {
		t.Errorf("clamp Thursday = %v, want [[780 1020]]", got)
	}
	// Saturday 10 AM-1 PM but the club opens 1:30 PM (late tide) -> no overlap.
	if got := clampWindows([][2]int{{600, 780}}, toMinutes("13:30"), toMinutes("19:58")); len(got) != 0 {
		t.Errorf("clamp late-open Saturday = %v, want empty", got)
	}
	// Saturday 10 AM-1 PM with the club open 9 AM-8 PM -> unchanged.
	if got := clampWindows([][2]int{{600, 780}}, toMinutes("09:00"), toMinutes("20:02")); !reflect.DeepEqual(got, [][2]int{{600, 780}}) {
		t.Errorf("clamp open Saturday = %v, want [[600 780]]", got)
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

func TestEnvToday(t *testing.T) {
	t.Setenv("EVENTS_TODAY", "2026-06-03")
	if got := envToday(); got != "2026-06-03" {
		t.Errorf("envToday = %q, want 2026-06-03", got)
	}
}
