package tools

import (
	"database/sql"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// Replay scores the matchers on a recording made by record, against what the buses actually
// did. Ground truth comes from the recorded GPS fixes: each vehicle's run is tracked along
// its route and the moment it passes every stop is interpolated between consecutive fixes.

var horizons = [][2]int{{0, 5}, {5, 10}, {10, 20}, {20, 30}} // minutes ahead

const (
	maxFixGapS       = 180 // no passage is interpolated across a longer gap between fixes
	replayRunGapS    = 600
	overtakeMarginM  = 150 // both before and after the swap, to ignore GPS noise
	firstStopLeaveM  = 50  // leaving the first stop, not idling at it
	circularRestartM = 500
)

type fixRec struct {
	line, route       string
	lat, lon, heading float64
}

type etaRec struct {
	polled    float64
	stop, veh string
	minutes   int
}

// Recording is a loaded record database.
type Recording struct {
	Cycles    []float64                          // sorted
	ByCycle   map[float64]map[string][]match.Obs // cycle -> line -> vehicles
	LineOrder map[float64][]string
	Fixes     map[string]map[float64]fixRec // veh -> ts -> fix
	ETA       []etaRec
}

func LoadRecording(db *sql.DB) (*Recording, error) {
	rows, err := db.Query(`SELECT m.cycle, m.line, m.veh, m.route_code, f.ts, f.lat, f.lon, f.heading, f.line_code
		FROM match m JOIN fix f ON f.veh = m.veh AND f.ts = m.fix_ts ORDER BY m.cycle, m.line, m.veh`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rec := &Recording{ByCycle: map[float64]map[string][]match.Obs{}, LineOrder: map[float64][]string{},
		Fixes: map[string]map[float64]fixRec{}}
	for rows.Next() {
		var cycle, ts, lat, lon float64
		var heading sql.NullFloat64
		var line, veh, route string
		var lineCode sql.NullString
		if err := rows.Scan(&cycle, &line, &veh, &route, &ts, &lat, &lon, &heading, &lineCode); err != nil {
			return nil, err
		}
		if rec.ByCycle[cycle] == nil {
			rec.ByCycle[cycle] = map[string][]match.Obs{}
			rec.Cycles = append(rec.Cycles, cycle)
		}
		if rec.ByCycle[cycle][line] == nil {
			rec.LineOrder[cycle] = append(rec.LineOrder[cycle], line)
		}
		t := time.Unix(int64(ts), 0).In(gtfs.Athens)
		rec.ByCycle[cycle][line] = append(rec.ByCycle[cycle][line], match.Obs{Vehicle: telematics.Vehicle{
			VehNo: veh, RouteCode: route, Lat: lat, Lon: lon, Heading: heading.Float64, Time: t}, LineCode: lineCode.String})
		if rec.Fixes[veh] == nil {
			rec.Fixes[veh] = map[float64]fixRec{}
		}
		rec.Fixes[veh][ts] = fixRec{line, route, lat, lon, heading.Float64}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	eta, err := db.Query(`SELECT polled, stop_id, veh, minutes FROM oasa_eta`)
	if err != nil {
		return nil, err
	}
	defer eta.Close()
	for eta.Next() {
		var e etaRec
		if err := eta.Scan(&e.polled, &e.stop, &e.veh, &e.minutes); err != nil {
			return nil, err
		}
		rec.ETA = append(rec.ETA, e)
	}
	return rec, eta.Err()
}

// ---------------------------------------------------------------- ground truth from GPS

type routeGeo struct {
	geo   *geo.Geometry
	stops []string
}

type departure struct {
	veh, line, route string
	t                float64
}

type alongRec struct {
	route string
	along float64
}

type Truth struct {
	passages   map[[2]string][]float64 // (veh, stop) -> sorted passage times
	departures []departure
	along      map[string]map[float64]alongRec
}

// routeGeometry returns the geometry and stop ids of the most common stop pattern of a live
// route code (cached).
func routeGeometry(f *gtfs.Feed, mapper *match.RouteMapper, m *match.Matcher) func(line, route string) *routeGeo {
	cache := map[[2]string]*routeGeo{}
	done := map[[2]string]bool{}
	return func(line, route string) *routeGeo {
		k := [2]string{line, route}
		if done[k] {
			return cache[k]
		}
		done[k] = true
		shapes := mapper.ShapesFor(line, route)
		counts, order := map[int32]int{}, []int32{}
		first := map[int32]int32{}
		for _, ti := range f.TripsForLine(line) {
			t := &f.Trips[ti]
			if !shapes[t.Shape] {
				continue
			}
			if counts[t.Pattern] == 0 {
				order = append(order, t.Pattern)
				first[t.Pattern] = ti
			}
			counts[t.Pattern]++
		}
		if len(order) == 0 {
			return nil
		}
		best := order[0]
		for _, p := range order[1:] {
			if counts[p] > counts[best] {
				best = p
			}
		}
		g := m.Geometry(f.Trip(first[best]))
		if g == nil {
			return nil
		}
		var ids []string
		for _, s := range f.Patterns[best].Stops {
			ids = append(ids, f.Stops[s].ID)
		}
		cache[k] = &routeGeo{g, ids}
		return cache[k]
	}
}

func NewTruth(rec *Recording, geometryOf func(line, route string) *routeGeo) *Truth {
	tr := &Truth{passages: map[[2]string][]float64{}, along: map[string]map[float64]alongRec{}}
	vehs := make([]string, 0, len(rec.Fixes))
	for v := range rec.Fixes {
		vehs = append(vehs, v)
	}
	sort.Strings(vehs)
	for _, veh := range vehs {
		byTS := rec.Fixes[veh]
		tss := make([]float64, 0, len(byTS))
		for ts := range byTS {
			tss = append(tss, ts)
		}
		sort.Float64s(tss)
		var run []runPt
		for _, ts := range tss {
			f := byTS[ts]
			if len(run) > 0 && (f.route != run[len(run)-1].f.route || ts-run[len(run)-1].ts > replayRunGapS) {
				tr.run(veh, run[0].f.line, run[0].f.route, toTrack(run), geometryOf)
				run = nil
			}
			run = append(run, runPt{ts, f})
		}
		if len(run) > 0 {
			tr.run(veh, run[0].f.line, run[0].f.route, toTrack(run), geometryOf)
		}
	}
	for _, times := range tr.passages {
		sort.Float64s(times)
	}
	return tr
}

type runPt struct {
	ts float64
	f  fixRec
}

type trackFix struct {
	ts                float64
	lat, lon, heading float64
}

func toTrack(run []runPt) []trackFix {
	out := make([]trackFix, len(run))
	for i, p := range run {
		out[i] = trackFix{p.ts, p.f.lat, p.f.lon, p.f.heading}
	}
	return out
}

func (tr *Truth) run(veh, line, route string, run []trackFix, geometryOf func(line, route string) *routeGeo) {
	rg := geometryOf(line, route)
	if rg == nil {
		return
	}
	type tp struct{ ts, along float64 }
	var track []tp
	havePrev, prev := false, 0.0
	flush := func() {
		ts := make([][2]float64, len(track))
		for i, p := range track {
			ts[i] = [2]float64{p.ts, p.along}
		}
		tr.passagesOf(veh, line, route, ts, rg)
	}
	for _, f := range run {
		xy := geo.XY(f.lat, f.lon)
		cands := rg.geo.Line.Candidates(xy[0], xy[1], match.MaxOffRouteM, f.heading, f.heading != 0)
		if len(cands) == 0 {
			continue
		}
		along := cands[0].Along
		key := func(a float64) float64 {
			if havePrev {
				return math.Abs(a - prev)
			}
			return a
		}
		for _, c := range cands[1:] {
			if key(c.Along) < key(along) {
				along = c.Along
			}
		}
		if havePrev && along < prev-circularRestartM {
			flush() // circular route restarted
			track = nil
		}
		track = append(track, tp{f.ts, along})
		if tr.along[veh] == nil {
			tr.along[veh] = map[float64]alongRec{}
		}
		tr.along[veh][f.ts] = alongRec{route, along}
		prev, havePrev = along, true
	}
	flush()
}

func (tr *Truth) passagesOf(veh, line, route string, track [][2]float64, rg *routeGeo) {
	for k, sid := range rg.stops {
		threshold := rg.geo.StopAlong[k]
		if k == 0 {
			threshold += firstStopLeaveM
		}
		for i := 0; i+1 < len(track); i++ {
			t0, a0, t1, a1 := track[i][0], track[i][1], track[i+1][0], track[i+1][1]
			if a0 < threshold && threshold <= a1 && t1-t0 <= maxFixGapS {
				t := t0 + (threshold-a0)/(a1-a0)*(t1-t0)
				key := [2]string{veh, sid}
				tr.passages[key] = append(tr.passages[key], t)
				if k == 0 {
					tr.departures = append(tr.departures, departure{veh, line, route, t})
				}
				break
			}
		}
	}
}

// passageAfter is the first passage of veh at stop at or after `after` (ok=false if none).
func (tr *Truth) passageAfter(veh, stop string, after float64) (float64, bool) {
	times := tr.passages[[2]string{veh, stop}]
	i := sort.SearchFloat64s(times, after)
	if i < len(times) {
		return times[i], true
	}
	return 0, false
}

// ---------------------------------------------------------------- scoring

type replayResult struct {
	trip      string // "" when unmatched
	tripIdx   int32
	day       time.Time
	delay     int
	nextIndex int
	waiting   bool
	route     string
}

type cycleVeh struct {
	cycle float64
	veh   string
}

func midnightOf(day time.Time) float64 { return float64(day.Unix()) }

type horizonRow struct {
	label          string
	n              int
	med, p90, bias float64
}

func medianOf(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// summarise: per horizon N, median |e|, p90 |e| and median e, in minutes.
func summarise(errors [][2]float64) []horizonRow {
	var out []horizonRow
	for _, h := range horizons {
		var abs, signed []float64
		for _, e := range errors {
			if float64(h[0]*60) < e[0] && e[0] <= float64(h[1]*60) {
				abs = append(abs, math.Abs(e[1]))
				signed = append(signed, e[1])
			}
		}
		row := horizonRow{label: fmt.Sprintf("%d-%d", h[0], h[1]), n: len(abs)}
		if len(abs) > 0 {
			sort.Float64s(abs)
			row.med = medianOf(abs) / 60
			row.p90 = abs[int(0.9*float64(len(abs)-1))] / 60
			row.bias = medianOf(signed) / 60
		}
		out = append(out, row)
	}
	return out
}

func printTable(w io.Writer, title string, rows []horizonRow) {
	fmt.Fprintf(w, "\n%s\n", title)
	fmt.Fprintf(w, "  %13s %7s %13s %10s %11s\n", "minutes ahead", "N", "median |err|", "p90 |err|", "median err")
	for _, r := range rows {
		if r.n > 0 {
			fmt.Fprintf(w, "  %13s %7d %11.1f m %8.1f m %+9.1f m\n", r.label, r.n, r.med, r.p90, r.bias)
		} else {
			fmt.Fprintf(w, "  %13s %7d\n", r.label, r.n)
		}
	}
}

// predictionErrors: errors of "scheduled time + current delay" for every downstream stop reached.
func predictionErrors(f *gtfs.Feed, results map[cycleVeh]replayResult, tr *Truth, scheduleOnly bool) [][2]float64 {
	var out [][2]float64
	for k, r := range results {
		if r.trip == "" {
			continue
		}
		t := f.Trip(r.tripIdx)
		base := midnightOf(r.day)
		from := r.nextIndex
		if r.waiting {
			from--
		}
		for i := from; i < f.NumStops(t); i++ {
			st := f.StopTime(t, i)
			actual, ok := tr.passageAfter(k.veh, f.Stops[st.Stop].ID, k.cycle)
			if !ok || actual-k.cycle > float64(horizons[len(horizons)-1][1]*60) {
				continue
			}
			sched := float64(st.Arr)
			if r.waiting {
				sched = float64(st.Dep)
			}
			delay := float64(r.delay)
			if scheduleOnly {
				delay = 0
			}
			out = append(out, [2]float64{actual - k.cycle, base + sched + delay - actual})
		}
	}
	return out
}

// Replay runs the full report (upstream replay.main without --blocks).
func Replay(w io.Writer, rec *Recording, f *gtfs.Feed, stops match.StopsFunc) {
	mapper := match.NewRouteMapper(f, stops)
	names := []string{"greedy (now)", "hungarian", "memory"}
	methods := map[string]*match.Matcher{
		"greedy (now)": match.New(f, mapper, match.Greedy),
		"hungarian":    match.New(f, mapper, match.Hungarian),
		"memory":       match.New(f, mapper, match.Memory),
	}
	nFix, lines := 0, map[string]bool{}
	for _, by := range rec.Fixes {
		nFix += len(by)
	}
	for _, c := range rec.Cycles {
		for l := range rec.ByCycle[c] {
			lines[l] = true
		}
	}
	fmt.Fprintf(w, "%d cycles, %d GPS fixes, %d vehicles, %d lines\n", len(rec.Cycles), nFix, len(rec.Fixes), len(lines))

	// Replay: every method sees exactly what the live recorder saw, cycle by cycle.
	results := map[string]map[cycleVeh]replayResult{}
	for _, n := range names {
		results[n] = map[cycleVeh]replayResult{}
	}
	anchored := map[cycleVeh]bool{}
	for _, cycle := range rec.Cycles {
		for _, line := range rec.LineOrder[cycle] {
			for _, name := range names {
				m := methods[name]
				for _, r := range m.MatchLine(line, rec.ByCycle[cycle][line]) {
					res := replayResult{tripIdx: r.Trip, day: r.Day, delay: r.Delay, nextIndex: r.NextIndex,
						waiting: r.Waiting, route: r.RouteCode}
					if r.Matched() {
						res.trip = f.Trips[r.Trip].ID
					} else {
						res.delay, res.nextIndex = 0, 0
					}
					results[name][cycleVeh{cycle, r.VehicleID}] = res
					if name == "memory" && m.Anchored(&r) {
						anchored[cycleVeh{cycle, r.VehicleID}] = true
					}
				}
			}
		}
	}

	tr := NewTruth(rec, routeGeometry(f, mapper, methods["greedy (now)"]))
	nPass := 0
	for _, v := range tr.passages {
		nPass += len(v)
	}
	fmt.Fprintf(w, "ground truth: %d stop passages, %d observed departures\n", nPass, len(tr.departures))

	fmt.Fprintln(w, "\nTRIP IDENTITY")
	proxy := departureProxy(f, mapper, tr, rec, results["greedy (now)"])
	for _, name := range names {
		res := results[name]
		matched := 0
		for _, r := range res {
			if r.trip != "" {
				matched++
			}
		}
		agree, n := 0, 0
		for k, trip := range proxy {
			if r, ok := res[k]; ok {
				n++
				if r.trip == trip {
					agree++
				}
			}
		}
		fmt.Fprintf(w, "  %-13s matched %5.1f%%   mid-run trip changes %4d   agrees with departure-based trip %5.1f%% (n=%d)\n",
			name, pct(matched, len(res)), midRunSwitches(res), pct(agree, max(1, n)), n)
	}
	memMatched := 0
	for _, r := range results["memory"] {
		if r.trip != "" {
			memMatched++
		}
	}
	fmt.Fprintf(w, "  memory: %.1f%% of its matches come from an observed departure\n", pct(len(anchored), max(1, memMatched)))
	var parts []string
	for _, lc := range switchesByLine(results, names, rec) {
		s := make([]string, len(lc.counts))
		for i, c := range lc.counts {
			s[i] = fmt.Sprint(c)
		}
		parts = append(parts, lc.line+" "+strings.Join(s, "/"))
	}
	fmt.Fprintf(w, "  mid-run trip changes by line: %s\n", strings.Join(parts, ", "))
	a, b := results[names[0]], results[names[2]]
	differ := 0
	for k, r := range a {
		if r.trip != b[k].trip {
			differ++
		}
	}
	fmt.Fprintf(w, "  greedy vs memory disagree on %d of %d vehicle-cycles (%.1f%%)\n", differ, len(a), pct(differ, max(1, len(a))))

	fmt.Fprintln(w, "\nOVERTAKES (same route, order swapped by >150 m, sustained)")
	events := overtakes(tr, rec)
	fmt.Fprintf(w, "  %d found\n", len(events))
	for _, name := range names {
		kept, swapped, other := 0, 0, 0
		r := results[name]
		for _, e := range events {
			t1b, t2b := r[cycleVeh{e.before, e.v1}].trip, r[cycleVeh{e.before, e.v2}].trip
			t1a, t2a := r[cycleVeh{e.after, e.v1}].trip, r[cycleVeh{e.after, e.v2}].trip
			switch {
			case t1b != "" && t2b != "" && t1b == t2a && t2b == t1a:
				swapped++
			case t1b != "" && t2b != "" && t1b == t1a && t2b == t2a:
				kept++
			default:
				other++
			}
		}
		fmt.Fprintf(w, "  %-13s kept their trips %3d   swapped trips %3d   other %3d\n", name, kept, swapped, other)
	}

	fmt.Fprintln(w, "\nARRIVAL PREDICTIONS vs ACTUAL PASSAGES (from GPS)")
	for _, name := range names {
		printTable(w, name+": scheduled time + current delay", summarise(predictionErrors(f, results[name], tr, false)))
	}
	printTable(w, "schedule only (memory's trips, no delay) = what Google Maps shows today",
		summarise(predictionErrors(f, results["memory"], tr, true)))
	var oasa [][2]float64
	for _, e := range rec.ETA {
		actual, ok := tr.passageAfter(e.veh, e.stop, e.polled-60)
		if ok && actual-e.polled <= float64(horizons[len(horizons)-1][1]*60) {
			oasa = append(oasa, [2]float64{actual - e.polled, e.polled + float64(e.minutes*60) - actual})
		}
	}
	printTable(w, "OASA's own prediction (getStopArrivals)", summarise(oasa))
	paired(w, f, rec, tr, results["memory"])
}

func pct(a, b int) float64 { return 100 * float64(a) / float64(b) }

// departureProxy: for runs whose departure was observed, the scheduled trip leaving closest
// to it, applied to that vehicle's cycles until it leaves the route.
func departureProxy(f *gtfs.Feed, mapper *match.RouteMapper, tr *Truth, rec *Recording, greedy map[cycleVeh]replayResult) map[cycleVeh]string {
	proxy := map[cycleVeh]string{}
	for _, d := range tr.departures {
		shapes := mapper.ShapesFor(d.line, d.route)
		day := gtfs.ServiceDay(time.Unix(int64(d.t), 0))
		found, bestScore, bestTrip, bestDur := false, 0.0, "", 0.0
		for _, ti := range f.TripsForLine(d.line) {
			t := &f.Trips[ti]
			if !shapes[t.Shape] || !f.ServiceActive(t.Service, day) {
				continue
			}
			late := d.t - midnightOf(day) - float64(f.Start(t))
			if late < -300 || late > 900 {
				continue
			}
			score := late
			if late < 0 {
				score = -3 * late
			}
			if !found || score < bestScore {
				found, bestScore, bestTrip, bestDur = true, score, t.ID, float64(f.End(t)-f.Start(t))
			}
		}
		if !found {
			continue
		}
		start := sort.Search(len(rec.Cycles), func(i int) bool { return rec.Cycles[i] > d.t+60 })
		for _, c := range rec.Cycles[start:] {
			if c > d.t+bestDur+3600 {
				break
			}
			r, ok := greedy[cycleVeh{c, d.veh}]
			if !ok || r.route != d.route {
				break
			}
			proxy[cycleVeh{c, d.veh}] = bestTrip
		}
	}
	return proxy
}

type vehRow struct {
	cycle float64
	r     replayResult
}

func byVehicle(res map[cycleVeh]replayResult) map[string][]vehRow {
	out := map[string][]vehRow{}
	for k, r := range res {
		out[k.veh] = append(out[k.veh], vehRow{k.cycle, r})
	}
	for _, rows := range out {
		sort.Slice(rows, func(i, j int) bool { return rows[i].cycle < rows[j].cycle })
	}
	return out
}

// isSwitch: same route code, both matched, not at the terminal, but a different trip.
func isSwitch(a, b replayResult) bool {
	return a.route == b.route && a.trip != "" && b.trip != "" && a.trip != b.trip && !a.waiting && !b.waiting
}

func midRunSwitches(res map[cycleVeh]replayResult) int {
	n := 0
	for _, rows := range byVehicle(res) {
		for i := 0; i+1 < len(rows); i++ {
			if isSwitch(rows[i].r, rows[i+1].r) {
				n++
			}
		}
	}
	return n
}

type lineCounts struct {
	line   string
	counts []int
}

func switchesByLine(results map[string]map[cycleVeh]replayResult, names []string, rec *Recording) []lineCounts {
	lineOf := map[string]string{}
	for _, c := range rec.Cycles {
		for _, line := range rec.LineOrder[c] {
			for _, v := range rec.ByCycle[c][line] {
				lineOf[v.VehNo] = line
			}
		}
	}
	counts := map[string][]int{}
	var order []string
	for i, name := range names {
		for veh, rows := range byVehicle(results[name]) {
			for j := 0; j+1 < len(rows); j++ {
				if !isSwitch(rows[j].r, rows[j+1].r) {
					continue
				}
				l, ok := lineOf[veh]
				if !ok {
					l = "?"
				}
				if counts[l] == nil {
					counts[l] = make([]int, len(names))
					order = append(order, l)
				}
				counts[l][i]++
			}
		}
	}
	sort.Strings(order)
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]][0] > counts[order[j]][0] })
	out := make([]lineCounts, len(order))
	for i, l := range order {
		out[i] = lineCounts{l, counts[l]}
	}
	return out
}

