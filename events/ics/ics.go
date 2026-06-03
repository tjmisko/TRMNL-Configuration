// Command ics is the generic calendar events source adapter. It reads a list of
// .ics feed URLs (Luma calendars, a Google Calendar "secret iCal address", any
// RFC 5545 feed), fetches them concurrently, expands recurring events (RRULE) to
// today's occurrence, and prints a JSON array of normalized event objects for
// *today* on stdout.
//
// One dead/unreachable/malformed feed never breaks the others: its goroutine
// logs to stderr and contributes nothing. With no feeds configured it prints
// "[]" and exits 0, so the rest of the events pipeline keeps working. Pure Go
// stdlib — no external modules (same constraint as BART).
//
// Normalized schema (shared by every events source adapter; see events/README.md):
//
//	{ "title", "start"|null, "end"|null, "all_day", "date", "sort", "source" }
//
// start/end are local "HH:MM" (24h), date is local "YYYY-MM-DD", sort is
// "YYYY-MM-DDTHH:MM". This adapter pre-filters to today (in $EVENTS_TZ /
// $EVENTS_TODAY) for efficiency — a full Google calendar can be megabytes — but
// the aggregator (events/fetch) remains the source of truth for the window and
// re-applies the today filter and the cross-source ordering.
//
// Feeds come from a config file (one `label  url` per line, # comments), whose
// path is read from $EVENTS_FEEDS_FILE. $LUMA_ICS_URL, if set, is honored as an
// additional feed labeled "luma" for backward compatibility.
//
// Recurrence support: FREQ DAILY/WEEKLY/MONTHLY/YEARLY with INTERVAL, BYDAY
// (incl. ordinals like 2MO/-1SU for MONTHLY), BYMONTHDAY, BYMONTH, UNTIL, COUNT,
// and EXDATE. STATUS:CANCELLED events are dropped. A VEVENT carrying a
// RECURRENCE-ID is treated as an override instance: it is emitted on its own
// date and that date is suppressed on the matching master series (by UID).
// Known limitations: no DURATION (DTEND is used), no BYSETPOS, no WKST other
// than Monday.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const userAgent = "GooseTRM/1.0 (events-ics)"

type event struct {
	Title  string  `json:"title"`
	Start  *string `json:"start"`
	End    *string `json:"end"`
	AllDay bool    `json:"all_day"`
	Date   string  `json:"date"`
	Sort   string  `json:"sort"`
	Source string  `json:"source"`
}

type feed struct {
	label string
	url   string
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

// ── feed configuration ──────────────────────────────────────────────

// loadFeeds reads the feed list from $EVENTS_FEEDS_FILE plus the legacy
// $LUMA_ICS_URL. Config lines are "label  url"; a line with only a URL gets a
// label derived from its host. Blank lines and lines starting with # are ignored.
func loadFeeds() []feed {
	var feeds []feed
	seen := map[string]bool{}

	add := func(label, raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		if label == "" {
			label = deriveLabel(raw)
		}
		feeds = append(feeds, feed{label: label, url: raw})
	}

	if path := strings.TrimSpace(os.Getenv("EVENTS_FEEDS_FILE")); path != "" {
		if f, err := os.Open(path); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := strings.TrimSpace(strings.TrimRight(sc.Text(), "\r"))
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				label, rest := "", line
				if i := strings.IndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' }); i >= 0 {
					label = strings.TrimSpace(line[:i])
					rest = strings.TrimSpace(line[i+1:])
				}
				if rest == "" { // single token: it was the URL, not a label
					add("", label)
					continue
				}
				add(label, rest)
			}
		}
	}

	add("luma", os.Getenv("LUMA_ICS_URL"))
	return feeds
}

// deriveLabel produces a short provenance tag from a feed URL's host.
func deriveLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "ics"
	}
	host := strings.ToLower(u.Host)
	switch {
	case strings.Contains(host, "lu.ma"):
		return "luma"
	case strings.Contains(host, "google"):
		return "gcal"
	default:
		host = strings.TrimPrefix(host, "www.")
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		return host
	}
}

// ── ICS line parsing ────────────────────────────────────────────────

// unfold joins RFC 5545 folded lines (continuations begin with space or tab).
func unfold(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
		} else {
			lines = append(lines, line)
		}
	}
	return lines
}

