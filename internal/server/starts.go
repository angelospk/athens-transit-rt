package server

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/rail"
)

// The runs of OASA's daily timetable have no trip ids, so the seen flag of a GTFS trip cannot mark
// them. runStarts watches the buses leave instead: a bus seen near the start of its shape and then
// well along it left at its last fix near the start.
const (
	startNearM   = 300.0            // still at the start (waiting, or just pulling out)
	startAwayM   = 600.0            // gone: on its way
	startSwitchM = 1500.0           // the first fix on a late route code this close to the start
	startMaxGap  = 15 * time.Minute // longer between the two fixes is not one departure
	seenEarly    = 3 * time.Minute  // a run left this early still counts
	seenLate     = 20 * time.Minute // ...or this late; after that without a bus it is missed
)

type startKey struct {
	line  string
	shape int32
}

type startState struct {
	key     startKey
	last    time.Time // the newest fix: older ones come late and are dropped
	near    time.Time // last fix near the start; zero once the run is counted
	nearEnd bool      // ...put at the shape's end: a loop, whose end is its start
	from    float64   // s of the first fix after a late route switch; zero when not in that mode
	toEnd   float64   // metres to the shape's end at the newest fix
	left    time.Time // when the run on key left, once counted; zero before
}

// A nil runStarts watches nothing.
type runStarts struct {
	mu    sync.Mutex
	feed  *gtfs.Feed // the feed whose shape indexes the keys are
	day   string     // Athens date of the starts
	since time.Time  // the first fix of the day: what left before it was not watched
	veh   map[string]*startState
	got   map[startKey][]time.Time
}

func newRunStarts() *runStarts { return &runStarts{} }

func (rs *runStarts) reset(feed *gtfs.Feed, day string, t time.Time) {
	rs.feed, rs.day, rs.since = feed, day, t
	rs.veh, rs.got = map[string]*startState{}, map[startKey][]time.Time{}
}

