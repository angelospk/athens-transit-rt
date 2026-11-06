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
	list     []rail.SuburbanSchedule
	at       time.Time
	ok       bool
	failing  bool
	primary  map[string]string
	fetches  int
	days     map[string][]rail.SuburbanSchedule
	fetchDay func(day string) // a fetch fills days (a cold cache)
}

func (f *fakeSubBoard) Board(_ context.Context, now time.Time) ([]rail.SuburbanSchedule, time.Time, bool) {
	f.fetches++
	if f.fetchDay != nil {
		f.fetchDay(rail.SuburbanDay(now).Format(time.DateOnly))
	}
	return f.CachedBoard(now)
}
func (f *fakeSubBoard) CachedBoard(now time.Time) ([]rail.SuburbanSchedule, time.Time, bool) {
	if f.days != nil { // a board per board day
		l, ok := f.days[rail.SuburbanDay(now).Format(time.DateOnly)]
		return l, f.at, ok
	}
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
		*v.NextStop != rentis || v.Source != "proastiakos" || m == nil || v.Direction == nil || *v.Direction != 0 {
		t.Fatalf("2539 %s", asJSON(v))
	}
	// Path to Ρέντης, beyond it up to 1.5 km in all (Ταύρος is farther), at the GPS speed; no knots.
	if len(m.Path) < 2 || len(m.PathBeyond) < 2 || len(m.PathStops) != 1 || m.PathStops[0] <= 0 ||
		m.PathStops[0] > 1500 || *m.Speed != 10 || m.Knots != nil || m.PathSpeeds != nil {
		t.Fatalf("2539 motion %s", asJSON(m))
	}
	// No next station from the stream: the course picks the direction (west, towards Πειραιάς).
	w := by["proastiakos:2000"]
	if w.NextStop == nil || *w.NextStop != rentis || w.Motion == nil || w.DelayS != nil || w.Motion.PathStops[0] <= 0 ||
		w.Direction == nil || *w.Direction != 1 {
		t.Fatalf("2000 %s", asJSON(w))
	}
	// Far from its corridor: GPS only, nothing to move along.
	if f := by["proastiakos:3000"]; f.Motion != nil || f.NextStop != nil || f.Run != nil || f.Direction != nil {
		t.Fatalf("3000 %s", asJSON(f))
	}

	var lv v2Vehicles
	if _, ma := v2get(t, a, "/v2/lines/"+esc("proastiakos:KIATO")+"/vehicles", &lv); ma != 5 || len(lv.Vehicles) != 2 {
		t.Fatalf("line vehicles %d %s", ma, asJSON(lv))
	}
	var sv v2Vehicles
	if _, ma := v2get(t, a, "/v2/vehicles/suburban", &sv); ma != subVehMaxAge || len(sv.Vehicles) != 3 ||
		len(sv.Sources) != 1 || sv.Sources[0].ID != subNS {
		t.Fatalf("suburban max-age %d %s", ma, asJSON(sv))
	}
}

