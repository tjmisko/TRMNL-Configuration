// Events source adapter: Cal Sailing Club Beginning Sailing Lesson window.
//
// Two inputs are combined:
//
//  1. The club's live open/close schedule, scraped from
//     https://www.cal-sailing.org/resources/csc-openclose-times?view=month — a
//     server-rendered month table where each row carries a NOAA tide link
//     (bdate=YYYYMMDD) and a "club timeline" cell. The open cell holds a
//     <span class="tideok">OPEN to CLOSE</span> with 12-hour times (or the
//     literal "Noon"); a fully-closed day has no tideok span. Rows are parsed by
//     bdate (robust to the page's duplicated "today" summary and multi-line
//     rows) and converted to 24-hour HH:MM.
//
//  2. The Beginning Sailing Lesson windows, by weekday, from
//     events/csc-lessons.conf (e.g. Mon/Thu afternoons, Sat mornings, with an
//     optional Daylight-Saving variant). A weekday with no entry has no lessons.
//
// The emitted event is the INTERSECTION: lessons only happen inside their window,
// but the club closes per the live schedule, so the shown time is
// [max(open, lesson_start), min(close, lesson_end)] for each window today. If the
// club opens late enough to miss a window (tides), or is closed, or it isn't a
// lesson day, nothing is emitted. Schema (events/README.md):
//
//	{ "title", "start", "end", "all_day", "date", "sort", "source" }
//
// Robustness: a successful fetch caches the whole visible month (open AND closed
// days) to disk and is reused when the site is unreachable. A closed day, a
// non-lesson day, an empty overlap, or ANY error prints "[]" and exits 0, so the
// rest of the events pipeline keeps working. Pure Go stdlib — no third-party
// packages.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	title      = "Cal Sailing Club @ Berkeley Marina"
	source     = "csc"
	defaultURL = "https://www.cal-sailing.org/resources/csc-openclose-times?view=month"
	userAgent  = "GooseTRM/1.0 (events-csc)"
)

type event struct {
	Title  string `json:"title"`
	Start  string `json:"start"`
	End    string `json:"end"`
	AllDay bool   `json:"all_day"`
	Date   string `json:"date"`
	Sort   string `json:"sort"`
	Source string `json:"source"`
}

// dayInfo is one scraped day. Open days carry Open/Close (24h HH:MM); closed days
// carry Closed=true. Recording closed days (rather than omitting them) lets us
// tell "club shut today" apart from "we never scraped this day".
type dayInfo struct {
	Open   string `json:"open,omitempty"`
	Close  string `json:"close,omitempty"`
	Closed bool   `json:"closed,omitempty"`
}

// lessonDef is one configured lesson window for a weekday, in minutes since
// midnight, with an optional alternate window used while Daylight Saving Time is
// in effect.
type lessonDef struct {
	start, end       int
	dstStart, dstEnd int
	hasDST           bool
}

var (
	bdateRe  = regexp.MustCompile(`bdate=(\d{8})`)
	tideokRe = regexp.MustCompile(`(?s)class="tideok"[^>]*>(.*?)</span>`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	wsRe     = regexp.MustCompile(`\s+`)
	clockRe  = regexp.MustCompile(`^(\d{1,2}):(\d{2})\s*([AaPp][Mm])$`)
	hhmmRe   = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
)

func main() {
	emit(run())
}

// run never panics; any failure path returns nil so emit prints "[]".
func run() (events []event) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "csc: recovered from panic: %v\n", r)
			events = nil
		}
	}()

	today := envToday()
	tz := getenv("EVENTS_TZ", "America/Los_Angeles")
	lessons := loadLessons()

	// Degraded mode: no lesson schedule configured -> show raw club hours so a
	// missing config never silently blanks the feature.
	if len(lessons) == 0 {
		fmt.Fprintln(os.Stderr, "csc: no lessons config found; showing raw club hours")
		info, known := clubSchedule()[today]
		if !known || info.Closed || info.Open == "" || info.Close == "" {
			return nil
		}
		return []event{newEvent(today, info.Open, info.Close)}
	}

	windows := windowsForToday(today, tz, lessons)
	if len(windows) == 0 {
		return nil // not a lesson day
	}

	info, known := clubSchedule()[today]
	if known && info.Closed {
		return nil // club shut today: no lessons regardless of the schedule
	}
	open, close := 0, 24*60
	if known && info.Open != "" && info.Close != "" {
		open, close = toMinutes(info.Open), toMinutes(info.Close)
	} else {
		fmt.Fprintln(os.Stderr, "csc: club hours unavailable; showing lesson windows unclamped")
	}

	for _, w := range clampWindows(windows, open, close) {
		events = append(events, newEvent(today, fromMinutes(w[0]), fromMinutes(w[1])))
	}
	return events
}

