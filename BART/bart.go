// Command bart reports the next few 19th St Oakland → Montgomery St trains as
// JSON on stdout.
//
// It prints an empty array only when BART genuinely has no qualifying trains.
// Every failure mode — unusable static schedule, unreachable realtime feed,
// static data too old to resolve realtime trips — writes a diagnostic to
// stderr, prints nothing to stdout, and exits non-zero, so callers can tell
// "no trains" apart from "could not tell".
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// Exit codes. Callers use these to decide whether a retry could help;
// BART/fetch refreshes the static schedule on exitStaticData and exitStaleData.
const (
	exitOK           = 0
	exitInternal     = 1
	exitStaticData   = 2 // static GTFS is missing, unreadable, or empty
	exitRealtimeFeed = 3 // realtime feed is unreachable or unparseable
	exitStaleData    = 4 // static and realtime disagree; static needs a refresh
)

const (
	defaultFeedURL = "https://api.bart.gov/gtfsrt/tripupdate.aspx"

	// Realtime fetches are retried; BART's endpoint is occasionally flaky and a
	// transient failure should not read as "no trains".
	fetchAttempts = 3
	fetchTimeout  = 10 * time.Second
	fetchBackoff  = 2 * time.Second

	// A feed whose header timestamp is older than this is served but not being
	// updated. Warned about rather than fatal: the times may still be usable.
	maxFeedAge = 15 * time.Minute

	// Below this share of realtime trips resolving against the static schedule,
	// the static data is suspect. Zero matches is treated as outright stale.
	minMatchRate = 0.5

	// Skip trains leaving too soon to walk to the platform for.
	departureLeadTime = 5 * time.Minute

	tripsWanted = 5
)

var (
	// 19th St Oakland platforms.
	originStopIDs = map[string]struct{}{
		"K20-1": {}, "K20-2": {}, "K20-3": {},
	}
	// Montgomery St platforms.
	destStopIDs = map[string]struct{}{
		"M20-1": {}, "M20-2": {},
	}
	// Yellow-S and Red-S: the lines that serve this pair in this direction.
	allowedRouteIDs = map[string]struct{}{
		"1": {}, "7": {},
	}
)

var verbose bool

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bart: "+format+"\n", args...)
}

func debugf(format string, args ...any) {
	if verbose {
		logf(format, args...)
	}
}

// config holds the program's external inputs. The environment overrides exist
// so the shell wrapper and tests can point at fixtures without a rebuild.
type config struct {
	tripsPath string
	feedURL   string
	now       func() time.Time
	// backoff between realtime fetch attempts; tests set it to zero.
	backoff time.Duration
}

func configFromEnv() config {
	return config{
		tripsPath: filepath.Join(gtfsDir(), "trips.txt"),
		feedURL:   envOr("BART_FEED_URL", defaultFeedURL),
		now:       time.Now,
		backoff:   fetchBackoff,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func gtfsDir() string {
	if dir := os.Getenv("BART_GTFS_DIR"); dir != "" {
		return dir
	}
	exe, err := os.Executable()
	if err != nil {
		// Fall back to the working directory rather than dying; the caller gets
		// a clear exitStaticData if the guess is wrong.
		return "bart_gtfs"
	}
	return filepath.Join(filepath.Dir(exe), "bart_gtfs")
}

// schedule is the static GTFS view this program needs.
type schedule struct {
	// routeByTrip covers only the routes we report on, and drives selection.
	routeByTrip map[string]string
	// knownTrips covers every trip in the feed regardless of route. It exists
	// to measure how well the static data resolves realtime trips: scoring
	// against routeByTrip alone would look like a miss for every other line.
	knownTrips map[string]struct{}
}

func loadSchedule(path string) (*schedule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}

	routeIdx, tripIdx := -1, -1
	for i, h := range header {
		switch strings.TrimSpace(h) {
		case "route_id":
			routeIdx = i
		case "trip_id":
			tripIdx = i
		}
	}
	if routeIdx == -1 || tripIdx == -1 {
		return nil, errors.New("trips.txt has no route_id/trip_id columns")
	}

	s := &schedule{
		routeByTrip: make(map[string]string, 512),
		knownTrips:  make(map[string]struct{}, 4096),
	}

	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading row: %w", err)
		}
		if routeIdx >= len(row) || tripIdx >= len(row) {
			continue
		}

		routeID := strings.TrimSpace(row[routeIdx])
		tripID := strings.TrimSpace(row[tripIdx])
		if routeID == "" || tripID == "" {
			continue
		}

		s.knownTrips[tripID] = struct{}{}
		if _, ok := allowedRouteIDs[routeID]; ok {
			s.routeByTrip[tripID] = routeID
		}
	}

	if len(s.knownTrips) == 0 {
		return nil, errors.New("trips.txt has no usable rows")
	}
	if len(s.routeByTrip) == 0 {
		return nil, fmt.Errorf("trips.txt has no trips on routes %s", sortedKeys(allowedRouteIDs))
	}

	return s, nil
}

