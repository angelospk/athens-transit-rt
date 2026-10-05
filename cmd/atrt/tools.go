package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
	"github.com/angelospk/athens-transit-rt/internal/tools"
)

// Tools run on a developer machine. Their default pace is gentler than the server's.
const toolRPS = 2

func toolClient(fs *flag.FlagSet) (*float64, *string) {
	return fs.Float64("rps", toolRPS, "OASA requests per second (max 5)"),
		fs.String("telematics-url", "", "OASA telematics API base URL")
}

func lineSet(lines map[string]tools.RouteCodes) map[string]bool {
	out := map[string]bool{}
	for l := range lines {
		out[l] = true
	}
	return out
}

// compare: our ETAs vs OASA's own getStopArrivals predictions, once.
func compare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	gtfsZip := fs.String("gtfs", "data/osy_gtfs.zip", "static GTFS zip")
	snap := fs.String("snapshot", "", "snapshot file instead of --gtfs")
	linesSpec := fs.String("lines", "", "comma-separated line numbers, e.g. 040,550")
	ahead := fs.Int("ahead", 4, "compare at the stop this many stops ahead")
	rps, telURL := toolClient(fs)
	fs.Parse(args)
	if *linesSpec == "" {
		return fmt.Errorf("--lines is required")
	}
	ctx := context.Background()
	api := telematics.New(*telURL, telematics.NewPacer(*rps))
	lines, _, err := tools.ResolveLines(ctx, api, *linesSpec)
	if err != nil {
		return err
	}
	f, err := tools.LoadFeed(*gtfsZip, *snap, lineSet(lines))
	if err != nil {
		return err
	}
	mapper := match.NewRouteMapper(f, tools.StopsFunc(ctx, api))
	shapes, straight := match.New(f, mapper, match.Greedy), match.New(f, mapper, match.Greedy)
	straight.UseShapes = false
	methods := []struct {
		name string
		m    *match.Matcher
	}{{"shapes", shapes}, {"straight", straight}}
	now := time.Now().In(gtfs.Athens)
	arrivals := map[string][]telematics.Arrival{}
	oasaETA := func(stop, veh string) (time.Time, bool) {
		if _, ok := arrivals[stop]; !ok {
			arrivals[stop], _ = api.StopArrivals(ctx, stop)
		}
		for _, a := range arrivals[stop] {
			if a.VehCode == veh {
				return now.Add(time.Duration(a.Minutes) * time.Minute), true
			}
		}
		return time.Time{}, false
	}
	mins := func(t time.Time) float64 { return t.Sub(now).Minutes() }
	errs := map[string][]float64{}
	matched := map[string]int{}
	total := 0
	fmt.Printf("%7s %-8s %-26s %7s  sched   ours   OASA  (min from now)\n", "vehicle", "method", "trip", "stop")
	for _, line := range tools.SortedLines(lines) {
		obs := tools.Poll(ctx, api, lines[line], now, log.Printf)
		total += len(obs)
		res := map[string]map[string]match.Result{}
		for _, me := range methods {
			res[me.name] = map[string]match.Result{}
			for _, r := range me.m.MatchLine(line, obs) {
				res[me.name][r.VehicleID] = r
			}
		}
		var vids []string
		for v := range res["shapes"] {
			vids = append(vids, v)
		}
		sort.Strings(vids)
		for _, vid := range vids {
			type row struct {
				name              string
				r                 match.Result
				stop              string
				sched, ours, oasa time.Time
				hasOASA, ok       bool
			}
			var rows []row
			for _, me := range methods {
				r := res[me.name][vid]
				if !r.Matched() {
					rows = append(rows, row{name: me.name, r: r})
					continue
				}
				matched[me.name]++
				if r.Waiting {
					continue
				}
				t := f.Trip(r.Trip)
				k := min(r.NextIndex+*ahead, f.NumStops(t)-1)
				st := f.StopTime(t, k)
				sched := r.Day.Add(time.Duration(st.Arr) * time.Second)
				stopID := f.Stops[st.Stop].ID
				o, has := oasaETA(stopID, vid)
				rows = append(rows, row{me.name, r, stopID, sched, sched.Add(time.Duration(r.Delay) * time.Second), o, has, true})
			}
			for _, rw := range rows {
				if !rw.ok {
					fmt.Printf("%7s %-8s line %s route %s: no trip match\n", vid, rw.name, line, rw.r.RouteCode)
					continue
				}
				ref := "    --"
				if rw.hasOASA {
					ref = fmt.Sprintf("%6.1f", mins(rw.oasa))
				}
				fmt.Printf("%7s %-8s %-26s %7s %6.1f %6.1f %s\n", vid, rw.name, f.Trips[rw.r.Trip].ID, rw.stop,
					mins(rw.sched), mins(rw.ours), ref)
			}
			// Score only vehicles both methods matched and OASA predicts, so samples are comparable.
			var scored []row
			for _, rw := range rows {
				if rw.ok && rw.hasOASA {
					scored = append(scored, rw)
				}
			}
			if len(scored) == len(methods) {
				errs["schedule"] = append(errs["schedule"], abs(mins(scored[0].sched)-mins(scored[0].oasa)))
				for _, rw := range scored {
					errs[rw.name] = append(errs[rw.name], abs(mins(rw.ours)-mins(rw.oasa)))
				}
			}
		}
	}
	fmt.Printf("\n%d vehicles; matched to a trip: shapes %d, straight %d\n", total, matched["shapes"], matched["straight"])
	if n := len(errs["shapes"]); n > 0 {
		fmt.Printf("%d vehicles compared against OASA's prediction:\n", n)
		for _, name := range []string{"schedule", "shapes", "straight"} {
			e := errs[name]
			sort.Float64s(e)
			mean, mx := 0.0, 0.0
			for _, v := range e {
				mean += v / float64(len(e))
				mx = max(mx, v)
			}
			fmt.Printf("  %-9s: median |error| %.1f min, mean %.1f, max %.1f\n", name, median(e), mean, mx)
		}
	}
	return nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func parseLocal(v string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02T15:04", v, gtfs.Athens)
}

