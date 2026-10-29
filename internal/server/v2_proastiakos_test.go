package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/model"
	"github.com/angelospk/athens-transit-rt/internal/rail"
)

// Station ids of the embedded corridor table (internal/railgeo/suburban.json), KIATO corridor
// in its order: Πειραιάς, Λεύκα, Ρέντης, Ταύρος, Ρουφ, ...
const (
	stPir   = "6928c3c150ab5bc72b39cee6"
	stLefka = "6928c3c050ab5bc72b39cd93"
	stRen   = "6928c3c050ab5bc72b39cd95"
	stTav   = "6928c3c050ab5bc72b39cd98"
	stRouf  = "6928c3c150ab5bc72b39d041"
	stAir   = "6928c3c150ab5bc72b39ced8"
	rentis  = "proastiakos:" + stRen
)

type fakeSubBoard struct {
	list    []rail.SuburbanSchedule
	at      time.Time
	ok      bool
	failing bool
	primary map[string]string
	fetches int
}

func (f *fakeSubBoard) Board(context.Context, time.Time) ([]rail.SuburbanSchedule, time.Time, bool) {
	f.fetches++
	return f.list, f.at, f.ok
}
func (f *fakeSubBoard) CachedBoard(time.Time) ([]rail.SuburbanSchedule, time.Time, bool) {
	return f.list, f.at, f.ok
}
func (f *fakeSubBoard) BoardFailing() bool { return f.failing }
func (f *fakeSubBoard) StationPrimary(id string) (string, bool) {
	p, ok := f.primary[id]
	return p, ok
}
func (f *fakeSubBoard) Stations(context.Context) ([]rail.Station, bool) { return nil, true }

type fakeTrains struct {
	snap   *rail.SuburbanVehicles
	opened int
	open   bool
	last   time.Time
}

func (f *fakeTrains) Vehicles() *rail.SuburbanVehicles { f.opened++; return f.snap }
func (f *fakeTrains) Peek() *rail.SuburbanVehicles     { return f.snap }
func (f *fakeTrains) Health() (bool, time.Time)        { return f.open, f.last }

func athens(h, m int) time.Time { return time.Date(2026, 10, 28, h, m, 0, 0, gtfs.Athens) }

func bstop(id, name, role string, dep time.Time) rail.SuburbanBoardStop {
	s := rail.SuburbanBoardStop{StationID: id, Name: name, Role: role}
	if role == "destination" {
		s.Arr = dep
	} else {
		s.Dep = dep
	}
	return s
}

// The board of Wed 2026-10-28 around 10:00 (wed1000).
func subBoard() []rail.SuburbanSchedule {
	ren := bstop(stRen, "Ρέντης", "stop", athens(10, 5))
	ren.Platform = model.Ptr("2")
	return []rail.SuburbanSchedule{
		{Train: "2539", Corridor: "KIATO", Status: "in_progress", Destination: "Ρουφ", Delay: model.Ptr(3),
			Stops: []rail.SuburbanBoardStop{bstop(stPir, "Πειραιάς", "origin", athens(9, 50)),
				bstop(stLefka, "Λεύκα", "stop", athens(9, 55)), ren,
				bstop(stTav, "Ταύρος", "stop", athens(10, 10)), bstop(stRouf, "Ρουφ", "destination", athens(10, 20))}},
		{Train: "1500", Corridor: "KIATO", Status: "scheduled", Destination: "Ρουφ", Delay: model.Ptr(0),
			Stops: []rail.SuburbanBoardStop{bstop(stPir, "Πειραιάς", "origin", athens(10, 30)),
				bstop(stRen, "Ρέντης", "stop", athens(10, 40)), bstop(stRouf, "Ρουφ", "destination", athens(10, 55))}},
		{Train: "1600", Corridor: "KIATO", Status: "cancelled", Destination: "Ρουφ",
			Stops: []rail.SuburbanBoardStop{bstop(stRen, "Ρέντης", "origin", athens(10, 15)),
				bstop(stRouf, "Ρουφ", "destination", athens(10, 25))}},
		{Train: "1700", Corridor: "ATHLIA", Status: "scheduled", Destination: "Λιανοκλάδι",
			Stops: []rail.SuburbanBoardStop{bstop(stRen, "Ρέντης", "origin", athens(10, 20)),
				bstop("X", "Λιανοκλάδι", "destination", athens(13, 0))}},
		{Train: "1800", Corridor: "KIATO", Status: "scheduled", Destination: "Ρουφ",
			Stops: []rail.SuburbanBoardStop{bstop(stRen, "Ρέντης", "origin", athens(12, 30)),
				bstop(stRouf, "Ρουφ", "destination", athens(12, 40))}},
		{Train: "1900", Corridor: "KIATO", Status: "scheduled", Destination: "Ρουφ",
			Stops: []rail.SuburbanBoardStop{bstop("ALIAS-REN", "Ρέντης", "origin", athens(10, 45)),
				bstop(stRouf, "Ρουφ", "destination", athens(10, 55))}},
		{Train: "2000", Corridor: "KIATO", Status: "in_progress", Destination: "Πειραιάς",
			Stops: []rail.SuburbanBoardStop{bstop(stRouf, "Ρουφ", "origin", athens(10, 0)),
				bstop(stTav, "Ταύρος", "stop", athens(10, 5)), bstop(stRen, "Ρέντης", "stop", athens(10, 12)),
				bstop(stPir, "Πειραιάς", "destination", athens(10, 25))}},
	}
}

