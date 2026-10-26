package server

import (
	"math"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/segspeed"
)

// Stop boards (contract rev 7): the vehicles on their way to one stop and about when they get
// there, from what the line polls already hold. No OASA request, no line marked watched.
const (
	boardHorizon    = time.Hour // arrivals up to this far ahead
	boardPassedS    = 30        // an arrival this far in the past is gone
	boardAtStopM    = 30        // GPS noise: a vehicle this far past a stop is still at it
	boardPerVariant = 3         // rows per line and variant, soonest first
	// boardDefaultMS is the speed where neither typical speeds nor a timetable say anything
	// (an unmatched vehicle off the history): about the lines through the centre, stops included.
	boardDefaultMS = 3.5
	// boardMinBinMS: a slower typical bin counts this fast. The history counts layovers at
	// terminals as driving time there.
	boardMinBinMS = 0.5
)

// StopResponse is the /v1/stops/{stop_id} body.
type StopResponse struct {
	StopID       string    `json:"stop_id"`
	Name         string    `json:"name"`
	UpdatedAt    int64     `json:"updated_at"`
	NextUpdateAt int64     `json:"next_update_at"`
	Lines        []string  `json:"lines"`
	Arrivals     []Arrival `json:"arrivals"`
}

// Arrival is one vehicle (source "live") or one scheduled trip without a vehicle ("schedule").
type Arrival struct {
	Line       string  `json:"line"`
	Variant    *string `json:"variant"`
	Headsign   string  `json:"headsign"`
	TripID     *string `json:"trip_id"`
	Vehicle    *string `json:"vehicle"`
	ETA        *int64  `json:"eta"`       // null: waiting at its first stop, departure unknown
	Scheduled  *int64  `json:"scheduled"` // the trip's time at the stop
	PositionAt *int64  `json:"position_at"`
	StopsAway  *int    `json:"stops_away"` // 1: the stop is the vehicle's next one
	DistanceM  *int    `json:"distance_m"` // along the route
	Source     string  `json:"source"`
}

// boardPos is where a vehicle is on a stop pattern, recorded with each poll. trip is the
// matched trip, or for an unmatched vehicle the most common trip of its shape (pattern and
// headsign only). ok is false when the vehicle could not be placed.
type boardPos struct {
	g       *geo.Geometry
	trip    int32
	along   float64
	next    int  // index in the pattern of the first stop ahead
	waiting bool // at the first stop, not left yet
	ok      bool
}

// boardPosOf places a matched vehicle by its match, an unmatched one by its position on the
// shape (s from motionOf, -1 when ambiguous) when its shape's stops fit the shape.
func boardPosOf(w *world, r *match.Result, shape int32, s float64, speed *float64) boardPos {
	f := w.feed
	if r.Matched() {
		g := w.matcher.Geometry(f.Trip(r.Trip))
		if g == nil {
			return boardPos{}
		}
		next := r.NextIndex
		if r.Waiting {
			next = 0
		}
		return boardPos{g: g, trip: r.Trip, along: r.Along, next: next, waiting: r.Waiting, ok: true}
	}
	if shape < 0 || s < 0 {
		return boardPos{}
	}
	trip := shapeTrip(f, r.Line, shape)
	if trip < 0 {
		return boardPos{}
	}
	g := w.matcher.Geometry(f.Trip(trip))
	if g == nil || !g.FromShape { // s is along the shape; a line through the stops is another scale
		return boardPos{}
	}
	next := sort.SearchFloat64s(g.StopAlong, s-boardAtStopM)
	if next == len(g.StopAlong) {
		return boardPos{}
	}
	waiting := next == 0 && math.Abs(s-g.StopAlong[0]) <= match.TerminalRadiusM && (speed == nil || *speed == 0)
	return boardPos{g: g, trip: trip, along: s, next: next, waiting: waiting, ok: true}
}

// shapeTrip is the first trip of the line's most common stop pattern on a shape (the static
// variant's stops, staticsite), -1 when the line has no trip on it.
func shapeTrip(f *gtfs.Feed, line string, shape int32) int32 {
	count, first := map[int32]int{}, map[int32]int32{}
	best := int32(-1)
	for _, ti := range f.TripsForLine(line) {
		t := &f.Trips[ti]
		if t.Shape != shape {
			continue
		}
		if count[t.Pattern] == 0 {
			first[t.Pattern] = ti
		}
		count[t.Pattern]++
		if best < 0 || count[t.Pattern] > count[f.Trips[best].Pattern] {
			best = first[t.Pattern]
		}
	}
	return best
}

