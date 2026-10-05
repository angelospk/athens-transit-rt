package staticsite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

func TestWrite(t *testing.T) {
	f := gtfstest.StraightLine{Stops: 3, Trips: 2, First: 36000, Headway: 10, Leg: 5}.Feed(t)
	dir := t.TempDir()
	tel := []telematics.Line{{LineCode: "9", LineID: "L", LineDescr: "OTHER", LineDescrEng: "OTHER EN"},
		{LineCode: "R1", LineID: "L", LineDescr: "WEST - EAST", LineDescrEng: "WEST - EAST EN"}}
	if err := Write(f, tel, dir); err != nil {
		t.Fatal(err)
	}
	var lines LinesFile
	read(t, filepath.Join(dir, "static/v1/lines.json"), &lines)
	want := Line{ID: "L", Name: "WEST - EAST", NameEn: "WEST - EAST EN", Color: "#153ce0", TextColor: "#ffffff", Kind: "bus"}
	if lines.GTFSVersion != "2026-07-06" || len(lines.Lines) != 1 || lines.Lines[0] != want {
		t.Fatalf("lines.json %+v", lines)
	}
	var lf LineFile
	read(t, filepath.Join(dir, "static/v1/lines/L.json"), &lf)
	v := lf.Variants["SH"]
	if lf.ID != "L" || v.Headsign != "EAST" || v.Direction != 0 || !reflect.DeepEqual(v.Stops, []string{"S0", "S1", "S2"}) ||
		len(v.Shape) != 3 || v.Shape[1] != [2]float64{37.98, 23.71} {
		t.Fatalf("line file %+v", lf)
	}
	if s := lf.Stops["S1"]; s.Name != "STOP 1" || s.Lat != 37.98 || s.Lon != 23.71 {
		t.Fatalf("stop %+v", s)
	}
}

// Keys must match the contract fixtures exactly.
func TestFixtureKeys(t *testing.T) {
	f := gtfstest.StraightLine{Stops: 3, Trips: 2, First: 36000, Headway: 10, Leg: 5}.Feed(t)
	dir := t.TempDir()
	if err := Write(f, nil, dir); err != nil {
		t.Fatal(err)
	}
	for got, fixture := range map[string]string{
		"static/v1/lines.json":   "lines.json",
		"static/v1/lines/L.json": "lines-040.json",
	} {
		g, _ := os.ReadFile(filepath.Join(dir, got))
		w, err := os.ReadFile(filepath.Join("../../docs/fixtures", fixture))
		if err != nil {
			t.Fatal(err)
		}
		if gk, wk := keys(t, g), keys(t, w); !reflect.DeepEqual(gk, wk) {
			t.Fatalf("%s keys %v, fixture %v", got, gk, wk)
		}
	}
}

// keys lists object keys two levels deep, using the first element of maps/arrays.
func keys(t *testing.T, b []byte) []string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(prefix string, v any, depth int)
	walk = func(prefix string, v any, depth int) {
		if depth > 3 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				name := k
				if prefix == "id-map" {
					name = "*"
				}
				out = append(out, name)
				next := ""
				if k == "variants" || k == "stops" && depth == 0 {
					next = "id-map"
				}
				walk(next, c, depth+1)
				if prefix == "id-map" {
					break
				}
			}
		case []any:
			if len(x) > 0 {
				walk("", x[0], depth+1)
			}
		}
	}
	walk("", v, 0)
	sort.Strings(out)
	return out
}

func read(t *testing.T, path string, v any) {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}
