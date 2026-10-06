package server

import (
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/release"
)

// benchApp publishes n vehicles spread over the lines of a feed, each somewhere along one of its
// line's trips, with the speed and path a moving matched vehicle gets. ATRT_BENCH_STATE=<state
// dir> uses the real snapshot (real shapes, real path sizes); otherwise a synthetic line.
func benchApp(b *testing.B, n int) *App {
	b.Helper()
	var f *gtfs.Feed
	if dir := os.Getenv("ATRT_BENCH_STATE"); dir != "" {
		var err error
		if f, _, err = (&release.Fetcher{Dir: dir}).LoadLocal(); err != nil || f == nil {
			b.Fatalf("snapshot in %s: %v", dir, err)
		}
	} else {
		f = gtfstest.StraightLine{Stops: 30, Trips: 6, First: 10 * 3600, Headway: 10, Leg: 2}.Feed(b)
	}
	now := monday1020
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return now },
		lines: map[string]*lineData{}, motion: newMotion()}
	m := match.New(f, nil, match.Hungarian)
	a.w = &world{feed: f, matcher: m}
	rng := rand.New(rand.NewPCG(1, 2))
	lines := f.Lines()
	start := time.Now()
	paths, points, tries := 0, 0, 0
	for i := 0; i < n; i++ {
		line := lines[rng.IntN(len(lines))]
		trips := f.TripsForLine(line)
		t := f.Trip(trips[rng.IntN(len(trips))])
		g := m.Geometry(t)
		if g == nil || !g.FromShape {
			if tries++; tries > 100*n {
				b.Fatal("no trips with shape geometry")
			}
			i--
			continue
		}
		next := 1 + rng.IntN(len(g.StopAlong)-1)
		s := g.StopAlong[next-1] + rng.Float64()*(g.StopAlong[next]-g.StopAlong[next-1])
		at := g.Line.At(s)
		lat, lon := latLon(at)
		v := Vehicle{ID: strconv.Itoa(40000 + i), Lat: lat, Lon: lon,
			Bearing: ptr(90.0), PositionAt: now.Unix() - 30, Variant: ptr(f.Shapes[t.Shape].ID), DelayS: ptr(120),
			Speed: ptr(float64(rng.IntN(120)) / 10)}
		v.Path, v.PathStops = pathAhead(g.Line, s, v.Speed, g.StopAlong[next:])
		if v.Path != nil {
			paths++
			points += len(v.Path)
		}
		d := a.lines[line]
		if d == nil {
			d = &lineData{updated: now, feed: f}
			a.lines[line] = d
		}
		d.vehicles = append(d.vehicles, v)
	}
	b.Logf("%d vehicles on %d lines, %d paths, %.1f points/path, motion+path %.1f µs/vehicle",
		n, len(a.lines), paths, float64(points)/float64(max(paths, 1)), float64(time.Since(start).Microseconds())/float64(n))
	return a
}

func latLon(p [2]float64) (float64, float64) {
	lat, lon := geo.LatLon(p)
	return round5(lat), round5(lon)
}

// BenchmarkBuildVehicles: go test ./internal/server -bench BuildVehicles -benchmem
func BenchmarkBuildVehicles(b *testing.B) {
	a := benchApp(b, 1500)
	s, err := a.buildVehicles(monday1020)
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := a.buildVehicles(monday1020); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(s.raw))/1024, "raw-KB")
	b.ReportMetric(float64(len(s.gz))/1024, "gzip-KB")
}
