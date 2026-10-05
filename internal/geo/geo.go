// Package geo measures positions along routes: a local equirectangular projection centred on
// Athens, polylines, and the snapping of stops onto a shape.
package geo

import (
	"math"
	"sort"
)

const (
	EarthRadius = 6371000.0
	StopSnapM   = 150 // a stop farther than this from its shape -> shape unusable
)

var cosLat0 = math.Cos(37.98 * math.Pi / 180)

// XY projects (lat, lon) to metres.
func XY(lat, lon float64) [2]float64 {
	return [2]float64{lon * math.Pi / 180 * EarthRadius * cosLat0, lat * math.Pi / 180 * EarthRadius}
}

// Dist is the distance between two projected points.
func Dist(a, b [2]float64) float64 { return math.Hypot(a[0]-b[0], a[1]-b[1]) }

// pymod is Python's % for floats (result has the sign of the divisor).
func pymod(a, b float64) float64 {
	r := math.Mod(a, b)
	if r != 0 && (r < 0) != (b < 0) {
		r += b
	}
	return r
}

type Polyline struct {
	XY      [][2]float64
	Cum     []float64 // distance along the line at each vertex
	Bearing []float64 // compass bearing of each segment, degrees
}

func NewPolyline(xy [][2]float64) *Polyline {
	p := &Polyline{XY: xy, Cum: make([]float64, 1, len(xy))}
	for i := 0; i+1 < len(xy); i++ {
		a, b := xy[i], xy[i+1]
		p.Cum = append(p.Cum, p.Cum[i]+Dist(a, b))
		p.Bearing = append(p.Bearing, pymod(math.Atan2(b[0]-a[0], b[1]-a[1])*180/math.Pi, 360))
	}
	return p
}

func (p *Polyline) Length() float64 { return p.Cum[len(p.Cum)-1] }

// Cand is one pass of a line near a point.
type Cand struct {
	Along float64 // distance along the line
	Off   float64 // distance from the line
	Wrong bool    // the heading opposes the line's direction there
}

// Candidates returns every separate pass of the line within maxDist of (x, y): one per run of
// consecutive nearby segments, so a loop that passes the same street twice yields two.
func (p *Polyline) Candidates(x, y, maxDist, heading float64, hasHeading bool) []Cand {
	var out []Cand
	run := -1
	for i := 0; i+1 < len(p.XY); i++ {
		ax, ay, bx, by := p.XY[i][0], p.XY[i][1], p.XY[i+1][0], p.XY[i+1][1]
		dx, dy := bx-ax, by-ay
		l2 := dx*dx + dy*dy
		t := 0.0
		if l2 != 0 {
			t = max(0, min(1, ((x-ax)*dx+(y-ay)*dy)/l2))
		}
		d := math.Hypot(x-(ax+t*dx), y-(ay+t*dy))
		if d > maxDist {
			run = -1
			continue
		}
		wrong := hasHeading && l2 > 0 && math.Abs(pymod(heading-p.Bearing[i]+180, 360)-180) > 90
		c := Cand{p.Cum[i] + t*(p.Cum[i+1]-p.Cum[i]), d, wrong}
		if run < 0 {
			run = len(out)
			out = append(out, c)
		} else if d < out[run].Off {
			out[run] = c
		}
	}
	return out
}

// SnapStops returns the position of each stop along the line, in order (Viterbi over each
// stop's candidates), or nil if some stop is not within StopSnapM of the line.
func SnapStops(line *Polyline, stops [][2]float64) []float64 {
	layers := make([][]Cand, len(stops))
	for i, s := range stops {
		layers[i] = line.Candidates(s[0], s[1], StopSnapM, 0, false)
		if len(layers[i]) == 0 {
			return nil
		}
	}
	cost := make([]float64, len(layers[0]))
	for k, c := range layers[0] {
		cost[k] = c.Off
	}
	back := make([][]int, 0, len(layers)-1)
	for li := 1; li < len(layers); li++ {
		prev, cur := layers[li-1], layers[li]
		newCost, ptr := make([]float64, len(cur)), make([]int, len(cur))
		for j, c := range cur {
			best, bk := math.Inf(1), -1
			for k, pc := range prev {
				// min over (cost, k) tuples: ties go to the lower index, as in Python
				if pc.Along <= c.Along+1e-6 && (cost[k] < best || bk < 0) {
					best, bk = cost[k], k
				}
			}
			newCost[j], ptr[j] = best+c.Off, bk
		}
		cost = newCost
		back = append(back, ptr)
	}
	best := 0
	for k := range cost {
		if cost[k] < cost[best] {
			best = k
		}
	}
	if math.IsInf(cost[best], 1) {
		return nil
	}
	path := make([]int, len(layers))
	path[len(layers)-1] = best
	for i := len(back) - 1; i >= 0; i-- {
		path[i] = back[i][path[i+1]]
	}
	out := make([]float64, len(layers))
	for i, k := range path {
		out[i] = layers[i][k].Along
	}
	return out
}

// Geometry is the line a stop pattern is measured on, with each stop's position along it.
type Geometry struct {
	Line      *Polyline
	StopAlong []float64
	FromShape bool
}

// Progress is the fractional stop index (2.5 = halfway between the 3rd and 4th stop).
func (g *Geometry) Progress(along float64) float64 {
	a := g.StopAlong
	seg := sort.SearchFloat64s(a, math.Nextafter(along, math.Inf(1))) - 1 // bisect_right - 1
	seg = min(max(seg, 0), len(a)-2)
	span := a[seg+1] - a[seg]
	t := 0.0
	if span > 0 {
		t = max(0, min(1, (along-a[seg])/span))
	}
	return float64(seg) + t
}
