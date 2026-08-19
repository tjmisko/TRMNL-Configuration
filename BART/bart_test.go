package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// ── fixtures ────────────────────────────────────────────────────────

// writeTrips writes a trips.txt with CRLF endings, as BART ships it.
func writeTrips(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "trips.txt")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, "\n", "\r\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const tripsHeader = "route_id,service_id,trip_id,trip_headsign\n"

// validTrips covers both reported routes plus an unreported one.
const validTrips = tripsHeader +
	"1,SVC,1001,Millbrae\n" +
	"7,SVC,1002,Millbrae\n" +
	"5,SVC,1003,Daly City\n"

type stopTimes struct {
	stopID           string
	arrival, departs int64
}

func tripUpdate(tripID string, stops ...stopTimes) *gtfs.FeedEntity {
	stus := make([]*gtfs.TripUpdate_StopTimeUpdate, 0, len(stops))
	for _, s := range stops {
		stu := &gtfs.TripUpdate_StopTimeUpdate{StopId: proto.String(s.stopID)}
		if s.arrival != 0 {
			stu.Arrival = &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(s.arrival)}
		}
		if s.departs != 0 {
			stu.Departure = &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(s.departs)}
		}
		stus = append(stus, stu)
	}
	return &gtfs.FeedEntity{
		Id: proto.String(tripID),
		TripUpdate: &gtfs.TripUpdate{
			Trip:           &gtfs.TripDescriptor{TripId: proto.String(tripID)},
			StopTimeUpdate: stus,
		},
	}
}

func feedWith(timestamp int64, entities ...*gtfs.FeedEntity) *gtfs.FeedMessage {
	return &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Timestamp:           proto.Uint64(uint64(timestamp)),
		},
		Entity: entities,
	}
}

