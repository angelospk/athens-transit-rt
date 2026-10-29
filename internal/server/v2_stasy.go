package server

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/model"
	"github.com/angelospk/athens-transit-rt/internal/rail"
	"github.com/angelospk/athens-transit-rt/internal/railgeo"
)

// The stasy adapter: metro (M1-M3) and tram (T6, T7) from the STASY timetable. There is no live
// signal: Arrivals have source timetable, Vehicles are estimates placed on the OSM track with
// knots. A Stop is every platform of one folded name and one mode ("stasy:συνταγμα-metro").

const (
	stasyNS        = "stasy"
	stasyPrefix    = stasyNS + ":"
	stasyAhead     = 3  // stops in path + path_beyond of an estimate: the next one and two more
	stasyVehMaxAge = 15 // knots move the trains between snapshots
)

type stasyAdapter struct {
	a *App

	mu  sync.Mutex
	idx *stasyIndex // of the timetable it was built from
}

// stasyIndex is the model of one timetable: built once per *rail.STASY.
type stasyIndex struct {
	f        *rail.STASY
	lines    []model.Line // by route index
	modes    []string     // by route index
	stopOf   []int        // platform (STASY stop index) -> Stop number, -1 when no trip calls
	stops    []stasyStop
	byID     map[string]int
	patterns [][]stasyPattern // by route index, most trips first
	tripPat  []int32          // trip -> pattern number within its route
	unnamed  []string         // Stop ids the name table lacks (they keep the STASY name)
	// joined are the calls of the trips that stop twice in a row at one Stop (at a point merged
	// into it), as one call: see tripStops.
	joined map[int32][]rail.STASYStopTime
}

type stasyStop struct {
	m         model.Stop
	platforms []int32
	merged    []int32 // platforms of points merged into the Stop (aliases in the name table)
}

type stasyPattern struct {
	m     model.Pattern
	track *railgeo.Track // Along: one per stop of m.Stops
}

func (s *stasyAdapter) Namespace() string { return stasyNS }

// stasyKey is the id of a stop name and mode: folded words joined by '-'.
func stasyKey(name, mode string) string {
	// '-' splits words too: the key must stay the same through Resolve, which reads '-' as a space.
	words := strings.FieldsFunc(rail.Fold(name), func(r rune) bool { return r == ' ' || r == '/' || r == '-' })
	return stasyPrefix + strings.Join(words, "-") + "-" + mode
}

func (s *stasyAdapter) Resolve(kind Kind, id string) string {
	native := strings.TrimPrefix(id, stasyPrefix)
	switch kind {
	case KindLine:
		return stasyPrefix + strings.ToUpper(native)
	case KindStop:
		if i := strings.LastIndexByte(native, '-'); i > 0 {
			key := stasyKey(strings.ReplaceAll(native[:i], "-", " "), native[i+1:])
			if to, ok := railgeo.STASYAlias(key); ok {
				return to // a renamed Stop answers under its current id
			}
			return key
		}
	}
	return id
}

