package sched

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

var t0 = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)

type fakeSlots struct{ rps float64 }

func (f *fakeSlots) Wait(ctx context.Context) error { return ctx.Err() }
func (f *fakeSlots) Report(time.Duration, error)    {}
func (f *fakeSlots) RPS() float64                   { return f.rps }

type fakeFetch struct {
	mu    sync.Mutex
	calls []string
	rows  map[string][]telematics.Vehicle
}

func (f *fakeFetch) BusLocations(_ context.Context, rc string) ([]telematics.Vehicle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rc)
	return f.rows[rc], nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newSched(rps float64, fetch Fetcher, c *clock) *Scheduler {
	s := New(DefaultConfig(), &fakeSlots{rps}, fetch, func(LinePoll) {})
	s.now = c.now
	return s
}

func routes(codes ...string) []Route {
	var out []Route
	for _, c := range codes {
		out = append(out, Route{Code: c, LineCode: "LC", Active: true})
	}
	return out
}

func TestAllocateWatchedCappedAtHalf(t *testing.T) {
	// 4 rps * 0.9 headroom = 3.6 rps. Watched demand 120 routes / 30 s = 4 rps > 1.8 cap.
	st := allocate(3.6, [numTiers]float64{Watched: 4, Dense: 1, Other: 0.5})
	if math.Abs(st[Watched]-4/1.8) > 1e-9 {
		t.Fatalf("watched stretch %v", st[Watched])
	}
	// Remaining 1.8 rps covers dense (1) and other (0.5): no stretch.
	if st[Dense] != 1 || st[Other] != 1 {
		t.Fatalf("dense %v other %v", st[Dense], st[Other])
	}
	// Small watched demand leaves the rest to others; others over budget get stretched.
	st = allocate(3.6, [numTiers]float64{Watched: 0.2, Dense: 2.4, Other: 2})
	if st[Watched] != 1 || st[Dense] != 1 || math.Abs(st[Other]-2/1.0) > 1e-9 {
		t.Fatalf("stretch %v", st)
	}
	// Nothing left for "other": capped stretch instead of infinity.
	st = allocate(3.6, [numTiers]float64{Watched: 1.8, Dense: 5, Other: 1})
	if math.IsInf(st[Other], 0) || st[Other] < 10 {
		t.Fatalf("other stretch %v", st[Other])
	}
}

func TestClassify(t *testing.T) {
	c := DefaultConfig()
	now := t0
	l := &lineState{spec: LineSpec{Active: true}}
	if c.classify(l, now) != Other {
		t.Fatal("active line should be Other")
	}
	l.vehicles = 5
	if c.classify(l, now) != Dense {
		t.Fatal("5 vehicles should be Dense")
	}
	l.watchedAt = now.Add(-9 * time.Minute)
	if c.classify(l, now) != Watched {
		t.Fatal("recently requested line should be Watched")
	}
	l.watchedAt = now.Add(-11 * time.Minute)
	if c.classify(l, now) != Dense {
		t.Fatal("watch should expire after 10 min")
	}
	l.spec.Active = false
	l.watchedAt = now
	if c.classify(l, now) != Inactive {
		t.Fatal("inactive line is never polled, even when watched")
	}
}

func TestEarliestDueFirstAndRouteBatching(t *testing.T) {
	c := &clock{t0}
	s := newSched(4, &fakeFetch{}, c)
	s.SetLines([]LineSpec{
		{ID: "A", Active: true, Routes: routes("a1", "a2")},
		{ID: "B", Active: true, Routes: routes("b1")},
	})
	s.Watch("B")
	s.replan()
	// B is watched: same due time, higher tier first; its routes come back to back.
	var got []string
	for i := 0; i < 3; i++ {
		r := s.pick()
		if r == nil {
			t.Fatalf("pick %d returned nil", i)
		}
		got = append(got, r.route.Code)
	}
	if got[0] != "b1" || got[1] != "a1" || got[2] != "a2" {
		t.Fatalf("order %v", got)
	}
	if s.pick() != nil {
		t.Fatal("nothing else is due")
	}
}

func TestPollCompletesAndReschedules(t *testing.T) {
	c := &clock{t0}
	fresh := telematics.Vehicle{VehNo: "1", RouteCode: "a1", Time: t0.Add(-time.Minute)}
	stale := telematics.Vehicle{VehNo: "2", RouteCode: "a1", Time: t0.Add(-6 * time.Minute)}
	f := &fakeFetch{rows: map[string][]telematics.Vehicle{"a1": {fresh, stale}}}
	var polls []LinePoll
	s := New(DefaultConfig(), &fakeSlots{4}, f, func(p LinePoll) { polls = append(polls, p) })
	s.now = c.now
	s.SetLines([]LineSpec{{ID: "A", Active: true, Routes: routes("a1", "a2")}})
	s.replan()
	for r := s.pick(); r != nil; r = s.pick() {
		s.execute(context.Background(), r)
	}
	if len(polls) != 1 {
		t.Fatalf("polls %d", len(polls))
	}
	p := polls[0]
	want := []match.Obs{{Vehicle: fresh, LineCode: "LC"}}
	if p.Line != "A" || len(p.Obs) != 1 || p.Obs[0].VehNo != want[0].VehNo || p.Obs[0].LineCode != "LC" || p.Failed != 0 {
		t.Fatalf("poll %+v", p)
	}
	next, polled := s.NextUpdate("A")
	if !polled || !next.After(t0.Add(DefaultConfig().Other.Base)) {
		t.Fatalf("next update %v %v", next, polled)
	}
	if s.pick() != nil {
		t.Fatal("line polled again before its interval")
	}
	c.t = t0.Add(DefaultConfig().Other.Base)
	if s.pick() == nil {
		t.Fatal("line not due after its interval")
	}
}

