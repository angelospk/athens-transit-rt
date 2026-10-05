package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/meta"
	"github.com/angelospk/athens-transit-rt/internal/sched"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

var monday1020 = time.Date(2026, 10, 5, 10, 20, 0, 0, gtfs.Athens)

func newApp(t *testing.T, now time.Time) *App {
	t.Helper()
	dir := t.TempDir()
	m := meta.Data{FetchedAt: now.Unix(), Lines: []telematics.Line{{LineCode: "R1", LineID: "L"}, {LineCode: "77", LineID: "Α1"}},
		Routes: map[string][]telematics.Route{"R1": {{RouteCode: "SH"}}, "77": {{RouteCode: "7701"}}}}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "telematics-meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(Config{StateDir: dir, RPS: 4, Matcher: match.Memory, Sched: sched.DefaultConfig()},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.now = func() time.Time { return now }
	a.sched.SetClock(a.now)
	a.SetFeed(gtfstest.StraightLine{Stops: 5, Trips: 6, First: 10 * 3600, Headway: 10, Leg: 5}.Feed(t), nil)
	return a
}

func get(t *testing.T, a *App, path string) (*http.Response, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	if res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%s: no CORS header", path)
	}
	return res, body
}

func maxAge(t *testing.T, res *http.Response) int {
	cc := res.Header.Get("Cache-Control")
	v, err := strconv.Atoi(strings.TrimPrefix(cc, "public, max-age="))
	if err != nil {
		t.Fatalf("Cache-Control %q", cc)
	}
	return v
}

func poll(a *App, at time.Time) {
	a.onLine(sched.LinePoll{Line: "L", Started: at, Done: at, Routes: 1, Obs: []match.Obs{
		{Vehicle: telematics.Vehicle{VehNo: "44548", RouteCode: "SH", Lat: 37.98, Lon: 23.725, Heading: 90, Time: at.Add(-10 * time.Second)}, LineCode: "R1"},
		{Vehicle: telematics.Vehicle{VehNo: "9", RouteCode: "SH", Lat: 38.5, Lon: 23.0, Time: at.Add(-5 * time.Second)}, LineCode: "R1"},
	}})
}

func TestLineLifecycle(t *testing.T) {
	a := newApp(t, monday1020)
	res, body := get(t, a, "/v1/lines/L")
	if res.StatusCode != 503 || !strings.Contains(string(body), `"warming_up"`) || maxAge(t, res) != 5 {
		t.Fatalf("before first poll: %d %s", res.StatusCode, body)
	}
	if !a.sched.IsWatched("L") {
		t.Fatal("request did not mark the line watched")
	}
	poll(a, monday1020)
	res, body = get(t, a, "/v1/lines/L")
	if res.StatusCode != 200 {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	if ma := maxAge(t, res); ma < 5 || ma > 300 {
		t.Fatalf("max-age %d", ma)
	}
	var lr LineResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatal(err)
	}
	if lr.Line != "L" || lr.UpdatedAt != monday1020.Unix() || lr.NextUpdateAt < monday1020.Unix()+5 || len(lr.Vehicles) != 2 {
		t.Fatalf("response %+v", lr)
	}
	v := lr.Vehicles[0]
	// 2.5 stops along at 10:19:50: T00 is 440 s late (cost 440), T01 160 s early (cost 3x160).
	if v.TripID == nil || *v.TripID != "T00" || v.Variant == nil || *v.Variant != "SH" || v.NextStopID == nil ||
		*v.NextStopID != "S3" || v.TripLabel == nil || *v.TripLabel != "10:00 STOP 0 → STOP 4" || v.DelayS == nil || *v.DelayS != 440 ||
		v.Bearing == nil || *v.Bearing != 90 || v.PositionAt != monday1020.Unix()-10 {
		b, _ := json.Marshal(v)
		t.Fatalf("matched vehicle %s", b)
	}
	u := lr.Vehicles[1]
	if u.TripID != nil || u.DelayS != nil || u.TripLabel != nil || u.NextStopID != nil || u.Bearing != nil ||
		u.Variant == nil || *u.Variant != "SH" {
		b, _ := json.Marshal(u)
		t.Fatalf("unmatched vehicle %s", b)
	}
}

