package match

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// TestUpstreamParity replays 8 recorded polling cycles of lines 040 and Α1 (2026-10-05)
// through the three matchers and compares every result with upstream oasa_rt/matcher.py run
// on the same rows (testdata/upstream.json, made by the script in the commit message).
func TestUpstreamParity(t *testing.T) {
	in, err := os.Open("testdata/feed-040-A1.snap")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	f, err := gtfs.ReadSnapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Stops  map[string][]struct{ StopCode string }
		Cycles []struct{ Rows []map[string]string }
	}
	var want map[string][][][]any
	load(t, "testdata/cycles.json", &rec)
	load(t, "testdata/upstream.json", &want)
	stops := func(rc string) ([]string, bool) {
		var out []string
		for _, s := range rec.Stops[rc] {
			out = append(out, s.StopCode)
		}
		return out, true
	}
	for name, kind := range map[string]Kind{"greedy": Greedy, "hungarian": Hungarian, "memory": Memory} {
		m := New(f, NewRouteMapper(f, stops), kind)
		for ci, cyc := range rec.Cycles {
			byLine := map[string][]Obs{}
			for _, row := range cyc.Rows {
				byLine[row["LINE"]] = append(byLine[row["LINE"]], toObs(t, row))
			}
			lines := make([]string, 0, len(byLine))
			for l := range byLine {
				lines = append(lines, l)
			}
			sort.Strings(lines)
			var got [][]any
			for _, l := range lines {
				for _, r := range m.MatchLine(l, byLine[l]) {
					var trip, date any
					if r.Matched() {
						trip, date = f.Trips[r.Trip].ID, gtfs.ServiceDate(r.Day)
					}
					got = append(got, []any{r.VehicleID, r.Line, r.RouteID, trip, date,
						float64(r.Delay), float64(r.NextIndex), r.Waiting})
				}
			}
			w := want[name][ci]
			if len(got) != len(w) {
				t.Fatalf("%s cycle %d: %d results, upstream %d", name, ci, len(got), len(w))
			}
			for i := range w {
				gj, _ := json.Marshal(got[i])
				wj, _ := json.Marshal(w[i])
				if string(gj) != string(wj) {
					t.Errorf("%s cycle %d: got %s, upstream %s", name, ci, gj, wj)
				}
			}
		}
	}
}

func load(t *testing.T, path string, v any) {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func toObs(t *testing.T, row map[string]string) Obs {
	lat, _ := strconv.ParseFloat(row["CS_LAT"], 64)
	lon, _ := strconv.ParseFloat(row["CS_LNG"], 64)
	heading, _ := strconv.ParseFloat(row["VEH_HEADING"], 64)
	ts, err := telematics.ParseCSDate(row["CS_DATE"])
	if err != nil {
		t.Fatal(err)
	}
	return Obs{Vehicle: telematics.Vehicle{VehNo: row["VEH_NO"], RouteCode: row["ROUTE_CODE"], Lat: lat, Lon: lon,
		Heading: heading, Time: ts}, LineCode: row["LINE_CODE"]}
}
