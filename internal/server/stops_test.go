package server

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/sched"
)

func stopBody(t *testing.T, a *App, id string) (StopResponse, int) {
	t.Helper()
	res, body := get(t, a, "/v1/stops/"+id)
	if res.StatusCode != 200 {
		t.Fatalf("%s: %d %s", id, res.StatusCode, body)
	}
	var sr StopResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		t.Fatal(err)
	}
	return sr, maxAge(t, res)
}

func eta(r Arrival) int64 {
	if r.ETA == nil {
		return -1
	}
	return *r.ETA
}

// The test line: S0..S4 every ~880 m due east, trips every 10 min from 10:00, 5 min a stop.
// Vehicle 44548 is between S2 and S3 at 10:19:50, matched to T00 (next stop S3); vehicle 9 is
// far away. At S3 it arrives after half a stop's scheduled running time; the trips that have
// not left yet follow, from the timetable, three rows per variant in all.
func TestStopBoardLiveAndSchedule(t *testing.T) {
	a := newApp(t, monday1020)
	poll(a, monday1020)
	sr, ma := stopBody(t, a, "S3")
	if sr.StopID != "S3" || sr.Name != "STOP 3" || !reflect.DeepEqual(sr.Lines, []string{"L"}) || sr.UpdatedAt != monday1020.Unix() ||
		sr.NextUpdateAt < monday1020.Unix()+5 || ma < 5 || ma > 300 {
		t.Fatalf("header %+v, max-age %d", sr, ma)
	}
	if len(sr.Arrivals) != 3 {
		t.Fatalf("arrivals %+v", sr.Arrivals)
	}
	live := sr.Arrivals[0]
	fix := monday1020.Add(-10 * time.Second).Unix()
	if live.Source != "live" || *live.Vehicle != "44548" || *live.TripID != "T00" || *live.Variant != "SH" || live.Headsign != "EAST" ||
		*live.StopsAway != 1 || *live.PositionAt != fix || math.Abs(float64(*live.DistanceM)-440) > 10 ||
		*live.Scheduled != time.Date(2026, 10, 5, 10, 15, 0, 0, gtfs.Athens).Unix() || math.Abs(float64(eta(live)-fix-150)) > 3 {
		b, _ := json.Marshal(live)
		t.Fatalf("live row %s", b)
	}
	// T01 left at 10:10 with no vehicle: not shown. T02 leaves now, T03 at 10:30.
	for i, want := range []string{"T02", "T03"} {
		r := sr.Arrivals[i+1]
		at := time.Date(2026, 10, 5, 10, 35+10*i, 0, 0, gtfs.Athens).Unix()
		if r.Source != "schedule" || *r.TripID != want || eta(r) != at || *r.Scheduled != at || r.Vehicle != nil ||
			r.PositionAt != nil || r.StopsAway != nil || r.DistanceM != nil || *r.Variant != "SH" {
			b, _ := json.Marshal(r)
			t.Fatalf("schedule row %d %s", i, b)
		}
	}
	// S0: the vehicle has passed it. S4: every trip ends there, so nobody gets on.
	if sr, _ := stopBody(t, a, "S0"); len(sr.Arrivals) != 3 || sr.Arrivals[0].Source != "schedule" {
		t.Fatalf("S0 %+v", sr.Arrivals)
	}
	if sr, _ := stopBody(t, a, "S4"); len(sr.Lines) != 0 || len(sr.Arrivals) != 0 {
		t.Fatalf("S4 %+v", sr)
	}
}

// With typical speeds (5 m/s over the shape) they replace the timetable's running time.
func TestStopBoardTypicalSpeeds(t *testing.T) {
	a := newApp(t, monday1020)
	a.segs.Store(typicalTable(t, monday1020, a.gtfsVersion()))
	poll(a, monday1020)
	sr, _ := stopBody(t, a, "S3")
	if r := sr.Arrivals[0]; r.Source != "live" || math.Abs(float64(eta(r)-*r.PositionAt)-float64(*r.DistanceM)/5) > 3 {
		b, _ := json.Marshal(r)
		t.Fatalf("live row %s", b)
	}
}

// A matched vehicle waiting at its first stop leaves at its scheduled departure: T03 at 10:30,
// at S2 ten minutes later.
func TestStopBoardWaitingAtFirstStop(t *testing.T) {
	now := time.Date(2026, 10, 5, 10, 25, 0, 0, gtfs.Athens)
	a := newApp(t, now)
	pollOne(a, now, "w", 23.7001, now.Add(-10*time.Second))
	sr, _ := stopBody(t, a, "S2")
	var r *Arrival
	for i := range sr.Arrivals {
		if sr.Arrivals[i].Vehicle != nil {
			r = &sr.Arrivals[i]
		}
	}
	at := time.Date(2026, 10, 5, 10, 40, 0, 0, gtfs.Athens).Unix()
	if r == nil || *r.TripID != "T03" || eta(*r) != at || *r.Scheduled != at || *r.StopsAway != 3 {
		b, _ := json.Marshal(sr.Arrivals)
		t.Fatalf("arrivals %s", b)
	}
	for _, x := range sr.Arrivals {
		if x.TripID != nil && *x.TripID == "T03" && x.Source == "schedule" {
			t.Fatal("T03 listed twice: its vehicle and its timetable")
		}
	}
}

