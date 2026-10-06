package history

import (
	"compress/gzip"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 8, 30, 0, 0, time.UTC)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

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
		zr, err := gzip.NewReader(f) // multistream: every member
		if err != nil {
			t.Fatal(err)
		}
		r = zr
	}
	b, _ := io.ReadAll(r)
	return string(b)
}

func names(t *testing.T, dir string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*"))
	for i := range m {
		m[i] = filepath.Base(m[i])
	}
	return strings.Join(m, " ")
}

func TestWritesNewFixesOnce(t *testing.T) {
	dir, now := t.TempDir(), t0
	w, err := Open(dir, 1<<30, 30*24*time.Hour, clock(&now), quiet)
	if err != nil {
		t.Fatal(err)
	}
	d := 140
	w.Add(Row{FixT: 100, Line: "040", RouteCode: "3922", Veh: "A", Lat: 37.9755, Lon: 23.7348, GTFS: "2026-10-06",
		TripID: "T,1 \"x\"\n", ShapeID: "S1", SM: 1234, DelayS: &d})
	w.Add(Row{FixT: 100, Line: "040", Veh: "A", SM: -1})           // the same fix polled again
	w.Add(Row{FixT: 90, Line: "040", Veh: "A", SM: -1})            // older
	w.Add(Row{FixT: 100, Line: "Α1", Veh: "B", Lat: -1.5, SM: -1}) // unmatched, other vehicle
	w.Close()
	w.Add(Row{FixT: 200, Veh: "C"}) // after Close: dropped, counted
	got := read(t, filepath.Join(dir, "2026-10-06T08.csv"))
	want := Header + `100,040,3922,A,37975500,23734800,2026-10-06,"T,1 ""x""",S1,1234,140` + "\n" + "100,Α1,,B,-1500000,0,,,,-1,\n"
	if got != want || w.Written() != 2 || w.Dropped() != 1 || w.Lost() != 0 {
		t.Fatalf("got %q (written %d, dropped %d, lost %d)", got, w.Written(), w.Dropped(), w.Lost())
	}
}

func TestRotatesAndGzipsHours(t *testing.T) {
	dir, now := t.TempDir(), t0
	w, _ := newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), quiet)
	w.write(Row{FixT: 1, Veh: "A", SM: -1})
	now = now.Add(time.Hour)
	w.write(Row{FixT: 2, Veh: "A", SM: -1})
	w.closeFile()
	if got := read(t, filepath.Join(dir, "2026-10-06T08.csv.gz")); got != Header+"1,,,A,0,0,,,,-1,\n" {
		t.Fatalf("old hour %q", got)
	}
	if names(t, dir) != "2026-10-06T08.csv.gz 2026-10-06T09.csv" {
		t.Fatalf("files %v", names(t, dir))
	}
	// No rows for a while: the tick ends the hour and opens nothing.
	now = now.Add(2 * time.Hour)
	w.write(Row{FixT: 3, Veh: "A", SM: -1}) // reopens 11
	now = now.Add(time.Hour)
	w.tick()
	now = now.Add(time.Hour)
	w.tick()
	if names(t, dir) != "2026-10-06T08.csv.gz 2026-10-06T09.csv.gz 2026-10-06T11.csv.gz" {
		t.Fatalf("after idle hours %v", names(t, dir))
	}
}

// A restart within the hour appends to its file; a restart in a later hour gzips it next to the
// archive the hour already has, each archive with its own header.
func TestRestartsKeepEveryRow(t *testing.T) {
	dir, now := t.TempDir(), t0
	w, _ := newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), quiet)
	w.write(Row{FixT: 1, Veh: "A", SM: -1})
	w.closeFile()
	w, _ = newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), quiet) // same hour
	w.write(Row{FixT: 2, Veh: "A", SM: -1})
	w.closeFile()
	if got := read(t, filepath.Join(dir, "2026-10-06T08.csv")); got != Header+"1,,,A,0,0,,,,-1,\n2,,,A,0,0,,,,-1,\n" {
		t.Fatalf("same-hour restart %q", got)
	}
	now = now.Add(time.Hour)
	newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), quiet)
	os.WriteFile(filepath.Join(dir, "2026-10-06T08.csv"), []byte(Header+"3,,,A,0,0,,,,-1,\n"), 0o644) // written by a clock that was behind
	newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), quiet)
	if got := names(t, dir); got != "2026-10-06T08.1.csv.gz 2026-10-06T08.csv.gz" {
		t.Fatalf("files %v", got)
	}
	if got := read(t, filepath.Join(dir, "2026-10-06T08.csv.gz")); got != Header+"1,,,A,0,0,,,,-1,\n2,,,A,0,0,,,,-1,\n" {
		t.Fatalf("first archive %q", got)
	}
	if got := read(t, filepath.Join(dir, "2026-10-06T08.1.csv.gz")); got != Header+"3,,,A,0,0,,,,-1,\n" {
		t.Fatalf("second archive %q", got)
	}
}

