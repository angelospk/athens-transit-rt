// Small geometry on [lat, lon] polylines: metres along a shape, projection, cutting.
// Port of citybus-bridge src/geo.ts.

package citybus

import (
	"math"
	"sort"
)

const (
	earthR = 6371000
	degRad = math.Pi / 180
)

func dist(a, b LL) float64 {
	x := (b[1] - a[1]) * degRad * math.Cos((a[0]+b[0])/2*degRad)
	y := (b[0] - a[0]) * degRad
	return math.Sqrt(x*x+y*y) * earthR
}

// cumulative metres at each point of a shape.
func cumulative(shape []LL) []float64 {
	out := make([]float64, len(shape))
	for i := 1; i < len(shape); i++ {
		out[i] = out[i-1] + dist(shape[i-1], shape[i])
	}
	return out
}

// proj is the nearest point of a shape to p: metres along it and the distance off it.
type proj struct {
	M   float64
	Off float64
}

// project returns the nearest point of the shape to p, from metre minM on (a stop or bus
// further along a loop).
func project(shape []LL, cum []float64, p LL, minM float64) proj {
	best := proj{M: 0, Off: math.Inf(1)}
	for _, c := range segments(shape, cum, p, minM) {
		if c.Off < best.Off {
			best = c
		}
	}
	return best
}

// candidates returns every place where the shape passes near p (a route out and back on one
// street passes twice): the nearest point of each pass within slack metres of the nearest one,
// nearest first.
func candidates(shape []LL, cum []float64, p LL, slack float64) []proj {
	all := segments(shape, cum, p, 0)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Off < all[j].Off })
	if len(all) == 0 {
		return nil
	}
	out := []proj{}
	for _, c := range all {
		if c.Off > all[0].Off+slack {
			break
		}
		near := false
		for _, o := range out {
			if math.Abs(o.M-c.M) <= 100 {
				near = true
				break
			}
		}
		if !near {
			out = append(out, c)
		}
	}
	return out
}

func segments(shape []LL, cum []float64, p LL, minM float64) []proj {
	out := make([]proj, 0, len(shape))
	kx := math.Cos(p[0] * degRad)
	for i := 0; i+1 < len(shape); i++ {
		if cum[i+1] < minM {
			continue
		}
		a, b := shape[i], shape[i+1]
		ax := (a[1] - p[1]) * kx
		ay := a[0] - p[0]
		bx := (b[1] - p[1]) * kx
		by := b[0] - p[0]
		dx, dy := bx-ax, by-ay
		len2 := dx*dx + dy*dy
		seg := cum[i+1] - cum[i]
		t0 := 0.0
		if seg > 0 && cum[i] < minM {
			t0 = (minM - cum[i]) / seg // never before minM
		}
		t := t0
		if len2 > 0 {
			t = math.Max(t0, math.Min(1, -(ax*dx+ay*dy)/len2))
		}
		q := LL{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t}
		out = append(out, proj{M: cum[i] + seg*t, Off: dist(p, q)})
	}
	return out
}

// pointAt returns the point at metre m.
func pointAt(shape []LL, cum []float64, m float64) LL {
	if len(shape) == 0 {
		return LL{}
	}
	if m <= 0 || len(shape) < 2 {
		return shape[0]
	}
	for i := 0; i+1 < len(shape); i++ {
		if cum[i+1] >= m {
			seg := cum[i+1] - cum[i]
			t := 0.0
			if seg > 0 {
				t = (m - cum[i]) / seg
			}
			return LL{shape[i][0] + (shape[i+1][0]-shape[i][0])*t, shape[i][1] + (shape[i+1][1]-shape[i][1])*t}
		}
	}
	return shape[len(shape)-1]
}

// slice returns the shape from metre a to metre b, as points (5 decimals).
func slice(shape []LL, cum []float64, a, b float64) []LL {
	out := []LL{pointAt(shape, cum, a)}
	for i := range shape {
		if cum[i] > a && cum[i] < b {
			out = append(out, shape[i])
		}
	}
	out = append(out, pointAt(shape, cum, b))
	for i := range out {
		out[i] = LL{round5(out[i][0]), round5(out[i][1])}
	}
	return out
}

func round5(x float64) float64 {
	return math.Round(x*1e5) / 1e5
}

// bearing returns the whole-degree bearing from a to b, 0..359.
func bearing(a, b LL) float64 {
	y := math.Sin((b[1]-a[1])*degRad) * math.Cos(b[0]*degRad)
	x := math.Cos(a[0]*degRad)*math.Sin(b[0]*degRad) - math.Sin(a[0]*degRad)*math.Cos(b[0]*degRad)*math.Cos((b[1]-a[1])*degRad)
	return math.Mod(math.Round(math.Atan2(y, x)/degRad+360), 360)
}

// tileOf returns the slippy-map tile of a point.
func tileOf(lat, lon float64, z int) (x, y int) {
	n := math.Pow(2, float64(z))
	x = int(math.Floor((lon + 180) / 360 * n))
	y = int(math.Floor((1 - math.Asinh(math.Tan(lat*degRad))/math.Pi) / 2 * n))
	return x, y
}
