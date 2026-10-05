package gtfs

import "time"

func yyyymmdd(d time.Time) int32 { return int32(d.Year()*10000 + int(d.Month())*100 + d.Day()) }

func fromYYYYMMDD(v int32) time.Time {
	return time.Date(int(v/10000), time.Month(v/100%100), int(v%100), 0, 0, 0, 0, Athens)
}

// Midnight returns 00:00 Athens time of the calendar day of t (in Athens).
func Midnight(t time.Time) time.Time {
	t = t.In(Athens)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Athens)
}

// ServiceDate formats a service day as GTFS YYYYMMDD.
func ServiceDate(day time.Time) string { return day.In(Athens).Format("20060102") }

// ServiceActive reports whether service runs on the Athens calendar day of `day`.
func (f *Feed) ServiceActive(service int32, day time.Time) bool {
	d := yyyymmdd(day.In(Athens))
	if exc, ok := f.Exceptions[int64(service)<<32|int64(d)]; ok {
		return exc == 1
	}
	s := &f.Services[service]
	wd := (int(day.In(Athens).Weekday()) + 6) % 7 // Monday = 0
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

// Candidate is a trip whose scheduled run (padded) covers a moment.
type Candidate struct {
	Trip int32
	Day  time.Time // Athens midnight of the service day; stop times count from it
}

// CandidateTrips returns trips of a line whose run, padded by `before` seconds before the
// start and `after` seconds after the end, covers `now`. Today's and yesterday's service days
// are both checked, since GTFS times past 24:00 belong to the previous service day.
func (f *Feed) CandidateTrips(line string, now time.Time, before, after int32) []Candidate {
	var out []Candidate
	today := Midnight(now)
	for _, day := range []time.Time{today, Midnight(today.Add(-12 * time.Hour))} {
		secs := int32(now.Sub(day) / time.Second)
		for _, ti := range f.byLine[line] {
			t := &f.Trips[ti]
			start := f.Start(t)
			if start-before > secs {
				break // sorted by start
			}
			if secs <= f.End(t)+after && f.ServiceActive(t.Service, day) {
				out = append(out, Candidate{Trip: ti, Day: day})
			}
		}
	}
	return out
}
