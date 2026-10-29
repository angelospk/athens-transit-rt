package server

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/model"
)

// The rail test timetable (internal/rail/testdata/stasy) on Wed 2026-10-28 10:00: M2-3 runs
// between ΟΜΟΝΟΙΑ (left 09:52) and ΕΛΛΗΝΙΚΟ (arrives 10:09); M2-1, M2-4, M1-1 and M2-2 leave
// ΣΥΝΤΑΓΜΑ (platforms P1 and P2) at 10:01, 10:02, 10:04 and 10:07, M1-2 at 10:20.
var wed1000 = time.Date(2026, 10, 28, 10, 0, 0, 0, gtfs.Athens)

const syntagma = "stasy:συνταγμα-metro"

func TestV2STASYLinesAndStops(t *testing.T) {
	a := stasyApp(t, wed1000)
	var ls v2Lines
	v2get(t, a, "/v2/lines", &ls)
	byID := map[string]model.Line{}
	for _, l := range ls.Lines {
		byID[l.ID] = l
	}
	m1, m2 := byID["stasy:M1"], byID["stasy:M2"]
	if m1.Mode != model.Metro || *m1.Label != "M1" || m1.Name != "ΠΕΙΡΑΙΑΣ - ΚΗΦΙΣΙΑ" || m1.Color != "#00A650" ||
		m1.TextColor != "#ffffff" || m1.Source != "stasy" || m2.Color != "#e30613" || m2.NameEn != nil {
		t.Fatalf("lines %s", asJSON(ls.Lines))
	}
	if stateOf(ls.Sources, "stasy") != model.OK || stateOf(ls.Sources, "oasa") == "" {
		t.Fatalf("sources %s", asJSON(ls.Sources))
	}

	var s v2Stop
	if _, ma := v2get(t, a, "/v2/stops/"+esc(syntagma), &s); ma != stopMaxAge || s.Stop == nil {
		t.Fatalf("%d %s", ma, asJSON(s))
	}
	st := s.Stop
	// Both platforms are one Stop, at their mean.
	// Its name from the name table (OSM, checked by hand), not STASY's capitals.
	if st.Name != "Σύνταγμα" || st.NameEn == nil || *st.NameEn != "Syntagma" || st.Lat != 37.975 ||
		st.Lon != 23.7355 || strings.Join(st.Modes, ",") != "metro" ||
		strings.Join(st.Lines, ",") != "stasy:M1,stasy:M2" || len(s.Lines) != 2 {
		t.Fatalf("stop %s", asJSON(s))
	}
	// Ids as users type them: an unfolded name.
	v2get(t, a, "/v2/stops/"+esc("stasy:ΣΎΝΤΑΓΜΑ-metro"), &s)
	if s.Stop == nil || s.Stop.ID != syntagma {
		t.Fatalf("alias %s", asJSON(s.Stop))
	}
	for _, id := range []string{"stasy:συνταγμα-tram", "stasy:nope-metro", "stasy:συνταγμα"} {
		if res, _ := get(t, a, "/v2/stops/"+esc(id)); res.StatusCode != 404 {
			t.Fatalf("%s: %d", id, res.StatusCode)
		}
	}

	var idx struct {
		Stops []model.IndexEntry `json:"stops"`
	}
	v2get(t, a, "/v2/stops", &idx)
	found := false
	for _, e := range idx.Stops {
		found = found || e.ID == syntagma
	}
	if !found {
		t.Fatalf("index %s", asJSON(idx))
	}

	var d v2LineDetail
	v2get(t, a, "/v2/lines/"+esc("stasy:m2"), &d)
	if d.Line == nil || d.Line.ID != "stasy:M2" || len(d.Patterns) != 4 {
		t.Fatalf("detail %s", asJSON(d))
	}
	// The most common stop sequence first: M2-1, M2-2 and M2-3 (P1 and P2 are one Stop).
	p := d.Patterns[0]
	if p.ID != "stasy:M2-0" || p.Headsign != "ΕΛΛΗΝΙΚΟ" || p.Direction != 0 ||
		strings.Join(p.Stops, ",") != syntagma+",stasy:ομονοια-metro,stasy:ελληνικο-metro" || len(p.Shape) < 3 {
		t.Fatalf("pattern %s", asJSON(p))
	}
	if _, ok := d.Stops["stasy:ανθουπολη-metro"]; !ok || len(d.Stops) != 4 {
		t.Fatalf("stops %s", asJSON(d.Stops))
	}
}

