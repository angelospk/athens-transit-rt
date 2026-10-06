package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/history"
	"github.com/angelospk/athens-transit-rt/internal/sched"
)

// TestFixHistory: each new fix of a polled line becomes one history row, with trip, shape and
// position along it when matched; the same fix polled again adds nothing.
func TestFixHistory(t *testing.T) {
	now := monday1020
	a := newAppWith(t, sched.DefaultConfig(), func() time.Time { return now })
	dir := t.TempDir()
	h, err := history.Open(dir, 1<<30, 24*time.Hour, a.now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	a.hist = h
	fix := now.Add(-20 * time.Second)
	pollOne(a, now, "v", 23.72, fix)
	pollOne(a, now.Add(30*time.Second), "v", 23.72, fix) // OASA repeats the fix
	h.Close()
	names, _ := filepath.Glob(filepath.Join(dir, "*.csv"))
	if len(names) != 1 {
		t.Fatalf("files %v", names)
	}
	b, _ := os.ReadFile(names[0])
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || lines[0]+"\n" != history.Header {
		t.Fatalf("history %q", b)
	}
	f := strings.Split(lines[1], ",")
	if f[0] != strconv.FormatInt(fix.Unix(), 10) || f[1] != "L" || f[2] != "SH" || f[3] != "v" || f[4] != "37980000" || f[7] == "" || f[8] != "SH" ||
		f[9] == "-1" || f[10] == "" {
		t.Fatalf("row %q", lines[1])
	}
}