// splitProperty parses "NAME;PARAM=val:value" into name, params, value.
func splitProperty(line string) (string, map[string]string, string) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return strings.ToUpper(strings.TrimSpace(line)), nil, ""
	}
	head, value := line[:colon], line[colon+1:]
	parts := strings.Split(head, ";")
	name := strings.ToUpper(strings.TrimSpace(parts[0]))
	params := map[string]string{}
	for _, p := range parts[1:] {
		if eq := strings.IndexByte(p, '='); eq >= 0 {
			params[strings.ToUpper(strings.TrimSpace(p[:eq]))] = strings.TrimSpace(p[eq+1:])
		}
	}
	return name, params, value
}

func unescape(value string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return r.Replace(value)
}

// ── date/time interpretation ────────────────────────────────────────

// parsedTime is the result of interpreting a DTSTART/DTEND/EXDATE value.
type parsedTime struct {
	t      time.Time // in the target location
	allDay bool
	ok     bool
}

func parseDT(value string, params map[string]string, target *time.Location) parsedTime {
	value = strings.TrimSpace(value)
	if strings.EqualFold(params["VALUE"], "DATE") || (len(value) == 8 && !strings.Contains(value, "T")) {
		t, err := time.ParseInLocation("20060102", value, target)
		if err != nil {
			return parsedTime{}
		}
		return parsedTime{t: t, allDay: true, ok: true}
	}
	if strings.HasSuffix(value, "Z") {
		t, err := time.ParseInLocation("20060102T150405", strings.TrimSuffix(value, "Z"), time.UTC)
		if err != nil {
			return parsedTime{}
		}
		return parsedTime{t: t.In(target), ok: true}
	}
	src := target
	if tzid := params["TZID"]; tzid != "" {
		if loc, err := time.LoadLocation(tzid); err == nil {
			src = loc
		}
	}
	t, err := time.ParseInLocation("20060102T150405", value, src)
	if err != nil {
		return parsedTime{}
	}
	return parsedTime{t: t.In(target), ok: true}
}

// civil returns the UTC-midnight of t's calendar date, so date arithmetic
// (differences, week/month/year offsets) is free of DST hour drift.
func civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func dateStr(t time.Time) string { return t.Format("2006-01-02") }

func daysBetween(a, b time.Time) int {
	return int(civil(b).Sub(civil(a)).Hours() / 24)
}

// startOfWeek returns the Monday (WKST default) of t's week, as a civil date.
func startOfWeek(t time.Time) time.Time {
	c := civil(t)
	offset := (int(c.Weekday()) + 6) % 7 // days since Monday (Sun=6 … Mon=0)
	return c.AddDate(0, 0, -offset)
}

// ── RRULE ───────────────────────────────────────────────────────────

type byDay struct {
	ord int // 0 = no ordinal, e.g. 2 for "2nd", -1 for "last"
	wd  time.Weekday
}

type rrule struct {
	freq       string
	interval   int
	count      int // 0 = unset
	until      time.Time
	untilSet   bool
	byday      []byDay
	byMonthDay []int
	byMonth    []time.Month
}

var weekdayCodes = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

func parseRRule(value string, target *time.Location) (rrule, bool) {
	r := rrule{interval: 1}
	for _, part := range strings.Split(value, ";") {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(part[:eq]))
		val := strings.TrimSpace(part[eq+1:])
		switch key {
		case "FREQ":
			r.freq = strings.ToUpper(val)
		case "INTERVAL":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				r.interval = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				r.count = n
			}
		case "UNTIL":
			if pt := parseDT(val, nil, target); pt.ok {
				r.until = civil(pt.t)
				r.untilSet = true
			}
		case "BYDAY":
			for _, tok := range strings.Split(val, ",") {
				if bd, ok := parseByDay(tok); ok {
					r.byday = append(r.byday, bd)
				}
			}
		case "BYMONTHDAY":
			for _, tok := range strings.Split(val, ",") {
				if n, err := strconv.Atoi(strings.TrimSpace(tok)); err == nil {
					r.byMonthDay = append(r.byMonthDay, n)
				}
			}
		case "BYMONTH":
			for _, tok := range strings.Split(val, ",") {
				if n, err := strconv.Atoi(strings.TrimSpace(tok)); err == nil && n >= 1 && n <= 12 {
					r.byMonth = append(r.byMonth, time.Month(n))
				}
			}
		}
	}
	if r.freq == "" {
		return rrule{}, false
	}
	return r, true
}

