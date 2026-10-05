package match

import (
	"sync"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

const MinPatternSimilarity = 0.6

// StopsFunc returns the live stop codes of a route code. ok=false means "not known yet"
// (the result is then not cached, so a later call can succeed).
type StopsFunc func(routeCode string) (stops []string, ok bool)

// RouteMapper maps a live telematics route code to the GTFS shapes it corresponds to.
//
// Route codes in the live API and shape ids in the GTFS usually coincide but not always
// (line 040 runs on 3922/3923/3924 while the GTFS uses 5512/5513/5535), so the mapping is made
// by comparing stop sequences.
type RouteMapper struct {
	feed  *gtfs.Feed
	stops StopsFunc
	mu    sync.Mutex
	cache map[[2]string]map[int32]bool
}

func NewRouteMapper(feed *gtfs.Feed, stops StopsFunc) *RouteMapper {
	return &RouteMapper{feed: feed, stops: stops, cache: map[[2]string]map[int32]bool{}}
}

// ShapesFor returns the set of shape indices (-1 = trips without a shape) a route code runs on.
func (rm *RouteMapper) ShapesFor(line, routeCode string) map[int32]bool {
	key := [2]string{line, routeCode}
	rm.mu.Lock()
	cached, ok := rm.cache[key]
	rm.mu.Unlock()
	if ok {
		return cached
	}
	f := rm.feed
	// Most common stop pattern per shape; ties go to the pattern seen first (Counter.most_common).
	counts := map[int32]map[int32]int{}
	patOrder := map[int32][]int32{}
	var shapeOrder []int32
	for _, ti := range f.TripsForLine(line) {
		t := &f.Trips[ti]
		c := counts[t.Shape]
		if c == nil {
			c = map[int32]int{}
			counts[t.Shape] = c
			shapeOrder = append(shapeOrder, t.Shape)
		}
		if c[t.Pattern] == 0 {
			patOrder[t.Shape] = append(patOrder[t.Shape], t.Pattern)
		}
		c[t.Pattern]++
	}
	best := map[int32]int32{}
	for _, s := range shapeOrder {
		b := patOrder[s][0]
		for _, p := range patOrder[s][1:] {
			if counts[s][p] > counts[s][b] {
				b = p
			}
		}
		best[s] = b
	}
	result := map[int32]bool{}
	for _, s := range shapeOrder {
		if f.ShapeIDOf(s) == routeCode {
			result[s] = true
			rm.store(key, result)
			return result
		}
	}
	live, ok := rm.stops(routeCode)
	if !ok {
		return result
	}
	liveSet := map[string]bool{}
	for _, s := range live {
		liveSet[s] = true
	}
	for _, s := range shapeOrder {
		patSet := map[string]bool{}
		for _, st := range f.Patterns[best[s]].Stops {
			patSet[f.Stops[st].ID] = true
		}
		common := 0
		for s := range liveSet {
			if patSet[s] {
				common++
			}
		}
		if len(liveSet) > 0 && float64(common)/float64(max(len(liveSet), len(patSet))) >= MinPatternSimilarity {
			result[s] = true
		}
	}
	rm.store(key, result)
	return result
}

func (rm *RouteMapper) store(key [2]string, v map[int32]bool) {
	rm.mu.Lock()
	rm.cache[key] = v
	rm.mu.Unlock()
}