// The stream at 10:00: 2539 between Λεύκα and Ρέντης going east, 2000 between Ταύρος and Ρέντης
// going west with no next station, a freight train and a train far off any track.
func subTrains(now time.Time) *rail.SuburbanVehicles {
	at := now.Add(-10 * time.Second).Unix()
	return &rail.SuburbanVehicles{UpdatedAt: now.Unix(), Trains: []rail.SuburbanTrain{
		{Train: "2539", Corridor: "KIATO", Service: "Suburban", Lat: 37.95888, Lon: 23.66121, Course: model.Ptr(60.0),
			SpeedKmh: 36, DelayMin: model.Ptr(4), To: "Ρουφ", Next: "Άγιος Ιωάννης Ρέντης (Α.Ι.Ρ)", NextID: stRen,
			PositionAt: at},
		{Train: "2000", Corridor: "KIATO", Service: "Suburban", Lat: 37.96557, Lon: 23.68094, Course: model.Ptr(250.0),
			SpeedKmh: 54, To: "Πειραιάς", PositionAt: at},
		{Train: "9001", Corridor: "E85", Service: "Freight", Lat: 38.0, Lon: 23.7, PositionAt: at},
		{Train: "3000", Corridor: "CHALKIDA", Service: "Suburban", Lat: 38.6, Lon: 23.2, PositionAt: at},
	}}
}

func proastiakosApp(t *testing.T, now time.Time) (*App, *fakeSubBoard, *fakeTrains) {
	t.Helper()
	a := newAppConfig(t, now, Config{})
	fb := &fakeSubBoard{list: subBoard(), at: now.Add(-2 * time.Minute), ok: true,
		primary: map[string]string{"ALIAS-REN": stRen, stRen: stRen}}
	ft := &fakeTrains{snap: subTrains(now), open: true, last: now.Add(-time.Second)}
	ad := &proastiakosAdapter{a: a, board: fb, trains: ft}
	for i, x := range a.adapters {
		if x.Namespace() == "proastiakos" {
			a.adapters[i] = ad
		}
	}
	return a, fb, ft
}

