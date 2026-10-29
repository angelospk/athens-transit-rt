package server

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/model"
	"github.com/angelospk/athens-transit-rt/internal/rail"
	"github.com/angelospk/athens-transit-rt/internal/railgeo"
)

// The proastiakos adapter: the three suburban corridors of Athens. Lines, Stops and Patterns
// come from the embedded corridor table (always available); Arrivals and Runs from the day's
// railway.gov.gr board with the train-stream's delays; Vehicles from the train-stream (GPS),
// snapped to the corridor track. ose.mpass is not used here (DECISIONS-phase2.md).

const (
	subNS          = "proastiakos"
	subPrefix      = subNS + ":"
	subBoardFresh  = 10 * time.Minute // a board delay counts as live this long
	subBoardStale  = 30 * time.Minute
	subStreamStale = 2 * time.Minute
	subVehMaxAge   = 5   // the stream sends positions about once a second
	subSnapM       = 300 // a train farther than this from its corridor track is not placed on it
)

// subNameEn names the corridors in English; the corridor table has Greek names only.
var subNameEn = map[string]string{
	"CHALKIDA": "Athens – Chalkida",
	"KIATO":    "Piraeus – Kiato",
	"PIRAIR":   "Piraeus – Airport",
}

// subAliases are the other railway.gov.gr ids of stations of the corridor table
// (corridor-stations, 2026-10-29). They stay valid even when the station table cannot load.
var subAliases = map[string]string{
	"6928c3c150ab5bc72b39cec7": "6928c3c150ab5bc72b39ced8", // Αεροδρόμιο
	"6928c3c150ab5bc72b39cfeb": "6928c3c150ab5bc72b39cf4f", // Άγιος Γεώργιος
}

// subBoardSource is the part of *rail.Suburban the adapter reads.
type subBoardSource interface {
	Stations(ctx context.Context) ([]rail.Station, bool)
	Board(ctx context.Context, now time.Time) ([]rail.SuburbanSchedule, time.Time, bool)
	CachedBoard(now time.Time) ([]rail.SuburbanSchedule, time.Time, bool)
	BoardFailing() bool
	StationPrimary(id string) (string, bool)
}

// subTrainSource is the part of *rail.TrainStream the adapter reads. Vehicles opens the stream.
type subTrainSource interface {
	Vehicles() *rail.SuburbanVehicles
	Peek() *rail.SuburbanVehicles
	Health() (open bool, last time.Time)
}

type proastiakosAdapter struct {
	a      *App
	board  subBoardSource
	trains subTrainSource

	once sync.Once
	x    *subIndex
}

// subIndex is the model of the embedded corridor table, built once.
type subIndex struct {
	corrs    []railgeo.Corridor
	lines    []model.Line // by corridor
	lineOf   map[string]int
	stops    []model.Stop
	byID     map[string]int      // native station id -> stop
	tracks   [][2]*railgeo.Track // by corridor and direction; Along per station of the pattern
	patterns [][2]model.Pattern
}

func (s *proastiakosAdapter) Namespace() string { return subNS }

func (s *proastiakosAdapter) index() *subIndex {
	s.once.Do(func() { s.x = buildSubIndex() })
	return s.x
}

