package telematics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

func TestParseCSDate(t *testing.T) {
	cases := map[string]string{
		"Oct  5 2026 11:10:38:000AM":  "2026-10-05 11:10:38",
		"Oct  4 2026 10:15:38:000PM":  "2026-10-04 22:15:38",
		"Jan 1 2026 12:00:01:000AM":   "2026-01-01 00:00:01",
		"Jan 1 2026 12:30:00:000PM":   "2026-01-01 12:30:00",
		" Dec 31 2026 9:05:00:123 PM": "2026-12-31 21:05:00",
	}
	for in, want := range cases {
		got, err := ParseCSDate(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got.Location() != gtfs.Athens || got.Format(time.DateTime) != want {
			t.Errorf("%q -> %v, want %s Athens", in, got, want)
		}
	}
	if _, err := ParseCSDate("yesterday"); err == nil {
		t.Error("garbage accepted")
	}
}

// fakeAPI serves recorded responses keyed by act+p1.
func fakeAPI(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "athens-transit-rt") {
			t.Errorf("user agent %q", ua)
		}
		key := r.URL.Query().Get("act") + ":" + r.URL.Query().Get("p1")
		body, ok := responses[key]
		if !ok {
			http.Error(w, "no fixture "+key, 500)
			return
		}
		w.Write([]byte(body))
	}))
}

func file(t *testing.T, name string) string {
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClientDecodes(t *testing.T) {
	srv := fakeAPI(t, map[string]string{
		"webGetLines:":         file(t, "lines.json"),
		"webGetRoutes:938":     file(t, "routes-938.json"),
		"webGetStops:3922":     file(t, "stops-3922.json"),
		"getBusLocation:3922":  file(t, "busloc-3922.json"),
		"getBusLocation:9999":  `null`,
		"getBusLocation:9998":  `""`,
		"getStopArrivals:1":    file(t, "arrivals.json"),
		"getBusLocation:error": `{"error":"bad"}`,
	})
	defer srv.Close()
	c := New(srv.URL+"/", nil)
	ctx := context.Background()

	lines, err := c.Lines(ctx)
	if err != nil || len(lines) == 0 || lines[0].LineID == "" || lines[0].LineCode == "" {
		t.Fatalf("lines %v %v", lines, err)
	}
	routes, err := c.Routes(ctx, "938")
	if err != nil || len(routes) != 3 || routes[0].RouteCode != "3922" {
		t.Fatalf("routes %v %v", routes, err)
	}
	stops, err := c.Stops(ctx, "3922")
	if err != nil || stops[0].StopCode != "10183" {
		t.Fatalf("stops %v %v", stops, err)
	}
	vs, err := c.BusLocations(ctx, "3922")
	if err != nil || len(vs) != 10 {
		t.Fatalf("vehicles %d %v", len(vs), err)
	}
	v := vs[0]
	if v.VehNo != "44537" || v.Lat != 37.956144 || v.Lon != 23.716094 || v.Heading != 52 || v.RouteCode != "3922" {
		t.Fatalf("vehicle %+v", v)
	}
	if v.Time.Format(time.DateTime) != "2026-10-05 11:10:38" {
		t.Fatalf("vehicle time %v", v.Time)
	}
	for _, code := range []string{"9999", "9998"} {
		if vs, err := c.BusLocations(ctx, code); err != nil || len(vs) != 0 {
			t.Fatalf("empty answer %s: %v %v", code, vs, err)
		}
	}
	if _, err := c.BusLocations(ctx, "error"); err == nil {
		t.Fatal("error object not reported")
	}
	arr, err := c.StopArrivals(ctx, "1")
	if err != nil || len(arr) == 0 || arr[0].VehCode == "" {
		t.Fatalf("arrivals %v %v", arr, err)
	}
	if c.Requests() != 8 {
		t.Fatalf("requests counted %d", c.Requests())
	}
}

func TestClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := New(srv.URL+"/", nil).Lines(context.Background()); err == nil {
		t.Fatal("502 not reported")
	}
}

