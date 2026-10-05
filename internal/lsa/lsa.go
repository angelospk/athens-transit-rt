// Package lsa solves the rectangular linear sum assignment problem. It is a port of
// scipy.optimize.linear_sum_assignment (scipy/optimize/rectangular_lsap/rectangular_lsap.cpp,
// BSD-3-Clause), the shortest augmenting path method of Crouse (2016), including scipy's
// tie-breaking so both give the same assignment.
package lsa

import (
	"errors"
	"math"
	"sort"
)

var (
	ErrInvalid    = errors.New("lsa: cost matrix contains NaN or -Inf")
	ErrInfeasible = errors.New("lsa: cost matrix is infeasible")
)

// Solve returns row and column indices of a minimum-cost assignment of min(rows, cols)
// pairs, rows sorted ascending (like scipy).
func Solve(cost [][]float64) (rows, cols []int, err error) {
	nr := len(cost)
	if nr == 0 || len(cost[0]) == 0 {
		return []int{}, []int{}, nil
	}
	nc := len(cost[0])
	transpose := nc < nr
	flat := make([]float64, 0, nr*nc)
	if transpose {
		for j := 0; j < nc; j++ {
			for i := 0; i < nr; i++ {
				flat = append(flat, cost[i][j])
			}
		}
		nr, nc = nc, nr
	} else {
		for _, row := range cost {
			flat = append(flat, row...)
		}
	}
	for _, c := range flat {
		if c != c || math.IsInf(c, -1) {
			return nil, nil, ErrInvalid
		}
	}

	u, v := make([]float64, nr), make([]float64, nc)
	shortest := make([]float64, nc)
	path := make([]int, nc)
	col4row, row4col := make([]int, nr), make([]int, nc)
	for i := range path {
		path[i] = -1
	}
	for i := range col4row {
		col4row[i] = -1
	}
	for i := range row4col {
		row4col[i] = -1
	}
	sr, sc := make([]bool, nr), make([]bool, nc)
	remaining := make([]int, nc)

	for cur := 0; cur < nr; cur++ {
		sink, minVal := augmentingPath(nc, flat, u, v, path, row4col, shortest, cur, sr, sc, remaining)
		if sink < 0 {
			return nil, nil, ErrInfeasible
		}
		u[cur] += minVal
		for i := 0; i < nr; i++ {
			if sr[i] && i != cur {
				u[i] += minVal - shortest[col4row[i]]
			}
		}
		for j := 0; j < nc; j++ {
			if sc[j] {
				v[j] -= minVal - shortest[j]
			}
		}
		j := sink
		for {
			i := path[j]
			row4col[j] = i
			col4row[i], j = j, col4row[i]
			if i == cur {
				break
			}
		}
	}

	rows, cols = make([]int, nr), make([]int, nr)
	if transpose {
		order := make([]int, nr)
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return col4row[order[a]] < col4row[order[b]] })
		for k, i := range order {
			rows[k], cols[k] = col4row[i], i
		}
	} else {
		for i := 0; i < nr; i++ {
			rows[i], cols[i] = i, col4row[i]
		}
	}
	return rows, cols, nil
}

func augmentingPath(nc int, cost, u, v []float64, path, row4col []int, shortest []float64, i int,
	sr, sc []bool, remaining []int) (int, float64) {
	minVal := 0.0
	num := nc
	// Filled in reverse so a constant cost matrix yields the identity (scipy #11602).
	for it := 0; it < nc; it++ {
		remaining[it] = nc - it - 1
	}
	clear(sr)
	clear(sc)
	for j := range shortest {
		shortest[j] = math.Inf(1)
	}
	sink := -1
	for sink == -1 {
		index := -1
		lowest := math.Inf(1)
		sr[i] = true
		for it := 0; it < num; it++ {
			j := remaining[it]
			r := minVal + cost[i*nc+j] - u[i] - v[j]
			if r < shortest[j] {
				path[j] = i
				shortest[j] = r
			}
			// Among equal minima prefer a column that gives a new sink.
			if shortest[j] < lowest || (shortest[j] == lowest && row4col[j] == -1) {
				lowest = shortest[j]
				index = it
			}
		}
		minVal = lowest
		if math.IsInf(minVal, 1) {
			return -1, 0
		}
		j := remaining[index]
		if row4col[j] == -1 {
			sink = j
		} else {
			i = row4col[j]
		}
		sc[j] = true
		num--
		remaining[index] = remaining[num]
	}
	return sink, minVal
}
