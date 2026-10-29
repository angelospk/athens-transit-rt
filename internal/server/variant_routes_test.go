package server

import (
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/meta"
)

// The OASA GTFS puts the "every Thursday (street market)" variants of Α3, 447, 731, 120 and 856
// on Wednesday's service. A route code is polled when its shapes run at this time of day on any
// day, so the Thursday buses are seen (#20). Route codes are shape ids here (RouteMapper).
func TestVariantRouteOnAnotherWeekdayIsPolled(t *testing.T) {
	files := gtfstest.StraightLine{Stops: 5, Trips: 6, First: 10 * 3600, Headway: 10, Leg: 5}.Files()
	// SHT: a Tuesday-only shape at the same hours (service day_2).
	files["calendar.txt"] += "day_2,0,1,0,0,0,0,0,20260706,20261006\n"
	files["trips.txt"] += "R1,day_2,TT,WEST,1,SHT\n"
	files["stop_times.txt"] += "TT,10:00:00,10:00:00,S4,1\nTT,10:20:00,10:20:00,S0,2\n"
	files["shapes.txt"] += "SHT,37.98,23.74,1\nSHT,37.98,23.70,2\n"
	// SHN: a night shape, never near 10:20.
	files["trips.txt"] += "R1,day_1,TN,EAST,0,SHN\n"
	files["stop_times.txt"] += "TN,23:00:00,23:00:00,S0,1\nTN,23:20:00,23:20:00,S4,2\n"
	files["shapes.txt"] += "SHN,37.98,23.70,1\nSHN,37.98,23.74,2\n"
	f, err := gtfs.LoadZip(gtfstest.Zip(t, files), gtfs.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mapper := match.NewRouteMapper(f, func(string) ([]string, bool) { return nil, false })
	routes := []meta.RouteRef{{Code: "SH", LineCode: "R1"}, {Code: "SHT", LineCode: "R1"}, {Code: "SHN", LineCode: "R1"}, {Code: "X", LineCode: "R1"}}
	spec := lineSpec(f, mapper, "L", monday1020, routes) // Monday
	got := map[string]bool{}
	for _, r := range spec.Routes {
		got[r.Code] = r.Active
	}
	if !spec.Active || !got["SH"] || !got["SHT"] || got["SHN"] || !got["X"] {
		t.Fatalf("active %v, routes %v (want SH, SHT and the unmapped X; not SHN)", spec.Active, got)
	}
	// Tuesday 10:20: SHT's own day; the line is active through it, SH (Mondays only) runs at
	// this time of day too.
	tue := monday1020.AddDate(0, 0, 1)
	spec = lineSpec(f, mapper, "L", tue, routes)
	if !spec.Active {
		t.Fatalf("Tuesday: line inactive")
	}
}