func sortedKeys(m map[string]struct{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func fetchFeed(url string, backoff time.Duration) (*gtfs.FeedMessage, error) {
	client := &http.Client{Timeout: fetchTimeout}

	var lastErr error
	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		if attempt > 1 && backoff > 0 {
			time.Sleep(time.Duration(attempt-1) * backoff)
			debugf("realtime fetch retry %d/%d after: %v", attempt, fetchAttempts, lastErr)
		}

		feed, err := fetchFeedOnce(client, url)
		if err == nil {
			return feed, nil
		}
		lastErr = err
	}

	return nil, fmt.Errorf("after %d attempts: %w", fetchAttempts, lastErr)
}

func fetchFeedOnce(client *http.Client, url string) (*gtfs.FeedMessage, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("empty response body")
	}

	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("parsing protobuf (%d bytes): %w", len(data), err)
	}

	return &feed, nil
}

// coverage reports how many realtime trips the static schedule can resolve.
// This is the authoritative staleness signal: BART regenerates every trip_id
// when a new schedule takes effect, so stale static data resolves nothing.
type coverage struct {
	feedTrips int
	matched   int
}

func (c coverage) rate() float64 {
	if c.feedTrips == 0 {
		return 0
	}
	return float64(c.matched) / float64(c.feedTrips)
}

func measureCoverage(feed *gtfs.FeedMessage, s *schedule) coverage {
	var c coverage
	for _, ent := range feed.GetEntity() {
		tu := ent.GetTripUpdate()
		if tu == nil {
			continue
		}
		tripID := strings.TrimSpace(tu.GetTrip().GetTripId())
		if tripID == "" {
			continue
		}
		c.feedTrips++
		if _, ok := s.knownTrips[tripID]; ok {
			c.matched++
		}
	}
	return c
}

type candidate struct {
	departOrigin int64
	arriveDest   int64
	tripID       string
}

func selectTrips(feed *gtfs.FeedMessage, s *schedule, n int, now time.Time) []candidate {
	earliest := now.Add(departureLeadTime).Unix()

	cands := make([]candidate, 0, 128)

	for _, ent := range feed.GetEntity() {
		tu := ent.GetTripUpdate()
		if tu == nil {
			continue
		}

		tripID := strings.TrimSpace(tu.GetTrip().GetTripId())
		if tripID == "" {
			continue
		}
		if _, ok := s.routeByTrip[tripID]; !ok {
			continue
		}

		depart, arrive, ok := originDestTimes(tu)
		if !ok {
			continue
		}
		// Already gone, or too soon to reach the platform.
		if depart < earliest {
			continue
		}
		// Wrong direction: this trip hits Montgomery before 19th St.
		if depart >= arrive {
			continue
		}

		cands = append(cands, candidate{departOrigin: depart, arriveDest: arrive, tripID: tripID})
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].departOrigin == cands[j].departOrigin {
			return cands[i].tripID < cands[j].tripID
		}
		return cands[i].departOrigin < cands[j].departOrigin
	})

	out := make([]candidate, 0, n)
	seen := make(map[string]struct{}, n)
	for _, c := range cands {
		if _, dup := seen[c.tripID]; dup {
			continue
		}
		seen[c.tripID] = struct{}{}
		out = append(out, c)
		if len(out) >= n {
			break
		}
	}

	return out
}

