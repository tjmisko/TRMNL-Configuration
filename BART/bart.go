package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

func baseDir() string {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("cannot get executable path: %v", err)
	}
	return filepath.Dir(exePath)
}

const GTFSRTTripUpdatesURL = "http://api.bart.gov/gtfsrt/tripupdate.aspx"

var TripsTXT = filepath.Join(baseDir(), "bart_gtfs", "trips.txt")

var (
	OriginStopIDs = map[string]struct{}{
		"K20-1": {}, "K20-2": {}, "K20-3": {}, // 19th St Oakland platforms
	}
	DestStopIDs = map[string]struct{}{
		"M20-1": {}, "M20-2": {}, // Montgomery St platforms
	}
	AllowedRouteIDs = map[string]struct{}{
		"1": {}, "7": {}, // Yellow-S / Red-S
	}
)

func fmtHHMM(epoch int64) string {
	return time.Unix(epoch, 0).Local().Format("15:04")
}

func loadTripToRoute(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return nil, err
	}

	var routeIdx, tripIdx int = -1, -1
	for i, h := range header {
		switch strings.TrimSpace(h) {
		case "route_id":
			routeIdx = i
		case "trip_id":
			tripIdx = i
		}
	}

	if routeIdx == -1 || tripIdx == -1 {
		return nil, fmt.Errorf("route_id or trip_id column missing")
	}

	out := make(map[string]string, 4096)

	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if routeIdx >= len(row) || tripIdx >= len(row) {
			continue
		}

		rid := strings.TrimSpace(row[routeIdx])
		tid := strings.TrimSpace(row[tripIdx])

		if rid == "" || tid == "" {
			continue
		}

		if _, ok := AllowedRouteIDs[rid]; ok {
			out[tid] = rid
		}
	}

	return out, nil
}

func fetchFeed(url string) (*gtfs.FeedMessage, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http error: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(data, &feed); err != nil {
		return nil, err
	}

	return &feed, nil
}

type candidate struct {
	Dep19th    int64
	ArrMont    int64
	TripID     string
	OriginStop string
	DestStop   string
}

func nextNTripsWithTimes(feed *gtfs.FeedMessage, tripToRoute map[string]string, n int) []candidate {
	now := time.Now().Unix()
	threshold := now + 5*60 // discard anything departing <5 min from now

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

		if _, ok := tripToRoute[tripID]; !ok {
			continue
		}

		var dep19 *int64
		var arrMont *int64
		var originSID, destSID string

		for _, stu := range tu.GetStopTimeUpdate() {
			sid := strings.TrimSpace(stu.GetStopId())

			if _, ok := OriginStopIDs[sid]; ok {
				dep := stu.GetDeparture()
				if dep == nil || dep.GetTime() == 0 {
					continue
				}
				t := dep.GetTime()
				if dep19 == nil || t < *dep19 {
					tmp := t
					dep19 = &tmp
					originSID = sid
				}
				continue
			}

			if _, ok := DestStopIDs[sid]; ok {
				// Prefer ARRIVAL at Montgomery; fall back to DEPARTURE if arrival missing.
				var t int64
				if arr := stu.GetArrival(); arr != nil && arr.GetTime() != 0 {
					t = arr.GetTime()
				} else if dep := stu.GetDeparture(); dep != nil && dep.GetTime() != 0 {
					t = dep.GetTime()
				} else {
					continue
				}

				if arrMont == nil || t < *arrMont {
					tmp := t
					arrMont = &tmp
					destSID = sid
				}
			}
		}

		if dep19 == nil || arrMont == nil {
			continue
		}

		// Discard already-gone trains AND trains leaving in under 5 minutes
		if *dep19 < threshold {
			continue
		}

		// Direction check by time ordering
		if *dep19 >= *arrMont {
			continue
		}

		cands = append(cands, candidate{
			Dep19th:    *dep19,
			ArrMont:    *arrMont,
			TripID:     tripID,
			OriginStop: originSID,
			DestStop:   destSID,
		})
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Dep19th == cands[j].Dep19th {
			return cands[i].TripID < cands[j].TripID
		}
		return cands[i].Dep19th < cands[j].Dep19th
	})

	out := make([]candidate, 0, n)
	seen := make(map[string]struct{})

	for _, c := range cands {
		if _, ok := seen[c.TripID]; ok {
			continue
		}
		seen[c.TripID] = struct{}{}
		out = append(out, c)
		if len(out) >= n {
			break
		}
	}

	return out
}

type tripJSON struct {
	Depart string `json:"depart"`
	Arrive string `json:"arrive"`
}

func main() {
	tripToRoute, err := loadTripToRoute(TripsTXT)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Println("[]")
		return
	}

	feed, err := fetchFeed(GTFSRTTripUpdatesURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Println("[]")
		return
	}

	trips := nextNTripsWithTimes(feed, tripToRoute, 5)

	out := make([]tripJSON, len(trips))
	for i, t := range trips {
		out[i] = tripJSON{
			Depart: fmtHHMM(t.Dep19th),
			Arrive: fmtHHMM(t.ArrMont),
		}
	}

	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		fmt.Println("[]")
	}
}
