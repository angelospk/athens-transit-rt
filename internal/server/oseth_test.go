package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/meta"
	"github.com/angelospk/athens-transit-rt/internal/oseth"
	"github.com/angelospk/athens-transit-rt/internal/sched"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

func TestCityOSETHURL(t *testing.T) {
	ath, _ := CityByID("athens")
	th, _ := CityByID("thessaloniki")
	if ath.OSETHURL != "" {
		t.Errorf("athens OSETHURL %q, want empty: OSETH is Thessaloniki only", ath.OSETHURL)
	}
	if th.OSETHURL != "https://oseth.com.gr/el/telematics-api" {
		t.Errorf("thessaloniki OSETHURL %q", th.OSETHURL)
	}
}

// newThessApp is newApp for the OSETH source: the Thessaloniki city, one OASTH route code "SH"
// whose stop list matches shape "SH" of the line "L", and OSETH on.
func newThessApp(t *testing.T, clock func() time.Time) *App {
	t.Helper()
	now := clock()
	dir := t.TempDir()
	m := meta.Data{FetchedAt: now.Unix(),
		Lines:  []telematics.Line{{LineCode: "R1", LineID: "L"}},
		Routes: map[string][]telematics.Route{"R1": {{RouteCode: "SH"}}},
		Stops:  map[string][]string{"SH": {"S0", "S1", "S2", "S3", "S4"}}}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "telematics-meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(Config{StateDir: dir, RPS: 4, Matcher: match.Memory, City: "thessaloniki", OSETHRPS: 1,
		Sched: sched.DefaultConfig()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.now = clock
	a.sched.SetClock(a.now)
	a.SetFeed(gtfstest.StraightLine{Stops: 5, Trips: 6, First: 10 * 3600, Headway: 10, Leg: 5}.Feed(t), nil)
	return a
}

func TestOSETHOffForAthens(t *testing.T) {
	ath := newApp(t, monday1020) // city "" = athens
	if ath.oseth != nil {
		t.Fatal("athens started the OSETH source")
	}
}

func TestOSETHPairsAndRouteFor(t *testing.T) {
	a := newThessApp(t, func() time.Time { return monday1020 })
	pairs := osethPairs(a.world().feed, "L")
	if len(pairs) != 1 || pairs[0][0] != "R1" || pairs[0][1] != "SH" {
		t.Fatalf("pairs %v", pairs)
	}
	routeFor := a.osethRouteFor(a.world(), "L")
	if code, line, ok := routeFor("SH"); !ok || code != "SH" || line != "R1" {
		t.Fatalf("routeFor(SH) = %q %q %v", code, line, ok)
	}
	if _, _, ok := routeFor("nope"); ok {
		t.Fatal("routeFor mapped an unknown shape")
	}
}

func TestOSETHMergedIntoOASTHPoll(t *testing.T) {
	now := monday1020
	a := newThessApp(t, func() time.Time { return now })
	prev := map[string]oseth.Sighting{"2001": {Vehicle: oseth.Vehicle{ID: "2001", Lat: 37.98, Lon: 23.70,
		Bearing: 90, ShapeID: "SH"}, At: now.Add(-30 * time.Second)}}
	cur := map[string]oseth.Sighting{"2001": {Vehicle: oseth.Vehicle{ID: "2001", Lat: 37.98, Lon: 23.71,
		Bearing: 90, ShapeID: "SH"}, At: now}}
	a.osethLines["L"] = &osethLine{cur: cur, prev: prev, at: now}
	a.onLineWithOSETH(sched.LinePoll{Line: "L", Started: now, Done: now, Routes: 1, Obs: []match.Obs{
		{Vehicle: telematics.Vehicle{VehNo: "44548", RouteCode: "SH", Lat: 37.98, Lon: 23.725,
			Heading: 90, Time: now.Add(-10 * time.Second)}, LineCode: "R1"}}})
	d := a.lines["L"]
	if d == nil {
		t.Fatal("nothing published")
	}
	ids := map[string]bool{}
	for _, v := range d.vehicles {
		ids[v.ID] = true
	}
	if !ids["44548"] || !ids["2001"] {
		t.Fatalf("published vehicles %v, want the OASTH one and the OSETH-only one", ids)
	}
}

func TestOSETHStaleStateIsNotMerged(t *testing.T) {
	now := monday1020
	a := newThessApp(t, func() time.Time { return now })
	a.osethLines["L"] = &osethLine{
		cur:  map[string]oseth.Sighting{"2001": {Vehicle: oseth.Vehicle{ID: "2001", Lat: 37.98, Lon: 23.71, ShapeID: "SH"}, At: now}},
		prev: map[string]oseth.Sighting{"2001": {Vehicle: oseth.Vehicle{ID: "2001", Lat: 37.98, Lon: 23.70, ShapeID: "SH"}, At: now.Add(-30 * time.Second)}},
		at:   now.Add(-2 * osethFresh),
	}
	a.onLineWithOSETH(sched.LinePoll{Line: "L", Started: now, Done: now, Routes: 1, Obs: []match.Obs{
		{Vehicle: telematics.Vehicle{VehNo: "44548", RouteCode: "SH", Lat: 37.98, Lon: 23.725,
			Heading: 90, Time: now.Add(-10 * time.Second)}, LineCode: "R1"}}})
	d := a.lines["L"]
	if d == nil || len(d.vehicles) != 1 || d.vehicles[0].ID != "44548" {
		t.Fatalf("stale OSETH state was merged: %+v", d)
	}
}

func TestOSETHPickWatchedOldestFirst(t *testing.T) {
	now := monday1020
	a := newThessApp(t, func() time.Time { return now })
	a.sched.SetLines([]sched.LineSpec{{ID: "A", Active: true, Routes: []sched.Route{{Code: "SH", LineCode: "R1", Active: true}}},
		{ID: "B", Active: true, Routes: []sched.Route{{Code: "SH", LineCode: "R1", Active: true}}}})
	a.sched.Watch("A")
	a.sched.Watch("B")
	first, ok := a.osethPick()
	if !ok || first != "A" {
		t.Fatalf("first pick %q %v, want A", first, ok)
	}
	a.osethMu.Lock()
	a.osethLast[first] = now
	a.osethMu.Unlock()
	if second, ok := a.osethPick(); !ok || second != "B" {
		t.Fatalf("second pick %q %v, want B", second, ok)
	}
	a.osethMu.Lock()
	a.osethLast["B"] = now
	a.osethMu.Unlock()
	if line, ok := a.osethPick(); ok {
		t.Fatalf("polled %q again within the interval", line)
	}
	now = now.Add(osethPollEvery)
	if line, ok := a.osethPick(); !ok || line != "A" {
		t.Fatalf("after the interval: %q %v, want A", line, ok)
	}
}