func newEvent(date, start, end string) event {
	return event{
		Title:  title,
		Start:  start,
		End:    end,
		AllDay: false,
		Date:   date,
		Sort:   date + "T" + start,
		Source: source,
	}
}

func emit(events []event) {
	if events == nil {
		events = []event{}
	}
	out, err := json.Marshal(events)
	if err != nil {
		fmt.Println("[]")
		return
	}
	fmt.Println(string(out))
}

// envToday resolves the local date the way the aggregator does, so this adapter
// agrees with events/fetch even across a midnight boundary.
func envToday() string {
	if v := os.Getenv("EVENTS_TODAY"); v != "" {
		return v
	}
	tz := getenv("EVENTS_TZ", "America/Los_Angeles")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format("2006-01-02")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Lesson schedule ─────────────────────────────────────────────────

// windowsForToday returns today's lesson windows (minutes since midnight, DST
// resolved) for the weekday of `today`, or nil if it is not a lesson day.
func windowsForToday(today, tz string, lessons map[string][]lessonDef) [][2]int {
	wd := weekdayOf(today)
	defs := lessons[wd]
	if len(defs) == 0 {
		return nil
	}
	dst := isDST(today, tz)
	var out [][2]int
	for _, d := range defs {
		if dst && d.hasDST {
			out = append(out, [2]int{d.dstStart, d.dstEnd})
		} else {
			out = append(out, [2]int{d.start, d.end})
		}
	}
	return out
}

// clampWindows intersects each lesson window with the club's [open, close] and
// drops any that no longer overlap.
func clampWindows(windows [][2]int, open, close int) [][2]int {
	var out [][2]int
	for _, w := range windows {
		s, e := max(w[0], open), min(w[1], close)
		if s < e {
			out = append(out, [2]int{s, e})
		}
	}
	return out
}

func weekdayOf(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return strings.ToLower(t.Weekday().String())
}

func isDST(date, tz string) bool {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false
	}
	t, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return false
	}
	// Check at local noon, well clear of the 02:00 transition instant.
	return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, loc).IsDST()
}

var weekdayCanon = map[string]string{
	"mon": "monday", "monday": "monday",
	"tue": "tuesday", "tues": "tuesday", "tuesday": "tuesday",
	"wed": "wednesday", "weds": "wednesday", "wednesday": "wednesday",
	"thu": "thursday", "thur": "thursday", "thurs": "thursday", "thursday": "thursday",
	"fri": "friday", "friday": "friday",
	"sat": "saturday", "saturday": "saturday",
	"sun": "sunday", "sunday": "sunday",
}

func normalizeWeekday(tok string) string {
	return weekdayCanon[strings.ToLower(strings.TrimSpace(tok))]
}

func lessonsFile() string {
	if v := os.Getenv("CSC_LESSONS_FILE"); v != "" {
		return v
	}
	// Binary lives at events/sources/csc; config sits in events/ alongside the
	// other event-source configs (feeds.conf, ignore.conf).
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	return filepath.Join(dir, "..", "csc-lessons.conf")
}

// loadLessons parses csc-lessons.conf into weekday -> windows. A missing file
// yields an empty map (degraded mode). Bad lines are warned and skipped.
func loadLessons() map[string][]lessonDef {
	f, err := os.Open(lessonsFile())
	if err != nil {
		return map[string][]lessonDef{}
	}
	defer f.Close()

	out := map[string][]lessonDef{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			fmt.Fprintf(os.Stderr, "csc: lessons: ignoring malformed line %q\n", line)
			continue
		}
		wd := normalizeWeekday(fields[0])
		if wd == "" {
			fmt.Fprintf(os.Stderr, "csc: lessons: unknown weekday %q\n", fields[0])
			continue
		}
		st, ok1 := parseHHMM(fields[1])
		en, ok2 := parseHHMM(fields[2])
		if !ok1 || !ok2 || en <= st {
			fmt.Fprintf(os.Stderr, "csc: lessons: bad window in %q\n", line)
			continue
		}
		d := lessonDef{start: st, end: en, dstStart: st, dstEnd: en}
		if len(fields) >= 5 {
			ds, ok3 := parseHHMM(fields[3])
			de, ok4 := parseHHMM(fields[4])
			if ok3 && ok4 && de > ds {
				d.dstStart, d.dstEnd, d.hasDST = ds, de, true
			} else {
				fmt.Fprintf(os.Stderr, "csc: lessons: bad DST window in %q\n", line)
			}
		}
		out[wd] = append(out[wd], d)
	}
	return out
}

