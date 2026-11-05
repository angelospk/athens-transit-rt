package meta

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// oasthAPI: Thessaloniki-like answers (Latin line ids, a StopID key plus an arrival code).
type oasthAPI struct{}

func (oasthAPI) Lines(context.Context) ([]telematics.Line, error) {
	return []telematics.Line{{LineCode: "1", LineID: "39S"}, {LineCode: "2", LineID: "01A"}}, nil
}
func (oasthAPI) Routes(_ context.Context, lc string) ([]telematics.Route, error) {
	return []telematics.Route{{RouteCode: "r" + lc, LineCode: lc}}, nil
}
func (oasthAPI) Stops(context.Context, string) ([]telematics.RouteStop, error) {
	return []telematics.RouteStop{{StopCode: "11048", ArrivalCode: "4483"}, {StopCode: "11046", ArrivalCode: "4579"},
		{StopCode: "9", ArrivalCode: "9"}}, nil
}

func TestArrivalCodesArePersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	q := &queue{}
	s := Open(path, oasthAPI{}, q.submit, nil)
	if got, ok := s.ArrivalCode("11048"); ok || got != "11048" {
		t.Fatalf("unknown stop: %q %v, want the code itself and false", got, ok)
	}
	s.Stops("r1")
	q.drain()
	if st, ok := s.Stops("r1"); !ok || len(st) != 3 || st[0] != "11048" {
		t.Fatalf("stops %v %v: keyed by the GTFS id", st, ok)
	}
	if got, ok := s.ArrivalCode("11048"); !ok || got != "4483" {
		t.Fatalf("arrival code %q, want 4483", got)
	}
	// A reopened store (a restart) still knows it without asking OASTH again.
	s2 := Open(path, oasthAPI{}, (&queue{}).submit, nil)
	if got, ok := s2.ArrivalCode("11046"); !ok || got != "4579" {
		t.Fatalf("after reload: %q, want 4579", got)
	}
	// A stop whose code is the same on both sides is known too (a route listed it).
	if got, ok := s2.ArrivalCode("9"); !ok || got != "9" {
		t.Fatalf("same code: %q %v", got, ok)
	}
}

func TestLineAliasRenamesTelematicsLines(t *testing.T) {
	q := &queue{}
	s := Open(filepath.Join(t.TempDir(), "meta.json"), oasthAPI{}, q.submit, nil)
	s.Refresh()
	q.drain()
	s.SetLineAlias(func(id string) string {
		if id == "39S" {
			return "39Σ" // the GTFS route_short_name
		}
		return id
	})
	if r := s.LineRoutes()["39Σ"]; len(r) != 1 || r[0].Code != "r1" {
		t.Fatalf("LineRoutes[39Σ] = %v", s.LineRoutes())
	}
	if _, ok := s.LineRoutes()["39S"]; ok {
		t.Fatal("the telematics spelling is still a key")
	}
	if lc := s.LineCodes("39Σ"); len(lc) != 1 || lc[0] != "1" {
		t.Fatalf("LineCodes(39Σ) = %v", lc)
	}
	ids := map[string]bool{}
	for _, id := range s.LineIDs() {
		ids[id] = true
	}
	if !ids["39Σ"] || !ids["01A"] || ids["39S"] {
		t.Fatalf("LineIDs = %v", s.LineIDs())
	}
}

// countingAPI is oasthAPI that counts webGetStops calls per route.
type countingAPI struct {
	oasthAPI
	stops map[string]int
}

func (c *countingAPI) Stops(ctx context.Context, rc string) ([]telematics.RouteStop, error) {
	c.stops[rc]++
	return c.oasthAPI.Stops(ctx, rc)
}

// Thessaloniki (rt#86): every route's stop list is fetched once per refresh, not only the routes a
// bus runs on, so every stop gets an arrival code.
func TestFetchAllStopsAfterRefresh(t *testing.T) {
	api, q := &countingAPI{stops: map[string]int{}}, &queue{}
	s := Open(filepath.Join(t.TempDir(), "meta.json"), api, q.submit, nil)
	s.FetchAllStops()
	s.Refresh()
	q.drain()
	for _, rc := range []string{"r1", "r2"} {
		if st := s.Snapshot().Stops[rc]; len(st) != 3 {
			t.Fatalf("stops of %s = %v, want them fetched without a bus", rc, st)
		}
		if api.stops[rc] != 1 {
			t.Fatalf("webGetStops(%s) called %d times, want 1", rc, api.stops[rc])
		}
	}
	if got, ok := s.ArrivalCode("11048"); !ok || got != "4483" {
		t.Fatalf("arrival code %q %v", got, ok)
	}
}

// A restart with fresh persisted routes (no refresh due) still fills the routes that have no
// stop list, and asks nothing for those that have one.
func TestFetchAllStopsOnReloadAsksOnlyMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	q := &queue{}
	s := Open(path, oasthAPI{}, q.submit, nil)
	s.Refresh()
	q.drain()
	s.Stops("r1") // a bus ran on r1 only
	q.drain()

	api, q2 := &countingAPI{stops: map[string]int{}}, &queue{}
	s2 := Open(path, api, q2.submit, nil)
	if s2.Due() {
		t.Fatal("fresh meta should not be due")
	}
	s2.FetchAllStops()
	q2.drain()
	if api.stops["r1"] != 0 || api.stops["r2"] != 1 {
		t.Fatalf("calls %v, want only r2", api.stops)
	}
	if _, ok := s2.Snapshot().Stops["r2"]; !ok {
		t.Fatal("r2 stops not stored")
	}
}

// Athens keeps the old behaviour: without FetchAllStops a refresh asks no stop lists.
func TestNoStopsFetchedByDefault(t *testing.T) {
	api, q := &countingAPI{stops: map[string]int{}}, &queue{}
	s := Open(filepath.Join(t.TempDir(), "meta.json"), api, q.submit, nil)
	s.Refresh()
	q.drain()
	if len(api.stops) != 0 {
		t.Fatalf("stop lists asked: %v", api.stops)
	}
}