// At night no trip fits, so the vehicle is unmatched: placed on its shape, it comes at the
// default speed with no trip; standing at its first stop, with no time at all.
func TestStopBoardUnmatched(t *testing.T) {
	now := time.Date(2026, 10, 5, 3, 0, 0, 0, gtfs.Athens)
	a := newApp(t, now)
	pollOne(a, now, "u", 23.715, now.Add(-10*time.Second))
	sr, ma := stopBody(t, a, "S3")
	if ma != inactiveMaxAge || len(sr.Arrivals) != 1 {
		t.Fatalf("max-age %d, arrivals %+v", ma, sr.Arrivals)
	}
	r := sr.Arrivals[0]
	if r.TripID != nil || r.Scheduled != nil || *r.Vehicle != "u" || *r.StopsAway != 2 ||
		math.Abs(float64(eta(r)-*r.PositionAt)-float64(*r.DistanceM)/boardDefaultMS) > 2 {
		b, _ := json.Marshal(r)
		t.Fatalf("unmatched row %s", b)
	}
	// Standing at S0 over two polls (speed 0).
	a2 := newApp(t, now)
	pollOne(a2, now, "s", 23.7001, now.Add(-60*time.Second))
	pollOne(a2, now, "s", 23.7001, now.Add(-10*time.Second))
	sr, _ = stopBody(t, a2, "S2")
	if len(sr.Arrivals) != 1 || sr.Arrivals[0].ETA != nil || *sr.Arrivals[0].StopsAway != 3 {
		b, _ := json.Marshal(sr.Arrivals)
		t.Fatalf("waiting unmatched %s", b)
	}
}

// After the timetable expired, vehicles still match (PlanningTime) and come; the timetable's
// own trips are not listed, their times may not be OASA's any more.
func TestStopBoardExpiredTimetable(t *testing.T) {
	after := time.Date(2026, 10, 12, 10, 20, 0, 0, gtfs.Athens) // Monday; feed ended 2026-10-06
	a := newApp(t, after)
	poll(a, after)
	sr, _ := stopBody(t, a, "S3")
	if len(sr.Arrivals) != 1 || sr.Arrivals[0].Source != "live" || sr.Arrivals[0].TripID == nil {
		b, _ := json.Marshal(sr.Arrivals)
		t.Fatalf("arrivals %s", b)
	}
}

// A matched vehicle standing at the stop (its next stop already the one after) arrives now.
func TestStopBoardAtTheStop(t *testing.T) {
	a := newApp(t, monday1020)
	pollOne(a, monday1020, "at", 23.7201, monday1020.Add(-10*time.Second)) // ~9 m past S2
	sr, _ := stopBody(t, a, "S2")
	var r *Arrival
	for i := range sr.Arrivals {
		if sr.Arrivals[i].Vehicle != nil {
			r = &sr.Arrivals[i]
		}
	}
	if r == nil || r.TripID == nil || *r.TripID != "T01" || *r.StopsAway != 1 || *r.DistanceM != 0 || eta(*r) != *r.PositionAt {
		b, _ := json.Marshal(sr.Arrivals)
		t.Fatalf("arrivals %s", b)
	}
}

// An old fix is left out, as on /v1/vehicles; an arrival well past is gone.
func TestStopBoardOldFixes(t *testing.T) {
	a := newAppWith(t, sched.DefaultConfig(), func() time.Time { return monday1020.Add(6 * time.Minute) })
	poll(a, monday1020)
	sr, _ := stopBody(t, a, "S3")
	for _, r := range sr.Arrivals {
		if r.Source == "live" {
			t.Fatalf("old fix listed %+v", r)
		}
	}
}

func TestStopBoardErrors(t *testing.T) {
	a := newApp(t, monday1020)
	for _, p := range []string{"/v1/stops/NOPE", "/v1/stops/S3/x", "/v1/stops/..%2FS3", "/v1/stops/" + strings.Repeat("9", 2048), "/v1/stops/"} {
		res, body := get(t, a, p)
		if res.StatusCode != 404 || !json.Valid(body) || maxAge(t, res) > 60 {
			t.Fatalf("%.40s: %d %s", p, res.StatusCode, body)
		}
	}
	if _, body := get(t, a, "/v1/stops/NOPE"); !strings.Contains(string(body), `"unknown_stop"`) {
		t.Fatalf("unknown stop: %s", body)
	}
	a.mu.Lock()
	a.w = nil
	a.mu.Unlock()
	if res, body := get(t, a, "/v1/stops/S3"); res.StatusCode != 503 || !strings.Contains(string(body), "warming_up") {
		t.Fatalf("no feed: %d %s", res.StatusCode, body)
	}
}

// The JSON field names must be exactly those of the contract fixture.
func TestStopJSONMatchesFixture(t *testing.T) {
	a := newApp(t, monday1020)
	poll(a, monday1020)
	_, body := get(t, a, "/v1/stops/S3")
	fix, err := os.ReadFile("../../docs/fixtures/stop-61000.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := keys(t, body), keys(t, fix); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %v, fixture %v", got, want)
	}
}