func (s *stasyAdapter) index() (*stasyIndex, error) {
	f, ok := s.a.stasy.Feed()
	if !ok {
		return nil, errWarming
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx == nil || s.idx.f != f {
		s.idx = buildSTASYIndex(f)
		if len(s.idx.unnamed) > 0 {
			// A new or renamed Stop: it shows STASY's capitals and no English name until
			// scripts/stasy-stop-names.py adds it (and, for a rename, an alias).
			s.a.log.Warn("stasy: stops not in the name table", "ids", s.idx.unnamed)
		}
	}
	return s.idx, nil
}

func stasyMode(r rail.STASYRoute) string {
	if r.Type == 0 || r.Type < 0 && strings.HasPrefix(r.Short, "T") {
		return model.Tram
	}
	return model.Metro
}

func buildSTASYIndex(f *rail.STASY) *stasyIndex {
	x := &stasyIndex{f: f, byID: map[string]int{}, stopOf: make([]int, len(f.Stops)),
		patterns: make([][]stasyPattern, len(f.Routes)), tripPat: make([]int32, f.NumTrips())}
	for i, r := range f.Routes {
		mode := stasyMode(r)
		name := r.Long
		if name == "" {
			name = "Γραμμή " + strings.TrimLeft(r.Short, "MT")
		}
		x.modes = append(x.modes, mode)
		x.lines = append(x.lines, model.Line{ID: stasyPrefix + r.Short, Mode: mode, Label: model.Ptr(r.Short), Name: name,
			Color: f.RouteColor(int32(i)), TextColor: "#ffffff", Source: stasyNS})
	}
	// The mode and Lines of every platform, from the trips calling there.
	platLines := make([]map[int32]bool, len(f.Stops))
	for ti := range f.NumTrips() {
		_, route, _ := f.TripInfo(int32(ti))
		for _, st := range f.TripStops(int32(ti)) {
			if platLines[st.Stop] == nil {
				platLines[st.Stop] = map[int32]bool{}
			}
			platLines[st.Stop][route] = true
		}
	}
	for p := range f.Stops {
		x.stopOf[p] = -1
		if len(platLines[p]) == 0 {
			continue
		}
		var mode string
		for r := range platLines[p] {
			mode = x.modes[r] // one mode per platform
		}
		id := stasyKey(f.Stops[p].Name, mode)
		// An alias of an id still in the feed is a point merged into a Stop (ΣΥΝΔΕΣΗ
		// ΠΡΟΑΣΤΙΑΚΟΥ into Δουκίσσης Πλακεντίας); a renamed Stop's old id is no longer in it.
		to, merged := railgeo.STASYAlias(id)
		if merged {
			id = to
		}
		k, ok := x.byID[id]
		if !ok {
			k = len(x.stops)
			x.byID[id] = k
			st := model.Stop{ID: id, Name: f.Stops[p].Name, Modes: []string{mode}, Source: stasyNS}
			if n, ok := railgeo.STASYStopName(id); ok {
				st.Name, st.NameEn = n.Name, n.NameEn
			} else {
				x.unnamed = append(x.unnamed, id)
			}
			x.stops = append(x.stops, stasyStop{m: st})
		}
		x.stopOf[p] = k
		st := &x.stops[k]
		if merged {
			st.merged = append(st.merged, int32(p))
		} else {
			st.platforms = append(st.platforms, int32(p))
		}
		for r := range platLines[p] {
			if l := x.lines[r].ID; !contains(st.m.Lines, l) {
				st.m.Lines = append(st.m.Lines, l)
			}
		}
	}
	for k := range x.stops {
		st := &x.stops[k]
		// At its own platforms: a merged point does not move the Stop (unless it is all there is).
		plats := st.platforms
		if len(plats) == 0 {
			plats = st.merged
		}
		var lat, lon float64
		for _, p := range plats {
			lat, lon = lat+f.Stops[p].Lat, lon+f.Stops[p].Lon
		}
		n := float64(len(plats))
		st.m.Lat, st.m.Lon = round5(lat/n), round5(lon/n)
		sortLines(st.m.Lines, func(l string) (string, string) { return x.lines[x.route(l)].Mode, l })
	}
	x.joinCalls()
	x.buildPatterns()
	return x
}

// joinCalls fills joined: a trip calling at a Stop and then at a point merged into it calls
// there once, arriving at the first and leaving from the second.
func (x *stasyIndex) joinCalls() {
	for ti := range int32(x.f.NumTrips()) {
		rows := x.f.TripStops(ti)
		var out []rail.STASYStopTime
		for k, r := range rows {
			if k > 0 && x.stopOf[r.Stop] == x.stopOf[rows[k-1].Stop] {
				if out == nil {
					out = append(out, rows[:k]...)
				}
				out[len(out)-1].T = r.T
			} else if out != nil {
				out = append(out, r)
			}
		}
		if out != nil {
			if x.joined == nil {
				x.joined = map[int32][]rail.STASYStopTime{}
			}
			x.joined[ti] = out
		}
	}
}

// tripStops are a trip's calls, one per Stop it passes: what Patterns, Vehicles and Runs count.
func (x *stasyIndex) tripStops(ti int32) []rail.STASYStopTime {
	if rows, ok := x.joined[ti]; ok {
		return rows
	}
	return x.f.TripStops(ti)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// route is the route index of a Line id, -1 when none.
func (x *stasyIndex) route(id string) int {
	for i, l := range x.lines {
		if l.ID == id {
			return i
		}
	}
	return -1
}

// buildPatterns groups each route's trips by their sequence of Stops: one Pattern per distinct
// sequence, the most trips first ("stasy:M2-0"), each with the track cut to its stops.
func (x *stasyIndex) buildPatterns() {
	f := x.f
	type acc struct {
		route int32
		stops []int
		n     int
		heads map[string]int
		trips []int32
	}
	byKey := map[string]*acc{}
	var order []string
	for ti := range f.NumTrips() {
		_, route, head := f.TripInfo(int32(ti))
		rows := x.tripStops(int32(ti))
		if len(rows) < 2 {
			x.tripPat[ti] = -1
			continue
		}
		var b strings.Builder
		b.WriteString(strconv.Itoa(int(route)))
		stops := make([]int, len(rows))
		for k, r := range rows {
			stops[k] = x.stopOf[r.Stop]
			b.WriteByte(',')
			b.WriteString(strconv.Itoa(stops[k]))
		}
		key := b.String()
		c := byKey[key]
		if c == nil {
			c = &acc{route: route, stops: stops, heads: map[string]int{}}
			byKey[key] = c
			order = append(order, key)
		}
		c.n++
		c.heads[head]++
		c.trips = append(c.trips, int32(ti))
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := byKey[order[i]], byKey[order[j]]
		if a.route != b.route {
			return a.route < b.route
		}
		return a.n > b.n
	})
	for _, key := range order {
		c := byKey[key]
		r := c.route
		num := len(x.patterns[r])
		head, best := "", -1
		for h, n := range c.heads {
			if n > best || n == best && h < head {
				head, best = h, n
			}
		}
		p := stasyPattern{m: model.Pattern{ID: stasyPrefix + f.Routes[r].Short + "-" + strconv.Itoa(num), Headsign: head,
			Stops: make([]string, len(c.stops))}}
		pts := make([][2]float64, len(c.stops))
		for k, s := range c.stops {
			p.m.Stops[k] = x.stops[s].m.ID
			pts[k] = [2]float64{x.stops[s].m.Lat, x.stops[s].m.Lon}
		}
		p.track = stasyTrack(f.Routes[r].Short, pts)
		p.m.Shape = p.track.Shape()
		if num > 0 { // the direction of the most common pattern is 0
			main := x.patterns[r][0].m.Stops
			i, j := indexOf(main, p.m.Stops[0]), indexOf(main, p.m.Stops[len(p.m.Stops)-1])
			if i >= 0 && j >= 0 && i > j {
				p.m.Direction = 1
			}
		}
		for _, ti := range c.trips {
			x.tripPat[ti] = int32(num)
		}
		x.patterns[r] = append(x.patterns[r], p)
	}
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

// stasyTrack is the OSM track of the line through the stops, or straight lines between them
// when no track fits (a stop farther than 150 m from it).
func stasyTrack(line string, stops [][2]float64) *railgeo.Track {
	if tr, ok := railgeo.Fit(railgeo.MetroPaths(line), stops); ok {
		return tr
	}
	xy := make([][2]float64, len(stops))
	for i, s := range stops {
		xy[i] = geo.XY(s[0], s[1])
	}
	pl := geo.NewPolyline(xy)
	return &railgeo.Track{Line: pl, Along: append([]float64(nil), pl.Cum...)}
}

// State: warming up until a timetable loads, indicative when it does not cover today.
func (s *stasyAdapter) State(now time.Time) model.SourceState {
	out := model.SourceState{ID: stasyNS, State: model.WarmingUp}
	f, ok := s.a.stasy.Feed()
	if !ok {
		return out
	}
	out.UpdatedAt = model.Ptr(s.a.stasy.Updated().Unix())
	out.State = model.OK
	if f.Indicative(noonOf(now)) {
		out.State = model.Indicative
	}
	return out
}

func noonOf(t time.Time) time.Time {
	l := t.In(gtfs.Athens)
	return time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, gtfs.Athens)
}

func (s *stasyAdapter) Lines() ([]model.Line, error) {
	x, err := s.index()
	if err != nil {
		return nil, err
	}
	return append([]model.Line(nil), x.lines...), nil
}

func (s *stasyAdapter) LineInfo(id string) (*model.Line, error) {
	x, err := s.index()
	if err != nil {
		return nil, err
	}
	r := x.route(id)
	if r < 0 {
		return nil, errUnknown
	}
	l := x.lines[r]
	return &l, nil
}

func (s *stasyAdapter) Line(id string) (*LineDetail, error) {
	x, err := s.index()
	if err != nil {
		return nil, err
	}
	r := x.route(id)
	if r < 0 {
		return nil, errUnknown
	}
	d := &LineDetail{Line: x.lines[r], Patterns: []model.Pattern{}, Stops: map[string]model.Stop{}}
	for _, p := range x.patterns[r] {
		d.Patterns = append(d.Patterns, p.m)
		for _, id := range p.m.Stops {
			d.Stops[id] = x.stops[x.byID[id]].m
		}
	}
	return d, nil
}

func (s *stasyAdapter) Stops() ([]model.Stop, error) {
	x, err := s.index()
	if err != nil {
		return nil, err
	}
	out := make([]model.Stop, len(x.stops))
	for i := range x.stops {
		out[i] = x.stops[i].m
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *stasyAdapter) stop(id string) (*stasyIndex, *stasyStop, error) {
	x, err := s.index()
	if err != nil {
		return nil, nil, err
	}
	k, ok := x.byID[id]
	if !ok {
		return nil, nil, errUnknown
	}
	return x, &x.stops[k], nil
}

func (s *stasyAdapter) Stop(id string) (*model.Stop, error) {
	_, st, err := s.stop(id)
	if err != nil {
		return nil, err
	}
	m := st.m
	return &m, nil
}

// stasyDay is a service day whose trips may run now: its noon (the run id's date) and the
// reference its stop times count from.
type stasyDay struct {
	noon, midnight time.Time
	minSecs        int32 // yesterday: only what runs past midnight
}

func stasyDays(now time.Time) [2]stasyDay {
	today := noonOf(now)
	y := today.AddDate(0, 0, -1)
	return [2]stasyDay{{today, gtfs.ServiceDay(today), 0}, {y, gtfs.ServiceDay(y), 24 * 3600}}
}

// Arrivals are the departures from the Stop's platforms in the next 2 h, from the timetable;
// a trip's last stop is left out (nobody boards there). A Run under way names its Vehicle.
func (s *stasyAdapter) Arrivals(_ context.Context, stopID string, now time.Time) ([]model.Arrival, error) {
	x, st, err := s.stop(stopID)
	if err != nil {
		return nil, err
	}
	f := x.f
	stop := x.byID[st.m.ID]
	plats := append(append([]int32(nil), st.platforms...), st.merged...)
	until := now.Add(v2Horizon).Unix()
	var out []model.Arrival
	// Tomorrow too: before midnight the 2 h window can reach its first trips.
	tm := noonOf(now).AddDate(0, 0, 1)
	two := stasyDays(now)
	days := append(two[:], stasyDay{tm, gtfs.ServiceDay(tm), 0})
	for _, d := range days {
		secs := int32(now.Sub(d.midnight) / time.Second)
		for _, p := range plats {
			for _, row := range f.StopRows(p) {
				dep, ok := x.departure(row, p, stop)
				if !ok {
					continue // the other call of a joined pair
				}
				at := d.midnight.Unix() + int64(dep)
				if dep < d.minSecs || dep < secs || at > until {
					continue
				}
				rows := x.tripStops(row.Trip)
				if last := rows[len(rows)-1]; x.stopOf[last.Stop] == stop && last.T == dep {
					continue
				}
				if runs, _ := f.ServiceOn(row.Trip, d.noon); !runs {
					continue
				}
				id, route, head := f.TripInfo(row.Trip)
				run := model.RunID(stasyNS, id, d.midnight, gtfs.Athens)
				ar := model.Arrival{Line: x.lines[route].ID, Stop: stopID, Headsign: head, At: at, ScheduledAt: model.Ptr(at),
					Source: model.SourceTable, Run: model.Ptr(run), Status: model.Scheduled}
				if rows[0].T <= secs {
					ar.Vehicle = model.Ptr(run)
				}
				out = append(out, ar)
			}
		}
	}
	return cutArrivals(out), nil
}

// departure is when the trip of row leaves the Stop from platform p: the row's own time, or
// for a call joined with a merged point's (joinCalls) the joined call's, once: ok is false for
// the call of the pair whose platform the joined call does not keep.
func (x *stasyIndex) departure(row rail.STASYRow, p int32, stop int) (int32, bool) {
	rows, ok := x.joined[row.Trip]
	if !ok {
		return row.Dep, true
	}
	for _, c := range rows {
		if x.stopOf[c.Stop] == stop && c.A <= row.Dep && row.Dep <= c.T {
			return c.T, c.Stop == p
		}
	}
	return row.Dep, true
}

// cutArrivals sorts Arrivals soonest first and keeps 4 per Line + headsign, 30 in all.
func cutArrivals(rows []model.Arrival) []model.Arrival {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].At != rows[j].At {
			return rows[i].At < rows[j].At
		}
		return rows[i].Line < rows[j].Line
	})
	out := []model.Arrival{}
	per := map[[2]string]int{}
	for _, r := range rows {
		k := [2]string{r.Line, r.Headsign}
		if per[k] >= v2PerHeadsign {
			continue
		}
		per[k]++
		if out = append(out, r); len(out) == v2MaxArrivals {
			break
		}
	}
	return out
}

