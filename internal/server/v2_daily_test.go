package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/model"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// expiredMonday is a Monday after the test feed's calendar ended (2026-10-06).
var expiredMonday = time.Date(2026, 10, 12, 10, 20, 0, 0, gtfs.Athens)

// dailyApp is an app at now whose OASA daily schedule of line L is d, fetched into the cache
// (as the prefetch does). calls counts the fetches.
func dailyApp(t *testing.T, now time.Time, d telematics.DailySchedule) (*App, *int) {
	t.Helper()
	a := oasaOnly(newApp(t, now))
	calls := new(int)
	a.daily = func(context.Context, string) (telematics.DailySchedule, error) {
		*calls++
		return d, nil
	}
	if _, ok := a.dailyOfLine(context.Background(), "L", gtfs.Midnight(now)); !ok {
		t.Fatal("daily schedule not cached")
	}
	return a, calls
}

func oct12(h, m int) int64 { return time.Date(2026, 10, 12, h, m, 0, 0, gtfs.Athens).Unix() }

// On an expired GTFS the runs that have not left come from OASA's daily schedule: its departure
// plus the stop's offset from the start in the GTFS trip of that direction nearest in time.
func TestV2StopArrivalsFromDaily(t *testing.T) {
	a, calls := dailyApp(t, expiredMonday, telematics.DailySchedule{Descr: "WEST - EAST",
		Go: []string{"10:12", "10:40", "11:30", "13:00"}})
	poll(a, expiredMonday)
	var s v2Stop
	v2get(t, a, "/v2/stops/"+esc("oasa:S3"), &s)
	if len(s.Arrivals) != 3 || s.Arrivals[0].Source != model.SourceLive {
		t.Fatalf("arrivals %s", asJSON(s.Arrivals))
	}
	// 10:12 has left (its bus, if any, is live); 13:00 is past the 2 h horizon. S3 is 15 min
	// from the start.
	for i, want := range []model.Arrival{
		{Line: "oasa:L", Stop: "oasa:S3", Headsign: "EAST", At: oct12(10, 55), ScheduledAt: model.Ptr(oct12(10, 55)),
			Source: model.SourceTable, Status: model.Scheduled, Run: model.Ptr("oasa:run:T04@1040:2026-10-12")},
		{Line: "oasa:L", Stop: "oasa:S3", Headsign: "EAST", At: oct12(11, 45), ScheduledAt: model.Ptr(oct12(11, 45)),
			Source: model.SourceTable, Status: model.Scheduled, Run: model.Ptr("oasa:run:T05@1130:2026-10-12")},
	} {
		if got := s.Arrivals[i+1]; !reflect.DeepEqual(got, want) {
			t.Fatalf("row %d\n got %s\nwant %s", i+1, asJSON(got), asJSON(want))
		}
	}
	if stateOf(s.Sources, "oasa") != model.OK {
		t.Fatalf("sources %s", asJSON(s.Sources))
	}
	if *calls != 1 {
		t.Fatalf("OASA asked %d times", *calls)
	}
	// /v1 is unchanged: no timetable rows on an expired GTFS.
	if sr, _ := stopBody(t, a, "S3"); len(sr.Arrivals) != 1 {
		t.Fatalf("v1 %s", asJSON(sr.Arrivals))
	}
}

// A bus waiting at the start will make the next departure: that run is listed once, live.
func TestV2StopArrivalsFromDailyWaitingBus(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 25, 0, 0, gtfs.Athens)
	a, _ := dailyApp(t, now, telematics.DailySchedule{Descr: "WEST - EAST", Go: []string{"10:30", "10:50"}})
	pollOne(a, now, "w", 23.7001, now.Add(-10*time.Second))
	var s v2Stop
	v2get(t, a, "/v2/stops/"+esc("oasa:S2"), &s)
	if len(s.Arrivals) != 2 || s.Arrivals[0].Source != model.SourceLive || s.Arrivals[1].At != oct12(11, 0) {
		t.Fatalf("arrivals %s", asJSON(s.Arrivals))
	}
}

// Without the line's daily schedule in the cache: no OASA call from the request, the GTFS
// behaviour of today (no timetable rows when expired) and the Source indicative.
func TestV2StopNoDailyCached(t *testing.T) {
	a := oasaOnly(newApp(t, expiredMonday))
	calls := 0
	a.daily = func(context.Context, string) (telematics.DailySchedule, error) {
		calls++
		return telematics.DailySchedule{Descr: "WEST - EAST", Go: []string{"10:40"}}, nil
	}
	poll(a, expiredMonday)
	var s v2Stop
	v2get(t, a, "/v2/stops/"+esc("oasa:S3"), &s)
	if len(s.Arrivals) != 1 || s.Arrivals[0].Source != model.SourceLive || stateOf(s.Sources, "oasa") != model.Indicative {
		t.Fatalf("%s", asJSON(s))
	}
	if calls != 0 {
		t.Fatalf("OASA asked %d times", calls)
	}
	var ls v2Lines
	if v2get(t, a, "/v2/lines", &ls); stateOf(ls.Sources, "oasa") != model.Indicative {
		t.Fatalf("lines %s", asJSON(ls.Sources))
	}
}