type overtake struct {
	v1, v2        string
	before, after float64
}

func overtakes(tr *Truth, rec *Recording) []overtake {
	var out []overtake
	for i := 0; i+1 < len(rec.Cycles); i++ {
		a, b := positionsAt(tr, rec, rec.Cycles[i]), positionsAt(tr, rec, rec.Cycles[i+1])
		routes := make([]string, 0, len(a))
		for r := range a {
			routes = append(routes, r)
		}
		sort.Strings(routes)
		for _, route := range routes {
			va, vb := a[route], b[route]
			var ids []string
			for v := range va {
				if _, ok := vb[v]; ok {
					ids = append(ids, v)
				}
			}
			sort.Strings(ids)
			for x := 0; x < len(ids); x++ {
				for y := x + 1; y < len(ids); y++ {
					before, after := va[ids[x]]-va[ids[y]], vb[ids[x]]-vb[ids[y]]
					if math.Abs(before) > overtakeMarginM && math.Abs(after) > overtakeMarginM && before*after < 0 {
						out = append(out, overtake{ids[x], ids[y], rec.Cycles[i], rec.Cycles[i+1]})
					}
				}
			}
		}
	}
	return out
}

func positionsAt(tr *Truth, rec *Recording, cycle float64) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	for _, line := range rec.LineOrder[cycle] {
		for _, v := range rec.ByCycle[cycle][line] {
			hit, ok := tr.along[v.VehNo][float64(v.Time.Unix())]
			if !ok {
				continue
			}
			if out[hit.route] == nil {
				out[hit.route] = map[string]float64{}
			}
			out[hit.route][v.VehNo] = hit.along
		}
	}
	return out
}

