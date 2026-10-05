// Package staticsite writes the static map data of docs/CONTRACT.md (static/v1/...).
package staticsite

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

type Line struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	NameEn    string `json:"name_en"`
	Color     string `json:"color"`
	TextColor string `json:"text_color"`
	Kind      string `json:"kind"`
}

type LinesFile struct {
	GTFSVersion string `json:"gtfs_version"`
	Lines       []Line `json:"lines"`
}

type Variant struct {
	Headsign  string       `json:"headsign"`
	Direction int          `json:"direction"`
	Shape     [][2]float64 `json:"shape"`
	Stops     []string     `json:"stops"`
}

type Stop struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

type LineFile struct {
	ID       string             `json:"id"`
	Variants map[string]Variant `json:"variants"`
	Stops    map[string]Stop    `json:"stops"`
}

func round5(v float64) float64 { return math.Round(v*1e5) / 1e5 }

func color(hex, def string) string {
	if hex == "" {
		return def
	}
	return "#" + strings.ToLower(hex)
}

// mostCommon returns the most frequent key, the first seen winning ties.
func mostCommon[K comparable](order []K, counts map[K]int) K {
	best := order[0]
	for _, k := range order[1:] {
		if counts[k] > counts[best] {
			best = k
		}
	}
	return best
}

// Write creates dir/static/v1/lines.json and dir/static/v1/lines/{id}.json. tel (from
// webGetLines) supplies English names; it may be nil.
func Write(f *gtfs.Feed, tel []telematics.Line, dir string) error {
	base := filepath.Join(dir, "static", "v1")
	if err := os.MkdirAll(filepath.Join(base, "lines"), 0o755); err != nil {
		return err
	}
	out := LinesFile{GTFSVersion: f.Version(), Lines: []Line{}}
	for _, id := range f.Lines() {
		if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
			continue
		}
		trips := f.TripsForLine(id)
		// Name and colours of the line's route with the most trips.
		routeCount, routeOrder := map[int32]int{}, []int32{}
		for _, ti := range trips {
			r := f.Trips[ti].Route
			if routeCount[r] == 0 {
				routeOrder = append(routeOrder, r)
			}
			routeCount[r]++
		}
		r := f.Routes[mostCommon(routeOrder, routeCount)]
		kind := "bus"
		if r.Type == 11 {
			kind = "trolley"
		}
		out.Lines = append(out.Lines, Line{ID: id, Name: r.LongName, NameEn: englishName(tel, id, r.LongName),
			Color: color(r.Color, "#1a73e8"), TextColor: color(r.TextColor, "#ffffff"), Kind: kind})
		if err := writeJSON(filepath.Join(base, "lines", id+".json"), lineFile(f, id, trips)); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(base, "lines.json"), out)
}

// englishName prefers the telematics LineCode whose Greek name equals the GTFS name.
func englishName(tel []telematics.Line, id, greek string) string {
	first := ""
	for _, l := range tel {
		if l.LineID != id {
			continue
		}
		if strings.TrimSpace(l.LineDescr) == greek {
			return strings.TrimSpace(l.LineDescrEng)
		}
		if first == "" {
			first = strings.TrimSpace(l.LineDescrEng)
		}
	}
	return first
}

func lineFile(f *gtfs.Feed, id string, trips []int32) LineFile {
	lf := LineFile{ID: id, Variants: map[string]Variant{}, Stops: map[string]Stop{}}
	type acc struct {
		dir                  int
		patOrder, headsOrder []int32
		pats, heads          map[int32]int
	}
	byShape, shapeOrder := map[int32]*acc{}, []int32{}
	for _, ti := range trips {
		t := &f.Trips[ti]
		if t.Shape < 0 {
			continue
		}
		a := byShape[t.Shape]
		if a == nil {
			a = &acc{dir: int(t.Dir), pats: map[int32]int{}, heads: map[int32]int{}}
			byShape[t.Shape] = a
			shapeOrder = append(shapeOrder, t.Shape)
		}
		if a.pats[t.Pattern] == 0 {
			a.patOrder = append(a.patOrder, t.Pattern)
		}
		a.pats[t.Pattern]++
		if a.heads[t.Headsign] == 0 {
			a.headsOrder = append(a.headsOrder, t.Headsign)
		}
		a.heads[t.Headsign]++
	}
	for _, s := range shapeOrder {
		a := byShape[s]
		v := Variant{Headsign: f.Headsigns[mostCommon(a.headsOrder, a.heads)], Direction: a.dir}
		for _, p := range f.Shapes[s].Points {
			v.Shape = append(v.Shape, [2]float64{round5(p[0]), round5(p[1])})
		}
		for _, st := range f.Patterns[mostCommon(a.patOrder, a.pats)].Stops {
			stop := &f.Stops[st]
			v.Stops = append(v.Stops, stop.ID)
			lf.Stops[stop.ID] = Stop{Name: stop.Name, Lat: round5(stop.Lat), Lon: round5(stop.Lon)}
		}
		lf.Variants[f.Shapes[s].ID] = v
	}
	return lf
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, b, 0o644)
}
