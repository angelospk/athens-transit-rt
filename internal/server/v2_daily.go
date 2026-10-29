package server

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// OASA's daily schedule has no trip ids: a departure is placed by the GTFS trip of its
// direction that starts nearest its time, whose stop offsets give the times at the stops.

// dailyRun is one departure of a daily schedule and the GTFS trip that stands for it.
type dailyRun struct {
	m    int   // minutes past midnight, past 24 h for runs after midnight
	trip int32 // the line's trip whose start is nearest m
}

// dailyRuns pairs every departure of the daily schedule with the trip that stands for it, in
// time order.
func dailyRuns(f *gtfs.Feed, line, date string, d telematics.DailySchedule, day time.Time) []dailyRun {
	shapes := lineShapes(f, line)
	resp := dailyToSchedule(line, date, d)
	loop := d.Circular || len(shapes) == 1 && len(resp.Directions) == 1 // one shape, one direction: a loop too
	var out []dailyRun
	for _, dr := range resp.Directions {
		cands := dailyCandidates(f, line, shapesOf(shapes, dr.Headsign, loop), day)
		for _, dep := range dr.Departures {
			m, ok := minutesOf(dep.T)
			if !ok {
				continue
			}
			if ti := nearestTrip(f, cands, m); ti >= 0 {
				out = append(out, dailyRun{m: m, trip: ti})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].m < out[j].m })
	return out
}

// dailyCandidates is the line's trips on the shapes of a direction: those that run on day when
// there are some, else all of them.
func dailyCandidates(f *gtfs.Feed, line string, on []int32, day time.Time) []int32 {
	var active, all []int32
	for _, ti := range f.TripsForLine(line) {
		t := f.Trip(ti)
		if !slices.Contains(on, t.Shape) {
			continue
		}
		all = append(all, ti)
		if f.ServiceActive(t.Service, day) {
			active = append(active, ti)
		}
	}
	if len(active) > 0 {
		return active
	}
	return all
}

// nearestTrip is the trip of cands whose start is nearest minute m of the day; the first one
// wins a tie, -1 for none.
func nearestTrip(f *gtfs.Feed, cands []int32, m int) int32 {
	best := int32(-1)
	for _, ti := range cands {
		if best < 0 || nearerStart(f.Start(f.Trip(ti)), m, f.Start(f.Trip(best))) {
			best = ti
		}
	}
	return best
}

// nearerStart tells whether a start is nearer minute m of the day than b.
func nearerStart(a int32, m int, b int32) bool {
	x, y := int64(a)-int64(m)*60, int64(b)-int64(m)*60
	if x < 0 {
		x = -x
	}
	if y < 0 {
		y = -y
	}
	return x < y
}

// dailyAt is the wall clock of a departure at minute m of the Athens day of today.
func dailyAt(today time.Time, m int) time.Time {
	return time.Date(today.Year(), today.Month(), today.Day(), 0, m, 0, 0, gtfs.Athens) // DST days too
}

// dailyNative is a daily run's native id: the trip that stands for it and its departure.
func dailyNative(t *gtfs.Trip, m int) string {
	return fmt.Sprintf("%s@%02d%02d", t.ID, m/60, m%60)
}

// dailyMinutes reads the "HHMM" of a daily run's native id (hours past 24 for runs after
// midnight).
func dailyMinutes(hhmm string) (int, bool) {
	if len(hhmm) != 4 || strings.Trim(hhmm, "0123456789") != "" {
		return 0, false
	}
	h, m := int(hhmm[0]-'0')*10+int(hhmm[1]-'0'), int(hhmm[2]-'0')*10+int(hhmm[3]-'0')
	return h*60 + m, true
}

// dailyArrivals is the line's daily runs that have not left yet and pass the stop within horizon
// (the first pass, not as the last stop): OASA's departure plus the stop's offset in the trip
// that stands for the run. A bus waiting at its first stop makes the next departure of its
// variant (waiting counts them, by line and shape): that row is the live one, once.
func dailyArrivals(f *gtfs.Feed, line string, d telematics.DailySchedule, stop int32, now time.Time,
	horizon time.Duration, waiting map[[2]string]int) []StopArrival {
	day, today := gtfs.ServiceDay(now), gtfs.Midnight(now)
	var out []StopArrival
	for _, r := range dailyRuns(f, line, today.Format(time.DateOnly), d, day) {
		t := f.Trip(r.trip)
		dep := dailyAt(today, r.m)
		if dep.Before(now) {
			continue
		}
		if t.Shape >= 0 {
			k := [2]string{line, f.ShapeID(t)}
			if waiting[k] > 0 {
				waiting[k]--
				continue
			}
		}
		stops := f.Patterns[t.Pattern].Stops
		j := slices.Index(stops[:len(stops)-1], stop)
		if j < 0 {
			continue
		}
		at := dep.Unix() + int64(f.StopTime(t, j).Arr) - int64(f.Start(t))
		if at > now.Add(horizon).Unix() {
			continue
		}
		row := StopArrival{Line: line, Headsign: f.Headsign(t), TripID: ptr(dailyNative(t, r.m)), At: at,
			Scheduled: ptr(at), Source: "schedule", day: day}
		if t.Shape >= 0 {
			row.Variant = f.Shapes[t.Shape].ID
		}
		out = append(out, row)
	}
	return out
}