func TestV2ProastiakosLinesAndStops(t *testing.T) {
	a, _, _ := proastiakosApp(t, wed1000)
	var ls v2Lines
	v2get(t, a, "/v2/lines", &ls)
	byID := map[string]model.Line{}
	for _, l := range ls.Lines {
		byID[l.ID] = l
	}
	k := byID["proastiakos:KIATO"]
	if k.Mode != model.Suburban || k.Label != nil || k.Name != "Πειραιάς – Κιάτο" || k.NameEn == nil ||
		*k.NameEn != "Piraeus – Kiato" || k.Color != "#D9822B" || k.TextColor != "#ffffff" || k.Source != "proastiakos" {
		t.Fatalf("KIATO %s", asJSON(k))
	}
	if byID["proastiakos:CHALKIDA"].NameEn == nil || byID["proastiakos:PIRAIR"].NameEn == nil {
		t.Fatalf("lines %s", asJSON(ls.Lines))
	}
	if stateOf(ls.Sources, "proastiakos") != model.OK {
		t.Fatalf("sources %s", asJSON(ls.Sources))
	}

	var d v2LineDetail
	v2get(t, a, "/v2/lines/"+esc("proastiakos:kiato"), &d)
	if d.Line == nil || d.Line.ID != "proastiakos:KIATO" || len(d.Patterns) != 2 || len(d.Stops) != 20 {
		t.Fatalf("detail %s", asJSON(d))
	}
	p0, p1 := d.Patterns[0], d.Patterns[1]
	if p0.ID != "proastiakos:KIATO-0" || p0.Direction != 0 || p0.Stops[0] != "proastiakos:"+stPir ||
		p1.ID != "proastiakos:KIATO-1" || p1.Direction != 1 || p1.Stops[0] != p0.Stops[len(p0.Stops)-1] ||
		p1.Headsign != "Πειραιάς" || len(p0.Shape) < 10 || len(p1.Shape) != len(p0.Shape) {
		t.Fatalf("patterns %s", asJSON(d.Patterns))
	}

	var s v2Stop
	v2get(t, a, "/v2/stops/"+esc("proastiakos:6928c3c150ab5bc72b39cee5"), &s)
	st := s.Stop
	if st == nil || st.Name != "Αγ. Ανάργυροι" || st.NameEn == nil || *st.NameEn != "Ag. Anargyroi" ||
		strings.Join(st.Modes, ",") != "suburban" ||
		strings.Join(st.Lines, ",") != "proastiakos:CHALKIDA,proastiakos:KIATO,proastiakos:PIRAIR" || len(s.Lines) != 3 {
		t.Fatalf("stop %s", asJSON(s))
	}
	// A permanent alias (the airport's second railway.gov.gr id) and an unknown id.
	v2get(t, a, "/v2/stops/"+esc("proastiakos:6928c3c150ab5bc72b39cec7"), &s)
	if s.Stop == nil || s.Stop.ID != "proastiakos:"+stAir || s.Stop.NameEn != nil {
		t.Fatalf("alias %s", asJSON(s.Stop))
	}
	if res, _ := get(t, a, "/v2/stops/"+esc("proastiakos:nope")); res.StatusCode != 404 {
		t.Fatalf("unknown stop: %d", res.StatusCode)
	}
	var idx struct {
		Stops []model.IndexEntry `json:"stops"`
	}
	v2get(t, a, "/v2/stops", &idx)
	n := 0
	for _, e := range idx.Stops {
		if strings.HasPrefix(e.ID, "proastiakos:") {
			n++
		}
	}
	if n != 45 {
		t.Fatalf("%d suburban stops in the index", n)
	}
}