// /v2/vehicles/rail is metro and tram only and never opens the suburban stream: a map that
// shows the metro must not keep railway.gov.gr streaming. /v2/vehicles/suburban opens it.
func TestV2RailVehiclesLeaveTheStreamClosed(t *testing.T) {
	a, _, ft := proastiakosApp(t, wed1000)
	var rv v2Vehicles
	v2get(t, a, "/v2/vehicles/rail", &rv)
	for _, v := range rv.Vehicles {
		if v.Mode == model.Suburban {
			t.Fatalf("suburban train in /v2/vehicles/rail: %s", asJSON(v))
		}
	}
	for _, s := range rv.Sources {
		if s.ID == subNS {
			t.Fatalf("proastiakos in /v2/vehicles/rail sources: %s", asJSON(rv.Sources))
		}
	}
	if ft.opened != 0 {
		t.Fatalf("/v2/vehicles/rail opened the stream %d times", ft.opened)
	}
	v2get(t, a, "/v2/vehicles/suburban", nil)
	if ft.opened != 1 {
		t.Fatalf("/v2/vehicles/suburban opened the stream %d times", ft.opened)
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

// A train standing at its next station gives no direction by position; its Run (2000: Ρουφ →
// Πειραιάς on the board) does: direction 1, as the Run's Pattern.
func TestV2ProastiakosDirectionFollowsTheRun(t *testing.T) {
	a, _, ft := proastiakosApp(t, wed1000)
	var st struct{ Stop model.Stop }
	v2get(t, a, "/v2/stops/"+esc(rentis), &st)
	for i := range ft.snap.Trains {
		if tr := &ft.snap.Trains[i]; tr.Train == "2000" {
			tr.Lat, tr.Lon, tr.NextID, tr.Course = st.Stop.Lat, st.Stop.Lon, stRen, nil
		}
	}
	vs, _ := a.adapter("proastiakos:x").(*proastiakosAdapter).Vehicles(wed1000)
	for _, v := range vs {
		if v.ID != "proastiakos:2000" {
			continue
		}
		var r struct{ Run model.Run }
		v2get(t, a, "/v2/runs/"+esc(*v.Run), &r)
		if v.Direction == nil || *v.Direction != 1 || r.Run.Pattern == nil || *r.Run.Pattern != "proastiakos:KIATO-1" {
			t.Fatalf("2000 %s run %s", asJSON(v), asJSON(r.Run))
		}
		return
	}
	t.Fatalf("no 2000 in %s", asJSON(vs))
}

// Without a board Run the position decides, in both directions (2539 east, 2000 west).
func TestV2ProastiakosDirectionWithoutRuns(t *testing.T) {
	a, b, _ := proastiakosApp(t, wed1000)
	b.list = nil
	vs, _ := a.adapter("proastiakos:x").(*proastiakosAdapter).Vehicles(wed1000)
	want := map[string]int{"proastiakos:2539": 0, "proastiakos:2000": 1}
	for _, v := range vs {
		if d, ok := want[v.ID]; ok && (v.Run != nil || v.Direction == nil || *v.Direction != d || v.Motion == nil) {
			t.Fatalf("%s", asJSON(v))
		}
	}
}

// At 00:30 a run of the evening before is still the board's (#84): its vehicle names the run of
// that day, and the run answers with its stops passed.
func TestV2ProastiakosAfterMidnight(t *testing.T) {
	night := time.Date(2026, 10, 29, 0, 30, 0, 0, gtfs.Athens)
	a, _, _ := proastiakosApp(t, night)
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-28"), &r)
	if r.Run == nil || len(r.Run.Stops) == 0 {
		t.Fatalf("run %s", asJSON(r.Run))
	}
	for _, s := range r.Run.Stops {
		if !s.Passed {
			t.Fatalf("stop not passed %s", asJSON(s))
		}
	}
	if res, _ := get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-30")); res.StatusCode != 404 {
		t.Fatalf("the day after: %d", res.StatusCode)
	}
	if got := subRunID("2539", rail.SuburbanDay(night)); got != "proastiakos:run:2539:2026-10-28" {
		t.Fatalf("run id %s", got)
	}
}

// From 00:00 to 03:00 a stop shows the last runs of the evening before and the first of the day to
// come (#84), and the run of the day to come answers.
func TestV2ProastiakosArrivalsAcrossMidnight(t *testing.T) {
	night := time.Date(2026, 10, 29, 2, 30, 0, 0, gtfs.Athens)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, gtfs.Athens) }
	a, fb, _ := proastiakosApp(t, night)
	fb.days = map[string][]rail.SuburbanSchedule{
		"2026-10-28": {{Train: "2599", Corridor: "KIATO", Status: "in_progress", Destination: "Ρουφ",
			Stops: []rail.SuburbanBoardStop{bstop(stPir, "Πειραιάς", "origin", at(29, 2, 20)),
				bstop(stRen, "Ρέντης", "stop", at(29, 2, 40)), bstop(stRouf, "Ρουφ", "destination", at(29, 2, 50))}}},
		"2026-10-29": {{Train: "2501", Corridor: "KIATO", Status: "scheduled", Destination: "Ρουφ",
			Stops: []rail.SuburbanBoardStop{bstop(stPir, "Πειραιάς", "origin", at(29, 4, 10)),
				bstop(stRen, "Ρέντης", "stop", at(29, 4, 20)), bstop(stRouf, "Ρουφ", "destination", at(29, 4, 30))}}},
	}
	got, err := a.subAdapter().Arrivals(context.Background(), rentis, night)
	if err != nil || len(got) != 2 {
		t.Fatalf("err %v arrivals %s", err, asJSON(got))
	}
	if *got[0].Run != "proastiakos:run:2599:2026-10-28" || got[0].At != at(29, 2, 40).Unix() ||
		*got[1].Run != "proastiakos:run:2501:2026-10-29" || got[1].At != at(29, 4, 20).Unix() || got[1].Vehicle != nil {
		t.Fatalf("arrivals %s", asJSON(got))
	}
	var r struct {
		Run *model.Run `json:"run"`
	}
	for _, id := range []string{"proastiakos:run:2599:2026-10-28", "proastiakos:run:2501:2026-10-29"} {
		r.Run = nil
		v2get(t, a, "/v2/runs/"+esc(id), &r)
		if r.Run == nil || len(r.Run.Stops) != 3 {
			t.Fatalf("%s: %s", id, asJSON(r.Run))
		}
	}
	if res, _ := get(t, a, "/v2/runs/"+esc("proastiakos:run:2501:2026-10-30")); res.StatusCode != 404 {
		t.Fatalf("day after: %d", res.StatusCode)
	}
}