// indicative only when the GTFS expired and the line has no daily schedule today.
func TestV2StateWithDaily(t *testing.T) {
	a, _ := dailyApp(t, expiredMonday, telematics.DailySchedule{Descr: "WEST - EAST", Go: []string{"10:40"}})
	poll(a, expiredMonday)
	var ls v2Lines
	v2get(t, a, "/v2/lines", &ls)
	var d v2LineDetail
	v2get(t, a, "/v2/lines/"+esc("oasa:L"), &d)
	if stateOf(ls.Sources, "oasa") != model.OK || stateOf(d.Sources, "oasa") != model.OK {
		t.Fatalf("lines %s line %s", asJSON(ls.Sources), asJSON(d.Sources))
	}
}

// A daily run's Run: its stops at the daily departure plus the GTFS offsets.
func TestV2RunFromDaily(t *testing.T) {
	a, _ := dailyApp(t, expiredMonday, telematics.DailySchedule{Descr: "WEST - EAST", Go: []string{"10:40", "11:30"}})
	poll(a, expiredMonday)
	var r struct {
		Run     *model.Run          `json:"run"`
		Sources []model.SourceState `json:"sources"`
	}
	if _, ma := v2get(t, a, "/v2/runs/"+esc("oasa:run:T05@1130:2026-10-12"), &r); ma != 15 || r.Run == nil {
		t.Fatalf("%d %s", ma, asJSON(r))
	}
	run := r.Run
	if run.ID != "oasa:run:T05@1130:2026-10-12" || run.Line != "oasa:L" || run.Headsign != "EAST" || *run.Pattern != "oasa:SH" ||
		run.Vehicle != nil || run.Status != model.Scheduled || len(run.Stops) != 5 || stateOf(r.Sources, "oasa") != model.OK {
		t.Fatalf("%s", asJSON(r))
	}
	for i, s := range run.Stops {
		at := oct12(11, 30+5*i)
		if s.Stop != "oasa:S"+itoa(i) || s.At != at || *s.ScheduledAt != at || s.Passed || s.Source != model.SourceTable ||
			s.DelayS != nil {
			t.Fatalf("stop %d %s", i, asJSON(s))
		}
	}
	// Not a departure of today's daily schedule, another day, or another trip: unknown.
	for _, id := range []string{"oasa:run:T05@1131:2026-10-12", "oasa:run:T05@1130:2026-10-13",
		"oasa:run:NOPE@1130:2026-10-12", "oasa:run:T05@:2026-10-12", "oasa:run:T05@xx30:2026-10-12"} {
		if res, body := get(t, a, "/v2/runs/"+esc(id)); res.StatusCode != 404 {
			t.Fatalf("%s: %d %s", id, res.StatusCode, body)
		}
	}
}

// The waiting bus leaves at the daily departure it makes, not at its expired GTFS trip's: T03
// starts 10:30 in the GTFS, OASA's run 10:40, S2 ten minutes on.
func TestV2WaitingBusLeavesAtDailyDeparture(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 25, 0, 0, gtfs.Athens)
	a, _ := dailyApp(t, now, telematics.DailySchedule{Descr: "WEST - EAST", Go: []string{"10:40", "10:55"}})
	pollOne(a, now, "w", 23.7001, now.Add(-10*time.Second))
	var s v2Stop
	v2get(t, a, "/v2/stops/"+esc("oasa:S2"), &s)
	if len(s.Arrivals) != 2 || s.Arrivals[0].Source != model.SourceLive || s.Arrivals[0].At != oct12(10, 50) ||
		*s.Arrivals[0].ScheduledAt != oct12(10, 50) || s.Arrivals[1].At != oct12(11, 5) {
		t.Fatalf("arrivals %s", asJSON(s.Arrivals))
	}
	// Its Run says the same.
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc(*s.Arrivals[0].Run), &r)
	if r.Run == nil || r.Run.Vehicle == nil || *r.Run.Vehicle != "oasa:w" || r.Run.Stops[2].At != oct12(10, 50) ||
		*r.Run.Stops[2].ScheduledAt != oct12(10, 50) || r.Run.Stops[0].At != oct12(10, 40) {
		t.Fatalf("run %s", asJSON(r.Run))
	}
}
