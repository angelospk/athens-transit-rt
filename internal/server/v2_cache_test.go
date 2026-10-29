package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/model"
	"github.com/angelospk/athens-transit-rt/internal/sched"
)

// writeV2's cache headers for each Source state: only warming_up and down make a response
// incomplete (5 s, no ETag); indicative is a steady state and is cached like ok.
func TestV2CacheHeadersByState(t *testing.T) {
	for _, c := range []struct {
		state  string
		maxAge int
		etag   bool
	}{
		{model.OK, 3600, true},
		{model.Stale, 3600, true},
		{model.Indicative, 3600, true},
		{model.WarmingUp, 5, false},
		{model.Down, 5, false},
	} {
		src := []model.SourceState{{ID: "oasa", State: model.OK}, {ID: "stasy", State: c.state}}
		rec := httptest.NewRecorder()
		writeV2(rec, httptest.NewRequest("GET", "/v2/lines", nil), v2ListMaxAge, src, map[string]any{"sources": src})
		res := rec.Result()
		if got := maxAge(t, res); got != c.maxAge || (res.Header.Get("ETag") != "") != c.etag {
			t.Errorf("%s: max-age %d, ETag %q", c.state, got, res.Header.Get("ETag"))
		}
	}
	// Below a minute no ETag, whatever the state.
	rec := httptest.NewRecorder()
	writeV2(rec, httptest.NewRequest("GET", "/v2/runs/x", nil), v2RunMaxAge, []model.SourceState{{State: model.OK}}, 1)
	if res := rec.Result(); maxAge(t, res) != v2RunMaxAge || res.Header.Get("ETag") != "" {
		t.Errorf("run: max-age %d, ETag %q", maxAge(t, res), res.Header.Get("ETag"))
	}
}

// An expired static GTFS (oasa indicative), the state production can stay in for weeks, keeps
// the hour-long cache and the ETag on every list endpoint, and the long snapshot life.
func TestV2ExpiredGTFSIsCachedLong(t *testing.T) {
	after := time.Date(2026, 10, 12, 10, 20, 0, 0, gtfs.Athens) // a Monday; the feed ends 2026-10-06
	a := oasaOnly(newApp(t, after))
	poll(a, after)
	var ls v2Lines
	if _, ma := v2get(t, a, "/v2/lines", &ls); stateOf(ls.Sources, "oasa") != model.Indicative || ma != v2ListMaxAge {
		t.Fatalf("max-age %d %s", ma, asJSON(ls.Sources))
	}
	for _, p := range []string{"/v2/lines", "/v2/stops", "/v2/lines/" + esc("oasa:L")} {
		res, _ := get(t, a, p)
		if maxAge(t, res) != v2ListMaxAge || res.Header.Get("ETag") == "" {
			t.Errorf("%s: max-age %d, ETag %q", p, maxAge(t, res), res.Header.Get("ETag"))
		}
	}
	if _, ma := v2get(t, a, "/v2/vehicles", nil); ma <= v2IncompleteAge {
		t.Errorf("/v2/vehicles max-age %d: the snapshot is rebuilt every 5 s", ma)
	}
}

// /v2/stops is built once: the same strong ETag across requests while the stops and the
// Source states stay the same (an ok Source's updated_at moving does not count), 304 on a
// match, the gzipped bytes decoding to the plain ones, and a new body when a state changes.
func TestV2StopsPrebuilt(t *testing.T) {
	now := monday1020
	a := oasaOnly(newAppWith(t, sched.DefaultConfig(), func() time.Time { return now }))
	poll(a, now)

	plain, body := do(a, "GET", "/v2/stops", nil)
	gz, zbody := do(a, "GET", "/v2/stops", map[string]string{"Accept-Encoding": "gzip"})
	etag := plain.Header.Get("ETag")
	if etag == "" || strings.HasPrefix(etag, "W/") || maxAge(t, plain) != v2ListMaxAge {
		t.Fatalf("ETag %q max-age %d", etag, maxAge(t, plain))
	}
	if gz.Header.Get("Content-Encoding") != "gzip" || gz.Header.Get("ETag") == etag {
		t.Fatalf("gzip: %q %q", gz.Header.Get("Content-Encoding"), gz.Header.Get("ETag"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(zbody))
	if err != nil {
		t.Fatal(err)
	}
	if unz, _ := io.ReadAll(zr); !bytes.Equal(unz, body) {
		t.Fatal("gzipped body differs from the plain one")
	}
	if !strings.Contains(string(body), `"oasa:S3"`) {
		t.Fatalf("index without its stops: %.200s", body)
	}

	now = now.Add(30 * time.Second)
	poll(a, now) // oasa's updated_at moves; the index does not
	if r, _ := do(a, "GET", "/v2/stops", nil); r.Header.Get("ETag") != etag {
		t.Fatalf("ETag changed with updated_at: %q", r.Header.Get("ETag"))
	}
	if r, _ := do(a, "GET", "/v2/stops", map[string]string{"If-None-Match": etag}); r.StatusCode != 304 {
		t.Fatalf("If-None-Match: %d", r.StatusCode)
	}

	now = now.Add(10 * time.Minute) // no poll for 10 min: oasa stale
	r, b := do(a, "GET", "/v2/stops", nil)
	if r.Header.Get("ETag") == etag || !strings.Contains(string(b), `"stale"`) {
		t.Fatalf("state change kept the old body: %q %.300s", r.Header.Get("ETag"), b)
	}
}