func parseByDay(tok string) (byDay, bool) {
	tok = strings.TrimSpace(strings.ToUpper(tok))
	if len(tok) < 2 {
		return byDay{}, false
	}
	code := tok[len(tok)-2:]
	wd, ok := weekdayCodes[code]
	if !ok {
		return byDay{}, false
	}
	ord := 0
	if prefix := tok[:len(tok)-2]; prefix != "" {
		if n, err := strconv.Atoi(prefix); err == nil {
			ord = n
		}
	}
	return byDay{ord: ord, wd: wd}, true
}

// matchesPattern reports whether civil date d satisfies the freq/interval +
// BY* constraints of r, anchored at dtstart. It ignores COUNT/UNTIL/EXDATE
// (handled by occursOn) and assumes d is not before dtstart.
func matchesPattern(r rrule, dtstart, d time.Time) bool {
	if len(r.byMonth) > 0 && !containsMonth(r.byMonth, d.Month()) {
		return false
	}
	switch r.freq {
	case "DAILY":
		if daysBetween(dtstart, d)%r.interval != 0 {
			return false
		}
		return bydayWeekdayOK(r.byday, d) && bymonthdayOK(r.byMonthDay, d)
	case "WEEKLY":
		weeks := daysBetween(startOfWeek(dtstart), startOfWeek(d)) / 7
		if weeks%r.interval != 0 {
			return false
		}
		days := r.byday
		if len(days) == 0 {
			days = []byDay{{wd: dtstart.Weekday()}}
		}
		return bydayWeekdayOK(days, d)
	case "MONTHLY":
		months := (d.Year()-dtstart.Year())*12 + int(d.Month()) - int(dtstart.Month())
		if months%r.interval != 0 {
			return false
		}
		if len(r.byMonthDay) > 0 {
			return bymonthdayOK(r.byMonthDay, d)
		}
		if len(r.byday) > 0 {
			return bydayOrdinalOK(r.byday, d)
		}
		return d.Day() == dtstart.Day()
	case "YEARLY":
		if (d.Year()-dtstart.Year())%r.interval != 0 {
			return false
		}
		monthOK := len(r.byMonth) > 0 || d.Month() == dtstart.Month()
		if len(r.byMonthDay) > 0 {
			return monthOK && bymonthdayOK(r.byMonthDay, d)
		}
		return monthOK && d.Day() == dtstart.Day()
	}
	return false
}

func containsMonth(list []time.Month, m time.Month) bool {
	for _, x := range list {
		if x == m {
			return true
		}
	}
	return false
}

// bydayWeekdayOK is true if d's weekday is among the listed weekdays (ordinals
// ignored — used for DAILY/WEEKLY filtering).
func bydayWeekdayOK(days []byDay, d time.Time) bool {
	if len(days) == 0 {
		return true
	}
	for _, bd := range days {
		if bd.wd == d.Weekday() {
			return true
		}
	}
	return false
}

func bymonthdayOK(list []int, d time.Time) bool {
	if len(list) == 0 {
		return true
	}
	last := daysInMonth(d.Year(), d.Month())
	for _, n := range list {
		if n < 0 {
			n = last + n + 1
		}
		if n == d.Day() {
			return true
		}
	}
	return false
}

