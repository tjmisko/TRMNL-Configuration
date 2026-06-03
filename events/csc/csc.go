// Events source adapter: Cal Sailing Club daily open/close window.
//
// Scrapes the club's published open/close schedule
// (https://www.cal-sailing.org/resources/csc-openclose-times?view=month), a
// server-rendered month table where each row carries a NOAA tide link
// (bdate=YYYYMMDD) and a "club timeline" cell. The open cell holds a
// <span class="tideok">OPEN to CLOSE</span> with 12-hour times (or the literal
// "Noon"); a fully-closed day has no tideok span. We parse every row by its
// bdate (robust to the page's duplicated "today" summary and multi-line rows),
// convert to 24-hour HH:MM, and emit a single normalized event for $EVENTS_TODAY
// (schema in events/README.md):
//
//	{ "title", "start", "end", "all_day", "date", "sort", "source" }
//
// Robustness: the schedule is tide-deterministic and published a month ahead, so
// a successful fetch is cached to disk (the whole visible month) and reused when
// the site is unreachable. On a closed day, a missing date, or ANY error the
// adapter prints "[]" and exits 0, so the rest of the events pipeline keeps
// working. Pure Go stdlib — no third-party packages.
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

// dayInfo is one open day's window in 24-hour HH:MM. Closed/unparseable days are
// simply absent from the map (and therefore from the cache), which renders as
// "no event" — the desired behavior on closed days.
type dayInfo struct {
	Open  string `json:"open"`
	Close string `json:"close"`
}

var (
	bdateRe  = regexp.MustCompile(`bdate=(\d{8})`)
	tideokRe = regexp.MustCompile(`(?s)class="tideok"[^>]*>(.*?)</span>`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	wsRe     = regexp.MustCompile(`\s+`)
	clockRe  = regexp.MustCompile(`^(\d{1,2}):(\d{2})\s*([AaPp][Mm])$`)
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
	url := getenv("CSC_URL", defaultURL)

	var lookup map[string]dayInfo
	if body, err := fetch(url); err != nil {
		fmt.Fprintf(os.Stderr, "csc: fetch failed: %v\n", err)
	} else if live := parseMonth(body); len(live) > 0 {
		merged := mergeCache(live)
		saveCache(merged)
		lookup = merged
	}
	if lookup == nil {
		lookup = loadCache() // fetch failed or yielded nothing: fall back to cache
	}

	info, ok := lookup[today]
	if !ok || info.Open == "" || info.Close == "" {
		return nil // closed today, not in schedule, or no usable data
	}
	return []event{{
		Title:  title,
		Start:  info.Open,
		End:    info.Close,
		AllDay: false,
		Date:   today,
		Sort:   today + "T" + info.Open,
		Source: source,
	}}
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

// parseMonth extracts every open day's window from the schedule page, keyed by
// "YYYY-MM-DD". Rows are delimited by their bdate=YYYYMMDD tide link (exactly one
// per row); within a row's slice the first tideok span holds "OPEN to CLOSE".
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
			continue // no open window => closed day
		}
		open, close, ok := parseWindow(cleanText(tm[1]))
		if !ok {
			fmt.Fprintf(os.Stderr, "csc: unparseable window for %s: %q\n", date, cleanText(tm[1]))
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