func TestV2STASYArrivals(t *testing.T) {
	a := stasyApp(t, wed1000)
	var s v2Stop
	v2get(t, a, "/v2/stops/"+esc(syntagma), &s)
	var got []string
	for _, ar := range s.Arrivals {
		got = append(got, ar.Line+" "+ar.Headsign+" "+time.Unix(ar.At, 0).In(gtfs.Athens).Format("15:04"))
		if ar.Source != model.SourceTable || ar.DelayS != nil || ar.Vehicle != nil || ar.VehicleLabel != nil ||
			ar.Platform != nil || ar.Status != model.Scheduled || ar.ScheduledAt == nil || *ar.ScheduledAt != ar.At ||
			ar.Stop != syntagma || ar.Run == nil {
			t.Fatalf("arrival %s", asJSON(ar))
		}
	}
	want := "stasy:M2 ΕΛΛΗΝΙΚΟ 10:01,stasy:M2 ΑΝΘΟΥΠΟΛΗ 10:02,stasy:M1 ΚΗΦΙΣΙΑ 10:04,stasy:M2 ΕΛΛΗΝΙΚΟ 10:07,stasy:M1 ΚΗΦΙΣΙΑ 10:20"
	if strings.Join(got, ",") != want || *s.Arrivals[0].Run != "stasy:run:M2-1:2026-10-28" {
		t.Fatalf("arrivals %v %s", got, asJSON(s.Arrivals))
	}
	// A train's last stop is no Arrival: nobody boards it there.
	v2get(t, a, "/v2/stops/"+esc("stasy:ελληνικο-metro"), &s)
	if len(s.Arrivals) != 0 {
		t.Fatalf("terminus %s", asJSON(s.Arrivals))
	}
}

func TestV2STASYVehicles(t *testing.T) {
	a := stasyApp(t, wed1000)
	var v v2Vehicles
	res, _ := get(t, a, "/v2/vehicles/rail")
	if _, ma := v2get(t, a, "/v2/vehicles/rail", &v); ma < 5 || ma > 30 || len(v.Vehicles) != 1 ||
		stateOf(v.Sources, "stasy") != model.OK || res.StatusCode != 200 {
		t.Fatalf("%d %s", ma, asJSON(v))
	}
	veh := v.Vehicles[0]
	run := "stasy:run:M2-3:2026-10-28"
	if veh.ID != run || *veh.Run != run || veh.Line != "stasy:M2" || veh.Mode != model.Metro || *veh.Headsign != "ΕΛΛΗΝΙΚΟ" ||
		veh.Label != nil || veh.DelayS != nil || *veh.NextStop != "stasy:ελληνικο-metro" ||
		veh.Position.Kind != model.Estimate || veh.Position.At != wed1000.Unix() || veh.Motion == nil {
		t.Fatalf("vehicle %s", asJSON(veh))
	}
	// 8 of the 17 minutes from ΟΜΟΝΟΙΑ to ΕΛΛΗΝΙΚΟ: south of ΟΜΟΝΟΙΑ, north of ΕΛΛΗΝΙΚΟ.
	if veh.Position.Lat >= 37.984 || veh.Position.Lat <= 37.890 {
		t.Fatalf("position %s", asJSON(veh.Position))
	}
	m := veh.Motion
	arr := time.Date(2026, 10, 28, 10, 9, 0, 0, gtfs.Athens).Unix()
	if len(m.Knots) != 2 || m.Knots[0] != [2]int64{0, wed1000.Unix()} || m.Knots[1][1] != arr ||
		len(m.PathStops) != 1 || m.Knots[1][0] != int64(m.PathStops[0]) || m.Speed != nil || m.PathSpeeds != nil ||
		len(m.Path) < 2 {
		t.Fatalf("motion %s", asJSON(m))
	}
	// Before 09:50 and after 10:30 nothing runs.
	a.now = func() time.Time { return wed1000.Add(-12 * time.Minute) }
	v2get(t, a, "/v2/vehicles/rail", &v)
	if len(v.Vehicles) != 0 {
		t.Fatalf("09:48 %s", asJSON(v.Vehicles))
	}
	// The bus snapshot has no train.
	a.now = func() time.Time { return wed1000 }
	v2get(t, a, "/v2/vehicles", &v)
	for _, x := range v.Vehicles {
		if x.Source != "oasa" {
			t.Fatalf("rail in /v2/vehicles: %s", asJSON(x))
		}
	}
	var lv v2Vehicles
	v2get(t, a, "/v2/lines/"+esc("stasy:M2")+"/vehicles", &lv)
	if lv.Line != "stasy:M2" || len(lv.Vehicles) != 1 {
		t.Fatalf("line vehicles %s", asJSON(lv))
	}
}

