package server

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/oseth"
	"github.com/angelospk/athens-transit-rt/internal/sched"
)

// The OSETH source runs on its own budget, beside the OASTH one. It is polled line by line, for
// the lines a user is watching, and its sightings are merged into OASTH's positions: OSETH
// covers buses OASTH does not report, and its fix is fresher for the buses it does.
const (
	osethMinMove   = 30.0             // metres a bus must have moved before an OSETH fix is trusted
	osethFresh     = 60 * time.Second // an older OSETH state is not merged into an OASTH poll
	osethPollEvery = 20 * time.Second // at most one OSETH poll per line
	osethIdle      = 3 * time.Second  // no watched line: look again soon
	osethPrevMax   = 2 * time.Minute  // an older previous state proves no movement
)

// osethLine is one line's OSETH state: the last fetched sightings and the ones before them (the
// movement test needs both), plus the last OASTH observations to merge them into.
type osethLine struct {
	cur, prev map[string]oseth.Sighting
	at        time.Time
	oasth     []match.Obs
}

// onLineWithOSETH is the scheduler's callback on a city with the OSETH source: it keeps the
// OASTH observations and folds fresh OSETH sightings in, then publishes as usual.
func (a *App) onLineWithOSETH(p sched.LinePoll) {
	if a.oseth == nil {
		a.onLine(p)
		return
	}
	// One publication at a time: the OSETH path merges into the newest OASTH observations, and
	// neither path publishes a snapshot older than the other's.
	a.osethPub.Lock()
	defer a.osethPub.Unlock()
	now := a.now()
	a.osethMu.Lock()
	l := a.osethLines[p.Line]
	if l == nil {
		l = &osethLine{}
		a.osethLines[p.Line] = l
	}
	l.oasth = slices.Clone(p.Obs)
	var cur map[string]oseth.Sighting
	if now.Sub(l.at) <= osethFresh {
		cur = freshSightings(l.cur, now)
	}
	prev := l.prev
	a.osethMu.Unlock()
	// Merged here too, so the OSETH-only buses do not vanish at each OASTH poll.
	if len(cur) > 0 {
		if w := a.world(); w != nil {
			p.Obs = oseth.Merge(p.Obs, cur, prev, a.osethRouteFor(w, p.Line), osethMinMove)
		}
	}
	a.onLine(p)
}

// freshSightings keeps the sightings fetched in the last osethFresh: a slow poll of many shapes
// must not publish its first answers as new.
func freshSightings(m map[string]oseth.Sighting, now time.Time) map[string]oseth.Sighting {
	out := make(map[string]oseth.Sighting, len(m))
	for id, s := range m {
		if now.Sub(s.At) <= osethFresh {
			out[id] = s
		}
	}
	return out
}

// freshObs drops the cached OASTH observations the scheduler would no longer accept.
func freshObs(obs []match.Obs, now time.Time, staleAfter time.Duration) []match.Obs {
	out := make([]match.Obs, 0, len(obs))
	for _, o := range obs {
		if now.Sub(o.Time) <= staleAfter {
			out = append(out, o)
		}
	}
	return out
}

// osethRun polls the watched lines for OSETH sightings until ctx ends.
func (a *App) osethRun(ctx context.Context) {
	for {
		line, ok := a.osethPick()
		if !ok {
			if !sleepCtx(ctx, osethIdle) {
				return
			}
			continue
		}
		a.osethPollLine(ctx, line)
	}
}

// osethPick is the next line to poll: the watched line whose OSETH data is oldest, round-robin
// from the last one picked, at most once per osethPollEvery.
func (a *App) osethPick() (string, bool) {
	now := a.now()
	var ids []string
	for _, l := range a.sched.Lines() {
		if l.Tier == sched.Watched {
			ids = append(ids, l.ID)
		}
	}
	if len(ids) == 0 {
		return "", false
	}
	a.osethMu.Lock()
	defer a.osethMu.Unlock()
	start := a.osethRR % len(ids)
	best, bestAt := -1, time.Time{}
	for k := range ids {
		i := (start + k) % len(ids)
		last := a.osethLast[ids[i]]
		if !last.IsZero() && now.Sub(last) < osethPollEvery {
			continue
		}
		if best < 0 || last.Before(bestAt) {
			best, bestAt = i, last
		}
	}
	if best < 0 {
		return "", false
	}
	a.osethRR = (best + 1) % len(ids)
	return ids[best], true
}

// osethPollLine fetches the line's shapes and publishes the merge of their sightings with the
// line's last OASTH observations.
func (a *App) osethPollLine(ctx context.Context, line string) {
	w := a.world()
	if w == nil {
		return
	}
	started := a.now()
	a.osethMu.Lock()
	a.osethLast[line] = started
	a.osethMu.Unlock()
	pairs := osethPairs(w.feed, line)
	if len(pairs) == 0 {
		return
	}
	cur := make(map[string]oseth.Sighting, 8)
	failed := 0
	for _, p := range pairs {
		if ctx.Err() != nil {
			return
		}
		vs, err := a.oseth.RouteVehicles(ctx, p[0], p[1])
		if err != nil {
			failed++
			continue
		}
		at := a.now()
		for _, v := range vs {
			cur[v.ID] = oseth.Sighting{Vehicle: v, At: at}
		}
	}
	if failed == len(pairs) {
		return // the source is down: keep the previous state
	}
	a.osethPub.Lock()
	defer a.osethPub.Unlock()
	done := a.now()
	a.osethMu.Lock()
	l := a.osethLines[line]
	if l == nil {
		l = &osethLine{}
		a.osethLines[line] = l
	}
	prev := l.cur
	if done.Sub(l.at) > osethPrevMax {
		prev = nil // a bus parked since then would look like it moved
	}
	l.prev, l.cur, l.at = prev, cur, done
	oasth := freshObs(l.oasth, done, a.cfg.Sched.StaleAfter)
	a.osethMu.Unlock()
	obs := oseth.Merge(oasth, freshSightings(cur, done), prev, a.osethRouteFor(w, line), osethMinMove)
	a.onLine(sched.LinePoll{Line: line, Obs: obs, Started: done, Done: done, Routes: 1})
}

// osethPairs returns the (route_id, shape_id) pairs of the line's trips, unique and sorted. The
// OSETH API takes the GTFS ids of both.
func osethPairs(f *gtfs.Feed, line string) [][2]string {
	seen := map[[2]string]bool{}
	var out [][2]string
	for _, ti := range f.TripsForLine(line) {
		t := f.Trip(ti)
		shape := f.ShapeID(t)
		if shape == "" {
			continue
		}
		p := [2]string{f.Routes[t.Route].ID, shape}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// osethRouteFor maps an OSETH shape id to the OASTH route code of the line's shapes: the filter
// of an OSETH-only vehicle keeps only the lines the OASTH side polls.
func (a *App) osethRouteFor(w *world, line string) func(shapeID string) (routeCode, lineCode string, ok bool) {
	byShape := map[string][2]string{} // shape id -> route code, line code
	for _, r := range a.meta.LineRoutes()[line] {
		for shape := range w.mapper.ShapesFor(line, r.Code) {
			id := w.feed.ShapeIDOf(shape)
			if id == "" {
				continue
			}
			if _, ok := byShape[id]; !ok {
				byShape[id] = [2]string{r.Code, r.LineCode}
			}
		}
	}
	return func(shapeID string) (string, string, bool) {
		r, ok := byShape[shapeID]
		return r[0], r[1], ok
	}
}

// sleepCtx waits d, or returns false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