func TestV2ProastiakosArrivals(t *testing.T) {
	a, fb, ft := proastiakosApp(t, wed1000)
	ad := a.adapter("proastiakos:x").(*proastiakosAdapter)
	got, err := ad.Arrivals(context.Background(), rentis, wed1000)
	if err != nil {
		t.Fatal(err)
	}
	if ft.opened == 0 || fb.fetches == 0 {
		t.Fatal("a stop request opens the stream and reads the board")
	}
	if len(got) != 4 {
		t.Fatalf("arrivals %s", asJSON(got))
	}
	// Stream delay first: 4 min, not the board's 3.
	a0 := got[0]
	if a0.Line != "proastiakos:KIATO" || a0.Stop != rentis || a0.Headsign != "Ρουφ" || a0.At != athens(10, 9).Unix() ||
		*a0.ScheduledAt != athens(10, 5).Unix() || *a0.DelayS != 240 || a0.Source != model.SourceLive ||
		*a0.Run != "proastiakos:run:2539:2026-10-28" || *a0.Vehicle != "proastiakos:2539" || *a0.VehicleLabel != "2539" ||
		*a0.Platform != "2" || a0.Status != model.Running {
		t.Fatalf("2539 %s", asJSON(a0))
	}
	// On the stream with no delay: timetable time, but its Vehicle is known.
	a1 := got[1]
	if a1.Headsign != "Πειραιάς" || a1.At != athens(10, 12).Unix() || a1.DelayS != nil || a1.Source != model.SourceTable ||
		*a1.Vehicle != "proastiakos:2000" || a1.Status != model.Scheduled {
		t.Fatalf("2000 %s", asJSON(a1))
	}
	// Not started: a board delay of 0 on a scheduled run is no report.
	a2 := got[2]
	if a2.At != athens(10, 40).Unix() || a2.DelayS != nil || a2.Source != model.SourceTable || a2.Vehicle != nil ||
		a2.VehicleLabel != nil || a2.Platform != nil {
		t.Fatalf("1500 %s", asJSON(a2))
	}
	// A board stop under an alias id of the Stop.
	if got[3].Run == nil || *got[3].Run != "proastiakos:run:1900:2026-10-28" {
		t.Fatalf("1900 %s", asJSON(got[3]))
	}

	// No stream: the board's delay of a run under way, while the board is fresh.
	ft.snap = nil
	got, _ = ad.Arrivals(context.Background(), rentis, wed1000)
	if got[0].At != athens(10, 8).Unix() || *got[0].DelayS != 180 || got[0].Source != model.SourceLive || got[0].Vehicle != nil {
		t.Fatalf("board delay %s", asJSON(got[0]))
	}
	// A board older than 10 min: its delays are not used.
	fb.at = wed1000.Add(-15 * time.Minute)
	got, _ = ad.Arrivals(context.Background(), rentis, wed1000)
	if got[0].At != athens(10, 5).Unix() || got[0].DelayS != nil || got[0].Source != model.SourceTable {
		t.Fatalf("old board %s", asJSON(got[0]))
	}
	// A stale stream snapshot gives neither delay nor vehicle.
	ft.snap = subTrains(wed1000.Add(-5 * time.Minute))
	got, _ = ad.Arrivals(context.Background(), rentis, wed1000)
	if got[0].Vehicle != nil || got[0].DelayS != nil {
		t.Fatalf("stale stream %s", asJSON(got[0]))
	}
	// No board at all: no arrivals, no error.
	fb.ok, fb.list = false, nil
	if got, err := ad.Arrivals(context.Background(), rentis, wed1000); err != nil || len(got) != 0 {
		t.Fatalf("no board %v %s", err, asJSON(got))
	}
}

func TestV2ProastiakosVehicles(t *testing.T) {
	a, _, ft := proastiakosApp(t, wed1000)
	ad := a.adapter("proastiakos:x").(*proastiakosAdapter)
	vs, err := ad.Vehicles(wed1000)
	if err != nil || ft.opened != 1 {
		t.Fatalf("%v opened %d", err, ft.opened)
	}
	if len(vs) != 3 { // the freight train is left out
		t.Fatalf("vehicles %s", asJSON(vs))
	}
	by := map[string]model.Vehicle{}
	for _, v := range vs {
		by[v.ID] = v
	}
	v := by["proastiakos:2539"]
	m := v.Motion
	if v.Line != "proastiakos:KIATO" || v.Mode != model.Suburban || *v.Label != "2539" || *v.Headsign != "Ρουφ" ||
		*v.Run != "proastiakos:run:2539:2026-10-28" || v.Position.Kind != model.GPS || v.Position.Lat != 37.95888 ||
		*v.Position.Bearing != 60 || v.Position.At != wed1000.Add(-10*time.Second).Unix() || *v.DelayS != 240 ||
		*v.NextStop != rentis || v.Source != "proastiakos" || m == nil {
		t.Fatalf("2539 %s", asJSON(v))
	}
	// Path to Ρέντης, beyond it up to 1.5 km in all (Ταύρος is farther), at the GPS speed; no knots.
	if len(m.Path) < 2 || len(m.PathBeyond) < 2 || len(m.PathStops) != 1 || m.PathStops[0] <= 0 ||
		m.PathStops[0] > 1500 || *m.Speed != 10 || m.Knots != nil || m.PathSpeeds != nil {
		t.Fatalf("2539 motion %s", asJSON(m))
	}
	// No next station from the stream: the course picks the direction (west, towards Πειραιάς).
	w := by["proastiakos:2000"]
	if w.NextStop == nil || *w.NextStop != rentis || w.Motion == nil || w.DelayS != nil || w.Motion.PathStops[0] <= 0 {
		t.Fatalf("2000 %s", asJSON(w))
	}
	// Far from its corridor: GPS only, nothing to move along.
	if f := by["proastiakos:3000"]; f.Motion != nil || f.NextStop != nil || f.Run != nil {
		t.Fatalf("3000 %s", asJSON(f))
	}

	var lv v2Vehicles
	if _, ma := v2get(t, a, "/v2/lines/"+esc("proastiakos:KIATO")+"/vehicles", &lv); ma != 5 || len(lv.Vehicles) != 2 {
		t.Fatalf("line vehicles %d %s", ma, asJSON(lv))
	}
	var rv v2Vehicles
	if _, ma := v2get(t, a, "/v2/vehicles/rail", &rv); ma != 5 {
		t.Fatalf("rail max-age %d", ma)
	}
}