// linesAt is every line with a trip that stops at the stop before its last stop, sorted.
func (w *world) linesAt(stop int32) []string {
	w.stopsOnce.Do(func() {
		f := w.feed
		seen := map[[2]int32]bool{} // pattern, route
		sets := map[int32]map[string]bool{}
		for i := range f.Trips {
			t := &f.Trips[i]
			if k := [2]int32{t.Pattern, t.Route}; !seen[k] {
				seen[k] = true
				stops := f.Patterns[t.Pattern].Stops
				for _, s := range stops[:max(0, len(stops)-1)] {
					if sets[s] == nil {
						sets[s] = map[string]bool{}
					}
					sets[s][f.Line(t)] = true
				}
			}
		}
		w.stopLines = make(map[int32][]string, len(sets))
		for s, set := range sets {
			ls := make([]string, 0, len(set))
			for l := range set {
				ls = append(ls, l)
			}
			sort.Strings(ls)
			w.stopLines[s] = ls
		}
	})
	return w.stopLines[stop]
}

func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	wd := a.world()
	if wd == nil {
		writeJSON(w, http.StatusServiceUnavailable, minMaxAge, map[string]string{"error": "warming_up"})
		return
	}
	stop := wd.feed.StopIndex(r.PathValue("id"))
	if stop < 0 {
		writeJSON(w, http.StatusNotFound, unknownMaxAge, map[string]string{"error": "unknown_stop"})
		return
	}
	now := a.now()
	lines := wd.linesAt(stop)
	// The earliest next poll of the stop's lines, read before the data as in handleLine.
	var next time.Time
	polled := false
	for _, l := range lines {
		if t, ok := a.sched.NextUpdate(l); ok && (!polled || t.Before(next)) {
			next, polled = t, true
		}
	}
	resp := a.stopBoard(wd, stop, lines, now)
	maxAge := int64(inactiveMaxAge)
	resp.NextUpdateAt = now.Unix() + inactiveMaxAge
	if polled {
		maxAge = clamp(int64(next.Sub(now)/time.Second), minMaxAge, maxMaxAge)
		resp.NextUpdateAt = max(next.Unix(), now.Unix()+minMaxAge)
	}
	writeJSON(w, http.StatusOK, int(maxAge), resp)
}

// boardVehicle is one vehicle of a line's last poll, with its place.
type boardVehicle struct {
	line string
	r    *match.Result
	pos  boardPos
}

// stopBoard lists the vehicles on their way to the stop (each once, its newest fix) and, while
// the timetable is valid, the trips that leave later and have no vehicle yet; soonest first, at
// most boardPerVariant per line and variant.
func (a *App) stopBoard(w *world, stop int32, lines []string, now time.Time) StopResponse {
	f := w.feed
	resp := StopResponse{StopID: f.Stops[stop].ID, Name: f.Stops[stop].Name, UpdatedAt: now.Unix(),
		Lines: lines, Arrivals: []Arrival{}}
	if resp.Lines == nil {
		resp.Lines = []string{}
	}
	a.mu.RLock()
	byVeh := map[string]boardVehicle{}
	running := map[[2]int64]bool{} // trip, service day of every matched vehicle of these lines
	for _, l := range lines {
		d := a.lines[l]
		if d == nil || d.feed != f {
			continue
		}
		for i := range d.results {
			r := &d.results[i]
			if now.Sub(r.Time) > vehicleMaxAge {
				continue
			}
			if r.Matched() {
				running[[2]int64{int64(r.Trip), r.Day.Unix()}] = true
			}
			if b, ok := byVeh[r.VehicleID]; !ok || r.Time.After(b.r.Time) {
				byVeh[r.VehicleID] = boardVehicle{l, r, d.board[i]}
			}
		}
	}
	a.mu.RUnlock()

	tab := a.segs.Load()
	if tab.Feed() != worldVersion(w) {
		tab = nil
	}
	horizon := now.Add(boardHorizon).Unix()
	var rows []Arrival
	for _, b := range byVeh {
		if row, ok := liveArrival(f, tab, stop, b, now); ok && (row.ETA == nil || *row.ETA <= horizon) {
			rows = append(rows, row)
		}
	}
	// An expired timetable still matches vehicles (PlanningTime), but its times are not OASA's.
	if end := f.FeedEnd(); end.IsZero() || !gtfs.Midnight(now).After(end) {
		for _, l := range lines {
			rows = append(rows, scheduleArrivals(f, l, stop, now, running)...)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ei, ej := etaOrMax(rows[i]), etaOrMax(rows[j])
		if ei != ej {
			return ei < ej
		}
		if rows[i].Line != rows[j].Line {
			return rows[i].Line < rows[j].Line
		}
		return orEmpty(rows[i].Vehicle) < orEmpty(rows[j].Vehicle)
	})
	per := map[[2]string]int{}
	for _, row := range rows {
		k := [2]string{row.Line, orEmpty(row.Variant)}
		if per[k] < boardPerVariant {
			per[k]++
			resp.Arrivals = append(resp.Arrivals, row)
		}
	}
	return resp
}

