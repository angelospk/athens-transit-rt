package model

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// knotMetres is the contract's client rule for motion.knots, as a reference: where a marker
// stands at time t, in metres along path + path_beyond. Clients implement the same rule and
// test it against docs/fixtures/v2/motion-cases.json.
func knotMetres(knots [][2]int64, t int64) float64 {
	if len(knots) == 0 || t <= knots[0][1] {
		return 0
	}
	for i := 1; i < len(knots); i++ {
		a, b := knots[i-1], knots[i]
		if t <= b[1] {
			return float64(a[0]) + float64(t-a[1])/float64(b[1]-a[1])*float64(b[0]-a[0])
		}
	}
	return float64(knots[len(knots)-1][0])
}

func TestMotionCases(t *testing.T) {
	raw, err := os.ReadFile("../../docs/fixtures/v2/motion-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			Name  string       `json:"name"`
			Knots [][2]int64   `json:"knots"`
			At    [][2]float64 `json:"at"` // [unix time, metres]
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 4 {
		t.Fatalf("%d cases", len(f.Cases))
	}
	for _, c := range f.Cases {
		if err := CheckKnots(c.Knots); err != "" {
			t.Errorf("%s: knots break the contract: %s", c.Name, err)
		}
		for _, at := range c.At {
			if got := knotMetres(c.Knots, int64(at[0])); math.Abs(got-at[1]) > 0.5 {
				t.Errorf("%s: at %v: %.1f m, want %v", c.Name, at[0], got, at[1])
			}
		}
	}
}
