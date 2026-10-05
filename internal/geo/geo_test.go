package geo

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type golden struct {
	Pts     [][2]float64
	Q       [2]float64
	Heading *float64
	Cands   [][3]any
	Stops   [][2]float64
	Snapped []float64
	Prog    []float64
	Cum     []float64
	Bearing []float64
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-6*math.Max(1, math.Abs(b)) }

// TestGoldenAgainstUpstream compares with values computed by upstream oasa_rt/matcher.py.
func TestGoldenAgainstUpstream(t *testing.T) {
	b, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []golden
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for k, c := range cases {
		xy := make([][2]float64, len(c.Pts))
		for i, p := range c.Pts {
			xy[i] = XY(p[0], p[1])
		}
		line := NewPolyline(xy)
		for i := range c.Cum {
			if !near(line.Cum[i], c.Cum[i]) {
				t.Fatalf("case %d cum[%d] %v want %v", k, i, line.Cum[i], c.Cum[i])
			}
		}
		for i := range c.Bearing {
			if !near(line.Bearing[i], c.Bearing[i]) {
				t.Fatalf("case %d bearing[%d] %v want %v", k, i, line.Bearing[i], c.Bearing[i])
			}
		}
		q := XY(c.Q[0], c.Q[1])
		heading, has := 0.0, c.Heading != nil
		if has {
			heading = *c.Heading
		}
		got := line.Candidates(q[0], q[1], 250, heading, has)
		if len(got) != len(c.Cands) {
			t.Fatalf("case %d: %d candidates, want %d", k, len(got), len(c.Cands))
		}
		for i, w := range c.Cands {
			if !near(got[i].Along, w[0].(float64)) || !near(got[i].Off, w[1].(float64)) || got[i].Wrong != w[2].(bool) {
				t.Fatalf("case %d cand %d: %+v want %v", k, i, got[i], w)
			}
		}
		stops := make([][2]float64, len(c.Stops))
		for i, s := range c.Stops {
			stops[i] = XY(s[0], s[1])
		}
		snapped := SnapStops(line, stops)
		if (snapped == nil) != (c.Snapped == nil) {
			t.Fatalf("case %d: snapped %v want %v", k, snapped, c.Snapped)
		}
		for i := range c.Snapped {
			if !near(snapped[i], c.Snapped[i]) {
				t.Fatalf("case %d snapped[%d] %v want %v", k, i, snapped[i], c.Snapped[i])
			}
		}
		if c.Prog != nil {
			g := &Geometry{Line: line, StopAlong: snapped, FromShape: true}
			if p := g.Progress(c.Prog[0]); !near(p, c.Prog[1]) {
				t.Fatalf("case %d progress(%v) = %v want %v", k, c.Prog[0], p, c.Prog[1])
			}
		}
	}
}

func TestLoopGivesTwoCandidates(t *testing.T) {
	// East along a street, a detour north, then west along the same street.
	pts := [][2]float64{XY(37.98, 23.70), XY(37.98, 23.72), XY(38.00, 23.72), XY(38.00, 23.71),
		XY(37.98, 23.71), XY(37.98, 23.70)}
	line := NewPolyline(pts)
	q := XY(37.9801, 23.705)
	c := line.Candidates(q[0], q[1], 250, 90, true)
	if len(c) != 2 {
		t.Fatalf("got %d candidates, want 2", len(c))
	}
	if c[0].Wrong || !c[1].Wrong {
		t.Fatalf("heading east fits the first pass only: %+v", c)
	}
}
