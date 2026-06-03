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
		"2026-06-01": {"12:00", "19:55"}, // Noon to 7:55 PM
		"2026-06-03": {"12:02", "19:56"}, // Late Open 12:02 PM to 7:56 PM (today, appears twice in page)
		"2026-06-13": {"09:00", "20:02"}, // 9:00 AM to 8:02 PM
		"2026-06-30": {"12:00", "20:05"}, // Noon to 8:05 PM
	}
	for date, w := range want {
		if got[date] != w {
			t.Errorf("parseMonth[%s] = %+v, want %+v", date, got[date], w)
		}
	}
	// June is fully open: expect a window for every calendar day, no duplicates.
	if len(got) != 30 {
		t.Errorf("parseMonth produced %d open days, want 30", len(got))
	}
}

func TestParseMonthClosedDayExcluded(t *testing.T) {
	const mixed = `
<tr class="tidedata"><td class="tidedatadate nopadding"><a href="https://x?id=9414816&bdate=20261214"> Sun Dec 14</a></td>
<td class="nopadding timelinecell"><span class="tideok" style="width:100%;">Noon to 4:51 PM</span></td></tr>
<tr class="tidedata"><td class="tidedatadate nopadding"><a href="https://x?id=9414816&bdate=20261215"> Mon Dec 15</a></td>
<td class="nopadding timelinecell"><span class="tideclosed" style="width:100%;">Closed</span></td></tr>`
	got := parseMonth([]byte(mixed))
	if _, ok := got["2026-12-15"]; ok {
		t.Error("closed day 2026-12-15 should be absent")
	}
	if got["2026-12-14"] != (dayInfo{"12:00", "16:51"}) {
		t.Errorf("open day 2026-12-14 = %+v, want 12:00/16:51", got["2026-12-14"])
	}
}

func TestCacheRoundTripAndMerge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CSC_CACHE_FILE", filepath.Join(dir, "cache.json"))

	first := map[string]dayInfo{"2026-06-01": {"12:00", "19:55"}}
	saveCache(first)
	if got := loadCache(); !reflect.DeepEqual(got, first) {
		t.Fatalf("loadCache = %+v, want %+v", got, first)
	}

	// A later fetch of a new month must not drop the cached prior month.
	live := map[string]dayInfo{"2026-07-01": {"12:00", "20:30"}}
	merged := mergeCache(live)
	if merged["2026-06-01"] != (dayInfo{"12:00", "19:55"}) || merged["2026-07-01"] != (dayInfo{"12:00", "20:30"}) {
		t.Fatalf("mergeCache lost a key: %+v", merged)
	}
}

func TestEnvToday(t *testing.T) {
	t.Setenv("EVENTS_TODAY", "2026-06-03")
	if got := envToday(); got != "2026-06-03" {
		t.Errorf("envToday = %q, want 2026-06-03", got)
	}
}