// originDestTimes pulls the earliest origin departure and destination arrival
// from a trip's stop time updates.
func originDestTimes(tu *gtfs.TripUpdate) (depart, arrive int64, ok bool) {
	var haveDepart, haveArrive bool

	for _, stu := range tu.GetStopTimeUpdate() {
		stopID := strings.TrimSpace(stu.GetStopId())

		if _, isOrigin := originStopIDs[stopID]; isOrigin {
			t, found := stopTime(stu.GetDeparture(), stu.GetArrival())
			if !found {
				continue
			}
			if !haveDepart || t < depart {
				depart, haveDepart = t, true
			}
			continue
		}

		if _, isDest := destStopIDs[stopID]; isDest {
			// Prefer arrival at the destination; fall back to departure.
			t, found := stopTime(stu.GetArrival(), stu.GetDeparture())
			if !found {
				continue
			}
			if !haveArrive || t < arrive {
				arrive, haveArrive = t, true
			}
		}
	}

	return depart, arrive, haveDepart && haveArrive
}

// stopTime returns the first of the given events carrying a usable time.
func stopTime(events ...*gtfs.TripUpdate_StopTimeEvent) (int64, bool) {
	for _, e := range events {
		if e != nil && e.GetTime() != 0 {
			return e.GetTime(), true
		}
	}
	return 0, false
}

type tripJSON struct {
	Depart string `json:"depart"`
	Arrive string `json:"arrive"`
}

func hhmm(epoch int64) string {
	return time.Unix(epoch, 0).Local().Format("15:04")
}

func main() {
	flag.BoolVar(&verbose, "v", false, "log diagnostics to stderr")
	flag.Parse()

	os.Exit(run(configFromEnv(), os.Stdout))
}

func run(cfg config, stdout io.Writer) int {
	sched, err := loadSchedule(cfg.tripsPath)
	if err != nil {
		logf("static schedule unusable (%s): %v", cfg.tripsPath, err)
		return exitStaticData
	}
	debugf("static schedule: %d trips, %d on reported routes", len(sched.knownTrips), len(sched.routeByTrip))

	feed, err := fetchFeed(cfg.feedURL, cfg.backoff)
	if err != nil {
		logf("realtime feed unavailable: %v", err)
		return exitRealtimeFeed
	}

	if ts := feed.GetHeader().GetTimestamp(); ts > 0 {
		if age := cfg.now().Sub(time.Unix(int64(ts), 0)); age > maxFeedAge {
			logf("realtime feed is %s old; times may be unreliable", age.Round(time.Minute))
		}
	}

	cov := measureCoverage(feed, sched)
	debugf("coverage: %d/%d realtime trips resolved (%.0f%%)", cov.matched, cov.feedTrips, cov.rate()*100)

	switch {
	case cov.feedTrips == 0:
		// Fetched and parsed cleanly, but BART reports no active trips. Treat
		// as a real (if unusual) empty result rather than inventing an error.
		logf("realtime feed contains no trip updates")
	case cov.matched == 0:
		logf("static schedule resolves none of %d realtime trips; it is out of date", cov.feedTrips)
		return exitStaleData
	case cov.rate() < minMatchRate:
		logf("static schedule resolves only %d/%d realtime trips; a schedule change may be in progress",
			cov.matched, cov.feedTrips)
	}

	trips := selectTrips(feed, sched, tripsWanted, cfg.now())
	debugf("selected %d trips", len(trips))

	out := make([]tripJSON, len(trips))
	for i, t := range trips {
		out[i] = tripJSON{Depart: hhmm(t.departOrigin), Arrive: hhmm(t.arriveDest)}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		logf("encoding result: %v", err)
		return exitInternal
	}
	fmt.Fprintln(stdout, string(encoded))

	return exitOK
}
