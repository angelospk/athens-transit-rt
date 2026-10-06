package server

import (
	"math"
	"slices"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
)

// Motion: a few recent fixes per vehicle as distance along its line, for the `speed` and `path`
// fields. Owned by App.matchMu.
const (
	motionFixes      = 5                // fixes kept per vehicle
	motionWindowS    = 300              // fixes used for speed (other-tier lines poll every 150 s)
	motionMinSpanS   = 20               // speed needs fixes at least this far apart
	motionMinPairS   = 10               // pairs of fixes closer in time are not used for slopes
	motionNoiseM     = 20               // moved less than this over the window: standing
	motionMaxSpeed   = 20.0             // m/s (72 km/h)
	motionBackM      = 30               // GPS noise backwards along the line
	motionSlackM     = 100              // forward jump beyond maxSpeed*dt that is still plausible
	motionForget     = 10 * time.Minute // tracks of vehicles not seen for this long are dropped
	motionMaxOffM    = 150              // farther than this from the shape: no motion
	pathHorizonS     = 150              // path covers this many seconds at the current speed ...
	pathMinM         = 300              // ... at least this far ...
	pathMaxM         = 1500             // ... and at most this far
	pathStops        = 3                // with known stops: up to this many stops ahead
	pathSimplifyM    = 5
	motionCoordScale = 1e5 // 5 decimals
)

type motionFix struct {
	s float64 // metres along line
	t int64   // GPS time, unix seconds
}

type motionTrack struct {
	line  *geo.Polyline
	fixes []motionFix // oldest first, at most motionFixes
	seen  time.Time   // GPS time of the newest fix
}

type motion struct {
	tracks map[string]*motionTrack
}

func newMotion() *motion { return &motion{tracks: map[string]*motionTrack{}} }

// plausible reports whether a vehicle can be at s at time t after fix f on the same line.
func plausible(f motionFix, s float64, t int64) bool {
	dt := float64(t - f.t)
	return s >= f.s-motionBackM && s-f.s <= motionMaxSpeed*dt+motionSlackM
}

// observe records a fix and returns the vehicle's speed (nil when unknown). A fix older than
// the track's newest one changes nothing and gets no speed.
func (m *motion) observe(id string, line *geo.Polyline, s float64, at time.Time) *float64 {
	t := at.Unix()
	tr := m.tracks[id]
	if tr != nil && len(tr.fixes) > 0 {
		last := tr.fixes[len(tr.fixes)-1]
		switch {
		case t < last.t:
			return nil
		case t == last.t && tr.line == line:
			return tr.speed() // the same fix polled again
		}
	}
	if tr == nil || tr.line != line || !plausible(tr.fixes[len(tr.fixes)-1], s, t) {
		// New shape, new run or another pass of a loop: start over.
		tr = &motionTrack{line: line, fixes: make([]motionFix, 0, motionFixes)}
		m.tracks[id] = tr
	}
	if len(tr.fixes) == motionFixes {
		tr.fixes = append(tr.fixes[:0], tr.fixes[1:]...)
	}
	tr.fixes = append(tr.fixes, motionFix{s, t})
	tr.seen = at
	return tr.speed()
}

// speed is the median slope over pairs of recent fixes (Theil-Sen), so one bad fix among three
// or more does not change it.
func (tr *motionTrack) speed() *float64 {
	newest := tr.fixes[len(tr.fixes)-1]
	first := len(tr.fixes) - 1
	for first > 0 && newest.t-tr.fixes[first-1].t <= motionWindowS {
		first--
	}
	fx := tr.fixes[first:]
	if len(fx) < 2 || newest.t-fx[0].t < motionMinSpanS {
		return nil
	}
	if math.Abs(newest.s-fx[0].s) < motionNoiseM {
		return ptr(0.0)
	}
	var slopes []float64
	for i := range fx {
		for j := i + 1; j < len(fx); j++ {
			if dt := fx[j].t - fx[i].t; dt >= motionMinPairS {
				slopes = append(slopes, (fx[j].s-fx[i].s)/float64(dt))
			}
		}
	}
	if len(slopes) == 0 {
		return nil
	}
	v := max(0, min(motionMaxSpeed, median(slopes)))
	return ptr(math.Round(v*10) / 10)
}