// The stream can report delay 0 while a train runs late (#93): at 10:40 train 2539 is still
// before Ρέντης (scheduled 10:05). The stops before its next station are passed, the rest are not,
// and the delay is at least the 35 min since 10:05, on the run, the vehicle and the stop board.
func TestV2ProastiakosLateTrainWithZeroDelay(t *testing.T) {
	now := athens(10, 40)
	a, _, ft := proastiakosApp(t, now)
	for i := range ft.snap.Trains {
		if tr := &ft.snap.Trains[i]; tr.Train == "2539" {
			tr.DelayMin = model.Ptr(0)
		}
	}
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-28"), &r)
	run := r.Run
	if run == nil || len(run.Stops) != 5 || run.Vehicle == nil {
		t.Fatalf("%s", asJSON(run))
	}
	for j, s := range run.Stops {
		if s.Passed != (j < 2) {
			t.Fatalf("stop %d passed %v: %s", j, s.Passed, asJSON(run.Stops))
		}
		if j >= 2 && (s.DelayS == nil || *s.DelayS != 35*60 || s.At != *s.ScheduledAt+35*60 || s.Source != model.SourceLive) {
			t.Fatalf("stop %d: %s", j, asJSON(s))
		}
	}
	if run.Status != model.Delayed {
		t.Fatalf("status %s", run.Status)
	}
	vs, _ := a.adapter("proastiakos:x").(*proastiakosAdapter).Vehicles(now)
	seen := false
	for _, v := range vs {
		if v.ID == "proastiakos:2539" {
			seen = true
			if v.DelayS == nil || *v.DelayS != 35*60 {
				t.Fatalf("vehicle %s", asJSON(v))
			}
		}
	}
	if !seen {
		t.Fatalf("no 2539 in %s", asJSON(vs))
	}
	got, err := a.subAdapter().Arrivals(context.Background(), rentis, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ar := range got {
		if ar.Run != nil && *ar.Run == "proastiakos:run:2539:2026-10-28" {
			found = ar.At == athens(10, 40).Unix() && *ar.DelayS == 35*60
		}
	}
	if !found {
		t.Fatalf("2539 not at Ρέντης at 10:40: %s", asJSON(got))
	}
}

// A train far behind any run of its number is on another run: no bound, no passed override.
func TestV2ProastiakosLateBoundHasALimit(t *testing.T) {
	now := athens(14, 0) // 2539's Ρέντης was at 10:05: 3 h 55 min ago
	a, _, _ := proastiakosApp(t, now)
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-28"), &r)
	if r.Run == nil {
		t.Fatal("no run")
	}
	for _, s := range r.Run.Stops {
		if !s.Passed {
			t.Fatalf("stop not passed %s", asJSON(r.Run.Stops))
		}
	}
}

// A run that leaves after midnight is on the board of the day to come (#93): at 00:10 the live
// 2539 (left Πειραιάς at 00:00) links the run of 10-29, not the 10-28 one of the night before;
// 2304 (left 22:14) keeps the run of 10-28.
func TestV2ProastiakosLiveTrainAfterMidnightTakesTheNearestRun(t *testing.T) {
	night := time.Date(2026, 10, 29, 0, 10, 0, 0, gtfs.Athens)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, gtfs.Athens) }
	run := func(no string, d, h, m int) rail.SuburbanSchedule {
		return rail.SuburbanSchedule{Train: no, Corridor: "KIATO", Status: "in_progress", Destination: "Ρουφ",
			Stops: []rail.SuburbanBoardStop{bstop(stPir, "Πειραιάς", "origin", at(d, h, m)),
				bstop(stLefka, "Λεύκα", "stop", at(d, h, m+5)), bstop(stRen, "Ρέντης", "stop", at(d, h, m+15)),
				bstop(stRouf, "Ρουφ", "destination", at(d, h, m+30))}}
	}
	a, fb, ft := proastiakosApp(t, night)
	fb.days = map[string][]rail.SuburbanSchedule{
		"2026-10-28": {run("2539", 28, 0, 0), run("2304", 28, 23, 50)},
		"2026-10-29": {run("2539", 29, 0, 0), run("2304", 29, 23, 50)},
	}
	ft.snap.Trains = append(ft.snap.Trains, rail.SuburbanTrain{Train: "2304", Corridor: "KIATO", Service: "Suburban",
		Lat: 37.95888, Lon: 23.66121, Course: model.Ptr(60.0), SpeedKmh: 36, DelayMin: model.Ptr(0), To: "Ρουφ",
		NextID: stRen, PositionAt: night.Unix()})
	vs, _ := a.adapter("proastiakos:x").(*proastiakosAdapter).Vehicles(night)
	want := map[string]string{"proastiakos:2539": "proastiakos:run:2539:2026-10-29", "proastiakos:2304": "proastiakos:run:2304:2026-10-28"}
	for _, v := range vs {
		if w, ok := want[v.ID]; ok {
			if v.Run == nil || *v.Run != w {
				t.Fatalf("%s: %s", v.ID, asJSON(v))
			}
			delete(want, v.ID)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing %v in %s", want, asJSON(vs))
	}
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-29"), &r)
	if r.Run == nil || r.Run.Vehicle == nil || r.Run.Stops[2].Passed {
		t.Fatalf("10-29 %s", asJSON(r.Run))
	}
	r.Run = nil
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-28"), &r)
	if r.Run == nil || r.Run.Vehicle != nil {
		t.Fatalf("10-28 %s", asJSON(r.Run))
	}
}