func TestPacerSpacing(t *testing.T) {
	p := NewPacer(4)
	t0 := time.Unix(1000, 0)
	if d := p.grant(t0); d != 0 {
		t.Fatalf("first grant waits %v", d)
	}
	if d := p.grant(t0.Add(100 * time.Millisecond)); d != 150*time.Millisecond {
		t.Fatalf("early caller waits %v, want 150ms", d)
	}
	// A late wake-up (timer fired 200 ms late) counts from the actual grant, so the next
	// caller still waits a full interval after it.
	late := t0.Add(450 * time.Millisecond)
	if d := p.grant(late); d != 0 {
		t.Fatalf("late grant waits %v", d)
	}
	if d := p.grant(late.Add(10 * time.Millisecond)); d != 240*time.Millisecond {
		t.Fatalf("after a late grant: wait %v, want 240ms", d)
	}
	// Idle time does not accumulate into a burst.
	later := t0.Add(time.Minute)
	if a, b := p.grant(later), p.grant(later); a != 0 || b != 250*time.Millisecond {
		t.Fatalf("after idle: %v %v", a, b)
	}
}

// The client never starts two requests closer than MinGap, whatever the caller does.
func TestClientMinGap(t *testing.T) {
	srv := fakeAPI(t, map[string]string{"getBusLocation:1": "[]"})
	defer srv.Close()
	c := New(srv.URL+"/", nil)
	c.MinGap = 50 * time.Millisecond
	rec := &startRecorder{next: http.DefaultTransport}
	c.HTTP.Transport = rec
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.BusLocations(context.Background(), "1") }()
	}
	wg.Wait()
	starts := rec.starts
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < c.MinGap {
			t.Fatalf("starts %d and %d only %v apart", i-1, i, gap)
		}
	}
}

type startRecorder struct {
	mu     sync.Mutex
	starts []time.Time
	next   http.RoundTripper
}

func (r *startRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.starts = append(r.starts, time.Now())
	r.mu.Unlock()
	return r.next.RoundTrip(req)
}

func TestPacerBackoff(t *testing.T) {
	p := NewPacer(4)
	p.Report(100*time.Millisecond, errors.New("boom"))
	if p.Interval() != 500*time.Millisecond {
		t.Fatalf("after error interval %v", p.Interval())
	}
	p.Report(6*time.Second, nil) // slow answer counts as trouble
	if p.Interval() != time.Second {
		t.Fatalf("after slow interval %v", p.Interval())
	}
	for i := 0; i < 10; i++ {
		p.Report(10*time.Second, nil)
	}
	if p.Interval() != 2*time.Second {
		t.Fatalf("backoff not capped at 8x: %v", p.Interval())
	}
	for i := 0; i < 200; i++ {
		p.Report(100*time.Millisecond, nil)
	}
	if p.Interval() != 250*time.Millisecond {
		t.Fatalf("did not recover: %v", p.Interval())
	}
}

func TestPacerClamp(t *testing.T) {
	if NewPacer(50).Interval() != 200*time.Millisecond {
		t.Fatal("rps not clamped to 5")
	}
	if NewPacer(0).Interval() != 250*time.Millisecond {
		t.Fatal("non-positive rps should use the default 4")
	}
}

func TestPacerWaitHonoursContext(t *testing.T) {
	p := NewPacer(0.5)
	ctx, cancel := context.WithCancel(context.Background())
	p.Wait(ctx) // first slot is immediate
	cancel()
	if err := p.Wait(ctx); err == nil {
		t.Fatal("cancelled wait returned nil")
	}
}