// The JSON field names must be exactly those of the contract fixture.
func TestLineJSONMatchesFixture(t *testing.T) {
	a := newApp(t, monday1020)
	poll(a, monday1020)
	_, body := get(t, a, "/v1/lines/L")
	fix, err := os.ReadFile("../../docs/fixtures/line-040.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := keys(t, body), keys(t, fix); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %v, fixture %v", got, want)
	}
	_, body = get(t, a, "/v1/status")
	fix, _ = os.ReadFile("../../docs/fixtures/status.json")
	if got, want := keys(t, body), keys(t, fix); !reflect.DeepEqual(got, want) {
		t.Fatalf("status keys %v, fixture %v", got, want)
	}
}

func keys(t *testing.T, b []byte) []string {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	for k, v := range m {
		out = append(out, k)
		if arr, ok := v.([]any); ok && len(arr) > 0 {
			for k2 := range arr[0].(map[string]any) {
				out = append(out, k+"."+k2)
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestUnknownAndLatinLines(t *testing.T) {
	a := newApp(t, monday1020)
	res, body := get(t, a, "/v1/lines/999")
	if res.StatusCode != 404 || !strings.Contains(string(body), `"unknown_line"`) {
		t.Fatalf("unknown: %d %s", res.StatusCode, body)
	}
	// "A1" (Latin) and the percent-encoded Greek id both reach line Α1 (known, no GTFS trips).
	for _, p := range []string{"/v1/lines/A1", "/v1/lines/a1", "/v1/lines/%CE%911"} {
		res, body = get(t, a, p)
		if res.StatusCode != 200 || !strings.Contains(string(body), `"line":"Α1"`) || maxAge(t, res) != 120 {
			t.Fatalf("%s: %d %s", p, res.StatusCode, body)
		}
	}
}

func TestWarmingWhileRoutesUnknown(t *testing.T) {
	a := newApp(t, monday1020)
	// First boot: no metadata at all yet.
	a.meta = meta.Open(filepath.Join(t.TempDir(), "none.json"), a.client, func(func(context.Context) error) {}, nil)
	a.refreshLines()
	a.mu.Lock()
	a.known["L"] = true // known from the GTFS, routes unknown
	a.mu.Unlock()
	res, body := get(t, a, "/v1/lines/L")
	if res.StatusCode != 503 || !strings.Contains(string(body), "warming_up") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

func TestInactiveLineAtNight(t *testing.T) {
	a := newApp(t, time.Date(2026, 10, 5, 3, 0, 0, 0, gtfs.Athens))
	res, body := get(t, a, "/v1/lines/L")
	if res.StatusCode != 200 || !strings.Contains(string(body), `"vehicles":[]`) || maxAge(t, res) != 120 {
		t.Fatalf("inactive: %d %s", res.StatusCode, body)
	}
}

func TestStatusAndFeeds(t *testing.T) {
	a := newApp(t, monday1020)
	poll(a, monday1020)
	res, body := get(t, a, "/v1/status")
	var st StatusResponse
	json.Unmarshal(body, &st)
	if maxAge(t, res) != 10 || !st.OK || st.GTFSExpires != "2026-10-06" || st.GTFSVersion != "2026-07-06" ||
		st.LinesActive != 1 || st.BudgetRPS != 4 || st.UpdatedAt != monday1020.Unix() {
		t.Fatalf("status %s", body)
	}
	res, body = get(t, a, "/v1/gtfs-rt/vehicle_positions.pb")
	var msg rt.FeedMessage
	if err := proto.Unmarshal(body, &msg); err != nil || len(msg.Entity) != 2 || maxAge(t, res) != 15 {
		t.Fatalf("vehicle positions: %v %d", err, len(msg.Entity))
	}
	_, body = get(t, a, "/v1/gtfs-rt/trip_updates.json")
	if !strings.Contains(string(body), `"tripId": "T00"`) {
		t.Fatalf("trip updates json %s", body)
	}
	if res, _ := get(t, a, "/v1/gtfs-rt/other.pb"); res.StatusCode != 404 {
		t.Fatal("unknown feed name served")
	}
}

func TestCanonicalLine(t *testing.T) {
	for in, want := range map[string]string{" a1 ": "Α1", "x96": "Χ96", "040": "040", "Α1": "Α1", "e14": "Ε14"} {
		if got := CanonicalLine(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
