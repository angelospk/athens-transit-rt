package telematics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	var slots []time.Time
	for i := 0; i < 4; i++ {
		slots = append(slots, p.reserve(t0))
	}
	for i, s := range slots {
		if want := t0.Add(time.Duration(i) * 250 * time.Millisecond); !s.Equal(want) {
			t.Fatalf("slot %d = %v, want %v", i, s.Sub(t0), want.Sub(t0))
		}
	}
	// Idle time does not accumulate into a burst.
	later := t0.Add(time.Minute)
	if a, b := p.reserve(later), p.reserve(later); !a.Equal(later) || b.Sub(a) != 250*time.Millisecond {
		t.Fatalf("after idle: %v %v", a.Sub(later), b.Sub(later))
	}
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
