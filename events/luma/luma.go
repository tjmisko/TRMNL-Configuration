// Command luma is an events source adapter for a Luma calendar .ics feed.
//
// It reads the feed URL from $LUMA_ICS_URL, fetches it, and prints a JSON array
// of normalized event objects on stdout. If the URL is unset or the fetch/parse
// fails it prints "[]" and exits 0, so the rest of the events pipeline keeps
// working. Like BART, it is pure Go stdlib — no external modules.
//
// Normalized schema (shared by every events source adapter):
//
//	{ "title", "start"|null, "end"|null, "all_day", "date", "sort", "source" }
//
// start/end are local "HH:MM" (24h), date is local "YYYY-MM-DD", sort is
// "YYYY-MM-DDTHH:MM". The aggregator (events/fetch) applies the today filter and
// final ordering, so this adapter just normalizes whatever the feed contains.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const userAgent = "GooseTRM/1.0 (events-luma)"

type event struct {
	Title  string  `json:"title"`
	Start  *string `json:"start"`
	End    *string `json:"end"`
	AllDay bool    `json:"all_day"`
	Date   string  `json:"date"`
	Sort   string  `json:"sort"`
	Source string  `json:"source"`
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

// parsedTime is the result of interpreting a DTSTART/DTEND value.
type parsedTime struct {
	t      time.Time
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

func fetch(url string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
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

func parse(text string, target *time.Location) []event {
	var out []event
	var (
		inEvent bool
		summary string
		start   parsedTime
		end     parsedTime
	)
	for _, line := range unfold(text) {
		name, params, value := splitProperty(line)
		switch {
		case name == "BEGIN" && strings.EqualFold(strings.TrimSpace(value), "VEVENT"):
			inEvent, summary, start, end = true, "", parsedTime{}, parsedTime{}
		case name == "END" && strings.EqualFold(strings.TrimSpace(value), "VEVENT"):
			if inEvent && start.ok {
				out = append(out, normalize(summary, start, end))
			}
			inEvent = false
		case inEvent && name == "SUMMARY":
			summary = strings.TrimSpace(unescape(value))
		case inEvent && name == "DTSTART":
			start = parseDT(value, params, target)
		case inEvent && name == "DTEND":
			end = parseDT(value, params, target)
		}
	}
	return out
}

func normalize(summary string, start, end parsedTime) event {
	title := summary
	if title == "" {
		title = "(untitled)"
	}
	date := start.t.Format("2006-01-02")
	if start.allDay {
		return event{
			Title: title, AllDay: true, Date: date,
			Sort: date + "T00:00", Source: "luma",
		}
	}
	hhmm := start.t.Format("15:04")
	ev := event{
		Title: title, Start: &hhmm, AllDay: false, Date: date,
		Sort: date + "T" + hhmm, Source: "luma",
	}
	if end.ok && !end.allDay {
		e := end.t.Format("15:04")
		ev.End = &e
	}
	return ev
}

func main() {
	url := strings.TrimSpace(os.Getenv("LUMA_ICS_URL"))
	if url == "" {
		emit(nil)
		return
	}

	tzName := os.Getenv("EVENTS_TZ")
	if tzName == "" {
		tzName = "America/Los_Angeles"
	}
	target, err := time.LoadLocation(tzName)
	if err != nil {
		target = time.Local
	}

	text, err := fetch(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "luma: fetch failed: %v\n", err)
		emit(nil)
		return
	}

	emit(parse(text, target))
}
