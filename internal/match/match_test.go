package match

import (
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// Monday 2026-10-05. Line L: 5 stops 0.01 deg apart, trips every 10 min from 10:00, 5 min/leg.
var monday = time.Date(2026, 10, 5, 0, 0, 0, 0, gtfs.Athens)

func at(h, m, s int) time.Time { return monday.Add(time.Duration(h*3600+m*60+s) * time.Second) }

func line(t *testing.T) *gtfs.Feed {
	return gtfstest.StraightLine{Stops: 5, Trips: 6, First: 10 * 3600, Headway: 10, Leg: 5}.Feed(t)
}

func noStops(string) ([]string, bool) { return nil, false }

// obs puts a vehicle at fractional stop index `progress` along line L.
func obs(id string, progress float64, ts time.Time) Obs {
	return Obs{Vehicle: telematics.Vehicle{VehNo: id, RouteCode: "SH", Lat: 37.98, Lon: 23.70 + 0.01*progress,
		Heading: 90, Time: ts}, LineCode: "R1"}
}

func newMatcher(t *testing.T, kind Kind) *Matcher {
	f := line(t)
	return New(f, NewRouteMapper(f, noStops), kind)
}

func tripID(m *Matcher, r Result) string {
	if !r.Matched() {
		return ""
	}
	return m.Feed.Trips[r.Trip].ID
}

func TestOnTimeAndLate(t *testing.T) {
	for _, kind := range []Kind{Greedy, Hungarian, Memory} {
		m := newMatcher(t, kind)
		// T01 leaves 10:10; halfway between stops 2 and 3 is 10:22:30 on time.
		res := m.MatchLine("L", []Obs{obs("v1", 2.5, at(10, 24, 30))})
		r := res[0]
		if tripID(m, r) != "T01" || r.Delay != 120 || r.NextIndex != 3 || r.Waiting {
			t.Fatalf("kind %d: %+v trip %s", kind, r, tripID(m, r))
		}
		if r.RouteID != "R1" || r.Day != monday {
			t.Fatalf("route %s day %v", r.RouteID, r.Day)
		}
	}
}

func TestWaitingAtTerminal(t *testing.T) {
	m := newMatcher(t, Hungarian)
	r := m.MatchLine("L", []Obs{obs("v1", 0, at(10, 8, 0))})[0]
	if tripID(m, r) != "T01" || !r.Waiting || r.Delay != 0 || r.NextIndex != 1 {
		t.Fatalf("%+v trip %s", r, tripID(m, r))
	}
}

func TestOutOfRangeDelayUnmatched(t *testing.T) {
	m := newMatcher(t, Hungarian)
	// At stop 1 at 12:30: the latest trip there (T05, 10:55) would be 90 min late.
	r := m.MatchLine("L", []Obs{obs("v1", 1, at(12, 30, 0))})[0]
	if r.Matched() {
		t.Fatalf("matched %s", tripID(m, r))
	}
	if r.RouteID != "R1" {
		t.Fatalf("unmatched route id %q", r.RouteID)
	}
}

func TestOneToOne(t *testing.T) {
	for _, kind := range []Kind{Greedy, Hungarian, Memory} {
		m := newMatcher(t, kind)
		res := m.MatchLine("L", []Obs{obs("a", 2, at(10, 20, 0)), obs("b", 2.05, at(10, 20, 0))})
		if !res[0].Matched() || !res[1].Matched() || res[0].Trip == res[1].Trip {
			t.Fatalf("kind %d: %s %s", kind, tripID(m, res[0]), tripID(m, res[1]))
		}
	}
}

// Greedy gives vehicle a its cheapest trip even when that leaves b without any; Hungarian
// minimises the total and matches both.
func TestHungarianBeatsGreedy(t *testing.T) {
	cands := []candidate{
		{cost: 10, vid: "a", trip: 0, day: monday},
		{cost: 20, vid: "a", trip: 1, day: monday},
		{cost: 15, vid: "b", trip: 0, day: monday},
	}
	m := &Matcher{Feed: line(t)}
	if g := m.assignGreedy(cands); len(g) != 1 {
		t.Fatalf("greedy matched %d", len(g))
	}
	h := m.assignHungarian(cands)
	if len(h) != 2 {
		t.Fatalf("hungarian matched %d", len(h))
	}
	for _, c := range h {
		if (c.vid == "a" && c.trip != 1) || (c.vid == "b" && c.trip != 0) {
			t.Fatalf("hungarian assignment %+v", h)
		}
	}
}

func TestStickyAndNoBackwards(t *testing.T) {
	m := newMatcher(t, Hungarian)
	r := m.MatchLine("L", []Obs{obs("v", 2, at(10, 20, 0))})[0]
	if tripID(m, r) != "T01" {
		t.Fatalf("first match %s", tripID(m, r))
	}
	// 3 min later, slightly further along: T01 (3.5 min late) vs T02 (6.5 min early).
	r = m.MatchLine("L", []Obs{obs("v", 2.1, at(10, 23, 0))})[0]
	if tripID(m, r) != "T01" || m.previous["v"].trip != "T01" {
		t.Fatalf("sticky match lost: %s", tripID(m, r))
	}
}

func TestMemoryAnchorsDeparture(t *testing.T) {
	m := newMatcher(t, Memory)
	m.MatchLine("L", []Obs{obs("v", 0, at(10, 9, 0))})            // waiting at the first stop
	r := m.MatchLine("L", []Obs{obs("v", 0.5, at(10, 12, 0))})[0] // departed ~10:10:30
	if tripID(m, r) != "T01" || !m.Anchored(&r) {
		t.Fatalf("not anchored: %s anchored=%v", tripID(m, r), m.Anchored(&r))
	}
	m.MatchLine("L", []Obs{obs("v", 1, at(10, 16, 0))})
	m.MatchLine("L", []Obs{obs("v", 1.5, at(10, 22, 0))})
	// At stop 2 at 10:28, T01 is 8 min late (cost 480) and T02 2 min early (cost 360):
	// by position alone T02 wins; the anchor holds T01.
	r = m.MatchLine("L", []Obs{obs("v", 2, at(10, 28, 0))})[0]
	if tripID(m, r) != "T01" {
		t.Fatalf("anchor overridden: %s", tripID(m, r))
	}
}

func TestRouteMapperBySimilarity(t *testing.T) {
	f := line(t)
	stops := func(rc string) ([]string, bool) {
		if rc == "999" {
			return []string{"S0", "S1", "S2", "S3", "X"}, true // 4 of 5 shared
		}
		if rc == "998" {
			return []string{"S0", "Y", "Z", "W", "V"}, true
		}
		return nil, false
	}
	rm := NewRouteMapper(f, stops)
	sh := f.ShapeIndex("SH")
	if !rm.ShapesFor("L", "SH")[sh] {
		t.Fatal("equal code not mapped")
	}
	if !rm.ShapesFor("L", "999")[sh] {
		t.Fatal("similar route not mapped")
	}
	if rm.ShapesFor("L", "998")[sh] {
		t.Fatal("dissimilar route mapped")
	}
	if len(rm.ShapesFor("L", "unknown")) != 0 {
		t.Fatal("unknown route mapped")
	}
	if _, cached := rm.cache[[2]string{"L", "unknown"}]; cached {
		t.Fatal("unknown answer was cached")
	}
}