func TestV2STASYRuns(t *testing.T) {
	a := stasyApp(t, wed1000)
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc("stasy:run:M2-3:2026-10-28"), &r)
	run := r.Run
	if run == nil || run.Line != "stasy:M2" || *run.Vehicle != "stasy:run:M2-3:2026-10-28" || run.VehicleLabel != nil ||
		run.Status != model.Scheduled || *run.Pattern != "stasy:M2-0" || len(run.Stops) != 3 ||
		!run.Stops[0].Passed || !run.Stops[1].Passed || run.Stops[2].Passed ||
		run.Stops[1].At != time.Date(2026, 10, 28, 9, 52, 0, 0, gtfs.Athens).Unix() || // the departure
		run.Stops[2].At != time.Date(2026, 10, 28, 10, 9, 0, 0, gtfs.Athens).Unix() { // the arrival at the end
		t.Fatalf("%s", asJSON(run))
	}
	v2get(t, a, "/v2/runs/"+esc("stasy:run:M2-1:2026-10-28"), &r)
	if r.Run == nil || r.Run.Vehicle != nil || r.Run.Stops[0].Passed {
		t.Fatalf("not started %s", asJSON(r.Run))
	}
	for _, id := range []string{"stasy:run:M2-5:2026-10-28", "stasy:run:NOPE:2026-10-28", "stasy:run:M2-1:2026-12-28"} {
		if res, _ := get(t, a, "/v2/runs/"+esc(id)); res.StatusCode != 404 {
			t.Fatalf("%s: %d", id, res.StatusCode)
		}
	}
}

func TestV2STASYWarmingUp(t *testing.T) {
	a := newApp(t, wed1000) // no timetable loaded
	var s v2Stop
	if code, ma := v2get(t, a, "/v2/stops/"+esc(syntagma), &s); code != 200 || ma != 5 || s.Stop != nil ||
		stateOf(s.Sources, "stasy") != model.WarmingUp {
		t.Fatalf("%d %d %s", code, ma, asJSON(s))
	}
	var ls v2Lines
	if _, ma := v2get(t, a, "/v2/lines", &ls); ma != 5 {
		t.Fatalf("an incomplete list is cached 5 s, got %d", ma)
	}
}

// A published Stop id resolves to itself, also for a name with a hyphen next to a space.
func TestSTASYKeyStableThroughResolve(t *testing.T) {
	s := &stasyAdapter{}
	for _, name := range []string{"Μηδείας - Μυκάλης", "Α-Β", "ΣΥΝΤΑΓΜΑ (ΚΕΝΤΡΟ)", "Α / Β"} {
		id := stasyKey(name, "tram")
		if got := s.Resolve(KindStop, id); got != id {
			t.Errorf("%q: %s resolves to %s", name, id, got)
		}
	}
	if stasyKey("Α - Β", "tram") != stasyKey("Α-Β", "tram") {
		t.Error("punctuation changes the id")
	}
}

