// Events filter: rewrite sailing events to Cal Sailing Club's tide-driven hours.
//
// Cal Sailing sits on Berkeley Marina, whose shallow basin empties at low tide,
// so the club's open and close times move day to day with the water rather than
// following a fixed timetable. The lesson SCHEDULE lives in the notes vault
// (notes tagged `event`, `recurring`, `sailing`; see events/vault-notes/sailing);
// this filter is the part that knows about the water.
//
// It reads the pipeline's JSON array of events on stdin, and for every event
// tagged `sailing` (override with $TIDES_TAG) it intersects the note's nominal
// window with the club's real hours for that date:
//
//	shown = [max(open, note_start), min(close, note_end)]
//
// A day the club never opens, or a window the tide has swallowed whole (a low
// Saturday tide that holds the club shut past 1 PM), drops the event instead of
// advertising hours you cannot sail. Untagged events pass through untouched.
//
// The hours come from the club's published open/close schedule at
// https://www.cal-sailing.org/resources/csc-openclose-times?view=month — a
// server-rendered month table where each row carries a NOAA tide link
// (bdate=YYYYMMDD) and a "club timeline" cell. The open cell holds a
// <span class="tideok">OPEN to CLOSE</span> with 12-hour times (or the literal
// "Noon"); a fully-closed day has no tideok span. Rows are parsed by bdate
// (robust to the page's duplicated "today" summary and multi-line rows) and
// converted to 24-hour HH:MM.
//
// Robustness: a successful fetch caches the whole visible month (open AND closed
// days) to disk and is reused when the site is unreachable. Unlike a source, a
// filter that fails must not empty the dashboard — ANY error, unparseable input,
// or missing schedule reprints stdin verbatim and exits 0, so the worst case is
// sailing times that are nominal rather than tide-corrected. Pure Go stdlib.
//
// Overridable: $CSC_URL, $CSC_CACHE_FILE, $TIDES_TAG.
package main

import (
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
	defaultTag = "sailing"
	defaultURL = "https://www.cal-sailing.org/resources/csc-openclose-times?view=month"
	userAgent  = "GooseTRM/1.0 (events-tides)"
)

// An event is carried as a bag of fields rather than a struct so that every key
// the rest of the pipeline cares about (message, tags, source, ...) survives the
// round trip even though this filter only ever rewrites start, end, and sort.
type event map[string]any

// dayInfo is one scraped day. Open days carry Open/Close (24h HH:MM); closed days
// carry Closed=true. Recording closed days (rather than omitting them) lets us
// tell "club shut today" apart from "we never scraped this day".
type dayInfo struct {
	Open   string `json:"open,omitempty"`
	Close  string `json:"close,omitempty"`
	Closed bool   `json:"closed,omitempty"`
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
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tides: read stdin: %v\n", err)
		fmt.Println("[]")
		return
	}
	os.Stdout.Write(clampAll(in))
}

// clampAll is the whole filter: parse, rewrite the sailing events, re-encode.
// Every failure path returns the input verbatim — a filter that cannot do its
// job must still hand the pipeline back the events it was given.
func clampAll(in []byte) (out []byte) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "tides: recovered from panic: %v\n", r)
			out = in
		}
	}()

	var events []event
	if err := json.Unmarshal(in, &events); err != nil {
		fmt.Fprintf(os.Stderr, "tides: input is not an event array: %v\n", err)
		return in
	}

	tag := getenv("TIDES_TAG", defaultTag)
	if !anyTagged(events, tag) {
		return in // nothing to do; don't hit the network at all
	}

	hours := clubSchedule()
	kept := make([]event, 0, len(events))
	for _, e := range events {
		if !hasTag(e, tag) {
			kept = append(kept, e)
			continue
		}
		if clamped, ok := clamp(e, hours); ok {
			kept = append(kept, clamped)
		}
	}

	encoded, err := json.Marshal(kept)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tides: re-encode failed: %v\n", err)
		return in
	}
	return append(encoded, '\n')
}

// clamp intersects one sailing event with the club's hours for its date. It
// reports false when the event should disappear: the club is shut, or the tide
// has pushed opening past the end of the lesson window.
func clamp(e event, hours map[string]dayInfo) (event, bool) {
	date, _ := e["date"].(string)
	title, _ := e["title"].(string)

	info, known := hours[date]
	if !known || (info.Open == "" && !info.Closed) {
		// No schedule for this date (site down and nothing cached). Nominal
		// times are better than no event, so pass it through and say so.
		fmt.Fprintf(os.Stderr, "tides: no club hours for %s; leaving %q at its nominal times\n", date, title)
		return e, true
	}
	if info.Closed {
		fmt.Fprintf(os.Stderr, "tides: club closed %s; dropping %q\n", date, title)
		return nil, false
	}

	open, close := toMinutes(info.Open), toMinutes(info.Close)

	// An all-day sailing event has no window of its own to narrow, so it simply
	// adopts the club's hours for the day.
	if allDay, _ := e["all_day"].(bool); allDay {
		return withWindow(e, date, open, close), true
	}

	startStr, _ := e["start"].(string)
	start, ok := parseHHMM(startStr)
	if !ok {
		fmt.Fprintf(os.Stderr, "tides: unparseable start %q on %q; leaving it alone\n", startStr, title)
		return e, true
	}

	// An open-ended event (start, no end) is clamped at its start only; the club
	// closing before it begins is still enough to drop it.
	end := close
	endStr, hasEnd := e["end"].(string)
	if hasEnd && endStr != "" {
		if end, ok = parseHHMM(endStr); !ok {
			fmt.Fprintf(os.Stderr, "tides: unparseable end %q on %q; leaving it alone\n", endStr, title)
			return e, true
		}
	}

	lo, hi := max(start, open), min(end, close)
	if lo >= hi {
		fmt.Fprintf(os.Stderr, "tides: club hours %s-%s miss %q on %s; dropping it\n",
			info.Open, info.Close, title, date)
		return nil, false
	}
	if !hasEnd || endStr == "" {
		return withWindow(e, date, lo, -1), true
	}
	return withWindow(e, date, lo, hi), true
}

// withWindow returns the event with its times rewritten to [lo, hi] minutes past
// midnight (hi < 0 leaves the event open-ended) and its sort key realigned.
func withWindow(e event, date string, lo, hi int) event {
	e["start"] = fromMinutes(lo)
	e["all_day"] = false
	if hi < 0 {
		e["end"] = nil
	} else {
		e["end"] = fromMinutes(hi)
	}
	e["sort"] = date + "T" + fromMinutes(lo)
	return e
}

func hasTag(e event, want string) bool {
	tags, _ := e["tags"].([]any)
	for _, t := range tags {
		if s, ok := t.(string); ok && strings.EqualFold(strings.TrimSpace(s), want) {
			return true
		}
	}
	return false
}

func anyTagged(events []event, want string) bool {
	for _, e := range events {
		if hasTag(e, want) {
			return true
		}
	}
	return false
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Clock helpers ───────────────────────────────────────────────────

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