func buildSubIndex() *subIndex {
	corrs, stations := railgeo.Suburban()
	x := &subIndex{corrs: corrs, lineOf: map[string]int{}, byID: map[string]int{},
		tracks: make([][2]*railgeo.Track, len(corrs)), patterns: make([][2]model.Pattern, len(corrs))}
	for i, st := range stations {
		m := model.Stop{ID: subPrefix + st.ID, Name: st.Name, Lat: st.Lat, Lon: st.Lon, Modes: []string{model.Suburban},
			Lines: []string{}, Source: subNS}
		if st.En != "" {
			m.NameEn = model.Ptr(st.En)
		}
		for _, c := range st.Corridors {
			if !contains(m.Lines, subPrefix+c) {
				m.Lines = append(m.Lines, subPrefix+c)
			}
		}
		sort.Strings(m.Lines)
		x.stops = append(x.stops, m)
		x.byID[st.ID] = i
	}
	for i, c := range corrs {
		l := model.Line{ID: subPrefix + c.ID, Mode: model.Suburban, Name: c.Name, Color: c.Color, TextColor: "#ffffff",
			Source: subNS}
		if en, ok := subNameEn[c.ID]; ok {
			l.NameEn = model.Ptr(en)
		}
		x.lines = append(x.lines, l)
		x.lineOf[c.ID] = i
		for dir := range 2 {
			ids := append([]string(nil), c.Stations...)
			if dir == 1 {
				for a, b := 0, len(ids)-1; a < b; a, b = a+1, b-1 {
					ids[a], ids[b] = ids[b], ids[a]
				}
			}
			pts := make([][2]float64, len(ids))
			p := model.Pattern{ID: subPrefix + c.ID + "-" + itoaDir(dir), Direction: dir, Stops: make([]string, len(ids))}
			for k, id := range ids {
				st := x.stops[x.byID[id]]
				pts[k] = [2]float64{st.Lat, st.Lon}
				p.Stops[k] = st.ID
			}
			p.Headsign = x.stops[x.byID[ids[len(ids)-1]]].Name
			tr := subTrack(c.Path, pts)
			p.Shape = tr.Shape()
			x.tracks[i][dir], x.patterns[i][dir] = tr, p
		}
	}
	return x
}

func itoaDir(d int) string { return string(rune('0' + d)) }

// subTrack is the corridor path through the stations, or straight lines between them when the
// path does not fit (a station farther than 150 m from it).
func subTrack(path [][2]float64, stops [][2]float64) *railgeo.Track {
	if tr, ok := railgeo.Fit([][][2]float64{path}, stops); ok {
		return tr
	}
	xy := make([][2]float64, len(stops))
	for i, s := range stops {
		xy[i] = geo.XY(s[0], s[1])
	}
	pl := geo.NewPolyline(xy)
	return &railgeo.Track{Line: pl, Along: append([]float64(nil), pl.Cum...)}
}

// stopOf finds the Stop of any railway.gov.gr station id: its own id, a permanent alias, or an
// id the cached station table merges with it.
func (s *proastiakosAdapter) stopOf(x *subIndex, id string) (int, bool) {
	if k, ok := x.byID[id]; ok {
		return k, true
	}
	if k, ok := x.byID[subAliases[id]]; ok {
		return k, true
	}
	p, ok := s.board.StationPrimary(id)
	if !ok {
		return -1, false
	}
	if k, ok := x.byID[p]; ok {
		return k, true
	}
	if k, ok := x.byID[subAliases[p]]; ok {
		return k, true
	}
	for k := range x.stops {
		if q, ok := s.board.StationPrimary(strings.TrimPrefix(x.stops[k].ID, subPrefix)); ok && q == p {
			return k, true
		}
	}
	return -1, false
}

func (s *proastiakosAdapter) Resolve(kind Kind, id string) string {
	native := strings.TrimPrefix(id, subPrefix)
	switch kind {
	case KindLine:
		return subPrefix + strings.ToUpper(native)
	case KindStop:
		x := s.index()
		if k, ok := s.stopOf(x, native); ok {
			return x.stops[k].ID
		}
	}
	return id
}

// State: never warming up (the corridor table is embedded). Down when the board failed and none
// of today is cached; stale when the cached board is over 30 min old and its refresh fails, or
// the open stream sent nothing for 2 min.
func (s *proastiakosAdapter) State(now time.Time) model.SourceState {
	out := model.SourceState{ID: subNS, State: model.OK}
	_, at, ok := s.board.CachedBoard(now)
	open, last := s.trains.Health()
	upd := last
	if ok && at.After(upd) {
		upd = at
	}
	if !upd.IsZero() {
		out.UpdatedAt = model.Ptr(upd.Unix())
	}
	failing := s.board.BoardFailing()
	switch {
	case !ok && failing:
		out.State = model.Down
	case ok && failing && now.Sub(at) > subBoardStale:
		out.State = model.Stale
	case open && !last.IsZero() && now.Sub(last) > subStreamStale:
		out.State = model.Stale
	}
	return out
}