func etaOrMax(r Arrival) int64 {
	if r.ETA == nil {
		return math.MaxInt64
	}
	return *r.ETA
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// liveArrival is the vehicle's next pass of the stop (not as the last stop of its pattern):
// from its fix, the time to drive there (boardTravel); a matched vehicle waiting at its first
// stop leaves at its scheduled departure, or now when that is past.
func liveArrival(f *gtfs.Feed, tab *segspeed.Table, stop int32, b boardVehicle, now time.Time) (Arrival, bool) {
	p, r := b.pos, b.r
	if !p.ok {
		return Arrival{}, false
	}
	t := f.Trip(p.trip)
	stops := f.Patterns[t.Pattern].Stops
	first := p.next // a stop just reached still counts: the vehicle is at it
	if first > 0 && p.along-p.g.StopAlong[first-1] <= boardAtStopM {
		first--
	}
	j := slices.Index(stops[first:], stop)
	if j < 0 || first+j == len(stops)-1 {
		return Arrival{}, false
	}
	j += first
	from := p.along
	if p.waiting {
		from = p.g.StopAlong[0]
	}
	dist := max(0, p.g.StopAlong[j]-from)
	row := Arrival{Line: b.line, Headsign: f.Headsign(t), Vehicle: ptr(r.VehicleID), PositionAt: ptr(r.Time.Unix()),
		StopsAway: ptr(j - first + 1), DistanceM: ptr(int(math.Round(dist))), Source: "live"}
	if t.Shape >= 0 {
		row.Variant = ptr(f.Shapes[t.Shape].ID)
	}
	start, timetable := r.Time, (*gtfs.Trip)(nil)
	if r.Matched() {
		row.TripID = ptr(t.ID)
		row.Scheduled = ptr(r.Day.Unix() + int64(f.StopTime(t, j).Arr))
		timetable = t
		if p.waiting {
			start = maxTime(r.Day.Add(time.Duration(f.Start(t))*time.Second), now)
		}
	} else if p.waiting {
		return row, true // departure unknown
	}
	secs := boardTravel(f, p.g, f.ShapeID(t), tab, segspeed.Slot(start, gtfs.Athens), timetable, from, p.g.StopAlong[j])
	row.ETA = ptr(start.Unix() + int64(math.Round(secs)))
	if *row.ETA < now.Unix()-boardPassedS {
		return Arrival{}, false
	}
	return row, true
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// boardTravel is the seconds to drive from `from` to `to` along g: per 200 m bin of the shape
// the typical speed where the history has one (a FromShape geometry; at least boardMinBinMS),
// elsewhere the timetable's running time over that stretch (trip, nil for none), or
// boardDefaultMS. Waits at stops and lights are in both.
func boardTravel(f *gtfs.Feed, g *geo.Geometry, shape string, tab *segspeed.Table, slot int, trip *gtfs.Trip, from, to float64) float64 {
	if !g.FromShape {
		tab = nil
	}
	if trip != nil && f.NumStops(trip) < 2 {
		trip = nil
	}
	secs := 0.0
	for x := from; x < to; {
		bin := int(math.Floor(x / segspeed.BinM))
		end := min(to, float64(bin+1)*segspeed.BinM)
		if v, ok := tab.Speed(shape, bin, slot); ok {
			secs += (end - x) / max(v, boardMinBinMS)
		} else if trip != nil {
			secs += schedAt(f, g, trip, end) - schedAt(f, g, trip, x)
		} else {
			secs += (end - x) / boardDefaultMS
		}
		x = end
	}
	return secs
}

// schedAt is the trip's scheduled time (seconds of its service day) at a point along g, between
// the stops' times as the matcher measures delay.
func schedAt(f *gtfs.Feed, g *geo.Geometry, t *gtfs.Trip, along float64) float64 {
	p := g.Progress(along)
	seg := min(int(p), f.NumStops(t)-2)
	a, b := f.StopTime(t, seg), f.StopTime(t, seg+1)
	return float64(a.Dep) + (p-float64(seg))*float64(b.Arr-a.Dep)
}

// scheduleArrivals is the line's trips that have not left their first stop yet, pass the stop
// within boardHorizon (the first pass, not as the last stop) and no matched vehicle runs.
func scheduleArrivals(f *gtfs.Feed, line string, stop int32, now time.Time, running map[[2]int64]bool) []Arrival {
	var out []Arrival
	for _, c := range f.CandidateTrips(line, now, int32(boardHorizon/time.Second), 0) {
		t := f.Trip(c.Trip)
		if running[[2]int64{int64(c.Trip), c.Day.Unix()}] || c.Day.Add(time.Duration(f.Start(t))*time.Second).Before(now) {
			continue
		}
		stops := f.Patterns[t.Pattern].Stops
		j := slices.Index(stops[:len(stops)-1], stop)
		if j < 0 {
			continue
		}
		at := c.Day.Unix() + int64(f.StopTime(t, j).Arr)
		if at > now.Add(boardHorizon).Unix() {
			continue
		}
		row := Arrival{Line: line, Headsign: f.Headsign(t), TripID: ptr(t.ID), ETA: ptr(at), Scheduled: ptr(at), Source: "schedule"}
		if t.Shape >= 0 {
			row.Variant = ptr(f.Shapes[t.Shape].ID)
		}
		out = append(out, row)
	}
	return out
}