// paired compares OASA's prediction with ours on identical samples (same vehicle, stop, moment).
func paired(w io.Writer, f *gtfs.Feed, rec *Recording, tr *Truth, res map[cycleVeh]replayResult) {
	hist := byVehicle(res)
	var ours, theirs [][2]float64
	for _, e := range rec.ETA {
		actual, ok := tr.passageAfter(e.veh, e.stop, e.polled-60)
		if !ok || actual-e.polled > float64(horizons[len(horizons)-1][1]*60) {
			continue
		}
		rows := hist[e.veh]
		i := sort.Search(len(rows), func(i int) bool { return rows[i].cycle > e.polled }) - 1
		if i < 0 || e.polled-rows[i].cycle > 60 {
			continue
		}
		r := rows[i].r
		if r.trip == "" {
			continue
		}
		t := f.Trip(r.tripIdx)
		var st *gtfs.StopTime
		for k := max(r.nextIndex-1, 0); k < f.NumStops(t); k++ {
			if s := f.StopTime(t, k); f.Stops[s.Stop].ID == e.stop {
				st = &s
				break
			}
		}
		if st == nil {
			continue
		}
		sched := float64(st.Arr)
		if r.waiting {
			sched = float64(st.Dep)
		}
		predicted := midnightOf(r.day) + sched + float64(r.delay)
		ours = append(ours, [2]float64{actual - e.polled, predicted - actual})
		theirs = append(theirs, [2]float64{actual - e.polled, e.polled + float64(e.minutes*60) - actual})
	}
	printTable(w, "PAIRED, identical samples: OASA", summarise(theirs))
	printTable(w, "PAIRED, identical samples: ours (memory)", summarise(ours))
}