// observe takes one fix of a vehicle: s metres along shape of line (length metres long, of feed),
// at t. A fix of an earlier day than the starts, or older than the vehicle's newest, is dropped.
func (rs *runStarts) observe(feed *gtfs.Feed, vehicle, line string, shape int32, s, length float64, t time.Time) {
	if rs == nil || shape < 0 {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	day := t.In(gtfs.Athens).Format(time.DateOnly)
	switch {
	case feed != rs.feed || day > rs.day:
		rs.reset(feed, day, t)
	case day < rs.day:
		return
	}
	k := startKey{line, shape}
	st := rs.veh[vehicle]
	if st != nil && !t.After(st.last) {
		return
	}
	if st != nil && st.key != k && st.key.line == line && s > startNearM && s <= startSwitchM &&
		st.toEnd <= startSwitchM && t.Sub(st.last) <= startMaxGap {
		// OASA gave the route code late, the bus already at the stand or past it: the run began at
		// the last fix of the old shape (at its end), and the fixes of the new one tell when it left.
		rs.veh[vehicle] = &startState{key: k, last: t, near: st.last, from: s, toEnd: length - s}
		return
	}
	if st == nil || st.key != k {
		st = &startState{key: k}
		rs.veh[vehicle] = st
	}
	st.last, st.toEnd = t, length-s
	if st.from > 0 {
		if s < st.from+startNearM {
			st.near = t // still at the stand
			return
		}
		if !st.near.IsZero() && t.Sub(st.near) <= startMaxGap {
			rs.got[k] = append(rs.got[k], st.near)
			st.left = st.near
		}
		st.near, st.from = time.Time{}, 0
		return
	}
	switch {
	case s <= startNearM:
		st.near, st.nearEnd, st.left = t, false, time.Time{} // at a start: the last run is over
	case s >= startAwayM && !st.near.IsZero() && (!st.nearEnd || s <= length/2):
		// From the end, only a bus at the line's start again left: one turning back has the end behind.
		if t.Sub(st.near) <= startMaxGap {
			rs.got[k] = append(rs.got[k], st.near)
			st.left = st.near
		}
		st.near = time.Time{}
	case length >= 2*startAwayM && s >= length-startNearM:
		st.near, st.nearEnd = t, true // the run's end, or a loop's next start: the caller tells (departed)
	case s >= startAwayM:
		st.near = time.Time{} // turned back from the end
	}
}

// atTerminus tells whether every place a fix may be on a shape (length metres) is at one of its
// ends: on a loop the start and the end are one place, so the fix is at the start, not ambiguous.
func atTerminus(cands []geo.Cand, length float64) bool {
	atStart := false
	for _, c := range cands {
		if c.Along > startNearM && c.Along < length-startNearM {
			return false
		}
		atStart = atStart || c.Along <= startNearM
	}
	return atStart
}

// of returns the starts of a shape of line (of feed) today (the Athens day of now) in time order,
// and since when the starts were watched.
func (rs *runStarts) of(feed *gtfs.Feed, line string, shape int32, now time.Time) ([]time.Time, time.Time) {
	if rs == nil {
		return nil, now
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.feed != feed || rs.day != now.In(gtfs.Athens).Format(time.DateOnly) {
		return nil, now
	}
	got := append([]time.Time(nil), rs.got[startKey{line, shape}]...)
	sort.Slice(got, func(i, j int) bool { return got[i].Before(got[j]) })
	return got, rs.since
}

// departed returns the start of the vehicle's last run (of feed, today): its line and shape, when
// it left, and whether the bus is now at the shape's end (there a loop's next run begins, so the
// start is of the current run only while the bus is matched near its trip's end). Not known
// before it left, once back at the start, after it changed shape, or on another day.
func (rs *runStarts) departed(feed *gtfs.Feed, vehicle string, now time.Time) (key startKey, left time.Time, atEnd, ok bool) {
	if rs == nil {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	st := rs.veh[vehicle]
	if rs.feed != feed || rs.day != now.In(gtfs.Athens).Format(time.DateOnly) || st == nil || st.left.IsZero() {
		return
	}
	return st.key, st.left, st.nearEnd && !st.near.IsZero(), true
}

// pairStarts pairs the runs of one direction on day (Athens midnight) with the starts of its
// buses (sorted): in time order, a run due by now takes the first unused start from seenEarly
// before to seenLate after its time. It returns the time of each run (zero without "HH:MM") and
// the start it took (-1: none).
func pairStarts(deps []ScheduleDeparture, day time.Time, starts []time.Time, now time.Time) ([]time.Time, []int) {
	times, took := make([]time.Time, len(deps)), make([]int, len(deps))
	used := make([]bool, len(starts))
	for i := range deps {
		took[i] = -1
		m, ok := minutesOf(deps[i].T)
		if !ok {
			continue
		}
		t := time.Date(day.Year(), day.Month(), day.Day(), 0, m, 0, 0, day.Location()) // wall clock: DST days too
		times[i] = t
		if t.After(now) {
			continue
		}
		for k, s := range starts {
			if used[k] || s.Before(t.Add(-seenEarly)) {
				continue
			}
			if s.After(t.Add(seenLate)) {
				break
			}
			used[k], took[i] = true, k
			break
		}
	}
	return times, took
}

// markSeen sets the seen flags of the runs of one direction on day (Athens midnight) from the
// starts of its buses (sorted, paired by pairStarts). A run without one is missed once seenLate
// has passed, and unknown before that or when it may have left before since (not watched).
func markSeen(deps []ScheduleDeparture, day time.Time, starts []time.Time, since, now time.Time) {
	times, took := pairStarts(deps, day, starts, now)
	for i := range deps {
		deps[i].Seen = nil
		t := times[i]
		if t.IsZero() || t.After(now) {
			continue
		}
		seen := took[i] >= 0
		if seen || (!t.Add(-seenEarly).Before(since) && !now.Before(t.Add(seenLate))) {
			deps[i].Seen = &seen
		}
	}
}

// plannedOf returns the time of the run that start k was paired with (pairStarts), when no run
// without a bus that could have taken it (pairStarts' window) is nearer to it: with buses minutes
// apart a late bus could be either run.
func plannedOf(times []time.Time, took []int, starts []time.Time, k int) (time.Time, bool) {
	at := starts[k]
	run := slices.Index(took, k)
	if run < 0 {
		return time.Time{}, false
	}
	d := times[run].Sub(at).Abs()
	for i, t := range times {
		if took[i] < 0 && !t.IsZero() && !at.Before(t.Add(-seenEarly)) && !at.After(t.Add(seenLate)) && t.Sub(at).Abs() < d {
			return time.Time{}, false
		}
	}
	return times[run], true
}

// minutesOf reads "HH:MM" (hours past 24 for runs after midnight).
func minutesOf(hhmm string) (int, bool) {
	h, m, ok := strings.Cut(hhmm, ":")
	if !ok {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return hh*60 + mm, true
}

var bracketed = regexp.MustCompile(`\([^)]*\)`)

// latinGreek maps the Latin letters that look like a Greek one: OASA writes some names with a
// Latin X or H. Lower case, as rail.Fold leaves them.
var latinGreek = map[rune]rune{
	'a': 'α', 'b': 'β', 'e': 'ε', 'h': 'η', 'i': 'ι', 'k': 'κ', 'm': 'μ',
	'n': 'ν', 'o': 'ο', 'p': 'ρ', 't': 'τ', 'x': 'χ', 'y': 'υ', 'z': 'ζ',
}

// endFold folds a folded name for the end comparison: no spaces, dots or hyphens, and the
// look-alike letters as Greek, so "ΑΓ. ΚΟΣΜΑΣ" and "ΑΓ.ΚΟΣΜΑΣ" are one name.
func endFold(s string) string {
	var b strings.Builder
	for _, r := range s {
		if g, ok := latinGreek[r]; ok {
			r = g
		}
		if r != ' ' && r != '.' && r != '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// endMatches tells whether a GTFS headsign ("START - VIA - END") ends at the place a daily
// direction is named after; bracketed notes do not count.
func endMatches(gtfsHeadsign, daily string) bool {
	end := bracketed.ReplaceAllString(gtfsHeadsign, "")
	if i := strings.LastIndex(end, " - "); i >= 0 {
		end = end[i+3:]
	}
	daily = bracketed.ReplaceAllString(daily, "")
	a := endFold(rail.Fold(end))
	b := endFold(rail.Fold(daily))
	if a != "" && b != "" && (strings.Contains(a, b) || strings.Contains(b, a)) {
		return true
	}
	wa, wb := endWords(end), endWords(daily)
	return wordsIn(wa, wb) || wordsIn(wb, wa)
}

// endWord is one word of a place name, folded as endFold; abbr when it ended in a dot.
type endWord struct {
	w    string
	abbr bool
}

// endWords splits a place name into words at spaces, hyphens and dots ("ΣΤ.ΝΙΚΑΙΑ" is two).
func endWords(s string) []endWord {
	var out []endWord
	var b strings.Builder
	flush := func(abbr bool) {
		if b.Len() > 0 {
			out = append(out, endWord{endFold(b.String()), abbr})
			b.Reset()
		}
	}
	for _, r := range rail.Fold(s) {
		switch r {
		case '.':
			flush(true)
		case ' ', '-':
			flush(false)
		default:
			b.WriteRune(r)
		}
	}
	flush(false)
	return out
}

// wordsIn tells whether short appears as consecutive words of long, an abbreviated word on either
// side standing for any word it begins ("ΣΤ." for "ΣΤΑΘΜΟΣ", #21); at least one pair must be
// whole words, so "ΣΤΑΘΜΟΣ" alone is not in "ΣΤ. ΚΗΦΙΣΙΑΣ".
func wordsIn(short, long []endWord) bool {
	for i := 0; len(short) > 0 && i+len(short) <= len(long); i++ {
		whole, ok := false, true
		for j, x := range short {
			y := long[i+j]
			switch {
			case x.w == y.w && !x.abbr && !y.abbr:
				whole = true
			case x.abbr && strings.HasPrefix(y.w, x.w), y.abbr && strings.HasPrefix(x.w, y.w):
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok && whole {
			return true
		}
	}
	return false
}

// shapesOf returns the shapes whose GTFS headsign ends where the daily headsign does. A loop has
// every shape: the daily timetable names no place to match.
func shapesOf(shapes map[int32]string, headsign string, loop bool) []int32 {
	var out []int32
	for shape, head := range shapes {
		if loop || endMatches(head, headsign) {
			out = append(out, shape)
		}
	}
	return out
}

// lineShapes maps each shape of the line to the headsign of its first trip on it.
func lineShapes(f *gtfs.Feed, line string) map[int32]string {
	shapes := map[int32]string{}
	for _, ti := range f.TripsForLine(line) {
		t := f.Trip(ti)
		if t.Shape >= 0 {
			if _, ok := shapes[t.Shape]; !ok {
				shapes[t.Shape] = f.Headsign(t)
			}
		}
	}
	return shapes
}

// markDailySeen sets the seen flags of today's daily timetable of line from the buses that left
// the start of a shape ending where the direction does.
func (a *App) markDailySeen(f *gtfs.Feed, line string, resp *ScheduleResponse, today time.Time, circular bool) {
	now := a.now()
	shapes := lineShapes(f, line)
	loop := circular || len(shapes) == 1 && len(resp.Directions) == 1 // one shape, one direction: a loop too
	for i := range resp.Directions {
		d := &resp.Directions[i]
		var starts []time.Time
		since := now
		for _, shape := range shapesOf(shapes, d.Headsign, loop) {
			got, s := a.starts.of(f, line, shape, now)
			starts = append(starts, got...)
			since = s
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
		markSeen(d.Departures, today, starts, since, now)
	}
}