// Garbage from OASA never panics and never yields a vehicle with bad coordinates.
func TestBusLocationsGarbage(t *testing.T) {
	ok := `{"VEH_NO":"1","CS_DATE":"Oct  5 2026 11:10:38:000AM","CS_LAT":"37.95","CS_LNG":"23.71","ROUTE_CODE":"1","VEH_HEADING":"5"}`
	srv := fakeAPI(t, map[string]string{
		"getBusLocation:html":     "<html><body>Service Unavailable</body></html>",
		"getBusLocation:obj":      `{"foo":1}`,
		"getBusLocation:nulls":    `[null,{},` + ok + `]`,
		"getBusLocation:missing":  `[{"VEH_NO":"2"},` + ok + `]`,
		"getBusLocation:zero":     `[{"VEH_NO":"3","CS_DATE":"Oct  5 2026 11:10:38:000AM","CS_LAT":"0","CS_LNG":"0","ROUTE_CODE":"1"},` + ok + `]`,
		"getBusLocation:far":      `[{"VEH_NO":"4","CS_DATE":"Oct  5 2026 11:10:38:000AM","CS_LAT":"NaN","CS_LNG":"23.7","ROUTE_CODE":"1"},{"VEH_NO":"5","CS_LAT":"51.5","CS_LNG":"-0.1"},` + ok + `]`,
		"getBusLocation:noid":     `[{"VEH_NO":"","CS_DATE":"Oct  5 2026 11:10:38:000AM","CS_LAT":"37.95","CS_LNG":"23.71","ROUTE_CODE":"1"},{"VEH_NO":"7","CS_DATE":"Oct  5 2026 11:10:38:000AM","CS_LAT":"37.95","CS_LNG":"23.71"},` + ok + `]`,
		"getBusLocation:nanhead":  `[{"VEH_NO":"1","CS_DATE":"Oct  5 2026 11:10:38:000AM","CS_LAT":"37.95","CS_LNG":"23.71","ROUTE_CODE":"1","VEH_HEADING":"NaN"}]`,
		"getBusLocation:redirect": "",
		"getBusLocation:baddate":  `[{"VEH_NO":"6","CS_DATE":"yesterday","CS_LAT":"37.95","CS_LNG":"23.71","ROUTE_CODE":"1"}]`,
	})
	defer srv.Close()
	c := New(srv.URL+"/", nil)
	ctx := context.Background()
	for _, code := range []string{"html", "obj"} {
		if _, err := c.BusLocations(ctx, code); err == nil {
			t.Errorf("%s: no error", code)
		}
	}
	for _, code := range []string{"nulls", "missing", "zero", "far", "noid"} {
		vs, err := c.BusLocations(ctx, code)
		if err != nil || len(vs) != 1 || vs[0].VehNo != "1" {
			t.Errorf("%s: %+v %v", code, vs, err)
		}
	}
	if vs, err := c.BusLocations(ctx, "nanhead"); err != nil || len(vs) != 1 || vs[0].Heading != 0 {
		t.Errorf("nanhead: %+v %v", vs, err)
	}
	// An unparseable time is kept but flagged; the scheduler drops it (TimeErr).
	if vs, err := c.BusLocations(ctx, "baddate"); err != nil || len(vs) != 1 || vs[0].TimeErr == nil {
		t.Errorf("baddate: %+v %v", vs, err)
	}
}

func TestRequestLogWindows(t *testing.T) {
	var l RequestLog
	t0 := time.Unix(1_800_000_000, 0)
	for i := 0; i < 40; i++ { // 4 per second for 10 s
		l.Add(t0.Add(time.Duration(i) * 250 * time.Millisecond))
	}
	l.Add(t0.Add(3*time.Second + 900*time.Millisecond)) // a 5th request in second 3
	per := l.PerSecond(t0.Add(9*time.Second), 10)
	if len(per) != 10 || per[3] != 5 || per[0] != 4 || per[9] != 4 {
		t.Fatalf("per second %v", per)
	}
	if got := MaxWindow(per, 1); got != 5 {
		t.Fatalf("max 1 s %d", got)
	}
	if got := MaxWindow(per, 10); got != 41 {
		t.Fatalf("max 10 s %d", got)
	}
	// Seconds older than the ring are forgotten, not double counted.
	per = l.PerSecond(t0.Add(time.Duration(RequestLogSeconds+20)*time.Second), RequestLogSeconds)
	if MaxWindow(per, RequestLogSeconds) != 0 {
		t.Fatal("old seconds leaked into the window")
	}
}

// A redirect is not followed: one request slot must be one request.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	c := New(srv.URL+"/", nil)
	if _, err := c.BusLocations(context.Background(), "1"); err == nil || hits != 1 || c.Requests() != 1 || c.Errors() != 1 {
		t.Fatalf("err %v hits %d requests %d errors %d", err, hits, c.Requests(), c.Errors())
	}
}
