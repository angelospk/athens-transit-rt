package gtfs

import (
	"testing"
	"time"
)

func load(t *testing.T) *Feed {
	t.Helper()
	f, err := LoadZip(writeZip(t, sampleFiles()), LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLoadZipBasics(t *testing.T) {
	f := load(t)
	if len(f.Trips) != 3 {
		t.Fatalf("trips = %d, want 3 (single-stop trip dropped)", len(f.Trips))
	}
	if got := f.Stops[f.StopIndex("A")].Name; got != "ΠΕΙΡΑΙΑΣ" {
		t.Fatalf("BOM header not handled: stop A name %q", got)
	}
	t1 := f.Trip(f.TripIndex("t1"))
	if f.Headsign(t1) != "ΣΥΝΤΑΓΜΑ" {
		t.Fatalf("headsign %q not trimmed", f.Headsign(t1))
	}
	var ids []string
	for i := 0; i < f.NumStops(t1); i++ {
		st := f.StopTime(t1, i)
		ids = append(ids, f.Stops[st.Stop].ID)
	}
	if ids[0] != "A" || ids[1] != "B" || ids[2] != "C" {
		t.Fatalf("stop order %v, want A B C", ids)
	}
	if st := f.StopTime(t1, 0); st.Arr != 36000 || st.Dep != 36060 || st.Seq != 1 {
		t.Fatalf("first stop time %+v", st)
	}
	if f.Start(t1) != 36060 || f.End(t1) != 36600 {
		t.Fatalf("start/end %d %d", f.Start(t1), f.End(t1))
	}
	n1 := f.Trip(f.TripIndex("n1"))
	if f.Start(n1) != 24*3600+20*60 {
		t.Fatalf("24:20:00 parsed as %d", f.Start(n1))
	}
	// Shape points sorted by sequence.
	s := f.Shapes[f.ShapeIndex("S1")]
	if s.Points[0][0] != 37.94 || s.Points[2][0] != 37.96 {
		t.Fatalf("shape order %v", s.Points)
	}
	r := f.Routes[t1.Route]
	if r.ShortName != "040" || r.Type != 3 || r.Color != "153CE0" {
		t.Fatalf("route %+v", r)
	}
}

func TestPatternAndProfileDedup(t *testing.T) {
	f := load(t)
	t1, t2 := f.Trip(f.TripIndex("t1")), f.Trip(f.TripIndex("t2"))
	if t1.Pattern != t2.Pattern {
		t.Fatal("same stops should share a pattern")
	}
	if t1.Profile != t2.Profile {
		t.Fatal("same relative times should share a profile")
	}
	if len(f.Patterns) != 2 {
		t.Fatalf("patterns = %d, want 2", len(f.Patterns))
	}
}

func TestServiceActive(t *testing.T) {
	f := load(t)
	svc := f.Trip(f.TripIndex("t1")).Service
	day := func(s string) time.Time {
		d, _ := time.ParseInLocation("2006-01-02", s, Athens)
		return d
	}
	cases := map[string]bool{
		"2026-10-05": true,  // Monday
		"2026-10-06": false, // Tuesday
		"2026-07-13": false, // Monday removed by exception
		"2026-07-15": true,  // Wednesday added by exception
		"2026-10-12": false, // Monday after end_date
	}
	for d, want := range cases {
		if got := f.ServiceActive(svc, day(d)); got != want {
			t.Errorf("%s: active=%v want %v", d, got, want)
		}
	}
	if got := f.FeedEnd().Format("2006-01-02"); got != "2026-10-06" {
		t.Errorf("feed end %s", got)
	}
}

func TestCandidateTrips(t *testing.T) {
	f := load(t)
	// Tuesday 00:30: n1 belongs to Monday's service day and runs 24:20-24:40.
	now := time.Date(2026, 10, 6, 0, 30, 0, 0, Athens)
	var got []string
	for _, c := range f.CandidateTrips("2", now, 15*60, 60*60) {
		got = append(got, f.Trip(c.Trip).ID+"@"+c.Day.Format("0102"))
	}
	if len(got) != 1 || got[0] != "n1@1005" {
		t.Fatalf("candidates %v, want [n1@1005]", got)
	}
	// Monday 10:30: t1 (10:01-10:10, +60 min after) and t2 (11:01, -15 min before? no: 10:46).
	now = time.Date(2026, 10, 5, 10, 30, 0, 0, Athens)
	got = nil
	for _, c := range f.CandidateTrips("040", now, 15*60, 60*60) {
		got = append(got, f.Trip(c.Trip).ID)
	}
	if len(got) != 1 || got[0] != "t1" {
		t.Fatalf("candidates %v, want [t1]", got)
	}
}

func TestLinesFilter(t *testing.T) {
	f, err := LoadZip(writeZip(t, sampleFiles()), LoadOptions{Lines: map[string]bool{"2": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Trips) != 1 || f.Trip(0).ID != "n1" {
		t.Fatalf("filtered trips %d", len(f.Trips))
	}
}
