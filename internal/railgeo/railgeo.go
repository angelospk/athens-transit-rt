// Package railgeo holds the geometry of the rail lines: metro and tram shapes with their
// stations, and the suburban railway corridors.
//
// The data was generated in the athens-transit-map repo (scripts/metro.ts and
// scripts/metro-osm.ts for metro.json, OSM corridor paths for suburban.json) at commit
// 370af6a and copied here unchanged. Tracks © OpenStreetMap contributors (ODbL).
// stasy_stops.json (Stop names) is built by scripts/stasy-stop-names.py from OpenStreetMap.
package railgeo

import (
	_ "embed"
	"encoding/json"
	"math"
	"sort"
	"sync"

	"github.com/angelospk/athens-transit-rt/internal/geo"
)

// metroJSON is the metro and tram shapes, with [lon, lat] points.
//
//go:embed metro.json
var metroJSON []byte

// suburbanJSON is the suburban railway corridors, with [lat, lon] points.
//
//go:embed suburban.json
var suburbanJSON []byte

type metroFile struct {
	Source string `json:"source"`
	Lines  []struct {
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Color string         `json:"color"`
		Paths [][][2]float64 `json:"paths"`
	} `json:"lines"`
}

type suburbanFile struct {
	Corridors map[string]struct {
		Name     string       `json:"name"`
		Stations []string     `json:"stations"`
		Color    string       `json:"color"`
		Path     [][2]float64 `json:"path"`
	} `json:"corridors"`
	Stations []struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Corridors []string `json:"corridors"`
		En        string   `json:"en"`
		Lat       float64  `json:"lat"`
		Lon       float64  `json:"lon"`
	} `json:"stations"`
}

var (
	loadOnce sync.Once
	metro    map[string][][][2]float64 // line id -> paths, [lat, lon]
	corrs    []Corridor
	suburbs  []Station
)

// Track is a line's polyline with each stop's distance along it.
type Track struct {
	Line  *geo.Polyline
	Along []float64
}

// Corridor is one suburban railway corridor, its path in [lat, lon] as in the file.
type Corridor struct {
	ID       string
	Name     string
	Color    string
	Stations []string
	Path     [][2]float64
}

// Station is one suburban railway station.
type Station struct {
	ID, Name, En string
	Lat, Lon     float64
	Corridors    []string
}

func load() {
	loadOnce.Do(func() {
		var m metroFile
		if err := json.Unmarshal(metroJSON, &m); err != nil {
			panic("railgeo: metro.json: " + err.Error())
		}
		metro = make(map[string][][][2]float64, len(m.Lines))
		for _, l := range m.Lines {
			paths := make([][][2]float64, len(l.Paths))
			for i, p := range l.Paths {
				pts := make([][2]float64, len(p))
				for k, c := range p { // the file has [lon, lat]
					pts[k] = [2]float64{c[1], c[0]}
				}
				paths[i] = pts
			}
			metro[l.ID] = paths
		}

		var s suburbanFile
		if err := json.Unmarshal(suburbanJSON, &s); err != nil {
			panic("railgeo: suburban.json: " + err.Error())
		}
		corrs = make([]Corridor, 0, len(s.Corridors))
		for id, c := range s.Corridors {
			corrs = append(corrs, Corridor{
				ID:       id,
				Name:     c.Name,
				Color:    c.Color,
				Stations: c.Stations,
				Path:     c.Path,
			})
		}
		sort.Slice(corrs, func(i, j int) bool { return corrs[i].ID < corrs[j].ID })
		suburbs = make([]Station, len(s.Stations))
		for i, st := range s.Stations {
			suburbs[i] = Station{
				ID:        st.ID,
				Name:      st.Name,
				En:        st.En,
				Lat:       st.Lat,
				Lon:       st.Lon,
				Corridors: st.Corridors,
			}
		}
	})
}

// MetroPaths returns the shapes of a metro or tram line as [lat, lon] points, or nil for an
// unknown line id.
func MetroPaths(line string) [][][2]float64 {
	load()
	return metro[line]
}

// Fit snaps stops ([lat, lon]) onto the best of the paths and returns the resulting track.
// Every stop must be on the track and in order along it; false means no path fits.
func Fit(paths [][][2]float64, stops [][2]float64) (*Track, bool) {
	if len(stops) < 2 {
		return nil, false
	}
	stopXY := make([][2]float64, len(stops))
	for i, s := range stops {
		stopXY[i] = geo.XY(s[0], s[1])
	}
	var best *Track
	bestScore := math.Inf(1)
	for _, p := range paths {
		for _, rev := range []bool{false, true} {
			line := geo.NewPolyline(project(p, rev))
			along := geo.SnapStops(line, stopXY)
			if along == nil {
				continue
			}
			score, ok := scoreAlong(line, along, stopXY)
			if !ok || score >= bestScore {
				continue
			}
			bestScore = score
			best = &Track{Line: line, Along: along}
		}
	}
	return best, best != nil
}

// project turns [lat, lon] points into projected metres, optionally reversed.
func project(path [][2]float64, rev bool) [][2]float64 {
	out := make([][2]float64, len(path))
	for i := range path {
		j := i
		if rev {
			j = len(path) - 1 - i
		}
		out[i] = geo.XY(path[j][0], path[j][1])
	}
	return out
}

// scoreAlong sums the distance from each stop to its snapped position, and reports whether the
// positions run strictly along the line.
func scoreAlong(line *geo.Polyline, along []float64, stopXY [][2]float64) (float64, bool) {
	score := 0.0
	for i, a := range along {
		if i > 0 && a <= along[i-1] {
			return 0, false
		}
		score += geo.Dist(stopXY[i], line.At(a))
	}
	return score, true
}

// Shape is the part of the track between the first and last stop, in [lat, lon] rounded to
// five decimals.
func (t *Track) Shape() [][2]float64 {
	pts := t.Line.Slice(t.Along[0], t.Along[len(t.Along)-1])
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		lat, lon := geo.LatLon(p)
		out[i] = [2]float64{round5(lat), round5(lon)}
	}
	return out
}

func round5(v float64) float64 { return math.Round(v*1e5) / 1e5 }

// Suburban returns the suburban railway corridors sorted by ID, and their stations in file
// order.
func Suburban() ([]Corridor, []Station) {
	load()
	return corrs, suburbs
}
