package railgeo

import (
	"math"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/geo"
)

func TestMetroPaths(t *testing.T) {
	for _, id := range []string{"M1", "M2", "M3", "T6", "T7"} {
		ps := MetroPaths(id)
		if len(ps) != 2 {
			t.Fatalf("%s: %d paths", id, len(ps))
		}
		for _, p := range ps {
			// [lat, lon] (the file has [lon, lat]): Athens is near 38 N, 23.7 E.
			if len(p) < 50 || p[0][0] < 37.8 || p[0][0] > 38.2 || p[0][1] < 23.5 || p[0][1] > 24.1 {
				t.Fatalf("%s: %d points, first %v", id, len(p), p[0])
			}
		}
	}
	if MetroPaths("M9") != nil {
		t.Fatal("unknown line has paths")
	}
}

// Points on M2's first path (south to north), and the same reversed.
func m2Stops(t *testing.T, reverse bool) [][2]float64 {
	p := MetroPaths("M2")[0]
	idx := []int{0, len(p) / 4, len(p) / 2, 3 * len(p) / 4, len(p) - 1}
	var out [][2]float64
	for _, i := range idx {
		out = append(out, p[i])
	}
	if reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

func TestFit(t *testing.T) {
	for _, rev := range []bool{false, true} {
		stops := m2Stops(t, rev)
		tr, ok := Fit(MetroPaths("M2"), stops)
		if !ok || len(tr.Along) != len(stops) {
			t.Fatalf("reverse %v: no fit", rev)
		}
		for i := 1; i < len(tr.Along); i++ {
			if tr.Along[i] <= tr.Along[i-1] {
				t.Fatalf("reverse %v: along not increasing %v", rev, tr.Along)
			}
		}
		for i, s := range stops {
			if d := geo.Dist(geo.XY(s[0], s[1]), tr.Line.At(tr.Along[i])); d > 30 {
				t.Fatalf("reverse %v: stop %d is %.0f m off", rev, i, d)
			}
		}
		sh := tr.Shape()
		first, last := sh[0], sh[len(sh)-1]
		if math.Abs(first[0]-stops[0][0]) > 3e-4 || math.Abs(last[0]-stops[len(stops)-1][0]) > 3e-4 {
			t.Fatalf("reverse %v: shape runs %v..%v, stops %v..%v", rev, first, last, stops[0], stops[len(stops)-1])
		}
		for _, p := range sh {
			if p[0] != math.Round(p[0]*1e5)/1e5 || p[1] != math.Round(p[1]*1e5)/1e5 {
				t.Fatalf("not rounded to 5 decimals: %v", p)
			}
		}
	}
	// A stop 2 km off every track: no fit.
	far := m2Stops(t, false)
	far[2][1] += 0.03
	if _, ok := Fit(MetroPaths("M2"), far); ok {
		t.Fatal("fitted a stop far off the track")
	}
	if _, ok := Fit(MetroPaths("M2"), far[:1]); ok {
		t.Fatal("fitted one stop")
	}
}

func TestSuburban(t *testing.T) {
	cs, st := Suburban()
	if len(cs) != 3 || cs[0].ID != "CHALKIDA" || cs[1].ID != "KIATO" || cs[2].ID != "PIRAIR" {
		t.Fatalf("corridors %+v", cs)
	}
	pir := cs[2]
	if pir.Name != "Πειραιάς – Αεροδρόμιο" || pir.Color != "#7E57C2" || len(pir.Stations) != 21 || len(pir.Path) < 100 ||
		pir.Path[0][0] < 37.8 || pir.Path[0][0] > 38.6 {
		t.Fatalf("PIRAIR %s %s %d %d %v", pir.Name, pir.Color, len(pir.Stations), len(pir.Path), pir.Path[0])
	}
	byID := map[string]Station{}
	for _, s := range st {
		byID[s.ID] = s
	}
	a := byID["6928c3c150ab5bc72b39cee5"]
	if a.Name != "Αγ. Ανάργυροι" || a.En != "Ag. Anargyroi" || math.Abs(a.Lat-38.021858) > 1e-9 || len(a.Corridors) == 0 {
		t.Fatalf("station %+v", a)
	}
	for _, c := range cs {
		for _, id := range c.Stations {
			if _, ok := byID[id]; !ok {
				t.Fatalf("%s: station %s missing", c.ID, id)
			}
		}
	}
}
