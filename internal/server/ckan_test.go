package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/sched"
)

// ckanServer serves a package_show answer naming one slice, and that slice's zip.
type ckanServer struct {
	srv       *httptest.Server
	slice     atomic.Value // "id|dd/mm/yyyy-dd/mm/yyyy"
	zip       atomic.Value // []byte
	downloads atomic.Int32
}

func newCKANServer(t *testing.T, zip []byte) *ckanServer {
	c := &ckanServer{}
	c.slice.Store("aaa|27/10/2026-15/11/2026")
	c.zip.Store(zip)
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id, dates string
		fmt.Sscanf(c.slice.Load().(string), "%3s|%s", &id, &dates)
		if r.URL.Path == "/pkg" {
			fmt.Fprintf(w, `{"success":true,"result":{"resources":[{"id":%q,"name":"GTFS %s","url":"%s/%s.zip"}]}}`,
				id, dates, c.srv.URL, id)
			return
		}
		c.downloads.Add(1)
		w.Write(c.zip.Load().([]byte))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func thessApp(t *testing.T, state, ckanURL string) *App {
	a := New(Config{City: "thessaloniki", OASTHSalt: "salt-", StateDir: state, RPS: 1, Matcher: match.Memory,
		Sched: sched.DefaultConfig(), GTFSCKANURL: ckanURL}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.now = func() time.Time { return time.Date(2026, 11, 5, 8, 0, 0, 0, gtfs.Athens) }
	return a
}

// Thessaloniki (no --gtfs): the GTFS comes from the data.gov.gr slice that covers today, is kept
// in the state dir, is downloaded again only for a new slice, and a bad file never replaces it.
func TestThessalonikiGTFSFromCKAN(t *testing.T) {
	good, err := os.ReadFile(gtfstest.Zip(t, gtfstest.StraightLine{Stops: 3, Trips: 2, First: 36000, Headway: 10, Leg: 5}.Files()))
	if err != nil {
		t.Fatal(err)
	}
	c := newCKANServer(t, good)
	state := t.TempDir()
	a := thessApp(t, state, c.srv.URL+"/pkg")
	ctx := context.Background()

	if err := a.loadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	w := a.world()
	if w == nil || w.manifest == nil || w.manifest.GTFSVersion != "2026-10-27" || w.manifest.GTFSExpires != "2026-11-15" {
		t.Fatalf("world %+v", w)
	}
	if c.downloads.Load() != 1 {
		t.Fatalf("%d downloads", c.downloads.Load())
	}

	// Same slice: nothing is downloaded and the feed stays.
	if err := a.checkCKAN(ctx); err != nil {
		t.Fatal(err)
	}
	if c.downloads.Load() != 1 || a.world() != w {
		t.Fatalf("same slice: %d downloads, feed replaced %v", c.downloads.Load(), a.world() != w)
	}

	// A new slice that is not a GTFS zip: an error, the old feed and file stay.
	c.slice.Store("bbb|05/11/2026-30/11/2026")
	c.zip.Store([]byte("not a zip"))
	if err := a.checkCKAN(ctx); err == nil {
		t.Fatal("bad zip accepted")
	}
	if a.world() != w {
		t.Fatal("bad zip replaced the feed")
	}

	// The slice is fixed: it loads, with its dates.
	c.zip.Store(good)
	if err := a.checkCKAN(ctx); err != nil {
		t.Fatal(err)
	}
	if m := a.world().manifest; a.world() == w || m.GTFSVersion != "2026-11-05" || m.GTFSExpires != "2026-11-30" {
		t.Fatalf("new slice not loaded: %+v", m)
	}

	// A restart with data.gov.gr down starts from the kept file.
	b := thessApp(t, state, "http://127.0.0.1:1/pkg")
	if err := b.loadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	if m := b.world().manifest; m == nil || m.GTFSVersion != "2026-11-05" {
		t.Fatalf("kept file not loaded: %+v", m)
	}
	// Only the zip of the slice in use is kept.
	if zips, _ := filepath.Glob(filepath.Join(state, "gtfs-ckan-*.zip")); len(zips) != 1 ||
		filepath.Base(zips[0]) != "gtfs-ckan-bbb.zip" {
		t.Fatalf("zips %v", zips)
	}
}

// A crash after the new zip is on disk but before the slice file names it: a restart still
// loads the old slice with its own dates.
func TestCKANCrashBeforeCommitKeepsOldSlice(t *testing.T) {
	good, _ := os.ReadFile(gtfstest.Zip(t, gtfstest.StraightLine{Stops: 3, Trips: 2, First: 36000, Headway: 10, Leg: 5}.Files()))
	c := newCKANServer(t, good)
	state := filepath.Join(t.TempDir(), "not-yet") // the state dir does not exist yet
	ctx := context.Background()
	if err := thessApp(t, state, c.srv.URL+"/pkg").loadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "gtfs-ckan-bbb.zip"), good, 0o644) // the half-done next slice
	b := thessApp(t, state, "http://127.0.0.1:1/pkg")
	if err := b.loadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	if m := b.world().manifest; m == nil || m.GTFSVersion != "2026-10-27" {
		t.Fatalf("manifest %+v, want the committed slice", m)
	}
}

// A zip with the required files but no trips is not a feed: it never replaces the kept one.
func TestCKANRejectsFeedWithoutTrips(t *testing.T) {
	files := gtfstest.StraightLine{Stops: 3, Trips: 2, First: 36000, Headway: 10, Leg: 5}.Files()
	good, _ := os.ReadFile(gtfstest.Zip(t, files))
	files["trips.txt"] = "route_id,service_id,trip_id,shape_id,direction_id\n"
	files["stop_times.txt"] = "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n"
	empty, _ := os.ReadFile(gtfstest.Zip(t, files))
	c := newCKANServer(t, good)
	a := thessApp(t, t.TempDir(), c.srv.URL+"/pkg")
	ctx := context.Background()
	if err := a.loadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	w := a.world()
	c.slice.Store("bbb|05/11/2026-30/11/2026")
	c.zip.Store(empty)
	if err := a.checkCKAN(ctx); err == nil {
		t.Fatal("feed without trips accepted")
	}
	if a.world() != w {
		t.Fatal("feed replaced")
	}
}

// Thessaloniki uses the OSETH dataset unless a URL is given; Athens has none.
func TestCityCKANDefault(t *testing.T) {
	th, _ := CityByID("thessaloniki")
	ath, _ := CityByID("athens")
	if th.GTFSCKANURL != "https://data.gov.gr/api/3/action/package_show?id=42c9a7da-c86c-48b1-914c-f340e8bef00d" || ath.GTFSCKANURL != "" {
		t.Fatalf("thessaloniki %q, athens %q", th.GTFSCKANURL, ath.GTFSCKANURL)
	}
}
