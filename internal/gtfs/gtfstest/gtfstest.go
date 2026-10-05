// Package gtfstest builds small synthetic GTFS feeds for tests.
package gtfstest

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

// Zip writes a GTFS zip from file name -> CSV content.
func Zip(t testing.TB, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gtfs.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	out.Close()
	return path
}

// StraightLine is line "L" (route "R1", shape "SH") running due east along latitude 37.98
// from lon 23.70 through Stops stops spaced 0.01 deg (~880 m) apart, one trip every
// Headway minutes from First (seconds after midnight) for Trips trips, Leg minutes between
// stops, on service "day_1" (Mondays, 2026-07-06..2026-10-06).
type StraightLine struct {
	Stops, Trips    int
	First           int
	Headway, Leg    int
	SecondRouteCode string // optional extra route on the same line, reversed direction
}

func (s StraightLine) Files() map[string]string {
	var stops, st, trips, shapes strings.Builder
	stops.WriteString("stop_id,stop_name,stop_lat,stop_lon\n")
	shapes.WriteString("shape_id,shape_pt_lat,shape_pt_lon,shape_pt_sequence\n")
	for i := 0; i < s.Stops; i++ {
		fmt.Fprintf(&stops, "S%d,STOP %d,37.98,%.4f\n", i, i, 23.70+0.01*float64(i))
		fmt.Fprintf(&shapes, "SH,37.98,%.4f,%d\n", 23.70+0.01*float64(i), i+1)
	}
	trips.WriteString("route_id,service_id,trip_id,trip_headsign,direction_id,shape_id\n")
	st.WriteString("trip_id,arrival_time,departure_time,stop_id,stop_sequence\n")
	for k := 0; k < s.Trips; k++ {
		id := fmt.Sprintf("T%02d", k)
		fmt.Fprintf(&trips, "R1,day_1,%s,EAST,0,SH\n", id)
		start := s.First + k*s.Headway*60
		for i := 0; i < s.Stops; i++ {
			at := hms(start + i*s.Leg*60)
			fmt.Fprintf(&st, "%s,%s,%s,S%d,%d\n", id, at, at, i, i+1)
		}
	}
	return map[string]string{
		"stops.txt":      stops.String(),
		"routes.txt":     "route_id,route_short_name,route_long_name,route_type,route_color,route_text_color\nR1,L,WEST - EAST,3,153CE0,FFFFFF\n",
		"trips.txt":      trips.String(),
		"stop_times.txt": st.String(),
		"shapes.txt":     shapes.String(),
		"calendar.txt":   "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nday_1,1,0,0,0,0,0,0,20260706,20261006\n",
	}
}

// Feed loads the line as a gtfs.Feed.
func (s StraightLine) Feed(t testing.TB) *gtfs.Feed {
	t.Helper()
	f, err := gtfs.LoadZip(Zip(t, s.Files()), gtfs.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func hms(secs int) string { return fmt.Sprintf("%02d:%02d:%02d", secs/3600, secs/60%60, secs%60) }
