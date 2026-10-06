package history

import (
	"compress/gzip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 8, 30, 0, 0, time.UTC)

func clock(t *time.Time) func() time.Time { return func() time.Time { return *t } }

func read(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		r = zr
	}
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestWritesNewFixesOnce(t *testing.T) {
	dir, now := t.TempDir(), t0
	w, err := Open(dir, 1<<30, 30*24*time.Hour, clock(&now), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	d := 140
	w.Add(Row{FixT: 100, Line: "040", RouteCode: "3922", Veh: "A", Lat: 37.9755, Lon: 23.7348, GTFS: "2026-10-06",
		TripID: "T1", ShapeID: "S1", SM: 1234, DelayS: &d})
	w.Add(Row{FixT: 100, Line: "040", Veh: "A", SM: -1})           // the same fix polled again
	w.Add(Row{FixT: 90, Line: "040", Veh: "A", SM: -1})            // older
	w.Add(Row{FixT: 100, Line: "Α1", Veh: "B", Lat: -1.5, SM: -1}) // unmatched, other vehicle
	w.Close()
	got := read(t, filepath.Join(dir, "2026-10-06T08.csv"))
	want := Header + "100,040,3922,A,37975500,23734800,2026-10-06,T1,S1,1234,140\n" + "100,Α1,,B,-1500000,0,,,,-1,\n"
	if got != want || w.Written() != 2 {
		t.Fatalf("got %q (%d rows)", got, w.Written())
	}
}

func TestRotatesAndGzipsHours(t *testing.T) {
	dir, now := t.TempDir(), t0
	w, _ := newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), slog.Default())
	w.write(Row{FixT: 1, Veh: "A", SM: -1})
	now = now.Add(time.Hour)
	w.write(Row{FixT: 2, Veh: "A", SM: -1})
	w.closeFile()
	if got := read(t, filepath.Join(dir, "2026-10-06T08.csv.gz")); got != Header+"1,,,A,0,0,,,,-1,\n" {
		t.Fatalf("old hour %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-06T08.csv")); !os.IsNotExist(err) {
		t.Fatal("plain file of the old hour left")
	}
	if got := read(t, filepath.Join(dir, "2026-10-06T09.csv")); got != Header+"2,,,A,0,0,,,,-1,\n" {
		t.Fatalf("new hour %q", got)
	}
	// A restart gzips the leftover plain file.
	if _, err := newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-06T09.csv.gz")); err != nil {
		t.Fatal("leftover not gzipped")
	}
}

func TestPrunesByAgeThenSize(t *testing.T) {
	dir, now := t.TempDir(), t0
	put := func(name string, n int) {
		os.WriteFile(filepath.Join(dir, name+".csv.gz"), make([]byte, n), 0o644)
	}
	put("2026-08-20T10", 100) // 47 days old
	put("2026-10-04T10", 1000)
	put("2026-10-05T10", 1000)
	put("2026-10-06T07", 1000)
	newWriter(dir, 2500, 30*24*time.Hour, clock(&now), slog.Default())
	names, _ := filepath.Glob(filepath.Join(dir, "*.csv.gz"))
	for i := range names {
		names[i] = filepath.Base(names[i])
	}
	if strings.Join(names, " ") != "2026-10-05T10.csv.gz 2026-10-06T07.csv.gz" {
		t.Fatalf("left %v", names)
	}
}

func TestDropsWhenFull(t *testing.T) {
	w := &Writer{ch: make(chan Row, 1)}
	w.Add(Row{})
	w.Add(Row{})
	if w.Dropped() != 1 {
		t.Fatalf("dropped %d", w.Dropped())
	}
}

func TestIdleHoursMakeNoFiles(t *testing.T) {
	dir, now := t.TempDir(), t0
	w, _ := Open(dir, 1<<30, time.Hour, clock(&now), slog.Default())
	w.Close()
	if names, _ := filepath.Glob(filepath.Join(dir, "*")); len(names) != 0 {
		t.Fatalf("files %v", names)
	}
}