func TestV2ProastiakosRuns(t *testing.T) {
	a, _, _ := proastiakosApp(t, wed1000)
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-28"), &r)
	run := r.Run
	if run == nil || run.Line != "proastiakos:KIATO" || run.Headsign != "Ρουφ" || *run.Pattern != "proastiakos:KIATO-0" ||
		*run.Vehicle != "proastiakos:2539" || *run.VehicleLabel != "2539" || run.Status != model.Running || len(run.Stops) != 5 {
		t.Fatalf("%s", asJSON(run))
	}
	s0, s2, s4 := run.Stops[0], run.Stops[2], run.Stops[4]
	if s0.Stop != "proastiakos:"+stPir || s0.At != athens(9, 54).Unix() || !s0.Passed || *s0.DelayS != 240 ||
		s0.Source != model.SourceLive || s2.Passed || s2.At != athens(10, 9).Unix() || *s2.Platform != "2" ||
		s4.At != athens(10, 24).Unix() || *s4.ScheduledAt != athens(10, 20).Unix() {
		t.Fatalf("stops %s", asJSON(run.Stops))
	}
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2000:2026-10-28"), &r)
	if r.Run == nil || *r.Run.Pattern != "proastiakos:KIATO-1" {
		t.Fatalf("2000 %s", asJSON(r.Run))
	}
	for _, id := range []string{"proastiakos:run:1600:2026-10-28", "proastiakos:run:2539:2026-10-27",
		"proastiakos:run:4242:2026-10-28", "proastiakos:run:1700:2026-10-28"} {
		if res, _ := get(t, a, "/v2/runs/"+esc(id)); res.StatusCode != 404 {
			t.Fatalf("%s: %d", id, res.StatusCode)
		}
	}
}

func TestV2ProastiakosState(t *testing.T) {
	a, fb, ft := proastiakosApp(t, wed1000)
	ad := a.adapter("proastiakos:x").(*proastiakosAdapter)
	if s := ad.State(wed1000); s.State != model.OK || *s.UpdatedAt != wed1000.Add(-time.Second).Unix() {
		t.Fatalf("ok %s", asJSON(s))
	}
	ft.last = wed1000.Add(-3 * time.Minute)
	if s := ad.State(wed1000); s.State != model.Stale {
		t.Fatalf("stalled stream %s", asJSON(s))
	}
	ft.open = false
	if s := ad.State(wed1000); s.State != model.OK {
		t.Fatalf("closed stream %s", asJSON(s))
	}
	fb.at, fb.failing = wed1000.Add(-40*time.Minute), true
	if s := ad.State(wed1000); s.State != model.Stale {
		t.Fatalf("old board %s", asJSON(s))
	}
	fb.ok, fb.list = false, nil
	if s := ad.State(wed1000); s.State != model.Down {
		t.Fatalf("no board %s", asJSON(s))
	}
	// Nothing asked yet: the static table answers, nothing has failed.
	fb.failing, ft.last = false, time.Time{}
	if s := ad.State(wed1000); s.State != model.OK || s.UpdatedAt != nil {
		t.Fatalf("idle %s", asJSON(s))
	}
}