// bydayOrdinalOK handles MONTHLY BYDAY with ordinals: "2MO" = 2nd Monday,
// "-1FR" = last Friday, "MO" (ord 0) = every Monday in the month.
func bydayOrdinalOK(days []byDay, d time.Time) bool {
	for _, bd := range days {
		if bd.wd != d.Weekday() {
			continue
		}
		if bd.ord == 0 {
			return true
		}
		idxFromStart := (d.Day()-1)/7 + 1
		last := daysInMonth(d.Year(), d.Month())
		idxFromEnd := (last-d.Day())/7 + 1
		if bd.ord > 0 && bd.ord == idxFromStart {
			return true
		}
		if bd.ord < 0 && -bd.ord == idxFromEnd {
			return true
		}
	}
	return false
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// occursOn reports whether the recurring series with this rrule and dtstart has
// an occurrence on civil date target, honoring UNTIL, COUNT and the excluded
// date set (EXDATE ∪ override dates).
func occursOn(r rrule, dtstart, target time.Time, excluded map[string]bool) bool {
	dtstart, target = civil(dtstart), civil(target)
	if target.Before(dtstart) {
		return false
	}
	if r.untilSet && target.After(r.until) {
		return false
	}
	if excluded[dateStr(target)] {
		return false
	}
	if !matchesPattern(r, dtstart, target) {
		return false
	}
	if r.count == 0 {
		return true
	}
	// COUNT is set: today qualifies only if its 1-based ordinal in the series
	// is within COUNT. Walk forward from dtstart, counting matches, until we
	// pass today or exhaust the count.
	ordinal := 0
	for d := dtstart; !d.After(target); d = d.AddDate(0, 0, 1) {
		if matchesPattern(r, dtstart, d) {
			ordinal++
			if ordinal > r.count {
				return false
			}
			if d.Equal(target) {
				return true
			}
		}
	}
	return false
}

// ── VEVENT model ────────────────────────────────────────────────────

type vevent struct {
	summary      string
	uid          string
	dtstart      parsedTime
	dtend        parsedTime
	hasEnd       bool
	rule         *rrule
	exdates      map[string]bool
	cancelled    bool
	recurrenceID *time.Time // set (civil) if this VEVENT overrides one instance
}

func parseCalendar(text string, target *time.Location) []vevent {
	var out []vevent
	var cur *vevent
	for _, line := range unfold(text) {
		name, params, value := splitProperty(line)
		switch {
		case name == "BEGIN" && strings.EqualFold(strings.TrimSpace(value), "VEVENT"):
			cur = &vevent{exdates: map[string]bool{}}
		case name == "END" && strings.EqualFold(strings.TrimSpace(value), "VEVENT"):
			if cur != nil && cur.dtstart.ok {
				out = append(out, *cur)
			}
			cur = nil
		case cur == nil:
			// ignore properties outside a VEVENT (VCALENDAR, VTIMEZONE, …)
		case name == "SUMMARY":
			cur.summary = strings.TrimSpace(unescape(value))
		case name == "UID":
			cur.uid = strings.TrimSpace(value)
		case name == "DTSTART":
			cur.dtstart = parseDT(value, params, target)
		case name == "DTEND":
			cur.dtend = parseDT(value, params, target)
			cur.hasEnd = cur.dtend.ok
		case name == "STATUS":
			cur.cancelled = strings.EqualFold(strings.TrimSpace(value), "CANCELLED")
		case name == "RRULE":
			if r, ok := parseRRule(value, target); ok {
				cur.rule = &r
			}
		case name == "EXDATE":
			for _, v := range strings.Split(value, ",") {
				if pt := parseDT(v, params, target); pt.ok {
					cur.exdates[dateStr(pt.t)] = true
				}
			}
		case name == "RECURRENCE-ID":
			if pt := parseDT(value, params, target); pt.ok {
				c := civil(pt.t)
				cur.recurrenceID = &c
			}
		}
	}
	return out
}

// expand turns parsed VEVENTs into normalized events occurring on `today`.
func expand(vevents []vevent, source string, today time.Time, target *time.Location) []event {
	// Suppress master-series occurrences that have been individually moved or
	// cancelled (override instances carry the same UID + a RECURRENCE-ID).
	overrides := map[string]map[string]bool{}
	for _, ve := range vevents {
		if ve.recurrenceID != nil && ve.uid != "" {
			if overrides[ve.uid] == nil {
				overrides[ve.uid] = map[string]bool{}
			}
			overrides[ve.uid][dateStr(*ve.recurrenceID)] = true
		}
	}

	var out []event
	todayStr := dateStr(today)
	for _, ve := range vevents {
		if ve.cancelled {
			continue
		}
		if ve.recurrenceID != nil {
			// Override instance: a one-off on its own DTSTART.
			if ev, ok := single(ve, today, todayStr, source); ok {
				out = append(out, ev)
			}
			continue
		}
		if ve.rule != nil {
			excluded := ve.exdates
			if ov := overrides[ve.uid]; ov != nil {
				excluded = union(ve.exdates, ov)
			}
			if occursOn(*ve.rule, ve.dtstart.t, today, excluded) {
				out = append(out, recurringOccurrence(ve, today, todayStr, source, target))
			}
			continue
		}
		if ev, ok := single(ve, today, todayStr, source); ok {
			out = append(out, ev)
		}
	}
	return out
}

func union(a, b map[string]bool) map[string]bool {
	m := map[string]bool{}
	for k := range a {
		m[k] = true
	}
	for k := range b {
		m[k] = true
	}
	return m
}

// single emits a non-recurring VEVENT if it falls on today. All-day events use
// an exclusive DTEND, so a multi-day span shows on every day it covers.
func single(ve vevent, today time.Time, todayStr, source string) (event, bool) {
	title := titleOf(ve.summary)
	if ve.dtstart.allDay {
		start := civil(ve.dtstart.t)
		endExcl := start.AddDate(0, 0, 1)
		if ve.hasEnd && ve.dtend.allDay {
			endExcl = civil(ve.dtend.t)
		}
		ct := civil(today)
		if !ct.Before(start) && ct.Before(endExcl) {
			return allDayEvent(title, todayStr, source), true
		}
		return event{}, false
	}
	if dateStr(ve.dtstart.t) != todayStr {
		return event{}, false
	}
	return timedEvent(title, ve.dtstart.t, ve.dtend, ve.hasEnd, todayStr, source), true
}

// recurringOccurrence builds the event for a series occurrence on `today`,
// re-anchoring the original clock time (and DTEND-derived duration) to today.
func recurringOccurrence(ve vevent, today time.Time, todayStr, source string, target *time.Location) event {
	title := titleOf(ve.summary)
	if ve.dtstart.allDay {
		return allDayEvent(title, todayStr, source)
	}
	y, m, d := today.Date()
	sh, sm, _ := ve.dtstart.t.Clock()
	start := time.Date(y, m, d, sh, sm, 0, 0, target)
	endPt := parsedTime{}
	if ve.hasEnd && !ve.dtend.allDay {
		endPt = parsedTime{t: start.Add(ve.dtend.t.Sub(ve.dtstart.t)), ok: true}
	}
	return timedEvent(title, start, endPt, endPt.ok, todayStr, source)
}

func titleOf(summary string) string {
	if summary == "" {
		return "(untitled)"
	}
	return summary
}

func allDayEvent(title, date, source string) event {
	return event{Title: title, AllDay: true, Date: date, Sort: date + "T00:00", Source: source}
}

func timedEvent(title string, start time.Time, end parsedTime, hasEnd bool, date, source string) event {
	hhmm := start.Format("15:04")
	ev := event{Title: title, Start: &hhmm, Date: date, Sort: date + "T" + hhmm, Source: source}
	if hasEnd && end.ok {
		e := end.t.Format("15:04")
		ev.End = &e
	}
	return ev
}

// ── fetch / main ────────────────────────────────────────────────────

func fetch(feedURL string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, feedURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func targetLocation() *time.Location {
	name := os.Getenv("EVENTS_TZ")
	if name == "" {
		name = "America/Los_Angeles"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

func targetToday(loc *time.Location) time.Time {
	if s := strings.TrimSpace(os.Getenv("EVENTS_TODAY")); s != "" {
		if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
			return t
		}
	}
	return time.Now().In(loc)
}

func main() {
	feeds := loadFeeds()
	if len(feeds) == 0 {
		emit(nil)
		return
	}

	loc := targetLocation()
	today := targetToday(loc)

	results := make([][]event, len(feeds))
	var wg sync.WaitGroup
	for i := range feeds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text, err := fetch(feeds[i].url)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ics: feed %q fetch failed: %v\n", feeds[i].label, err)
				return
			}
			results[i] = expand(parseCalendar(text, loc), feeds[i].label, today, loc)
		}(i)
	}
	wg.Wait()

	var all []event
	for _, evs := range results {
		all = append(all, evs...)
	}
	// Stable local ordering (the aggregator re-sorts across all sources).
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].AllDay != all[j].AllDay {
			return all[i].AllDay
		}
		if all[i].Sort != all[j].Sort {
			return all[i].Sort < all[j].Sort
		}
		return all[i].Title < all[j].Title
	})
	emit(all)
}