// record: GPS fixes, matches and OASA's predictions to SQLite.
func record(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	linesSpec := fs.String("lines", "", "comma-separated line numbers")
	gtfsZip := fs.String("gtfs", "data/osy_gtfs.zip", "static GTFS zip")
	snap := fs.String("snapshot", "", "snapshot file instead of --gtfs")
	dbPath := fs.String("db", "data/record.sqlite", "SQLite output")
	startS := fs.String("start", "", "Athens local time to start, e.g. 2026-10-05T07:00")
	endS := fs.String("end", "", "Athens local time to stop (required)")
	interval := fs.Duration("interval", 30*time.Second, "cycle length")
	empty := fs.Duration("empty-interval", 300*time.Second, "re-poll routes without vehicles this rarely")
	arrRate := fs.Float64("arrivals-rate", 0.5, "getStopArrivals requests per second")
	step := fs.Int("stop-step", 4, "sample every n-th stop of each route")
	rps, telURL := toolClient(fs)
	fs.Parse(args)
	if *linesSpec == "" || *endS == "" {
		return fmt.Errorf("--lines and --end are required")
	}
	end, err := parseLocal(*endS)
	if err != nil {
		return err
	}
	logf := func(format string, a ...any) {
		fmt.Printf("[%s] %s\n", time.Now().In(gtfs.Athens).Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
	}
	if !time.Now().Before(end) {
		logf("end time %s already passed, nothing to do", end.Format("2006-01-02 15:04"))
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *startS != "" {
		start, err := parseLocal(*startS)
		if err != nil {
			return err
		}
		if time.Now().Before(start) {
			logf("waiting until %s", start.Format("2006-01-02 15:04"))
			if err := tools.SleepCtx(ctx, time.Until(start)); err != nil {
				return nil
			}
		}
	}
	api := telematics.New(*telURL, telematics.NewPacer(*rps))
	lines, missing, err := tools.ResolveLines(ctx, api, *linesSpec)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		logf("unknown lines: %v", missing)
	}
	f, err := tools.LoadFeed(*gtfsZip, *snap, lineSet(lines))
	if err != nil {
		return err
	}
	stops, err := tools.SampleStops(ctx, api, lines, *step)
	if err != nil {
		return err
	}
	db, err := tools.OpenDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	routes := 0
	for _, rc := range lines {
		routes += len(rc)
	}
	logf("%d lines, %d routes, %d trips, %d sampled stops", len(lines), routes, len(f.Trips), len(stops))
	rec := &tools.Recorder{API: api, DB: db, Feed: f, Lines: lines, Interval: *interval, EmptyInterval: *empty,
		ArrivalsRate: *arrRate, Stops: stops, Requests: api.Requests, Logf: logf,
		Now: func() time.Time { return time.Now().In(gtfs.Athens) }, Sleep: tools.SleepCtx,
		Matcher: match.New(f, match.NewRouteMapper(f, tools.StopsFunc(ctx, api)), match.Greedy)}
	for time.Now().Before(end) && ctx.Err() == nil {
		if err := rec.Cycle(ctx, end); err != nil && ctx.Err() == nil {
			// keep recording through transient network/API failures
			logf("cycle failed: %v", err)
			tools.SleepCtx(ctx, 5*time.Second)
		}
	}
	logf("done")
	return nil
}

// replay: score the matchers on a recording.
func replay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	gtfsZip := fs.String("gtfs", "data/osy_gtfs.zip", "static GTFS zip")
	snap := fs.String("snapshot", "", "snapshot file instead of --gtfs")
	blocks := fs.Bool("blocks", false, "only check whether vehicles follow the GTFS vehicle blocks")
	rps, telURL := toolClient(fs)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: atrt replay [flags] recording.sqlite")
	}
	db, err := sql.Open("sqlite", "file:"+fs.Arg(0)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	rec, err := tools.LoadRecording(db)
	if err != nil {
		return err
	}
	ctx := context.Background()
	stops := tools.StopsFunc(ctx, telematics.New(*telURL, telematics.NewPacer(*rps)))
	var lines map[string]bool
	if !*blocks { // blocks span several lines: load the whole network
		lines = map[string]bool{}
		for _, c := range rec.Cycles {
			for l := range rec.ByCycle[c] {
				lines[l] = true
			}
		}
	}
	f, err := tools.LoadFeed(*gtfsZip, *snap, lines)
	if err != nil {
		return err
	}
	if *blocks {
		tools.Blocks(os.Stdout, rec, f, stops)
	} else {
		tools.Replay(os.Stdout, rec, f, stops)
	}
	return nil
}
