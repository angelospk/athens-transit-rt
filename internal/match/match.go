// Package match assigns live OASA vehicles to scheduled GTFS trips.
//
// The telematics API reports vehicles per route code with no trip identifier, so each
// vehicle is assigned to a scheduled trip by comparing where it is along the route with
// where each candidate trip should be at that moment. Positions along a trip are measured on
// the GTFS shape; trips whose shape is missing or does not fit their stops fall back to
// straight lines between consecutive stops.
//
// Port of upstream oasa_rt/matcher.py (Matcher, HungarianMatcher, MemoryMatcher).
package match

import (
	"math"
	"slices"
	"sort"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/lsa"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

const (
	MaxOffRouteM        = 250 // farther than this from the trip geometry -> not on this trip
	TerminalRadiusM     = 100 // "waiting at the first stop"
	MinDelay, MaxDelay  = -10 * 60, 60 * 60
	StickyBonus         = 180 // prefer keeping last cycle's assignment (seconds of cost)
	WrongHeadingCost    = 300 // vehicle heading opposes the direction of the route there
	Infeasible          = 1e9 // Hungarian assignment: impossible pair
	Unmatched           = 1e6 // Hungarian assignment: vehicle left unmatched
	HistoryS            = 15 * 60
	InconsistentFixCost = 3600 // a past position that does not fit the trip at all
	RunGapS             = 10 * 60
	AnchorBonus         = 1800 // departure-anchored trip; outweighs any position-based cost
	DepartedM           = 150  // this far from the first stop counts as departed
	AnchorEarlyS        = 5 * 60
	AnchorLateS         = 15 * 60
	CandidateBefore     = 15 * 60 // candidate trips: scheduled run padded before the start
	CandidateAfter      = 60 * 60 // ... and after the end
	forgetAfter         = time.Hour
)

// Kind selects the assignment method.
type Kind int

const (
	Greedy    Kind = iota // cheapest pair first (upstream's live default)
	Hungarian             // minimum total cost per line
	Memory                // Hungarian + departure anchoring + position history
)

func ParseKind(s string) (Kind, bool) {
	switch s {
	case "greedy":
		return Greedy, true
	case "hungarian":
		return Hungarian, true
	case "memory":
		return Memory, true
	}
	return 0, false
}

// Obs is one getBusLocation row tagged with the line code it was polled under.
type Obs struct {
	telematics.Vehicle
	LineCode string
}

// Result is a vehicle with its matched trip, if any.
type Result struct {
	VehicleID string
	Line      string // line number, e.g. "040"
	RouteID   string // GTFS route_id (of the trip once matched)
	RouteCode string
	LineCode  string
	Lat, Lon  float64
	Bearing   float64
	Time      time.Time // GPS fix
	Trip      int32     // -1 when unmatched
	Day       time.Time // service day of Trip
	Delay     int       // seconds, positive = late
	NextIndex int       // index in the trip's stop times of the next stop
	Waiting   bool      // waiting at the first stop
	Along     float64   // metres along the trip's Geometry line (matched only)
}

func (r *Result) Matched() bool { return r.Trip >= 0 }

type candidate struct {
	cost, rawCost float64
	vid           string
	trip          int32
	day           time.Time
	delay         int
	nextIndex     int
	waiting       bool
	progress      float64
	along         float64
	sticky        bool
}

type tripDay struct {
	trip int32
	day  int64 // unix seconds of the service day's midnight
}

type prevMatch struct {
	trip     string // upstream compares trip ids only, whatever the day
	progress float64
	seen     time.Time
}

type geoKey struct{ shape, pattern int32 }

type fix struct {
	ts      time.Time
	pos     [2]float64
	heading float64
	hasHead bool
}

type track struct {
	routeCode      string
	fixes          []fix
	lastAtTerminal time.Time // zero while no departure is pending
	anchor         *tripDay
	seen           time.Time
	positions      map[posKey][]geo.Cand
}

type posKey struct {
	ts   int64
	line *geo.Polyline
}

// Matcher is not safe for concurrent use.
type Matcher struct {
	Feed      *gtfs.Feed
	Mapper    *RouteMapper
	Kind      Kind
	UseShapes bool

	previous   map[string]prevMatch
	geometry   map[geoKey]*geo.Geometry
	shapeLines map[int32]*geo.Polyline
	tracks     map[string]*track
}

func New(feed *gtfs.Feed, mapper *RouteMapper, kind Kind) *Matcher {
	return &Matcher{Feed: feed, Mapper: mapper, Kind: kind, UseShapes: true,
		previous: map[string]prevMatch{}, geometry: map[geoKey]*geo.Geometry{},
		shapeLines: map[int32]*geo.Polyline{}, tracks: map[string]*track{}}
}

// Geometry returns the (cached) geometry of a trip's stop pattern, or nil when a stop is unknown.
func (m *Matcher) Geometry(t *gtfs.Trip) *geo.Geometry {
	k := geoKey{t.Shape, t.Pattern}
	if g, ok := m.geometry[k]; ok {
		return g
	}
	g := m.buildGeometry(t)
	m.geometry[k] = g
	return g
}

func (m *Matcher) stopXY(stop int32) [2]float64 {
	s := &m.Feed.Stops[stop]
	return geo.XY(s.Lat, s.Lon)
}

func (m *Matcher) buildGeometry(t *gtfs.Trip) *geo.Geometry {
	f := m.Feed
	pat := f.Patterns[t.Pattern].Stops
	stops := make([][2]float64, len(pat))
	for i, s := range pat {
		stops[i] = m.stopXY(s)
	}
	if m.UseShapes && t.Shape >= 0 {
		line := m.ShapeLine(t.Shape)
		if along := geo.SnapStops(line, stops); along != nil {
			return &geo.Geometry{Line: line, StopAlong: along, FromShape: true}
		}
	}
	line := geo.NewPolyline(stops)
	return &geo.Geometry{Line: line, StopAlong: slices.Clone(line.Cum), FromShape: false}
}

// ShapeLine is the (cached) projected line of a shape.
func (m *Matcher) ShapeLine(shape int32) *geo.Polyline {
	line := m.shapeLines[shape]
	if line == nil {
		pts := m.Feed.Shapes[shape].Points
		xy := make([][2]float64, len(pts))
		for i, p := range pts {
			xy[i] = geo.XY(p[0], p[1])
		}
		line = geo.NewPolyline(xy)
		m.shapeLines[shape] = line
	}
	return line
}

// routeIDFor is the GTFS route_id to report for a vehicle whose trip is unknown.
func (m *Matcher) routeIDFor(lineCode, line string) string {
	for _, ti := range m.Feed.TripsForLine(line) {
		r := &m.Feed.Routes[m.Feed.Trips[ti].Route]
		if r.ID == lineCode {
			return lineCode
		}
	}
	if trips := m.Feed.TripsForLine(line); len(trips) > 0 {
		return m.Feed.Routes[m.Feed.Trips[trips[0]].Route].ID
	}
	return lineCode
}

// MatchLine matches the vehicles of every route of one line number.
func (m *Matcher) MatchLine(line string, vehicles []Obs) []Result {
	order := []string{}
	byID := map[string]*Result{}
	for _, v := range vehicles {
		r := &Result{VehicleID: v.VehNo, Line: line, RouteID: m.routeIDFor(v.LineCode, line),
			RouteCode: v.RouteCode, LineCode: v.LineCode, Lat: v.Lat, Lon: v.Lon, Bearing: v.Heading,
			Time: v.Time, Trip: -1}
		if _, dup := byID[r.VehicleID]; !dup {
			order = append(order, r.VehicleID)
		}
		byID[r.VehicleID] = r
	}
	var cands []candidate
	for _, id := range order {
		cands = append(cands, m.candidates(line, byID[id])...)
	}
	progress := map[string]float64{}
	for _, c := range m.assign(cands) {
		r := byID[c.vid]
		r.Trip, r.Day, r.Delay, r.NextIndex, r.Waiting, r.Along = c.trip, c.day, c.delay, c.nextIndex, c.waiting, c.along
		r.RouteID = m.Feed.Routes[m.Feed.Trips[c.trip].Route].ID
		progress[c.vid] = c.progress
	}
	out := make([]Result, 0, len(order))
	for _, id := range order {
		r := byID[id]
		if r.Matched() {
			m.previous[id] = prevMatch{m.Feed.Trips[r.Trip].ID, progress[id], r.Time}
		} else {
			delete(m.previous, id)
		}
		out = append(out, *r)
	}
	return out
}

// Forget drops state of vehicles not seen for an hour (upstream keeps it forever).
func (m *Matcher) Forget(now time.Time) {
	for id, p := range m.previous {
		if now.Sub(p.seen) > forgetAfter {
			delete(m.previous, id)
		}
	}
	for id, t := range m.tracks {
		if now.Sub(t.seen) > forgetAfter {
			delete(m.tracks, id)
		}
	}
}

// baseCandidates scores every trip the vehicle could be running from its current position.
func (m *Matcher) baseCandidates(line string, r *Result) []candidate {
	f := m.Feed
	pos := geo.XY(r.Lat, r.Lon)
	shapes := m.Mapper.ShapesFor(line, r.RouteCode)
	prev, hasPrev := m.previous[r.VehicleID]
	onLine := map[*geo.Polyline][]geo.Cand{}
	var out []candidate
	for _, ct := range f.CandidateTrips(line, r.Time, CandidateBefore, CandidateAfter) {
		t := f.Trip(ct.Trip)
		if !shapes[t.Shape] {
			continue
		}
		g := m.Geometry(t)
		if g == nil {
			continue
		}
		cands, ok := onLine[g.Line]
		if !ok {
			cands = g.Line.Candidates(pos[0], pos[1], MaxOffRouteM, r.Bearing, r.Bearing != 0)
			onLine[g.Line] = cands
		}
		sticky := hasPrev && t.ID == prev.trip
		var prevProgress *float64
		if sticky {
			prevProgress = &prev.progress
		}
		s, ok := m.score(t, ct.Day, g, cands, r.Time, pos, prevProgress)
		if !ok {
			continue
		}
		cost := s.cost
		if sticky {
			cost -= StickyBonus
		}
		out = append(out, candidate{cost: cost, rawCost: s.cost, vid: r.VehicleID, trip: ct.Trip, day: ct.Day,
			delay: s.delay, nextIndex: s.nextIndex, waiting: s.waiting, progress: s.progress, along: s.along, sticky: sticky})
	}
	return out
}

type scored struct {
	cost      float64
	delay     int
	nextIndex int
	waiting   bool
	progress  float64
	along     float64
}

// score is the cheapest way the vehicle at pos at ts can be running trip t.
func (m *Matcher) score(t *gtfs.Trip, midnight time.Time, g *geo.Geometry, positions []geo.Cand,
	ts time.Time, pos [2]float64, prevProgress *float64) (scored, bool) {
	f := m.Feed
	secs := ts.Sub(midnight).Seconds()
	n := f.NumStops(t)
	start := float64(f.Start(t))
	var best scored
	found := false
	for _, c := range positions {
		progress := g.Progress(c.Along)
		if prevProgress != nil && progress < *prevProgress-1 {
			continue // buses do not drive backwards along their route
		}
		seg := int(progress)
		if seg >= n-1 {
			seg = n - 2
		}
		frac := progress - float64(seg)
		stop, next := f.StopTime(t, seg), f.StopTime(t, seg+1)
		waiting := seg == 0 && secs < start && geo.Dist(pos, m.stopXY(stop.Stop)) <= TerminalRadiusM
		var delay, cost float64
		if waiting {
			delay, cost = 0, (start-secs)*0.5
		} else {
			delay = secs - (float64(stop.Dep) + frac*float64(next.Arr-stop.Dep))
			if delay < MinDelay || delay > MaxDelay {
				continue
			}
			// Buses run late far more often than early.
			if delay >= 0 {
				cost = delay
			} else {
				cost = -3 * delay
			}
		}
		if c.Wrong {
			cost += WrongHeadingCost
		}
		if !found || cost < best.cost {
			best = scored{cost, int(math.RoundToEven(delay)), seg + 1, waiting, progress, c.Along}
			found = true
		}
	}
	return best, found
}

func (m *Matcher) candidates(line string, r *Result) []candidate {
	base := m.baseCandidates(line, r)
	if m.Kind != Memory {
		return base
	}
	tr := m.updateTrack(r, base)
	if len(base) == 0 {
		return base
	}
	out := make([]candidate, 0, len(base))
	for _, c := range base {
		t := m.Feed.Trip(c.trip)
		g := m.Geometry(t)
		costs := []float64{c.rawCost}
		for _, fx := range tr.fixes[:len(tr.fixes)-1] {
			k := posKey{fx.ts.Unix(), g.Line}
			cands, ok := tr.positions[k]
			if !ok {
				cands = g.Line.Candidates(fx.pos[0], fx.pos[1], MaxOffRouteM, fx.heading, fx.hasHead)
				tr.positions[k] = cands
			}
			if s, ok := m.score(t, c.day, g, cands, fx.ts, fx.pos, nil); ok {
				costs = append(costs, s.cost)
			} else {
				costs = append(costs, InconsistentFixCost)
			}
		}
		cost := median(costs)
		if c.sticky {
			cost -= StickyBonus
		}
		if tr.anchor != nil && *tr.anchor == (tripDay{c.trip, c.day.Unix()}) {
			cost -= AnchorBonus
		}
		c.cost = cost
		out = append(out, c)
	}
	return out
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// updateTrack keeps the vehicle's recent positions for this run and detects its departure.
func (m *Matcher) updateTrack(r *Result, base []candidate) *track {
	tr := m.tracks[r.VehicleID]
	pos := geo.XY(r.Lat, r.Lon)
	if tr == nil || tr.routeCode != r.RouteCode ||
		(len(tr.fixes) > 0 && r.Time.Sub(tr.fixes[len(tr.fixes)-1].ts).Seconds() > RunGapS) {
		tr = &track{routeCode: r.RouteCode, positions: map[posKey][]geo.Cand{}}
		m.tracks[r.VehicleID] = tr
	}
	tr.seen = r.Time
	if len(tr.fixes) == 0 || !tr.fixes[len(tr.fixes)-1].ts.Equal(r.Time) {
		tr.fixes = append(tr.fixes, fix{r.Time, pos, r.Bearing, r.Bearing != 0})
	}
	kept := tr.fixes[:0]
	for _, fx := range tr.fixes {
		if r.Time.Sub(fx.ts).Seconds() <= HistoryS {
			kept = append(kept, fx)
		}
	}
	tr.fixes = kept

	nearest, haveNear := math.Inf(1), false
	seen := map[int32]bool{}
	for _, c := range base {
		first := m.Feed.StopTime(m.Feed.Trip(c.trip), 0).Stop
		if seen[first] {
			continue
		}
		seen[first] = true
		nearest, haveNear = min(nearest, geo.Dist(pos, m.stopXY(first))), true
	}
	switch {
	case haveNear && nearest <= TerminalRadiusM:
		// At the first stop: a new run is about to start, so forget the previous one.
		tr.lastAtTerminal, tr.anchor = r.Time, nil
		tr.fixes = tr.fixes[len(tr.fixes)-1:]
	case !tr.lastAtTerminal.IsZero() && haveNear && nearest > DepartedM:
		departure := tr.lastAtTerminal.Add(r.Time.Sub(tr.lastAtTerminal) / 2)
		tr.anchor = m.anchorTrip(base, departure)
		tr.lastAtTerminal = time.Time{}
	}
	m.prunePositions(tr)
	return tr
}

// prunePositions drops cached projections of fixes that left the history window
// (upstream's cache grows without bound).
func (m *Matcher) prunePositions(tr *track) {
	if len(tr.fixes) == 0 {
		clear(tr.positions)
		return
	}
	oldest := tr.fixes[0].ts.Unix()
	for k := range tr.positions {
		if k.ts < oldest {
			delete(tr.positions, k)
		}
	}
}

// anchorTrip is the trip scheduled to leave closest to the observed departure (late is likelier).
func (m *Matcher) anchorTrip(base []candidate, departure time.Time) *tripDay {
	var best *tripDay
	bestScore := 0.0
	for _, c := range base {
		late := departure.Sub(c.day).Seconds() - float64(m.Feed.Start(m.Feed.Trip(c.trip)))
		if late < -AnchorEarlyS || late > AnchorLateS {
			continue
		}
		score := late
		if late < 0 {
			score = -3 * late
		}
		if best == nil || score < bestScore {
			best, bestScore = &tripDay{c.trip, c.day.Unix()}, score
		}
	}
	return best
}

// Anchored reports whether the vehicle's current match comes from an observed departure.
func (m *Matcher) Anchored(r *Result) bool {
	tr := m.tracks[r.VehicleID]
	return r.Matched() && tr != nil && tr.anchor != nil && *tr.anchor == (tripDay{r.Trip, r.Day.Unix()})
}

func (m *Matcher) assign(cands []candidate) []candidate {
	if m.Kind == Greedy {
		return m.assignGreedy(cands)
	}
	return m.assignHungarian(cands)
}

// assignGreedy: one-to-one, cheapest pair first.
func (m *Matcher) assignGreedy(cands []candidate) []candidate {
	sorted := slices.Clone(cands)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].cost < sorted[j].cost })
	var out []candidate
	usedTrips, usedVeh := map[tripDay]bool{}, map[string]bool{}
	for _, c := range sorted {
		k := tripDay{c.trip, c.day.Unix()}
		if usedVeh[c.vid] || usedTrips[k] {
			continue
		}
		usedTrips[k], usedVeh[c.vid] = true, true
		out = append(out, c)
	}
	return out
}