// Vehicles are the trains running now by the timetable, placed along their track: between the
// stop they left (at its departure) and the next one (at its arrival), or standing at a stop
// from its arrival to its departure. Sorted by line and id.
func (s *stasyAdapter) Vehicles(now time.Time) ([]model.Vehicle, error) {
	x, err := s.index()
	if err != nil {
		return nil, err
	}
	return x.vehicles(now, -1), nil
}

func (x *stasyIndex) vehicles(now time.Time, onlyRoute int) []model.Vehicle {
	f := x.f
	out := []model.Vehicle{}
	for _, d := range stasyDays(now) {
		secs := int32(now.Sub(d.midnight) / time.Second)
		for ti := range int32(f.NumTrips()) {
			rows := x.tripStops(ti)
			if len(rows) < 2 || secs < rows[0].T || secs >= rows[len(rows)-1].A || x.tripPat[ti] < 0 {
				continue
			}
			id, route, head := f.TripInfo(ti)
			if onlyRoute >= 0 && int(route) != onlyRoute {
				continue
			}
			if runs, _ := f.ServiceOn(ti, d.noon); !runs {
				continue
			}
			p := &x.patterns[route][x.tripPat[ti]]
			out = append(out, x.vehicle(p, rows, d.midnight, secs, now, id, int(route), head))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// vehicle places one running trip: j is the first stop it has not reached.
func (x *stasyIndex) vehicle(p *stasyPattern, rows []rail.STASYStopTime, midnight time.Time, secs int32, now time.Time,
	trip string, route int, head string) model.Vehicle {
	tr := p.track
	j := sort.Search(len(rows), func(k int) bool { return k > 0 && rows[k].A > secs })
	prev := rows[j-1]
	pos := tr.Along[j-1]
	dwell := secs < prev.T
	if !dwell {
		if span := rows[j].A - prev.T; span > 0 {
			pos += float64(secs-prev.T) / float64(span) * (tr.Along[j] - tr.Along[j-1])
		}
	}
	run := model.RunID(stasyNS, trip, midnight, gtfs.Athens)
	lat, lon := geo.LatLon(tr.Line.At(pos))
	v := model.Vehicle{ID: run, Line: x.lines[route].ID, Run: model.Ptr(run), Mode: x.modes[route], Headsign: model.Ptr(head),
		Position: model.Position{Kind: model.Estimate, Lat: round5(lat), Lon: round5(lon), Bearing: trackBearing(tr.Line, pos),
			At: now.Unix()},
		NextStop: model.Ptr(p.m.Stops[j]), Source: stasyNS}
	last := min(j+stasyAhead-1, len(rows)-1)
	m := &model.Motion{Path: latLons(tr.Line.Slice(pos, tr.Along[j])), PathBeyond: [][2]float64{},
		Knots: [][2]int64{{0, now.Unix()}}}
	if last > j {
		m.PathBeyond = latLons(tr.Line.Slice(tr.Along[j], tr.Along[last]))
	}
	knot := func(metres float64, secs int32) {
		k := [2]int64{int64(math.Round(metres)), midnight.Unix() + int64(secs)}
		if prevK := m.Knots[len(m.Knots)-1]; k[1] > prevK[1] && k[0] >= prevK[0] {
			m.Knots = append(m.Knots, k)
		}
	}
	if dwell {
		knot(0, prev.T)
	}
	for k := j; k <= last; k++ {
		at := tr.Along[k] - pos
		m.PathStops = append(m.PathStops, int(math.Round(at)))
		knot(at, rows[k].A)
		if k < len(rows)-1 {
			knot(at, rows[k].T)
		}
	}
	v.Motion = m
	return v
}

func latLons(xy [][2]float64) [][2]float64 {
	out := make([][2]float64, len(xy))
	for i, p := range xy {
		lat, lon := geo.LatLon(p)
		out[i] = [2]float64{round5(lat), round5(lon)}
	}
	return out
}

// trackBearing is the compass bearing of the track at along, nil on a track of no length.
func trackBearing(line *geo.Polyline, along float64) *float64 {
	a, b := along, along+10
	if b > line.Length() {
		a, b = along-10, along
	}
	p, q := line.At(a), line.At(b)
	if p == q {
		return nil
	}
	deg := math.Atan2(q[0]-p[0], q[1]-p[1]) * 180 / math.Pi
	return model.Ptr(math.Round(math.Mod(deg+360, 360)))
}

func (s *stasyAdapter) LineVehicles(id string, now time.Time) (*LineVehicles, error) {
	x, err := s.index()
	if err != nil {
		return nil, err
	}
	r := x.route(id)
	if r < 0 {
		return nil, errUnknown
	}
	return &LineVehicles{Vehicles: x.vehicles(now, r), UpdatedAt: now.Unix(), NextUpdateAt: now.Unix() + stasyVehMaxAge,
		MaxAge: stasyVehMaxAge}, nil
}

// Run is a trip of yesterday, today or tomorrow that runs that day, from the timetable: the
// departure at each stop (the arrival at the last), passed once that time is past. Its Vehicle
// is the Run itself while it is under way.
func (s *stasyAdapter) Run(id string, now time.Time) (*model.Run, error) {
	x, err := s.index()
	if err != nil {
		return nil, err
	}
	f := x.f
	native, noon, ok := model.ParseRunID(stasyNS, id, gtfs.Athens)
	if !ok {
		return nil, errUnknown
	}
	ti, ok := f.TripIndex(native)
	if !ok || x.tripPat[ti] < 0 {
		return nil, errUnknown
	}
	if d := noon.Sub(noonOf(now)); d > oasaRunDayLeew || d < -oasaRunDayLeew {
		return nil, errUnknown
	}
	if runs, _ := f.ServiceOn(ti, noon); !runs {
		return nil, errUnknown
	}
	_, route, head := f.TripInfo(ti)
	p := &x.patterns[route][x.tripPat[ti]]
	midnight := gtfs.ServiceDay(noon)
	rows := x.tripStops(ti)
	run := &model.Run{ID: id, Line: x.lines[route].ID, Headsign: head, Pattern: model.Ptr(p.m.ID), Status: model.Scheduled,
		Stops: make([]model.RunStop, len(rows))}
	if start, end := midnight.Unix()+int64(rows[0].T), midnight.Unix()+int64(rows[len(rows)-1].A); start <= now.Unix() && now.Unix() < end {
		run.Vehicle = model.Ptr(id)
	}
	for k, r := range rows {
		secs := r.T
		if k == len(rows)-1 {
			secs = r.A
		}
		at := midnight.Unix() + int64(secs)
		run.Stops[k] = model.RunStop{Stop: p.m.Stops[k], ScheduledAt: model.Ptr(at), At: at, Source: model.SourceTable,
			Passed: at <= now.Unix()}
	}
	return run, nil
}
