package lsa

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"reflect"
	"testing"
)

// TestScipyParity compares with scipy.optimize.linear_sum_assignment (1.18.1) on matrices
// with many ties, random reals, and the matcher's INFEASIBLE/UNMATCHED structure.
func TestScipyParity(t *testing.T) {
	b, err := os.ReadFile("testdata/scipy.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Cost       [][]float64
		Rows, Cols []int
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for k, c := range cases {
		rows, cols, err := Solve(c.Cost)
		if err != nil {
			t.Fatalf("case %d: %v", k, err)
		}
		if !reflect.DeepEqual(rows, c.Rows) || !reflect.DeepEqual(cols, c.Cols) {
			t.Fatalf("case %d: got %v %v, scipy %v %v\ncost %v", k, rows, cols, c.Rows, c.Cols, c.Cost)
		}
	}
}

func TestOptimalAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for k := 0; k < 500; k++ {
		n, m := 1+rng.Intn(5), 1+rng.Intn(6)
		cost := make([][]float64, n)
		for i := range cost {
			cost[i] = make([]float64, m)
			for j := range cost[i] {
				cost[i][j] = float64(rng.Intn(50))
			}
		}
		rows, cols, err := Solve(cost)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != min(n, m) {
			t.Fatalf("assigned %d, want %d", len(rows), min(n, m))
		}
		got := 0.0
		for i := range rows {
			got += cost[rows[i]][cols[i]]
		}
		if want := brute(cost); got != want {
			t.Fatalf("cost %v, optimum %v for %v", got, want, cost)
		}
	}
}

func brute(cost [][]float64) float64 {
	n, m := len(cost), len(cost[0])
	if n > m { // transpose
		t := make([][]float64, m)
		for j := range t {
			t[j] = make([]float64, n)
			for i := range cost {
				t[j][i] = cost[i][j]
			}
		}
		cost, n, m = t, m, n
	}
	best := math.Inf(1)
	used := make([]bool, m)
	var rec func(i int, acc float64)
	rec = func(i int, acc float64) {
		if i == n {
			best = min(best, acc)
			return
		}
		for j := 0; j < m; j++ {
			if !used[j] {
				used[j] = true
				rec(i+1, acc+cost[i][j])
				used[j] = false
			}
		}
	}
	rec(0, 0)
	return best
}

func TestInvalidAndEmpty(t *testing.T) {
	if r, c, err := Solve(nil); err != nil || len(r) != 0 || len(c) != 0 {
		t.Fatal("empty matrix")
	}
	if _, _, err := Solve([][]float64{{math.NaN()}}); err == nil {
		t.Fatal("NaN accepted")
	}
	if _, _, err := Solve([][]float64{{math.Inf(1), math.Inf(1)}}); err == nil {
		t.Fatal("infeasible accepted")
	}
}