// feedServer serves the given feed as protobuf.
func feedServer(t *testing.T, feed *gtfs.FeedMessage) *httptest.Server {
	t.Helper()
	body, err := proto.Marshal(feed)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── loadSchedule ────────────────────────────────────────────────────

func TestLoadScheduleShouldSeparateReportedRoutesFromAllTripsWhenFileIsValid(t *testing.T) {
	s, err := loadSchedule(writeTrips(t, validTrips))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := len(s.knownTrips), 3; got != want {
		t.Errorf("knownTrips = %d, want %d", got, want)
	}
	if got, want := len(s.routeByTrip), 2; got != want {
		t.Errorf("routeByTrip = %d, want %d (only routes 1 and 7)", got, want)
	}
	if _, ok := s.routeByTrip["1003"]; ok {
		t.Error("route 5 trip leaked into routeByTrip")
	}
	if _, ok := s.knownTrips["1003"]; !ok {
		t.Error("route 5 trip missing from knownTrips; coverage would undercount")
	}
}

func TestLoadScheduleShouldErrorWhenFileIsMissing(t *testing.T) {
	if _, err := loadSchedule(filepath.Join(t.TempDir(), "absent.txt")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestLoadScheduleShouldErrorWhenRequiredColumnsAreAbsent(t *testing.T) {
	_, err := loadSchedule(writeTrips(t, "foo,bar\n1,2\n"))
	if err == nil || !strings.Contains(err.Error(), "route_id/trip_id") {
		t.Fatalf("error = %v, want one naming the missing columns", err)
	}
}

func TestLoadScheduleShouldErrorWhenFileHasNoDataRows(t *testing.T) {
	_, err := loadSchedule(writeTrips(t, tripsHeader))
	if err == nil || !strings.Contains(err.Error(), "no usable rows") {
		t.Fatalf("error = %v, want one about empty data", err)
	}
}

func TestLoadScheduleShouldErrorWhenNoTripsServeReportedRoutes(t *testing.T) {
	_, err := loadSchedule(writeTrips(t, tripsHeader+"5,SVC,1003,Daly City\n"))
	if err == nil || !strings.Contains(err.Error(), "no trips on routes") {
		t.Fatalf("error = %v, want one about the reported routes", err)
	}
}

func TestLoadScheduleShouldSkipRowsMissingRequiredFields(t *testing.T) {
	s, err := loadSchedule(writeTrips(t, tripsHeader+"1,SVC,1001,X\n,SVC,1002,X\n7,SVC,,X\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := len(s.knownTrips), 1; got != want {
		t.Errorf("knownTrips = %d, want %d", got, want)
	}
}

// ── coverage ────────────────────────────────────────────────────────

func TestMeasureCoverageShouldReportZeroMatchesWhenScheduleIsStale(t *testing.T) {
	s, err := loadSchedule(writeTrips(t, validTrips))
	if err != nil {
		t.Fatal(err)
	}
	feed := feedWith(0, tripUpdate("9001"), tripUpdate("9002"))

	cov := measureCoverage(feed, s)
	if cov.feedTrips != 2 || cov.matched != 0 {
		t.Fatalf("coverage = %+v, want 0 of 2 matched", cov)
	}
	if cov.rate() != 0 {
		t.Errorf("rate = %v, want 0", cov.rate())
	}
}

func TestMeasureCoverageShouldCountTripsOnUnreportedRoutesAsMatches(t *testing.T) {
	s, err := loadSchedule(writeTrips(t, validTrips))
	if err != nil {
		t.Fatal(err)
	}
	// 1003 is on route 5, which we never report, but it proves the static data
	// still resolves the live feed.
	feed := feedWith(0, tripUpdate("1003"), tripUpdate("9001"))

	cov := measureCoverage(feed, s)
	if cov.matched != 1 || cov.feedTrips != 2 {
		t.Fatalf("coverage = %+v, want 1 of 2 matched", cov)
	}
	if cov.rate() != 0.5 {
		t.Errorf("rate = %v, want 0.5", cov.rate())
	}
}

func TestMeasureCoverageShouldIgnoreEntitiesWithoutATripID(t *testing.T) {
	s, err := loadSchedule(writeTrips(t, validTrips))
	if err != nil {
		t.Fatal(err)
	}
	blank := tripUpdate("")
	feed := feedWith(0, tripUpdate("1001"), blank, &gtfs.FeedEntity{Id: proto.String("x")})

	if cov := measureCoverage(feed, s); cov.feedTrips != 1 || cov.matched != 1 {
		t.Fatalf("coverage = %+v, want 1 of 1 matched", cov)
	}
}

func TestCoverageRateShouldBeZeroWhenFeedIsEmpty(t *testing.T) {
	if r := (coverage{}).rate(); r != 0 {
		t.Errorf("rate = %v, want 0 (and no division by zero)", r)
	}
}

// ── trip selection ──────────────────────────────────────────────────

func mustSchedule(t *testing.T) *schedule {
	t.Helper()
	s, err := loadSchedule(writeTrips(t, validTrips))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// northbound builds a 19th St → Montgomery trip departing at dep.
func northbound(tripID string, dep, arr int64) *gtfs.FeedEntity {
	return tripUpdate(tripID,
		stopTimes{stopID: "K20-1", departs: dep},
		stopTimes{stopID: "M20-1", arrival: arr},
	)
}

func TestSelectTripsShouldOrderByDepartureWhenSeveralQualify(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	feed := feedWith(0,
		northbound("1002", 1_001_500, 1_002_400),
		northbound("1001", 1_000_900, 1_001_800),
	)

	got := selectTrips(feed, mustSchedule(t), 5, now)
	if len(got) != 2 {
		t.Fatalf("got %d trips, want 2", len(got))
	}
	if got[0].tripID != "1001" || got[1].tripID != "1002" {
		t.Errorf("order = %s, %s; want 1001, 1002", got[0].tripID, got[1].tripID)
	}
}

func TestSelectTripsShouldSkipTrainsDepartingInsideTheLeadTime(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tooSoon := now.Add(departureLeadTime - time.Minute).Unix()
	feed := feedWith(0, northbound("1001", tooSoon, tooSoon+900))

	if got := selectTrips(feed, mustSchedule(t), 5, now); len(got) != 0 {
		t.Fatalf("got %d trips, want none within the lead time", len(got))
	}
}

func TestSelectTripsShouldKeepTrainsDepartingExactlyAtTheLeadTime(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	atEdge := now.Add(departureLeadTime).Unix()
	feed := feedWith(0, northbound("1001", atEdge, atEdge+900))

	if got := selectTrips(feed, mustSchedule(t), 5, now); len(got) != 1 {
		t.Fatalf("got %d trips, want the train at the lead-time boundary", len(got))
	}
}

func TestSelectTripsShouldRejectTripsRunningTheWrongDirection(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// Reaches Montgomery before 19th St: southbound.
	feed := feedWith(0, northbound("1001", 1_002_000, 1_001_000))

	if got := selectTrips(feed, mustSchedule(t), 5, now); len(got) != 0 {
		t.Fatalf("got %d trips, want none for the wrong direction", len(got))
	}
}

func TestSelectTripsShouldIgnoreRoutesWeDoNotReport(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	feed := feedWith(0, northbound("1003", 1_000_900, 1_001_800)) // route 5

	if got := selectTrips(feed, mustSchedule(t), 5, now); len(got) != 0 {
		t.Fatalf("got %d trips, want none from unreported routes", len(got))
	}
}

func TestSelectTripsShouldSkipTripsMissingEitherEndpoint(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	feed := feedWith(0,
		tripUpdate("1001", stopTimes{stopID: "K20-1", departs: 1_000_900}), // no Montgomery
		tripUpdate("1002", stopTimes{stopID: "M20-1", arrival: 1_001_800}), // no 19th St
	)

	if got := selectTrips(feed, mustSchedule(t), 5, now); len(got) != 0 {
		t.Fatalf("got %d trips, want none without both endpoints", len(got))
	}
}

func TestSelectTripsShouldCapResultsAtTheRequestedCount(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// Distinct trips, so dedup does not do the capping for us.
	feed := feedWith(0,
		northbound("1001", 1_000_900, 1_001_800),
		northbound("1002", 1_001_200, 1_002_100),
	)

	if got := selectTrips(feed, mustSchedule(t), 1, now); len(got) != 1 {
		t.Fatalf("got %d trips, want 1", len(got))
	}
}

func TestSelectTripsShouldReturnOneEntryPerTripWhenTheFeedRepeatsIt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	feed := feedWith(0,
		northbound("1001", 1_000_900, 1_001_800),
		northbound("1001", 1_001_500, 1_002_400),
	)

	got := selectTrips(feed, mustSchedule(t), 5, now)
	if len(got) != 1 {
		t.Fatalf("got %d trips, want 1 after dedup", len(got))
	}
	if got[0].departOrigin != 1_000_900 {
		t.Errorf("kept the later duplicate (%d); want the earliest", got[0].departOrigin)
	}
}

func TestSelectTripsShouldUseTheEarliestPlatformTimeWhenSeveralAreListed(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	feed := feedWith(0, tripUpdate("1001",
		stopTimes{stopID: "K20-2", departs: 1_001_500},
		stopTimes{stopID: "K20-1", departs: 1_000_900},
		stopTimes{stopID: "M20-2", arrival: 1_002_000},
		stopTimes{stopID: "M20-1", arrival: 1_001_800},
	))

	got := selectTrips(feed, mustSchedule(t), 5, now)
	if len(got) != 1 {
		t.Fatalf("got %d trips, want 1", len(got))
	}
	if got[0].departOrigin != 1_000_900 || got[0].arriveDest != 1_001_800 {
		t.Errorf("times = %d/%d, want the earliest 1000900/1001800",
			got[0].departOrigin, got[0].arriveDest)
	}
}

func TestSelectTripsShouldFallBackToDepartureWhenDestinationArrivalIsAbsent(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	feed := feedWith(0, tripUpdate("1001",
		stopTimes{stopID: "K20-1", departs: 1_000_900},
		stopTimes{stopID: "M20-1", departs: 1_001_800},
	))

	got := selectTrips(feed, mustSchedule(t), 5, now)
	if len(got) != 1 || got[0].arriveDest != 1_001_800 {
		t.Fatalf("got %+v, want the Montgomery departure used as the arrival", got)
	}
}

// ── run: the stdout/exit-code contract ──────────────────────────────

func runWith(t *testing.T, tripsPath, feedURL string, now time.Time) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(config{
		tripsPath: tripsPath,
		feedURL:   feedURL,
		now:       func() time.Time { return now },
	}, &out)
	return code, out.String()
}

func TestRunShouldPrintTrainsWhenEverythingIsHealthy(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	srv := feedServer(t, feedWith(now.Unix(), northbound("1001", 1_000_900, 1_001_800)))

	code, out := runWith(t, writeTrips(t, validTrips), srv.URL, now)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}

	var trips []tripJSON
	if err := json.Unmarshal([]byte(out), &trips); err != nil {
		t.Fatalf("stdout is not valid JSON (%q): %v", out, err)
	}
	if len(trips) != 1 {
		t.Fatalf("got %d trips, want 1", len(trips))
	}
}

func TestRunShouldPrintEmptyArrayWhenThereAreGenuinelyNoTrains(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// The feed resolves fine; every train is simply southbound.
	srv := feedServer(t, feedWith(now.Unix(), northbound("1001", 1_002_000, 1_001_000)))

	code, out := runWith(t, writeTrips(t, validTrips), srv.URL, now)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("stdout = %q, want %q", out, "[]")
	}
}

func TestRunShouldNotPrintEmptyArrayWhenScheduleIsStale(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// Feed is healthy but every trip_id is unknown to the static schedule.
	srv := feedServer(t, feedWith(now.Unix(),
		northbound("9001", 1_000_900, 1_001_800),
		northbound("9002", 1_001_500, 1_002_400),
	))

	code, out := runWith(t, writeTrips(t, validTrips), srv.URL, now)
	if code != exitStaleData {
		t.Fatalf("exit = %d, want %d", code, exitStaleData)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing; an empty array would read as 'no trains'", out)
	}
}

func TestRunShouldNotPrintEmptyArrayWhenStaticDataIsMissing(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	srv := feedServer(t, feedWith(now.Unix()))

	code, out := runWith(t, filepath.Join(t.TempDir(), "absent.txt"), srv.URL, now)
	if code != exitStaticData {
		t.Fatalf("exit = %d, want %d", code, exitStaticData)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

func TestRunShouldNotPrintEmptyArrayWhenFeedIsUnreachable(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	code, out := runWith(t, writeTrips(t, validTrips), srv.URL, now)
	if code != exitRealtimeFeed {
		t.Fatalf("exit = %d, want %d", code, exitRealtimeFeed)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

func TestRunShouldNotPrintEmptyArrayWhenFeedIsNotProtobuf(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<html>maintenance</html>"))
	}))
	defer srv.Close()

	code, out := runWith(t, writeTrips(t, validTrips), srv.URL, now)
	if code != exitRealtimeFeed {
		t.Fatalf("exit = %d, want %d", code, exitRealtimeFeed)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

func TestRunShouldPrintEmptyArrayWhenFeedHasNoTripUpdates(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// Overnight, BART may genuinely report nothing. That is not a fault.
	srv := feedServer(t, feedWith(now.Unix()))

	code, out := runWith(t, writeTrips(t, validTrips), srv.URL, now)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("stdout = %q, want %q", out, "[]")
	}
}

func TestRunShouldStillReportTrainsWhenCoverageIsPartial(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// One known trip, two unknown: below minMatchRate but not zero, so this
	// warns and carries on rather than discarding a usable answer.
	srv := feedServer(t, feedWith(now.Unix(),
		northbound("1001", 1_000_900, 1_001_800),
		northbound("9001", 1_001_500, 1_002_400),
		northbound("9002", 1_002_100, 1_003_000),
	))

	code, out := runWith(t, writeTrips(t, validTrips), srv.URL, now)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}

	var trips []tripJSON
	if err := json.Unmarshal([]byte(out), &trips); err != nil {
		t.Fatalf("stdout is not valid JSON (%q): %v", out, err)
	}
	if len(trips) != 1 {
		t.Fatalf("got %d trips, want the one resolvable train", len(trips))
	}
}

func TestRunShouldRetryWhenTheFeedFailsTransiently(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	body, err := proto.Marshal(feedWith(now.Unix(), northbound("1001", 1_000_900, 1_001_800)))
	if err != nil {
		t.Fatal(err)
	}

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	code, out := runWith(t, writeTrips(t, validTrips), srv.URL, now)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d after a retry", code, exitOK)
	}
	if calls < 2 {
		t.Errorf("server saw %d calls, want a retry", calls)
	}
	if strings.TrimSpace(out) == "[]" {
		t.Error("stdout is an empty array; the retry result was lost")
	}
}

func TestHHMMShouldFormatInLocalTime(t *testing.T) {
	epoch := time.Date(2026, 8, 19, 9, 37, 0, 0, time.Local).Unix()
	if got := hhmm(epoch); got != "09:37" {
		t.Errorf("hhmm = %q, want %q", got, "09:37")
	}
}