// A Stop the name table lacks (new or renamed in the feed) keeps STASY's name with no English
// name, and the index lists it, so the timetable load logs it.
func TestV2STASYStopNotInNameTable(t *testing.T) {
	a := stasyAppEdit(t, wed1000, func(dir string) {
		p := filepath.Join(dir, "stops.txt")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(string(b), "ΚΗΦΙΣΙΑ", "ΝΕΑ ΣΤΑΣΗ")), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	var s v2Stop
	v2get(t, a, "/v2/stops/"+esc("stasy:νεα-σταση-metro"), &s)
	if s.Stop == nil || s.Stop.Name != "ΝΕΑ ΣΤΑΣΗ" || s.Stop.NameEn != nil {
		t.Fatalf("%s", asJSON(s.Stop))
	}
	x, err := a.adapter("stasy:x").(*stasyAdapter).index()
	if err != nil || strings.Join(x.unnamed, ",") != "stasy:νεα-σταση-metro" {
		t.Fatalf("%v unnamed %v", err, x.unnamed)
	}
	if code, _ := v2get(t, a, "/v2/stops/"+esc("stasy:κηφισια-metro"), nil); code != 404 {
		t.Fatalf("old id answers %d without an alias", code)
	}
}

// The knots of every train of the test feed, minute by minute through the service day, keep the
// contract's rules: they start at [0, position.at], times strictly increase, metres never
// decrease, every Stop ahead (path_stops) is a knot, and the last knot is at the end of
// path + path_beyond. Clients move markers by these; a broken knot makes a train jump.
func TestV2STASYKnotInvariants(t *testing.T) {
	a := stasyApp(t, wed1000)
	ad := a.adapter("stasy:x").(*stasyAdapter)
	day := time.Date(2026, 10, 28, 4, 0, 0, 0, gtfs.Athens)
	trains := 0
	for m := 0; m < 22*60; m++ {
		now := day.Add(time.Duration(m) * time.Minute)
		vs, err := ad.Vehicles(now)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			mo := v.Motion
			if mo == nil {
				continue
			}
			trains++
			if msg := model.CheckKnots(mo.Knots); msg != "" {
				t.Fatalf("%s at %s: %s %v", v.ID, now.Format("15:04"), msg, mo.Knots)
			}
			if mo.Knots[0] != [2]int64{0, v.Position.At} {
				t.Fatalf("%s at %s: first knot %v, position.at %d", v.ID, now.Format("15:04"), mo.Knots[0], v.Position.At)
			}
			at := map[int64]bool{}
			for _, k := range mo.Knots {
				at[k[0]] = true
			}
			for _, s := range mo.PathStops {
				if !at[int64(s)] {
					t.Fatalf("%s at %s: path_stops %d m has no knot %v", v.ID, now.Format("15:04"), s, mo.Knots)
				}
			}
			length := polylineMetres(append(append([][2]float64{}, mo.Path...), mo.PathBeyond...))
			if last := float64(mo.Knots[len(mo.Knots)-1][0]); math.Abs(last-length) > 5+0.02*length {
				t.Fatalf("%s at %s: last knot %.0f m, path + path_beyond %.0f m", v.ID, now.Format("15:04"), last, length)
			}
		}
	}
	if trains < 100 {
		t.Fatalf("only %d train snapshots checked", trains)
	}
}

// polylineMetres is the length of [lat, lon] points in metres.
func polylineMetres(pts [][2]float64) float64 {
	var d float64
	for i := 1; i < len(pts); i++ {
		d += geo.Dist(geo.XY(pts[i-1][0], pts[i-1][1]), geo.XY(pts[i][0], pts[i][1]))
	}
	return d
}