// assignHungarian minimises the total cost of a line at once, so one vehicle cannot take the
// only trip another vehicle could be running.
func (m *Matcher) assignHungarian(cands []candidate) []candidate {
	if len(cands) == 0 {
		return nil
	}
	vehSet, tripSet := map[string]bool{}, map[tripDay]bool{}
	for _, c := range cands {
		vehSet[c.vid] = true
		tripSet[tripDay{c.trip, c.day.Unix()}] = true
	}
	vehicles := make([]string, 0, len(vehSet))
	for v := range vehSet {
		vehicles = append(vehicles, v)
	}
	sort.Strings(vehicles)
	trips := make([]tripDay, 0, len(tripSet))
	for t := range tripSet {
		trips = append(trips, t)
	}
	// Upstream sorts (trip_id, service_date) tuples.
	sort.Slice(trips, func(i, j int) bool {
		a, b := m.Feed.Trips[trips[i].trip].ID, m.Feed.Trips[trips[j].trip].ID
		if a != b {
			return a < b
		}
		return trips[i].day < trips[j].day
	})
	vi, ti := map[string]int{}, map[tripDay]int{}
	for i, v := range vehicles {
		vi[v] = i
	}
	for j, t := range trips {
		ti[t] = j
	}
	n, k := len(vehicles), len(trips)
	// One column per trip plus one "unmatched" column per vehicle. Unmatched dwarfs every real
	// cost, so as many vehicles as possible are matched, then the total cost is minimised.
	cost := make([][]float64, n)
	for i := range cost {
		cost[i] = make([]float64, k+n)
		for j := range cost[i] {
			cost[i][j] = Infeasible
		}
		cost[i][k+i] = Unmatched
	}
	byCell := map[[2]int]candidate{}
	for _, c := range cands {
		i, j := vi[c.vid], ti[tripDay{c.trip, c.day.Unix()}]
		if c.cost < cost[i][j] {
			cost[i][j] = c.cost
			byCell[[2]int{i, j}] = c
		}
	}
	rows, cols, err := lsa.Solve(cost)
	if err != nil {
		return m.assignGreedy(cands) // cannot happen with finite costs; stay useful anyway
	}
	var out []candidate
	for x := range rows {
		if c, ok := byCell[[2]int{rows[x], cols[x]}]; ok {
			out = append(out, c)
		}
	}
	return out
}