func parseHHMM(s string) (int, bool) {
	m := hhmmRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mn, _ := strconv.Atoi(m[2])
	if h > 23 || mn > 59 {
		return 0, false
	}
	return h*60 + mn, true
}

func toMinutes(hhmm string) int {
	m, _ := parseHHMM(hhmm)
	return m
}

func fromMinutes(m int) string {
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// ── Club schedule scrape + cache ────────────────────────────────────

// clubSchedule returns the date->dayInfo map, refreshing the on-disk cache from a
// live fetch when possible and falling back to the cache when the site is down.
func clubSchedule() map[string]dayInfo {
	if body, err := fetch(getenv("CSC_URL", defaultURL)); err != nil {
		fmt.Fprintf(os.Stderr, "csc: fetch failed: %v\n", err)
	} else if live := parseMonth(body); len(live) > 0 {
		merged := mergeCache(live)
		saveCache(merged)
		return merged
	}
	return loadCache()
}

// parseMonth extracts every day from the schedule page, keyed by "YYYY-MM-DD".
// Rows are delimited by their bdate=YYYYMMDD tide link (exactly one per row);
// within a row's slice the first tideok span holds "OPEN to CLOSE". A row with no
// (or an unparseable) tideok is recorded as closed.
func parseMonth(body []byte) map[string]dayInfo {
	s := string(body)
	out := map[string]dayInfo{}
	locs := bdateRe.FindAllStringSubmatchIndex(s, -1)
	for i, m := range locs {
		raw := s[m[2]:m[3]] // the 8 digits
		date := raw[0:4] + "-" + raw[4:6] + "-" + raw[6:8]
		segEnd := len(s)
		if i+1 < len(locs) {
			segEnd = locs[i+1][0]
		}
		seg := s[m[1]:segEnd]
		tm := tideokRe.FindStringSubmatch(seg)
		if tm == nil {
			out[date] = dayInfo{Closed: true} // no open window => closed day
			continue
		}
		open, close, ok := parseWindow(cleanText(tm[1]))
		if !ok {
			fmt.Fprintf(os.Stderr, "csc: unparseable window for %s: %q\n", date, cleanText(tm[1]))
			out[date] = dayInfo{Closed: true} // safe: render nothing
			continue
		}
		out[date] = dayInfo{Open: open, Close: close}
	}
	return out
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// parseWindow splits "OPEN to CLOSE" and converts each side to 24-hour HH:MM.
func parseWindow(text string) (open, close string, ok bool) {
	parts := strings.SplitN(text, " to ", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	o, ok1 := parseClock(parts[0])
	c, ok2 := parseClock(parts[1])
	return o, c, ok1 && ok2
}

// parseClock converts "9:00 AM", "12:02 PM", or the literal "Noon"/"Midnight"
// to 24-hour "HH:MM".
func parseClock(s string) (string, bool) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "noon":
		return "12:00", true
	case "midnight":
		return "00:00", true
	}
	m := clockRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	h, _ := strconv.Atoi(m[1])
	if h < 1 || h > 12 {
		return "", false
	}
	if h == 12 {
		h = 0
	}
	if strings.EqualFold(m[3], "PM") {
		h += 12
	}
	return fmt.Sprintf("%02d:%s", h, m[2]), true
}

func fetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}
		return body, nil
	}
	return nil, lastErr
}

func cacheFile() string {
	if v := os.Getenv("CSC_CACHE_FILE"); v != "" {
		return v
	}
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	return filepath.Join(dir, ".csc-cache.json")
}

func loadCache() map[string]dayInfo {
	m := map[string]dayInfo{}
	b, err := os.ReadFile(cacheFile())
	if err != nil {
		return m
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]dayInfo{}
	}
	return m
}

// mergeCache overlays the freshly parsed month on top of the existing cache so
// older months/days persist (covers month-boundary days the live page dropped).
func mergeCache(live map[string]dayInfo) map[string]dayInfo {
	merged := loadCache()
	for k, v := range live {
		merged[k] = v
	}
	return merged
}

func saveCache(m map[string]dayInfo) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	if err := os.WriteFile(cacheFile(), b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "csc: cache write failed: %v\n", err)
	}
}
