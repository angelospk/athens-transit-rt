package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/sched"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// pollOne publishes one vehicle of line L at lon (on the west-east line at lat 37.98).
func pollOne(a *App, at time.Time, id string, lon float64, fix time.Time) {
	a.onLine(sched.LinePoll{Line: "L", Started: at, Done: at, Routes: 1, Obs: []match.Obs{
		{Vehicle: telematics.Vehicle{VehNo: id, RouteCode: "SH", Lat: 37.98, Lon: lon, Heading: 90, Time: fix}, LineCode: "R1"},
	}})
}

func do(a *App, method, path string, hdr map[string]string) (*http.Response, []byte) {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

func decodeVehicles(t *testing.T, b []byte) VehiclesResponse {
	t.Helper()
	var v VehiclesResponse
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return v
}

func TestVehiclesSnapshot(t *testing.T) {
	now := monday1020
	a := newAppWith(t, sched.DefaultConfig(), func() time.Time { return now })
	res, body := get(t, a, "/v1/vehicles")
	if res.StatusCode != 200 || !bytes.Contains(body, []byte(`"vehicles":[]`)) {
		t.Fatalf("empty: %d %s", res.StatusCode, body)
	}
	poll(a, now)
	now = now.Add(vehiclesEvery) // the empty snapshot expires
	res, body = get(t, a, "/v1/vehicles")
	if a.sched.IsWatched("L") {
		t.Fatal("/v1/vehicles marked a line watched")
	}
	v := decodeVehicles(t, body)
	if res.StatusCode != 200 || len(v.Vehicles) != 2 || v.UpdatedAt != now.Unix() || v.NextUpdateAt != now.Unix()+30 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if c := v.Vehicles[0]; c.Line != "L" || c.ID != "44548" || c.Lat != 37.98 || c.Lon != 23.725 || c.Variant == nil {
		t.Fatalf("vehicle %+v", c)
	}
	if ma := maxAge(t, res); ma != 30 {
		t.Fatalf("max-age %d", ma)
	}
	etag := res.Header.Get("ETag")

	// Same bytes until the next build, whatever was polled meanwhile (this poll replaces L's vehicles).
	pollOne(a, now, "77", 23.71, now)
	now = now.Add(26 * time.Second)
	res2, body2 := get(t, a, "/v1/vehicles")
	if !bytes.Equal(body, body2) || res2.Header.Get("ETag") != etag || maxAge(t, res2) != 5 {
		t.Fatalf("not the same snapshot: max-age %d", maxAge(t, res2))
	}
	now = now.Add(4 * time.Second)
	_, body3 := get(t, a, "/v1/vehicles")
	if v := decodeVehicles(t, body3); len(v.Vehicles) != 1 || v.Vehicles[0].ID != "77" || v.UpdatedAt != now.Unix() {
		t.Fatalf("not rebuilt: %s", body3)
	}

	// A new static feed drops the snapshot at once.
	a.SetFeed(a.world().feed, nil)
	if a.vehSnap != nil {
		t.Fatal("snapshot kept after SetFeed")
	}
}

func TestVehiclesFilters(t *testing.T) {
	now := monday1020
	a := newAppWith(t, sched.DefaultConfig(), func() time.Time { return now })
	a.onLine(sched.LinePoll{Line: "Α1", Started: now, Done: now, Routes: 1, Obs: []match.Obs{
		{Vehicle: telematics.Vehicle{VehNo: "old", RouteCode: "7701", Lat: 37.99, Lon: 23.73, Time: now.Add(-vehicleMaxAge - time.Second)}, LineCode: "77"},
		{Vehicle: telematics.Vehicle{VehNo: "edge", RouteCode: "7701", Lat: 37.99, Lon: 23.73, Time: now.Add(-vehicleMaxAge)}, LineCode: "77"},
	}})
	_, body := get(t, a, "/v1/vehicles")
	if v := decodeVehicles(t, body).Vehicles; len(v) != 1 || v[0].ID != "edge" {
		t.Fatalf("GPS age filter: %s", body)
	}
	now = now.Add(vehiclesEvery)
	poll(a, now.Add(-time.Minute))
	// 44548 moved to Α1 later: listed once, under its newest line.
	a.onLine(sched.LinePoll{Line: "Α1", Started: now, Done: now, Routes: 1, Obs: []match.Obs{
		{Vehicle: telematics.Vehicle{VehNo: "44548", RouteCode: "7701", Lat: 37.99, Lon: 23.73, Time: now.Add(-5 * time.Second)}, LineCode: "77"},
	}})
	_, body = get(t, a, "/v1/vehicles")
	seen := map[string]string{}
	for _, c := range decodeVehicles(t, body).Vehicles {
		if _, dup := seen[c.ID]; dup {
			t.Fatalf("%s twice: %s", c.ID, body)
		}
		seen[c.ID] = c.Line
	}
	if _, ok := seen["old"]; ok || seen["44548"] != "Α1" || seen["9"] != "L" {
		t.Fatalf("vehicles %v", seen)
	}
}

func TestVehiclesHTTP(t *testing.T) {
	now := monday1020
	a := newAppWith(t, sched.DefaultConfig(), func() time.Time { return now })
	poll(a, now)
	res, raw := do(a, "GET", "/v1/vehicles", nil)
	resZ, gz := do(a, "GET", "/v1/vehicles", map[string]string{"Accept-Encoding": "gzip"})
	if res.Header.Get("Content-Encoding") != "" || resZ.Header.Get("Content-Encoding") != "gzip" ||
		res.Header.Get("Vary") != "Accept-Encoding" || resZ.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("headers %v / %v", res.Header, resZ.Header)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	unz, _ := io.ReadAll(zr)
	if !bytes.Equal(unz, raw) || res.Header.Get("Content-Length") == "" {
		t.Fatal("gzip body differs from raw")
	}
	etag, gzETag := res.Header.Get("ETag"), resZ.Header.Get("ETag")
	if etag == "" || etag == gzETag {
		t.Fatalf("etags %q %q", etag, gzETag)
	}
	for _, inm := range []string{etag, "W/" + etag, `"x", ` + etag, "*"} {
		r, b := do(a, "GET", "/v1/vehicles", map[string]string{"If-None-Match": inm})
		if r.StatusCode != 304 || len(b) != 0 || r.Header.Get("ETag") != etag || r.Header.Get("Cache-Control") == "" {
			t.Fatalf("If-None-Match %s: %d %q", inm, r.StatusCode, b)
		}
	}
	if r, _ := do(a, "GET", "/v1/vehicles", map[string]string{"If-None-Match": gzETag}); r.StatusCode != 200 {
		t.Fatal("gzip etag matched the raw body")
	}
	if r, b := do(a, "HEAD", "/v1/vehicles", nil); r.StatusCode != 200 || len(b) != 0 || r.Header.Get("Content-Length") != res.Header.Get("Content-Length") {
		t.Fatalf("HEAD: %d %d bytes", r.StatusCode, len(b))
	}
	if r, _ := do(a, "POST", "/v1/vehicles", nil); r.StatusCode != http.StatusNotFound { // like every path
		t.Fatalf("POST: %d", r.StatusCode)
	}
	if r, _ := do(a, "GET", "/v1/vehicles/", nil); r.StatusCode != 404 {
		t.Fatalf("trailing slash: %d", r.StatusCode)
	}
}

func TestVehiclesConcurrent(t *testing.T) {
	a := newApp(t, monday1020)
	var wg sync.WaitGroup
	etags := make([]string, 16)
	for i := range etags {
		wg.Add(2)
		go func() {
			defer wg.Done()
			res, _ := do(a, "GET", "/v1/vehicles", map[string]string{"Accept-Encoding": "gzip"})
			etags[i] = res.Header.Get("ETag")
		}()
		go func() {
			defer wg.Done()
			poll(a, monday1020)
			a.forget()
		}()
	}
	wg.Wait()
	for _, e := range etags {
		if e != etags[0] {
			t.Fatalf("several snapshots within one period: %v", etags)
		}
	}
}

// TestLineMotion: a matched vehicle 300 m further after 30 s has speed 10 m/s, a path to its
// next stop and a continuation through the next ones, on /v1/lines and /v1/vehicles alike.
func TestLineMotion(t *testing.T) {
	now := monday1020
	a := newAppWith(t, sched.DefaultConfig(), func() time.Time { return now })
	step := 300 / (geo.XY(37.98, 23.71)[0] - geo.XY(37.98, 23.70)[0]) * 0.01 // degrees of lon per 300 m
	pollOne(a, now, "v", 23.72, now.Add(-20*time.Second))
	now = now.Add(30 * time.Second)
	pollOne(a, now, "v", 23.72+step, now.Add(-20*time.Second))
	_, body := get(t, a, "/v1/lines/L")
	var lr LineResponse
	if err := json.Unmarshal(body, &lr); err != nil || len(lr.Vehicles) != 1 {
		t.Fatalf("%v %s", err, body)
	}
	v := lr.Vehicles[0]
	if v.TripID == nil || v.Speed == nil || math.Abs(*v.Speed-10) > 0.05 {
		t.Fatalf("vehicle %s", body)
	}
	// path to the next stop; path_beyond on to the third stop ahead (stops every 0.01° lon; the
	// last one, S4, is the second ahead).
	if len(v.Path) != 2 || math.Abs(v.Path[0][1]-(23.72+step)) > 2e-5 || v.Path[1] != [2]float64{37.98, 23.73} {
		t.Fatalf("path %v", v.Path)
	}
	if len(v.PathBeyond) != 2 || v.PathBeyond[0] != v.Path[1] || v.PathBeyond[1] != [2]float64{37.98, 23.74} {
		t.Fatalf("path_beyond %v", v.PathBeyond)
	}
	if len(v.PathStops) != 2 || math.Abs(float64(v.PathStops[0])-(877.6-300)) > 3 || math.Abs(float64(v.PathStops[1])-(2*877.6-300)) > 3 {
		t.Fatalf("path_stops %v", v.PathStops)
	}
	_, body = get(t, a, "/v1/vehicles")
	c := decodeVehicles(t, body).Vehicles[0]
	if *c.Speed != *v.Speed || len(c.Path) != 2 || c.Line != "L" || !slices.Equal(c.PathStops, v.PathStops) ||
		len(c.PathBeyond) != 2 {
		t.Fatalf("/v1/vehicles %s", body)
	}
	// Far from the shape: no motion.
	pollOne(a, now, "w", 23.72, now)
	a.onLine(sched.LinePoll{Line: "L", Started: now, Done: now, Routes: 1, Obs: []match.Obs{
		{Vehicle: telematics.Vehicle{VehNo: "w", RouteCode: "SH", Lat: 37.99, Lon: 23.72, Time: now.Add(time.Second)}, LineCode: "R1"},
	}})
	_, body = get(t, a, "/v1/lines/L")
	json.Unmarshal(body, &lr)
	if w := lr.Vehicles[0]; w.Speed != nil || w.Path != nil {
		t.Fatalf("1.1 km off the shape: %s", body)
	}
}
