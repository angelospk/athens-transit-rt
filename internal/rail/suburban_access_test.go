package rail

import (
	"context"
	"testing"
	"time"
)

func TestSuburbanBoardAccess(t *testing.T) {
	s, clk, f := newTestSuburban(t)
	if _, _, ok := s.CachedBoard(suburbanNow); ok {
		t.Fatal("a cached board before any fetch")
	}
	if s.BoardFailing() {
		t.Fatal("failing before any fetch")
	}
	list, at, ok := s.Board(context.Background(), suburbanNow)
	if !ok || !at.Equal(suburbanNow) || len(list) != 3 {
		t.Fatalf("board %v %v %d", ok, at, len(list))
	}
	sc := list[0]
	if sc.Train != "2539" || sc.Corridor != "KIATO" || sc.Status != "scheduled" || sc.Destination != "Κιάτο" ||
		sc.Delay == nil || *sc.Delay != 2 || len(sc.Stops) != 3 {
		t.Fatalf("schedule %+v", sc)
	}
	ren := sc.Stops[1]
	if ren.StationID != "ST-REN" || ren.Name != "Ρέντης" || ren.Role != "stop" || ren.Platform == nil || *ren.Platform != "3" ||
		ren.Delay == nil || *ren.Delay != 4 || !ren.Dep.Equal(time.Date(2026, 10, 28, 3, 26, 0, 0, time.UTC)) || !ren.Arr.IsZero() {
		t.Fatalf("stop %+v", ren)
	}
	if end := sc.Stops[2]; !end.Dep.IsZero() || !end.Arr.Equal(time.Date(2026, 10, 28, 4, 30, 0, 0, time.UTC)) {
		t.Fatalf("terminus %+v", end)
	}
	// The cached view does not fetch, and serves the same converted board.
	clk.t = suburbanNow.Add(30 * time.Minute)
	cached, cat, ok := s.CachedBoard(clk.t)
	if !ok || !cat.Equal(suburbanNow) || &cached[0] != &list[0] {
		t.Fatalf("cached %v %v", ok, cat)
	}
	if n := f.count("/api/public/schedule-board"); n != 1 {
		t.Fatalf("fetched %d times", n)
	}
	// Another day has no board.
	if _, _, ok := s.CachedBoard(suburbanNow.AddDate(0, 0, 1)); ok {
		t.Fatal("yesterday's board served for today")
	}
	// A failed refresh: the stale board stays, and the failure shows.
	f.setFail(true)
	if _, _, ok := s.Board(context.Background(), clk.t); !ok {
		t.Fatal("stale board not served")
	}
	s.refreshes.Wait()
	if !s.BoardFailing() {
		t.Fatal("failure not reported")
	}
}

func TestSuburbanStationPrimary(t *testing.T) {
	s, _, _ := newTestSuburban(t)
	if _, ok := s.StationPrimary("ST-AIR2"); ok {
		t.Fatal("an alias before the table loads")
	}
	s.Stations(context.Background())
	if p, ok := s.StationPrimary("ST-AIR2"); !ok || p != "ST-AIR" {
		t.Fatalf("alias %q %v", p, ok)
	}
	if _, ok := s.StationPrimary("nope"); ok {
		t.Fatal("unknown id resolved")
	}
}