// A crash after the archive was written but before the sealed CSV was removed: the retry only
// removes it.
func TestCompressionRetryIsIdempotent(t *testing.T) {
	dir, now := t.TempDir(), t0
	part := filepath.Join(dir, "2026-10-06T07.csv.part")
	os.WriteFile(part, []byte(Header+"1,,,A,0,0,,,,-1,\n"), 0o644)
	gzipFile(part, filepath.Join(dir, "2026-10-06T07.csv.gz"))
	newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), quiet)
	if got := names(t, dir); got != "2026-10-06T07.csv.gz" {
		t.Fatalf("files %v", got)
	}
	if got := read(t, filepath.Join(dir, "2026-10-06T07.csv.gz")); got != Header+"1,,,A,0,0,,,,-1,\n" {
		t.Fatalf("archive %q", got)
	}
}

// A row a failed write left half done is cut before the next one.
func TestCutsAPartialRow(t *testing.T) {
	dir, now := t.TempDir(), t0
	os.WriteFile(filepath.Join(dir, "2026-10-06T08.csv"), []byte(Header+"1,,,A,0,0,,,,-1,\n2,,,A,0"), 0o644)
	w, _ := newWriter(dir, 1<<30, 30*24*time.Hour, clock(&now), quiet)
	w.write(Row{FixT: 3, Veh: "A", SM: -1})
	w.closeFile()
	if got := read(t, filepath.Join(dir, "2026-10-06T08.csv")); got != Header+"1,,,A,0,0,,,,-1,\n3,,,A,0,0,,,,-1,\n" {
		t.Fatalf("got %q", got)
	}
}

func TestPrunesByAgeThenSize(t *testing.T) {
	dir, now := t.TempDir(), t0
	put := func(name string, n int) { os.WriteFile(filepath.Join(dir, name), make([]byte, n), 0o644) }
	put("2026-08-20T10.csv.gz", 100) // 47 days old
	put("2026-10-04T09.csv.gz.tmp", 1000)
	put("2026-10-04T10.csv.gz", 1000)
	put("2026-10-05T10.csv.gz", 1000)
	put("2026-10-06T07.csv.gz", 1000)
	newWriter(dir, 2500, 30*24*time.Hour, clock(&now), quiet)
	if got := names(t, dir); got != "2026-10-05T10.csv.gz 2026-10-06T07.csv.gz" {
		t.Fatalf("left %v", got)
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

func TestCountsRowsTheDiskLost(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	dir, now := t.TempDir(), t0
	w, _ := newWriter(dir, 1<<30, time.Hour, clock(&now), quiet)
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	w.write(Row{FixT: 1, Veh: "A"})
	if w.Lost() != 1 || w.Written() != 0 {
		t.Fatalf("lost %d written %d", w.Lost(), w.Written())
	}
}

func TestIdleWriterMakesNoFiles(t *testing.T) {
	dir, now := t.TempDir(), t0
	w, _ := Open(dir, 1<<30, time.Hour, clock(&now), quiet)
	w.Close()
	if got := names(t, dir); got != "" {
		t.Fatalf("files %v", got)
	}
}

func TestSM(t *testing.T) {
	for m, want := range map[float64]int32{12.4: 12, 0: 0, -3: -1, math.NaN(): -1, math.Inf(1): -1, 3e9: -1} {
		if got := SM(m); got != want {
			t.Fatalf("SM(%v) = %d", m, got)
		}
	}
}