func median(v []float64) float64 {
	slices.Sort(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

// pick chooses which pass of the line a vehicle is on: the only one that fits its heading, or
// the only one reachable from its last fix. ok is false when that stays ambiguous.
func (m *motion) pick(id string, line *geo.Polyline, cands []geo.Cand, at time.Time) (float64, bool) {
	right := make([]geo.Cand, 0, len(cands))
	for _, c := range cands {
		if !c.Wrong {
			right = append(right, c)
		}
	}
	if len(right) > 0 {
		cands = right
	}
	if len(cands) == 1 {
		return cands[0].Along, true
	}
	tr := m.tracks[id]
	if tr == nil || tr.line != line || len(tr.fixes) == 0 {
		return 0, false
	}
	last := tr.fixes[len(tr.fixes)-1]
	if at.Unix() < last.t {
		return 0, false
	}
	found, s := 0, 0.0
	for _, c := range cands {
		if plausible(last, c.Along, at.Unix()) {
			found, s = found+1, c.Along
		}
	}
	return s, found == 1
}

func (m *motion) forget(now time.Time) {
	for id, tr := range m.tracks {
		if now.Sub(tr.seen) > motionForget {
			delete(m.tracks, id)
		}
	}
}

// pathAhead is the line ahead of s as [lat, lon] points (contract rev 3): to the next stop
// (stops[0]) when stops are known, else max(speed * pathHorizonS, pathMinM); at most pathMaxM
// and never past the line's end. nil when there is nothing ahead (at the stop, at the end).
// With stops it also returns (rev 4) the continuation up to the pathStops-th stop ahead (from
// path's end, or from s when path is nil) and the stops on path + beyond, as metres along them.
func pathAhead(line *geo.Polyline, s float64, speed *float64, stops []float64) (path, beyond [][2]float64, at []int) {
	end := s + pathMaxM
	switch {
	case stops != nil:
		if len(stops) == 0 {
			return nil, nil, nil
		}
		end = min(end, stops[0])
	case speed != nil:
		end = s + min(pathMaxM, max(pathMinM, *speed*pathHorizonS))
	default:
		end = s + pathMinM
	}
	end = min(end, line.Length())
	var off func(float64) float64
	if end-s >= 1 {
		path, off = simplified(line, s, end)
	}
	if stops == nil {
		return path, nil, nil
	}
	var ahead []float64
	for _, x := range stops {
		if x > s+1 && (len(ahead) == 0 || x > ahead[len(ahead)-1]) {
			ahead = append(ahead, x)
		}
	}
	if len(ahead) == 0 {
		return path, nil, nil
	}
	from := s
	if path != nil {
		from = end
	}
	last := min(s+pathMaxM, line.Length(), ahead[min(pathStops, len(ahead))-1])
	var offB func(float64) float64
	if last-from >= 1 {
		beyond, offB = simplified(line, from, last)
	}
	base := 0.0
	if path != nil {
		base = off(end)
	}
	for _, x := range ahead {
		var m float64
		switch {
		case path != nil && x <= end:
			m = off(x)
		case beyond != nil && x <= last:
			m = base + offB(x)
		default:
			continue
		}
		if r := int(math.Round(m)); len(at) == 0 || r > at[len(at)-1] {
			at = append(at, r)
		}
	}
	return path, beyond, at
}

// simplified is the line from `from` to `to` as rounded [lat, lon] points (5 m simplification)
// and, for a position along the line in that range, the metres along those points (nil, nil
// when fewer than 2 points remain).
func simplified(line *geo.Polyline, from, to float64) ([][2]float64, func(float64) float64) {
	sl := line.Slice(from, to)
	pts := geo.Simplify(sl, pathSimplifyM)
	// Simplify keeps a subset of sl: the original along and the simplified along of each kept point.
	orig, cum := make([]float64, 0, len(pts)), make([]float64, 0, len(pts))
	a, j := from, 0
	for k, p := range pts {
		for ; j < len(sl) && sl[j] != p; j++ {
			if j+1 < len(sl) {
				a += geo.Dist(sl[j], sl[j+1])
			}
		}
		orig = append(orig, a)
		if k == 0 {
			cum = append(cum, 0)
		} else {
			cum = append(cum, cum[k-1]+geo.Dist(pts[k-1], p))
		}
	}
	out := make([][2]float64, 0, len(pts))
	for _, p := range pts {
		lat, lon := geo.LatLon(p)
		ll := [2]float64{round5(lat), round5(lon)}
		if len(out) == 0 || out[len(out)-1] != ll {
			out = append(out, ll)
		}
	}
	if len(out) < 2 {
		return nil, nil
	}
	return out, func(x float64) float64 {
		k := 0
		for k+2 < len(orig) && orig[k+1] < x {
			k++
		}
		if orig[k+1] <= orig[k] {
			return cum[k+1]
		}
		f := max(0, min(1, (x-orig[k])/(orig[k+1]-orig[k])))
		return cum[k] + f*(cum[k+1]-cum[k])
	}
}

func round5(v float64) float64 { return math.Round(v*motionCoordScale) / motionCoordScale }