// A stale next station behind the train (#93): the stream still names Λεύκα while the GPS has 2539
// past it, before Ρέντης. The track decides: Λεύκα is passed, the bound is from Ρέντης (10:05).
func TestV2ProastiakosStaleNextStation(t *testing.T) {
	now := athens(10, 40)
	a, _, ft := proastiakosApp(t, now)
	for i := range ft.snap.Trains {
		if tr := &ft.snap.Trains[i]; tr.Train == "2539" {
			tr.DelayMin, tr.NextID, tr.Next = model.Ptr(0), stLefka, "Λεύκα"
		}
	}
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-28"), &r)
	if r.Run == nil || len(r.Run.Stops) != 5 || !r.Run.Stops[1].Passed || r.Run.Stops[2].Passed ||
		*r.Run.Stops[2].DelayS != 35*60 {
		t.Fatalf("%s", asJSON(r.Run))
	}
	got, _ := a.subAdapter().Arrivals(context.Background(), "proastiakos:"+stLefka, now)
	for _, ar := range got {
		if ar.Run != nil && *ar.Run == "proastiakos:run:2539:2026-10-28" {
			t.Fatalf("2539 due at Λεύκα it has passed: %s", asJSON(ar))
		}
	}
}

// Run of the evening before, asked first at 00:10 with nothing cached: it reads the board of the
// day to come too, so the live 2539 (left 00:00) is not put on the run of the night before.
func TestV2ProastiakosRunReadsBothBoards(t *testing.T) {
	night := time.Date(2026, 10, 29, 0, 10, 0, 0, gtfs.Athens)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, gtfs.Athens) }
	run := func(d int) rail.SuburbanSchedule {
		return rail.SuburbanSchedule{Train: "2539", Corridor: "KIATO", Status: "in_progress", Destination: "Ρουφ",
			Stops: []rail.SuburbanBoardStop{bstop(stPir, "Πειραιάς", "origin", at(d, 0, 0)),
				bstop(stRen, "Ρέντης", "stop", at(d, 0, 15)), bstop(stRouf, "Ρουφ", "destination", at(d, 0, 30))}}
	}
	a, fb, _ := proastiakosApp(t, night)
	all := map[string][]rail.SuburbanSchedule{"2026-10-28": {run(28)}, "2026-10-29": {run(29)}}
	fb.days = map[string][]rail.SuburbanSchedule{}
	fb.fetchDay = func(day string) { fb.days[day] = all[day] }
	var r struct {
		Run *model.Run `json:"run"`
	}
	v2get(t, a, "/v2/runs/"+esc("proastiakos:run:2539:2026-10-28"), &r)
	if r.Run == nil || r.Run.Vehicle != nil {
		t.Fatalf("10-28 %s", asJSON(r.Run))
	}
}
