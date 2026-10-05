package tools

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
)

func recordedStops(t *testing.T) match.StopsFunc {
	b, err := os.ReadFile("../match/testdata/cycles.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Stops map[string][]struct{ StopCode string }
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	return func(rc string) ([]string, bool) {
		var out []string
		for _, s := range rec.Stops[rc] {
			out = append(out, s.StopCode)
		}
		return out, true
	}
}

func snapshot040(t *testing.T) *gtfs.Feed {
	in, err := os.Open("../match/testdata/feed-040-A1.snap")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	f, err := gtfs.ReadSnapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestReplayParity runs the replay report on a recording of lines 040 and Α1 (8 cycles,
// synthetic OASA ETAs) and compares it with upstream oasa_rt/replay.py on the same file.
func TestReplayParity(t *testing.T) {
	db, err := sql.Open("sqlite", "file:testdata/record-040-A1.sqlite?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rec, err := LoadRecording(db)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	Replay(&out, rec, snapshot040(t), recordedStops(t))
	want, _ := os.ReadFile("testdata/upstream-replay.txt")
	got, wl := strings.Split(out.String(), "\n"), strings.Split(strings.TrimRight(string(want), "\n"), "\n")
	got = got[:len(got)-1]
	for i := 0; i < max(len(got), len(wl)); i++ {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Errorf("line %d:\n go:       %q\n upstream: %q", i+1, g, w)
		}
	}
}