func TestEmptyRoutesSlowDown(t *testing.T) {
	c := &clock{t0}
	f := &fakeFetch{rows: map[string][]telematics.Vehicle{"a1": {{VehNo: "1", Time: t0}}}}
	s := newSched(4, f, c)
	s.SetLines([]LineSpec{{ID: "A", Active: true, Routes: routes("a1", "a2")}})
	poll := func() {
		s.replan()
		for r := s.pick(); r != nil; r = s.pick() {
			s.execute(context.Background(), r)
		}
	}
	for i := 0; i < 2; i++ {
		poll()
		c.t = c.t.Add(3 * time.Minute)
	}
	// a2 was empty in its last two polls (the last 3 min ago): skipped until 300 s have passed.
	f.calls = nil
	poll()
	if len(f.calls) != 1 || f.calls[0] != "a1" {
		t.Fatalf("calls %v, want only a1", f.calls)
	}
	// Watching the line polls every route again.
	c.t = c.t.Add(time.Minute)
	s.Watch("A")
	f.calls = nil
	poll()
	if len(f.calls) != 2 {
		t.Fatalf("watched calls %v", f.calls)
	}
}

func TestWatchPullsNextPollForward(t *testing.T) {
	c := &clock{t0}
	s := newSched(4, &fakeFetch{}, c)
	s.SetLines([]LineSpec{{ID: "A", Active: true, Routes: routes("a1")}})
	s.replan()
	for r := s.pick(); r != nil; r = s.pick() {
		s.execute(context.Background(), r)
	}
	before, _ := s.NextUpdate("A")
	c.t = t0.Add(10 * time.Second)
	s.Watch("A")
	after, _ := s.NextUpdate("A")
	if !after.Before(before) || after.After(t0.Add(40*time.Second)) {
		t.Fatalf("next update before %v after %v", before.Sub(t0), after.Sub(t0))
	}
}

func TestInactiveLinesNotPolled(t *testing.T) {
	c := &clock{t0}
	s := newSched(4, &fakeFetch{}, c)
	s.SetLines([]LineSpec{{ID: "N", Active: false, Routes: routes("n1")}})
	s.Watch("N")
	s.replan()
	if s.pick() != nil {
		t.Fatal("inactive line polled")
	}
	if _, polled := s.NextUpdate("N"); polled {
		t.Fatal("inactive line reported as polled")
	}
}

func TestBackgroundGetsEveryFourthSlot(t *testing.T) {
	c := &clock{t0}
	s := newSched(4, &fakeFetch{}, c)
	s.SetLines([]LineSpec{{ID: "A", Active: true, Routes: routes("a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8")}})
	s.replan()
	for i := 0; i < 3; i++ {
		s.Submit(func(context.Context) error { return nil })
	}
	var kinds []string
	for r := s.pick(); r != nil; r = s.pick() {
		if r.bg != nil {
			kinds = append(kinds, "bg")
		} else {
			kinds = append(kinds, "line")
		}
	}
	want := "line line line bg line line line bg line line bg"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("order %q, want %q", got, want)
	}
}

func TestBackgroundRuns(t *testing.T) {
	c := &clock{t0}
	s := newSched(4, &fakeFetch{}, c)
	s.SetLines([]LineSpec{{ID: "A", Active: true, Routes: routes("a1")}})
	s.replan()
	ran := false
	s.Submit(func(context.Context) error { ran = true; return nil })
	if r := s.pick(); r == nil || r.bg != nil {
		t.Fatal("a due line poll should go first")
	}
	r := s.pick()
	if r == nil || r.bg == nil {
		t.Fatal("background work not picked when idle")
	}
	s.execute(context.Background(), r)
	if !ran {
		t.Fatal("background work not run")
	}
}

func TestRunRespectsSlots(t *testing.T) {
	// Every request must take a slot: count Wait calls vs fetches.
	slots := &countingSlots{}
	f := &fakeFetch{}
	s := New(DefaultConfig(), slots, f, func(LinePoll) {})
	s.SetLines([]LineSpec{{ID: "A", Active: true, Routes: routes("a1", "a2", "a3")}})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 3 || slots.waits < 3 {
		t.Fatalf("calls %d waits %d", len(f.calls), slots.waits)
	}
}

type countingSlots struct {
	mu    sync.Mutex
	waits int
}

func (c *countingSlots) Wait(ctx context.Context) error {
	c.mu.Lock()
	c.waits++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Millisecond):
		return nil
	}
}
func (c *countingSlots) Report(time.Duration, error) {}
func (c *countingSlots) RPS() float64                { return 4 }
