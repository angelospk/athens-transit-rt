package tools

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

type fakeAPI struct {
	now      time.Time
	requests int64
}

func (f *fakeAPI) Lines(context.Context) ([]telematics.Line, error) {
	f.requests++
	return []telematics.Line{{LineCode: "R1", LineID: "L"}, {LineCode: "X", LineID: "OTHER"}}, nil
}
func (f *fakeAPI) Routes(_ context.Context, lc string) ([]telematics.Route, error) {
	f.requests++
	return []telematics.Route{{RouteCode: "SH"}, {RouteCode: "EMPTY"}}, nil
}
func (f *fakeAPI) Stops(context.Context, string) ([]telematics.RouteStop, error) {
	f.requests++
	return []telematics.RouteStop{{StopCode: "S0"}, {StopCode: "S1"}, {StopCode: "S2"}}, nil
}
func (f *fakeAPI) BusLocations(_ context.Context, rc string) ([]telematics.Vehicle, error) {
	f.requests++
	if rc != "SH" {
		return nil, nil
	}
	return []telematics.Vehicle{{VehNo: "1", RouteCode: "SH", Lat: 37.98, Lon: 23.72, Heading: 90, Time: f.now.Add(-20 * time.Second)},
		{VehNo: "2", RouteCode: "SH", Lat: 37.98, Lon: 23.70, Time: f.now.Add(-10 * time.Minute)}}, nil // stale
}
func (f *fakeAPI) StopArrivals(_ context.Context, stop string) ([]telematics.Arrival, error) {
	f.requests++
	return []telematics.Arrival{{RouteCode: "SH", VehCode: "1", Minutes: 3}}, nil
}

func TestRecorderCycle(t *testing.T) {
	now := time.Date(2026, 10, 5, 10, 20, 0, 0, gtfs.Athens)
	api := &fakeAPI{now: now}
	ctx := context.Background()
	lines, missing, err := ResolveLines(ctx, api, "l, nope")
	if err != nil || len(lines) != 1 || lines["L"]["SH"] != "R1" || len(missing) != 1 {
		t.Fatalf("resolve %v %v %v", lines, missing, err)
	}
	f := gtfstest.StraightLine{Stops: 5, Trips: 6, First: 36000, Headway: 10, Leg: 5}.Feed(t)
	db, err := OpenDB(filepath.Join(t.TempDir(), "r.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	stops, _ := SampleStops(ctx, api, lines, 2)
	clock := now
	r := &Recorder{API: api, DB: db, Feed: f, Lines: lines, Interval: 30 * time.Second, EmptyInterval: 300 * time.Second,
		ArrivalsRate: 0.5, Stops: stops, Requests: func() int64 { return api.requests }, Logf: t.Logf,
		Now:     func() time.Time { return clock },
		Sleep:   func(_ context.Context, d time.Duration) error { clock = clock.Add(max(d, 0) + time.Second); return nil },
		Matcher: match.New(f, match.NewRouteMapper(f, func(string) ([]string, bool) { return nil, false }), match.Greedy)}
	if err := r.Cycle(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("SELECT COUNT(*) FROM fix") != 1 || count("SELECT COUNT(*) FROM match WHERE trip_id IS NOT NULL") != 1 {
		t.Fatal("expected one fresh, matched fix")
	}
	if count("SELECT COUNT(*) FROM cycle WHERE routes_polled = 2") != 1 || count("SELECT COUNT(*) FROM oasa_eta") == 0 {
		t.Fatal("cycle / eta rows missing")
	}
	// The empty route waits EmptyInterval; the busy one is polled next cycle.
	before := api.requests
	if err := r.Cycle(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if count("SELECT routes_polled FROM cycle ORDER BY started DESC LIMIT 1") != 1 || api.requests <= before {
		t.Fatal("empty route should not be polled in the next cycle")
	}
	// The recording replays.
	rec, err := LoadRecording(db)
	if err != nil || len(rec.Cycles) != 2 || len(rec.ETA) == 0 {
		t.Fatalf("load recording: %v", err)
	}
}
