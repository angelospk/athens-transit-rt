// Package gtfs holds the subset of OASA's static GTFS needed for realtime matching, in a
// compact form: trips share stop patterns and time profiles, so the whole network fits in a
// few tens of megabytes.
package gtfs

import (
	"slices"
	"sort"
	"time"
	_ "time/tzdata" // the VPS may lack a system zoneinfo database
)

// Athens is the agency time zone; GTFS times count from local midnight of the service day.
var Athens = mustLoad("Europe/Athens")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

type Stop struct {
	ID       string
	Name     string
	Lat, Lon float64
}

type Route struct {
	ID        string
	ShortName string // line number, e.g. "040"; equals the telematics LineID
	LongName  string
	Type      int    // 3 bus, 11 trolley
	Color     string // hex without '#', may be empty
	TextColor string
}

type Shape struct {
	ID     string
	Points [][2]float64 // (lat, lon) in sequence order
}

// Pattern is an ordered stop list shared by every trip that serves exactly those stops.
type Pattern struct {
	Stops []int32 // indices into Feed.Stops
	Seq   []int32 // GTFS stop_sequence of each stop
}

// Profile holds a trip's stop times relative to its first arrival, shared by trips that run
// the same timetable at different hours. Dep is nil when every departure equals its arrival.
type Profile struct {
	Arr []uint16
	Dep []uint16
}

type Service struct {
	ID         string
	Days       [7]bool // Monday..Sunday
	Start, End int32   // yyyymmdd
}

type Trip struct {
	ID       string
	Route    int32
	Service  int32
	Shape    int32 // -1 when the trip has no shape
	Dir      int8
	Headsign int32 // index into Feed.Headsigns
	Pattern  int32
	Profile  int32
	Base     int32 // first arrival, seconds after midnight of the service day
}

// Meta describes where a feed came from.
type Meta struct {
	ETag         string
	LastModified string
	Size         int64
	BuiltAt      int64
}

type Feed struct {
	Meta       Meta
	Stops      []Stop
	Routes     []Route
	Services   []Service
	Exceptions map[int64]int8 // service<<32 | yyyymmdd -> exception_type (1 added, 2 removed)
	Shapes     []Shape
	Patterns   []Pattern
	Profiles   []Profile
	Headsigns  []string
	Trips      []Trip

	stopIdx  map[string]int32
	shapeIdx map[string]int32
	tripIdx  map[string]int32
	byLine   map[string][]int32 // trip indices in file order (upstream iteration order)
	byStart  map[string][]int32 // the same, sorted by start
}

// StopTime is one stop of a trip, in absolute seconds after midnight of the service day.
type StopTime struct {
	Seq      int32
	Stop     int32
	Arr, Dep int32
}

// index builds the lookup tables that are not serialised.
func (f *Feed) index() {
	f.stopIdx = make(map[string]int32, len(f.Stops))
	for i, s := range f.Stops {
		f.stopIdx[s.ID] = int32(i)
	}
	f.shapeIdx = make(map[string]int32, len(f.Shapes))
	for i, s := range f.Shapes {
		f.shapeIdx[s.ID] = int32(i)
	}
	f.tripIdx = make(map[string]int32, len(f.Trips))
	f.byLine = map[string][]int32{}
	for i := range f.Trips {
		t := &f.Trips[i]
		f.tripIdx[t.ID] = int32(i)
		line := f.Routes[t.Route].ShortName
		f.byLine[line] = append(f.byLine[line], int32(i))
	}
	f.byStart = make(map[string][]int32, len(f.byLine))
	for line, trips := range f.byLine {
		sorted := slices.Clone(trips)
		sort.SliceStable(sorted, func(a, b int) bool { return f.Start(&f.Trips[sorted[a]]) < f.Start(&f.Trips[sorted[b]]) })
		f.byStart[line] = sorted
	}
}

// StopIndex returns -1 for an unknown stop id.
func (f *Feed) StopIndex(id string) int32 { return lookup(f.stopIdx, id) }

// ShapeIndex returns -1 for an unknown shape id.
func (f *Feed) ShapeIndex(id string) int32 { return lookup(f.shapeIdx, id) }

// TripIndex returns -1 for an unknown trip id.
func (f *Feed) TripIndex(id string) int32 { return lookup(f.tripIdx, id) }

func lookup(m map[string]int32, id string) int32 {
	if i, ok := m[id]; ok {
		return i
	}
	return -1
}

func (f *Feed) Trip(i int32) *Trip { return &f.Trips[i] }

func (f *Feed) Headsign(t *Trip) string { return f.Headsigns[t.Headsign] }

func (f *Feed) NumStops(t *Trip) int { return len(f.Patterns[t.Pattern].Stops) }

func (f *Feed) StopTime(t *Trip, i int) StopTime {
	p, pr := &f.Patterns[t.Pattern], &f.Profiles[t.Profile]
	arr := t.Base + int32(pr.Arr[i])
	dep := arr
	if pr.Dep != nil {
		dep = t.Base + int32(pr.Dep[i])
	}
	return StopTime{Seq: p.Seq[i], Stop: p.Stops[i], Arr: arr, Dep: dep}
}

// Start is the departure from the first stop.
func (f *Feed) Start(t *Trip) int32 { return f.StopTime(t, 0).Dep }

// End is the arrival at the last stop.
func (f *Feed) End(t *Trip) int32 { return f.StopTime(t, f.NumStops(t)-1).Arr }

// ShapeID returns "" for a trip without a shape.
func (f *Feed) ShapeID(t *Trip) string {
	if t.Shape < 0 {
		return ""
	}
	return f.Shapes[t.Shape].ID
}

// ShapeIDOf returns the id of a shape index, "" for -1.
func (f *Feed) ShapeIDOf(shape int32) string {
	if shape < 0 {
		return ""
	}
	return f.Shapes[shape].ID
}

// Line is the line number (route_short_name) of a trip.
func (f *Feed) Line(t *Trip) string { return f.Routes[t.Route].ShortName }

// TripsForLine returns trip indices of one line number, in GTFS file order.
func (f *Feed) TripsForLine(line string) []int32 { return f.byLine[line] }

// Lines returns every line number with at least one trip, sorted.
func (f *Feed) Lines() []string {
	out := make([]string, 0, len(f.byLine))
	for l := range f.byLine {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}