func (s *proastiakosAdapter) Lines() ([]model.Line, error) {
	return append([]model.Line(nil), s.index().lines...), nil
}

func (s *proastiakosAdapter) corridor(id string) (*subIndex, int, error) {
	x := s.index()
	c, ok := x.lineOf[strings.TrimPrefix(id, subPrefix)]
	if !ok {
		return nil, -1, errUnknown
	}
	return x, c, nil
}

func (s *proastiakosAdapter) LineInfo(id string) (*model.Line, error) {
	x, c, err := s.corridor(id)
	if err != nil {
		return nil, err
	}
	l := x.lines[c]
	return &l, nil
}

func (s *proastiakosAdapter) Line(id string) (*LineDetail, error) {
	x, c, err := s.corridor(id)
	if err != nil {
		return nil, err
	}
	d := &LineDetail{Line: x.lines[c], Patterns: []model.Pattern{x.patterns[c][0], x.patterns[c][1]},
		Stops: map[string]model.Stop{}}
	for _, id := range x.patterns[c][0].Stops {
		d.Stops[id] = x.stops[x.byID[strings.TrimPrefix(id, subPrefix)]]
	}
	return d, nil
}

func (s *proastiakosAdapter) Stops() ([]model.Stop, error) {
	x := s.index()
	out := append([]model.Stop(nil), x.stops...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *proastiakosAdapter) Stop(id string) (*model.Stop, error) {
	x := s.index()
	k, ok := x.byID[strings.TrimPrefix(id, subPrefix)]
	if !ok {
		return nil, errUnknown
	}
	m := x.stops[k]
	return &m, nil
}

// schedDir is the direction of a board train on corridor c, by the corridor order of its first and
// last timed stops; -1 when one of them is not on the corridor.
func (s *proastiakosAdapter) schedDir(x *subIndex, c int, sc *rail.SuburbanSchedule) int {
	pos := []int{}
	for j := range sc.Stops {
		st := &sc.Stops[j]
		k, ok := s.stopOf(x, st.StationID)
		if !ok || st.Dep.IsZero() && st.Arr.IsZero() {
			continue
		}
		pos = append(pos, indexOf(x.corrs[c].Stations, strings.TrimPrefix(x.stops[k].ID, subPrefix)))
	}
	switch {
	case len(pos) < 2 || pos[0] < 0 || pos[len(pos)-1] < 0:
		return -1
	case pos[0] > pos[len(pos)-1]:
		return 1
	}
	return 0
}

// lineOfSchedule is the corridor of a board run: its own corridor field, or for a run without
// one the corridor holding most of its stations (at least two). -1 for other corridors.
func (s *proastiakosAdapter) lineOfSchedule(x *subIndex, sc *rail.SuburbanSchedule) int {
	if sc.Corridor != "" {
		if c, ok := x.lineOf[sc.Corridor]; ok {
			return c
		}
		return -1
	}
	best, bestN := -1, 1
	for c, corr := range x.corrs {
		n := 0
		for _, st := range sc.Stops {
			if k, ok := s.stopOf(x, st.StationID); ok && contains(corr.Stations, strings.TrimPrefix(x.stops[k].ID, subPrefix)) {
				n++
			}
		}
		if n > bestN {
			best, bestN = c, n
		}
	}
	return best
}

// liveTrains are the trains of a snapshot younger than rail.TrainFresh, by number.
func liveTrains(snap *rail.SuburbanVehicles, now time.Time) map[string]*rail.SuburbanTrain {
	if snap == nil || now.Sub(time.Unix(snap.UpdatedAt, 0)) > rail.TrainFresh {
		return nil
	}
	out := make(map[string]*rail.SuburbanTrain, len(snap.Trains))
	for i := range snap.Trains {
		out[snap.Trains[i].Train] = &snap.Trains[i]
	}
	return out
}

// subDelay is a run's delay in minutes at one stop and whether it is live: the stream's (train
// on it), else the board's while the board is fresh (the stop's own, else the run's once it is
// under way: a run not started reports 0, which is no observation).
func subDelay(sc *rail.SuburbanSchedule, st *rail.SuburbanBoardStop, tr *rail.SuburbanTrain, boardFresh bool) (*int, bool) {
	switch {
	case tr != nil && tr.DelayMin != nil:
		return tr.DelayMin, true
	case !boardFresh:
		return nil, false
	case st != nil && st.Delay != nil:
		return st.Delay, true
	case sc.Status == "in_progress" && sc.Delay != nil:
		return sc.Delay, true
	}
	return nil, false
}

// subRunID is the id of a train's run of now's day (the board is per Athens day).
func subRunID(no string, now time.Time) string {
	return subPrefix + "run:" + no + ":" + noonOf(now).Format(time.DateOnly)
}

// Arrivals are the board's departures from the Stop in the next 2 h, at scheduled time + delay
// (subDelay). The terminus and cancelled runs are left out. Opens the stream (contract).
func (s *proastiakosAdapter) Arrivals(ctx context.Context, stopID string, now time.Time) ([]model.Arrival, error) {
	x := s.index()
	k, ok := x.byID[strings.TrimPrefix(stopID, subPrefix)]
	if !ok {
		return nil, errUnknown
	}
	s.board.Stations(ctx) // the alias table; the board answers without it
	list, at, ok := s.board.Board(ctx, now)
	live := liveTrains(s.trains.Vehicles(), now)
	if !ok {
		return []model.Arrival{}, nil
	}
	fresh := now.Sub(at) <= subBoardFresh
	here := map[string]bool{}
	isHere := func(id string) bool {
		v, seen := here[id]
		if !seen {
			j, ok := s.stopOf(x, id)
			v = ok && j == k
			here[id] = v
		}
		return v
	}
	until := now.Add(v2Horizon)
	var out []model.Arrival
	for i := range list {
		sc := &list[i]
		if sc.Status == "cancelled" {
			continue
		}
		c := s.lineOfSchedule(x, sc)
		if c < 0 {
			continue
		}
		for j := range sc.Stops {
			st := &sc.Stops[j]
			if !isHere(st.StationID) {
				continue
			}
			sched := st.Dep
			if sched.IsZero() {
				sched = st.Arr
			}
			if st.Role == "destination" || st.Status == "cancelled" || sched.IsZero() {
				break
			}
			tr := live[sc.Train]
			delay, isLive := subDelay(sc, st, tr, fresh)
			t := sched
			var delayS *int
			if delay != nil {
				delayS = model.Ptr(*delay * 60)
				t = t.Add(time.Duration(*delay) * time.Minute)
			}
			if t.Before(now) || t.After(until) {
				break
			}
			ar := model.Arrival{Line: x.lines[c].ID, Stop: stopID, Headsign: sc.Destination, At: t.Unix(),
				ScheduledAt: model.Ptr(sched.Unix()), DelayS: delayS, Source: model.SourceTable,
				Run: model.Ptr(subRunID(sc.Train, now)), Platform: st.Platform, Status: model.StatusOf(isLive, delayS)}
			if isLive {
				ar.Source = model.SourceLive
			}
			if tr != nil {
				ar.Vehicle, ar.VehicleLabel = model.Ptr(subPrefix+sc.Train), model.Ptr(sc.Train)
			}
			out = append(out, ar)
			break
		}
	}
	return cutArrivals(out), nil
}

// Vehicles are the stream's trains of the three corridors (freight left out), at their GPS
// position; motion runs along the corridor track towards the next station. Opens the stream.
func (s *proastiakosAdapter) Vehicles(now time.Time) ([]model.Vehicle, error) {
	return s.vehicles(now, -1), nil
}

func (s *proastiakosAdapter) vehicles(now time.Time, only int) []model.Vehicle {
	x := s.index()
	out := []model.Vehicle{}
	snap := s.trains.Vehicles()
	live := liveTrains(snap, now)
	if live == nil {
		return out
	}
	runs := map[string][2]int{} // train: its board corridor and direction (-1 unknown)
	if list, _, ok := s.board.CachedBoard(now); ok {
		for i := range list {
			if _, seen := runs[list[i].Train]; seen || list[i].Status == "cancelled" {
				continue // the first entry, as Run
			}
			if c := s.lineOfSchedule(x, &list[i]); c >= 0 {
				runs[list[i].Train] = [2]int{c, s.schedDir(x, c, &list[i])}
			}
		}
	}
	for i := range snap.Trains {
		tr := &snap.Trains[i]
		c, ok := x.lineOf[tr.Corridor]
		if !ok || tr.Service == "Freight" || only >= 0 && c != only {
			continue
		}
		at := tr.PositionAt
		if at == 0 {
			at = snap.UpdatedAt
		}
		v := model.Vehicle{ID: subPrefix + tr.Train, Line: x.lines[c].ID, Mode: model.Suburban, Label: model.Ptr(tr.Train),
			Position: model.Position{Kind: model.GPS, Lat: round5(tr.Lat), Lon: round5(tr.Lon), At: at}, Source: subNS}
		if tr.Course != nil {
			v.Position.Bearing = model.Ptr(math.Round(*tr.Course))
		}
		if tr.To != "" {
			v.Headsign = model.Ptr(tr.To)
		}
		if tr.DelayMin != nil {
			v.DelayS = model.Ptr(*tr.DelayMin * 60)
		}
		runDir := -1
		if r, ok := runs[tr.Train]; ok {
			v.Run = model.Ptr(subRunID(tr.Train, now))
			if r[0] == c {
				runDir = r[1]
			}
		}
		s.place(x, c, tr, &v, runDir)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// nextStation is the corridor index (file order) of the stream's next station: by id, else by
// folded name. -1 when unknown or not on the corridor.
func (s *proastiakosAdapter) nextStation(x *subIndex, c int, tr *rail.SuburbanTrain) int {
	ids := x.corrs[c].Stations
	if tr.NextID != "" {
		if k, ok := s.stopOf(x, tr.NextID); ok {
			return indexOf(ids, strings.TrimPrefix(x.stops[k].ID, subPrefix))
		}
	}
	if tr.Next != "" {
		name := rail.Fold(tr.Next)
		for i, id := range ids {
			if rail.Fold(x.stops[x.byID[id]].Name) == name {
				return i
			}
		}
	}
	return -1
}

// nearest is the closest pass of the line within subSnapM, ok false when there is none.
func nearest(line *geo.Polyline, xy [2]float64, heading float64, hasHeading bool) (geo.Cand, bool) {
	cs := line.Candidates(xy[0], xy[1], subSnapM, heading, hasHeading)
	if len(cs) == 0 {
		return geo.Cand{}, false
	}
	best := cs[0]
	for _, c := range cs[1:] {
		if c.Off < best.Off {
			best = c
		}
	}
	return best, true
}

// place snaps a train onto its corridor in the direction of its Run (runDir, -1 when unknown), else
// where its next station lies ahead (or, with no next station, the direction of its course) and
// sets direction, next_stop and motion: path to the next station, path_beyond up to the third
// ahead, at the GPS speed, as for buses (pathAhead).
func (s *proastiakosAdapter) place(x *subIndex, c int, tr *rail.SuburbanTrain, v *model.Vehicle, runDir int) {
	xy := geo.XY(tr.Lat, tr.Lon)
	heading, hasHeading := 0.0, tr.Course != nil
	if hasHeading {
		heading = *tr.Course
	}
	next := s.nextStation(x, c, tr)
	if next >= 0 {
		v.NextStop = model.Ptr(subPrefix + x.corrs[c].Stations[next])
	}
	c0, ok := nearest(x.tracks[c][0].Line, xy, heading, hasHeading)
	if !ok {
		return
	}
	dir := runDir
	switch {
	case runDir >= 0:
	case next >= 0:
		if x.tracks[c][0].Along[next] < c0.Along-1 {
			dir = 1
		}
	case hasHeading:
		if c0.Wrong {
			dir = 1
		}
	default:
		return
	}
	v.Direction = model.Ptr(dir)
	track, pat := x.tracks[c][dir], &x.patterns[c][dir]
	cd, ok := nearest(track.Line, xy, 0, false)
	if !ok {
		return
	}
	along, n := cd.Along, len(track.Along)
	j := sort.Search(n, func(k int) bool { return track.Along[k] > along+1 })
	if next >= 0 {
		pj := next
		if dir == 1 {
			pj = n - 1 - next
		}
		if track.Along[pj] >= along-1 {
			j = pj
		}
	}
	if j >= n {
		v.NextStop = nil
		return
	}
	v.NextStop = model.Ptr(pat.Stops[j])
	// GPS trains follow the bus rules: path and path_beyond capped at 1.5 km (contract §4).
	speed := model.Ptr(math.Round(tr.SpeedKmh/3.6*10) / 10)
	path, beyond, at := pathAhead(track.Line, along, speed, track.Along[j:])
	v.Motion = &model.Motion{Path: path, PathBeyond: beyond, PathStops: at, Speed: speed}
}

func (s *proastiakosAdapter) LineVehicles(id string, now time.Time) (*LineVehicles, error) {
	_, c, err := s.corridor(id)
	if err != nil {
		return nil, err
	}
	return &LineVehicles{Vehicles: s.vehicles(now, c), UpdatedAt: now.Unix(), NextUpdateAt: now.Unix() + subVehMaxAge,
		MaxAge: subVehMaxAge}, nil
}

// Run is a train's run of today from the board: its departure at each stop (the arrival at the
// last) plus its delay, passed once that time is past. It does not open the stream; the Vehicle
// is set while the open stream has the train.
func (s *proastiakosAdapter) Run(id string, now time.Time) (*model.Run, error) {
	x := s.index()
	no, noon, ok := model.ParseRunID(subNS, id, gtfs.Athens)
	if !ok || !noon.Equal(noonOf(now)) {
		return nil, errUnknown
	}
	list, at, ok := s.board.Board(context.Background(), now)
	if !ok {
		return nil, errWarming
	}
	var sc *rail.SuburbanSchedule
	c := -1
	for i := range list {
		if list[i].Train == no && list[i].Status != "cancelled" {
			if c = s.lineOfSchedule(x, &list[i]); c >= 0 {
				sc = &list[i]
				break
			}
		}
	}
	if sc == nil {
		return nil, errUnknown
	}
	tr := liveTrains(s.trains.Peek(), now)[no]
	fresh := now.Sub(at) <= subBoardFresh
	run := &model.Run{ID: id, Line: x.lines[c].ID, Headsign: sc.Destination, Stops: []model.RunStop{}}
	for j := range sc.Stops {
		st := &sc.Stops[j]
		k, ok := s.stopOf(x, st.StationID)
		if !ok {
			continue
		}
		sched := st.Dep
		if sched.IsZero() || j == len(sc.Stops)-1 && !st.Arr.IsZero() {
			sched = st.Arr
		}
		if sched.IsZero() {
			continue
		}
		delay, isLive := subDelay(sc, st, tr, fresh)
		t, rs := sched, model.RunStop{Stop: x.stops[k].ID, ScheduledAt: model.Ptr(sched.Unix()), Source: model.SourceTable,
			Platform: st.Platform}
		if delay != nil {
			rs.DelayS = model.Ptr(*delay * 60)
			t = t.Add(time.Duration(*delay) * time.Minute)
		}
		if isLive {
			rs.Source = model.SourceLive
		}
		rs.At, rs.Passed = t.Unix(), !t.After(now)
		run.Stops = append(run.Stops, rs)
	}
	if dir := s.schedDir(x, c, sc); dir >= 0 {
		run.Pattern = model.Ptr(x.patterns[c][dir].ID)
	}
	delay, isLive := subDelay(sc, nil, tr, fresh)
	var delayS *int
	if delay != nil {
		delayS = model.Ptr(*delay * 60)
	}
	run.Status = model.StatusOf(isLive, delayS)
	if tr != nil {
		run.Vehicle, run.VehicleLabel = model.Ptr(subPrefix+no), model.Ptr(no)
	}
	return run, nil
}