// ---------------------------------------------------------------- --blocks

// blockOf: OASA encodes the vehicle block in the trip id: {route}_{service}_{block}_{HHMM}.
func blockOf(f *gtfs.Feed, t *gtfs.Trip) [2]string {
	return [2]string{f.Services[t.Service].ID, strings.Split(t.ID, "_")[3]}
}

type depCand struct {
	score, lateMin float64
	trip           int32
}

// identifyDeparture: trips of this route that could have left at dep (5 min early to 15 min
// late), best first.
func identifyDeparture(f *gtfs.Feed, mapper *match.RouteMapper, line, route string, dep float64) []depCand {
	day := gtfs.ServiceDay(time.Unix(int64(dep), 0))
	shapes := mapper.ShapesFor(line, route)
	var out []depCand
	for _, ti := range f.TripsForLine(line) {
		t := &f.Trips[ti]
		if !shapes[t.Shape] || !f.ServiceActive(t.Service, day) {
			continue
		}
		late := dep - midnightOf(day) - float64(f.Start(t))
		if late >= -300 && late <= 900 {
			score := late
			if late < 0 {
				score = -3 * late
			}
			out = append(out, depCand{score, late / 60, ti})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score < out[j].score })
	return out
}

// Blocks reports whether vehicles run consecutive trips of the same GTFS vehicle block.
// f must hold the whole network (blocks span several lines).
func Blocks(w io.Writer, rec *Recording, f *gtfs.Feed, stops match.StopsFunc) {
	mapper := match.NewRouteMapper(f, stops)
	tr := NewTruth(rec, routeGeometry(f, mapper, match.New(f, mapper, match.Greedy)))
	blocks := map[[2]string][]int32{}
	for i := range f.Trips {
		t := &f.Trips[i]
		if len(strings.Split(t.ID, "_")) == 5 {
			k := blockOf(f, t)
			blocks[k] = append(blocks[k], int32(i))
		}
	}
	for _, trips := range blocks {
		sort.SliceStable(trips, func(a, b int) bool { return f.Start(&f.Trips[trips[a]]) < f.Start(&f.Trips[trips[b]]) })
	}
	type dep struct {
		t      float64
		line   string
		trip   int32
		margin float64
	}
	byVeh, vehOrder := map[string][]dep{}, []string{}
	unidentified := 0
	var lateness []float64
	for _, d := range tr.departures {
		cands := identifyDeparture(f, mapper, d.line, d.route, d.t)
		if len(cands) == 0 {
			unidentified++
			continue
		}
		margin := math.Inf(1)
		if len(cands) > 1 {
			margin = cands[1].score - cands[0].score
		}
		if byVeh[d.veh] == nil {
			vehOrder = append(vehOrder, d.veh)
		}
		byVeh[d.veh] = append(byVeh[d.veh], dep{d.t, d.line, cands[0].trip, margin})
		lateness = append(lateness, cands[0].lateMin)
	}
	med := 0.0
	if len(lateness) > 0 {
		med = medianOf(lateness)
	}
	fmt.Fprintf(w, "%d observed departures; %d match no scheduled departure within 5 min early .. 15 min late; departure delay median %+.1f min\n",
		len(tr.departures), unidentified, med)

	type pair struct{ sameBlock, isNext, possible, unambiguous, changesLine bool }
	var pairs []pair
	var offsets []float64
	for _, veh := range vehOrder {
		deps := byVeh[veh]
		sort.SliceStable(deps, func(i, j int) bool { return deps[i].t < deps[j].t })
		for i := 0; i+1 < len(deps); i++ {
			a, b := deps[i], deps[i+1]
			if b.t-a.t > 3*3600 {
				continue
			}
			ta := &f.Trips[a.trip]
			next := int32(-1)
			for _, ti := range blocks[blockOf(f, ta)] {
				if f.Start(&f.Trips[ti]) > f.Start(ta) {
					next = ti
					break
				}
			}
			day := gtfs.ServiceDay(time.Unix(int64(b.t), 0))
			route := ""
			for _, d := range tr.departures {
				if d.veh == veh && d.t == b.t {
					route = d.route
					break
				}
			}
			possible := false
			if next >= 0 {
				for _, c := range identifyDeparture(f, mapper, b.line, route, b.t) {
					possible = possible || c.trip == next
				}
				offsets = append(offsets, (b.t-midnightOf(day)-float64(f.Start(&f.Trips[next])))/60)
			}
			pairs = append(pairs, pair{blockOf(f, ta) == blockOf(f, &f.Trips[b.trip]), next == b.trip, possible,
				min(a.margin, b.margin) >= 180, a.line != b.line})
		}
	}
	for _, sel := range []struct {
		label string
		only  bool
	}{{"all pairs", false}, {"unambiguous departures only (next-best trip >= 3 min worse)", true}} {
		var ps []pair
		for _, p := range pairs {
			if !sel.only || p.unambiguous {
				ps = append(ps, p)
			}
		}
		if len(ps) == 0 {
			continue
		}
		c := func(fn func(pair) bool) int {
			n := 0
			for _, p := range ps {
				if fn(p) {
					n++
				}
			}
			return n
		}
		n := len(ps)
		fmt.Fprintf(w, "\n%s: %d consecutive trip pairs of the same vehicle\n", sel.label, n)
		fmt.Fprintf(w, "  same vehicle block:                         %5.1f%%\n", pct(c(func(p pair) bool { return p.sameBlock }), n))
		fmt.Fprintf(w, "  exactly the next trip of that block:        %5.1f%%\n", pct(c(func(p pair) bool { return p.isNext }), n))
		fmt.Fprintf(w, "  block's next trip was even a possible match: %5.1f%%\n", pct(c(func(p pair) bool { return p.possible }), n))
		fmt.Fprintf(w, "  pairs that change line:                     %d\n", c(func(p pair) bool { return p.changesLine }))
	}
	if len(offsets) >= 10 {
		q := deciles(offsets)
		s := make([]string, len(q))
		for i, v := range q {
			s[i] = fmt.Sprintf("%+.0f", v)
		}
		fmt.Fprintf(w, "\nobserved departure minus the block's next scheduled departure, deciles (min): %s\n", strings.Join(s, ", "))
	}
}

// deciles matches Python statistics.quantiles(data, n=10) (method "exclusive").
func deciles(data []float64) []float64 {
	s := append([]float64(nil), data...)
	sort.Float64s(s)
	n, m := len(s), len(s)+1
	out := make([]float64, 0, 9)
	for i := 1; i < 10; i++ {
		j := min(max(i*m/10, 1), n-1)
		delta := float64(i*m - j*10)
		out = append(out, (s[j-1]*(10-delta)+s[j]*delta)/10)
	}
	return out
}
