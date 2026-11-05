package upstreamgtfs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

// A trimmed data.gov.gr package_show answer of the OSETH dataset (2026-11-05): an "all
// resources" zip, slices with overlaps and gaps, one slice without a url.
const ckanBody = `{"success": true, "result": {"resources": [
 {"id": "f0761273", "name": "Όλα τα δεδομένα πόρων", "url": "https://x/all.zip"},
 {"id": "4ad67d72", "name": "GTFS 06/08/2026-17/08/2026", "url": "https://x/20260812_gtfs.zip"},
 {"id": "331c9e2d", "name": "GTFS 27/04/2026-17/05/2026", "url": ""},
 {"id": "28c807ed", "name": "GTFS 02/10/2026-18/10/2026", "url": "https://x/20261002_gtfs.zip"},
 {"id": "11feeeca", "name": "GTFS 09/10/2026-25/10/2026", "url": "https://x/20261015_gtfs.zip"},
 {"id": "517f491b", "name": " GTFS 27/10/2026-15/11/2026 ", "url": "https://x/20261027_gtfs.zip"}
]}}`

func day(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, gtfs.Athens) }

func TestParseCKANKeepsDatedSlicesWithAURL(t *testing.T) {
	ss, err := ParseCKAN([]byte(ckanBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 4 {
		t.Fatalf("%d slices, want 4 (no 'all' zip, no empty url): %+v", len(ss), ss)
	}
	var last Slice
	for _, s := range ss {
		if s.ID == "517f491b" {
			last = s
		}
	}
	if last.URL != "https://x/20261027_gtfs.zip" || last.From != "2026-10-27" || last.To != "2026-11-15" {
		t.Fatalf("slice %+v", last)
	}
	if _, err := ParseCKAN([]byte(`{"success": false}`)); err == nil {
		t.Fatal("success:false accepted")
	}
}

func TestPickSlice(t *testing.T) {
	ss, _ := ParseCKAN([]byte(ckanBody))
	for _, c := range []struct {
		name  string
		today time.Time
		want  string
	}{
		{"inside one slice", day(2026, 11, 5, 8), "517f491b"},
		{"last day is included", day(2026, 11, 15, 23), "517f491b"},
		{"overlap: the newest slice", day(2026, 10, 12, 8), "11feeeca"},
		{"gap: the newest slice that already started", day(2026, 9, 1, 8), "4ad67d72"},
		{"after every slice: the newest", day(2026, 12, 1, 8), "517f491b"},
		// 23:30 UTC on 26/10 is already 27/10 in Athens.
		{"Athens date, not UTC", time.Date(2026, 10, 26, 23, 30, 0, 0, time.UTC), "517f491b"},
	} {
		got, ok := PickSlice(ss, c.today)
		if !ok || got.ID != c.want {
			t.Errorf("%s: got %q %v, want %q", c.name, got.ID, ok, c.want)
		}
	}
	if _, ok := PickSlice(ss, day(2026, 1, 1, 8)); ok {
		t.Error("a day before every slice picked one")
	}
}

func TestFetchCKAN(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		w.Write([]byte(ckanBody))
	}))
	defer srv.Close()
	ss, err := FetchCKAN(context.Background(), srv.Client(), srv.URL)
	if err != nil || len(ss) != 4 {
		t.Fatalf("%v %v", ss, err)
	}
}
