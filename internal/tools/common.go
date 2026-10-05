// Package tools holds the developer tools ported from upstream: compare, record and replay.
// They run on a developer machine, not on the VPS, with their own (gentle) request budget.
package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/server"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// StaleAfter: vehicles whose GPS fix is older than this are ignored (upstream STALE_AFTER).
const StaleAfter = 5 * time.Minute

// API is the part of the telematics client the tools use.
type API interface {
	Lines(ctx context.Context) ([]telematics.Line, error)
	Routes(ctx context.Context, lineCode string) ([]telematics.Route, error)
	Stops(ctx context.Context, routeCode string) ([]telematics.RouteStop, error)
	BusLocations(ctx context.Context, routeCode string) ([]telematics.Vehicle, error)
	StopArrivals(ctx context.Context, stopCode string) ([]telematics.Arrival, error)
}

// RouteCodes of one line: route code -> line code, iterated in sorted order.
type RouteCodes map[string]string

func (rc RouteCodes) Sorted() []string {
	out := make([]string, 0, len(rc))
	for c := range rc {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// ResolveLines returns {line: {route code: line code}} for a comma-separated list of line
// numbers, and the requested lines OASA does not know.
func ResolveLines(ctx context.Context, api API, spec string) (map[string]RouteCodes, []string, error) {
	wanted := map[string]bool{}
	for _, s := range strings.Split(spec, ",") {
		if s = strings.TrimSpace(s); s != "" {
			wanted[server.CanonicalLine(s)] = true
		}
	}
	all, err := api.Lines(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]RouteCodes{}
	for _, l := range all {
		if !wanted[l.LineID] {
			continue
		}
		routes, err := api.Routes(ctx, l.LineCode)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range routes {
			if out[l.LineID] == nil {
				out[l.LineID] = RouteCodes{}
			}
			out[l.LineID][r.RouteCode] = l.LineCode
		}
	}
	var missing []string
	for l := range wanted {
		if out[l] == nil {
			missing = append(missing, l)
		}
	}
	sort.Strings(missing)
	return out, missing, nil
}

// SortedLines returns the keys of a line map in order.
func SortedLines[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for l := range m {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// LoadFeed loads a GTFS zip (only the given lines; nil = all) or a snapshot file.
func LoadFeed(zipPath, snapshotPath string, lines map[string]bool) (*gtfs.Feed, error) {
	if snapshotPath != "" {
		f, err := os.Open(snapshotPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return gtfs.ReadSnapshot(f)
	}
	if zipPath == "" {
		return nil, fmt.Errorf("need --gtfs or --snapshot")
	}
	return gtfs.LoadZip(zipPath, gtfs.LoadOptions{Lines: lines})
}

// StopsFunc adapts the API for match.RouteMapper (errors count as "unknown").
func StopsFunc(ctx context.Context, api API) match.StopsFunc {
	return func(rc string) ([]string, bool) {
		stops, err := api.Stops(ctx, rc)
		if err != nil {
			return nil, false
		}
		out := make([]string, len(stops))
		for i, s := range stops {
			out[i] = s.StopCode
		}
		return out, true
	}
}

// Poll fetches fresh vehicles of every route of a line, tagged with their line code. One
// failing route does not drop the others.
func Poll(ctx context.Context, api API, routes RouteCodes, now time.Time, logf func(string, ...any)) []match.Obs {
	var out []match.Obs
	for _, rc := range routes.Sorted() {
		vs, err := api.BusLocations(ctx, rc)
		if err != nil {
			logf("getBusLocation %s: %v", rc, err)
			continue
		}
		out = append(out, Fresh(vs, routes[rc], now)...)
	}
	return out
}

// Fresh drops fixes older than StaleAfter (or unparseable) and tags the rest.
func Fresh(vs []telematics.Vehicle, lineCode string, now time.Time) []match.Obs {
	var out []match.Obs
	for _, v := range vs {
		if v.TimeErr == nil && now.Sub(v.Time) <= StaleAfter {
			out = append(out, match.Obs{Vehicle: v, LineCode: lineCode})
		}
	}
	return out
}
