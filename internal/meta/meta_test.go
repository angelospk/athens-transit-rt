package meta

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

type fakeAPI struct {
	calls     int
	failRoute map[string]int // remaining failures per line code
}

func (f *fakeAPI) Lines(context.Context) ([]telematics.Line, error) {
	f.calls++
	return []telematics.Line{{LineCode: "938", LineID: "040"}, {LineCode: "962", LineID: "Α1"},
		{LineCode: "1566", LineID: "Α1"}}, nil
}

func (f *fakeAPI) Routes(_ context.Context, lc string) ([]telematics.Route, error) {
	f.calls++
	if f.failRoute[lc] > 0 {
		f.failRoute[lc]--
		return nil, errors.New("boom")
	}
	return map[string][]telematics.Route{
		"938":  {{RouteCode: "3922"}, {RouteCode: "3923"}},
		"962":  {{RouteCode: "2045"}},
		"1566": {{RouteCode: "4451"}},
	}[lc], nil
}

func (f *fakeAPI) Stops(_ context.Context, rc string) ([]telematics.RouteStop, error) {
	f.calls++
	if rc == "empty" {
		return nil, nil
	}
	return []telematics.RouteStop{{StopCode: "1"}, {StopCode: "2"}}, nil
}

// queue runs submitted work in FIFO order when drained.
type queue struct{ fns []func(context.Context) error }

func (q *queue) submit(fn func(context.Context) error) { q.fns = append(q.fns, fn) }
func (q *queue) drain() {
	for len(q.fns) > 0 {
		fn := q.fns[0]
		q.fns = q.fns[1:]
		fn(context.Background())
	}
}

func TestBootstrapPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.json")
	api, q := &fakeAPI{failRoute: map[string]int{"962": 1}}, &queue{}
	changes := 0
	s := Open(path, api, q.submit, func() { changes++ })
	if !s.Due() {
		t.Fatal("empty store should be due")
	}
	s.Refresh()
	s.Refresh() // no double refresh
	q.drain()
	lr := s.LineRoutes()
	if len(lr["040"]) != 2 || lr["040"][0] != (RouteRef{"3922", "938"}) {
		t.Fatalf("040 routes %v", lr["040"])
	}
	if len(lr["Α1"]) != 2 {
		t.Fatalf("Α1 routes %v (one LineCode failed once, then retried)", lr["Α1"])
	}
	if changes == 0 || s.Due() {
		t.Fatalf("changes %d due %v", changes, s.Due())
	}
	if api.calls != 1+3+1 {
		t.Fatalf("calls %d", api.calls)
	}
	s2 := Open(path, api, q.submit, nil)
	if len(s2.LineRoutes()["040"]) != 2 || s2.Due() {
		t.Fatal("reload lost data")
	}
}

func TestRouteRetriesAreBounded(t *testing.T) {
	api, q := &fakeAPI{failRoute: map[string]int{"938": 100}}, &queue{}
	s := Open("", api, q.submit, nil)
	s.Refresh()
	q.drain()
	if len(s.LineRoutes()["040"]) != 0 || s.Due() {
		t.Fatal("refresh should finish without 040 routes")
	}
	if api.calls != 1+maxAttempts+2 {
		t.Fatalf("calls %d", api.calls)
	}
	// The failed LineCode is retried within IncompleteRetry, not after a day.
	if !s.Incomplete() {
		t.Fatal("incomplete refresh not reported")
	}
	now := time.Now().Add(IncompleteRetry)
	s.now = func() time.Time { return now }
	if !s.Due() {
		t.Fatal("incomplete refresh not due again after IncompleteRetry")
	}
	api.failRoute["938"] = 0
	s.Refresh()
	q.drain()
	if len(s.LineRoutes()["040"]) != 2 || s.Incomplete() || s.Due() {
		t.Fatal("retry did not complete the metadata")
	}
}

func TestStopsFetchedOnce(t *testing.T) {
	api, q := &fakeAPI{}, &queue{}
	s := Open("", api, q.submit, nil)
	if _, ok := s.Stops("3922"); ok {
		t.Fatal("unknown stops reported known")
	}
	s.Stops("3922")
	if len(q.fns) != 1 {
		t.Fatalf("queued %d fetches", len(q.fns))
	}
	q.drain()
	if st, ok := s.Stops("3922"); !ok || len(st) != 2 {
		t.Fatalf("stops %v %v", st, ok)
	}
}

// An empty stop list is not stored (OASA sometimes answers "" for a valid route) and is
// retried only after EmptyStopsRetry.
func TestEmptyStopsRetriedLater(t *testing.T) {
	api, q := &fakeAPI{}, &queue{}
	s := Open("", api, q.submit, nil)
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	s.Stops("empty")
	q.drain()
	if _, ok := s.Stops("empty"); ok || len(q.fns) != 0 {
		t.Fatalf("empty answer stored or retried at once (queued %d)", len(q.fns))
	}
	now = now.Add(EmptyStopsRetry)
	if s.Stops("empty"); len(q.fns) != 1 {
		t.Fatalf("not retried after %v", EmptyStopsRetry)
	}
}
