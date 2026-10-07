package gtfs

import (
	"slices"
	"time"
)

func yyyymmdd(d time.Time) int32 { return int32(d.Year()*10000 + int(d.Month())*100 + d.Day()) }

func fromYYYYMMDD(v int32) time.Time {
	return time.Date(int(v/10000), time.Month(v/100%100), int(v%100), 0, 0, 0, 0, Athens)
}

// Midnight returns 00:00 Athens time of the calendar day of t (in Athens).
func Midnight(t time.Time) time.Time {
	t = t.In(Athens)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Athens)
}

// ServiceDay returns the time reference of the Athens calendar day of t: noon minus 12 h.
// GTFS stop times count from it. It is midnight except on DST change days (then 01:00 or
// 23:00 the evening before), so it must not be fed back into ServiceDay.
func ServiceDay(t time.Time) time.Time {
	t = t.In(Athens)
	return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, Athens).Add(-12 * time.Hour)
}

// dayDate is the calendar date of a ServiceDay reference (its noon).
func dayDate(day time.Time) time.Time { return day.Add(12 * time.Hour).In(Athens) }

// ServiceDate formats a service day as GTFS YYYYMMDD.
func ServiceDate(day time.Time) string { return dayDate(day).Format("20060102") }

// ServiceActive reports whether service runs on the service day `day` (a ServiceDay).
// Outside the feed's validity (an expired feed not yet replaced by OASA), the same weekday
// whole weeks inside it decides (PlanningTime); exceptions apply to their own date only.
func (f *Feed) ServiceActive(service int32, day time.Time) bool {
	date := dayDate(day)
	d := yyyymmdd(date)
	if exc, ok := f.Exceptions[int64(service)<<32|int64(d)]; ok {
		return exc == 1
	}
	s := &f.Services[service]
	wd := (int(date.Weekday()) + 6) % 7 // Monday = 0
	d = yyyymmdd(f.PlanningTime(date))
	return s.Start <= d && d <= s.End && s.Days[wd]
}

// FeedEnd is the last calendar end_date, or the zero time without a calendar.
func (f *Feed) FeedEnd() time.Time {
	var end int32
	for _, s := range f.Services {
		end = max(end, s.End)
	}
	if end == 0 {
		return time.Time{}
	}
	return fromYYYYMMDD(end)
}

// FeedStart is the first calendar start_date, or the zero time without a calendar.
func (f *Feed) FeedStart() time.Time {
	var start int32
	for _, s := range f.Services {
		if start == 0 || s.Start < start {
			start = s.Start
		}
	}
	if start == 0 {
		return time.Time{}
	}
	return fromYYYYMMDD(start)
}

// PlanningTime maps now into the feed's validity by whole weeks (same weekday and wall
// time), so an expired or not yet valid feed still tells which lines usually run now. Inside
// the validity it returns now.
func (f *Feed) PlanningTime(now time.Time) time.Time {
	start, end := f.FeedStart(), f.FeedEnd()
	if start.IsZero() || end.Sub(start) < 6*24*time.Hour {
		return now
	}
	now = now.In(Athens)
	for Midnight(now).After(end) {
		now = now.AddDate(0, 0, -7)
	}
	for Midnight(now).Before(start) {
		now = now.AddDate(0, 0, 7)
	}
	return now
}

// Candidate is a trip whose scheduled run (padded) covers a moment.
type Candidate struct {
	Trip int32
	Day  time.Time // ServiceDay of the service day; stop times count from it
}

// CandidateTrips returns trips of a line whose run, padded by `before` seconds before the
// start and `after` seconds after the end, covers `now`. Today's and yesterday's service days
// are both checked, since GTFS times past 24:00 belong to the previous service day.
func (f *Feed) CandidateTrips(line string, now time.Time, before, after int32) []Candidate {
	var out []Candidate
	today := ServiceDay(now)
	for _, day := range []time.Time{today, ServiceDay(dayDate(today).Add(-24 * time.Hour))} {
		first := len(out)
		secs := int32(now.Sub(day) / time.Second)
		for _, ti := range f.byStart[line] {
			t := &f.Trips[ti]
			start := f.Start(t)
			if start-before > secs {
				break // sorted by start
			}
			if secs <= f.End(t)+after && f.ServiceActive(t.Service, day) {
				out = append(out, Candidate{Trip: ti, Day: day})
			}
		}
		// Report in file order within a day, as upstream does (it decides ties later).
		slices.SortFunc(out[first:], func(a, b Candidate) int { return int(a.Trip - b.Trip) })
	}
	return out
}
