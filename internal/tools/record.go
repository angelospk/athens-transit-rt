package tools

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
)

// Schema is upstream's (oasa_rt/record.py), so recordings from either tool replay in both.
const Schema = `
CREATE TABLE IF NOT EXISTS fix(veh TEXT, ts REAL, fetched REAL, line TEXT, line_code TEXT,
    route_code TEXT, lat REAL, lon REAL, heading REAL, PRIMARY KEY(veh, ts));
CREATE TABLE IF NOT EXISTS match(cycle REAL, veh TEXT, line TEXT, route_code TEXT, fix_ts REAL,
    trip_id TEXT, service_date TEXT, delay INTEGER, next_index INTEGER, waiting INTEGER);
CREATE TABLE IF NOT EXISTS oasa_eta(polled REAL, stop_id TEXT, veh TEXT, route_code TEXT, minutes INTEGER);
CREATE TABLE IF NOT EXISTS cycle(started REAL, routes_polled INTEGER, requests INTEGER,
    vehicles INTEGER, matched INTEGER, seconds REAL);
CREATE INDEX IF NOT EXISTS match_veh ON match(veh, cycle);
CREATE INDEX IF NOT EXISTS eta_stop ON oasa_eta(stop_id, veh, polled);
`

func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// Recorder records GPS fixes, our matches and OASA's arrival predictions.
type Recorder struct {
	API           API
	DB            *sql.DB
	Feed          *gtfs.Feed
	Matcher       *match.Matcher
	Lines         map[string]RouteCodes
	Interval      time.Duration // between cycles
	EmptyInterval time.Duration // routes with no vehicles are re-polled this rarely
	ArrivalsRate  float64       // getStopArrivals per second
	Stops         []string      // sampled stop codes, polled round-robin
	Requests      func() int64  // request counter (telematics.Client.Requests)
	Logf          func(string, ...any)
	Now           func() time.Time
	Sleep         func(context.Context, time.Duration) error

	nextPoll map[string]time.Time
	stopI    int
}

// Cycle polls the due routes, stores fixes and matches, then spends the rest of the interval
// sampling OASA's arrival predictions.
func (r *Recorder) Cycle(ctx context.Context, end time.Time) error {
	if r.nextPoll == nil {
		r.nextPoll = map[string]time.Time{}
	}
	started := r.Now()
	req0 := r.Requests()
	polled, vehicles, matched := 0, 0, 0
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, line := range SortedLines(r.Lines) {
		var rows []match.Obs
		for _, rc := range r.Lines[line].Sorted() {
			if r.nextPoll[rc].After(started) {
				continue
			}
			polled++
			got, err := r.API.BusLocations(ctx, rc)
			if err != nil {
				r.Logf("getBusLocation %s: %v", rc, err)
				r.nextPoll[rc] = started.Add(r.Interval)
				continue
			}
			if len(got) > 0 {
				r.nextPoll[rc] = started.Add(r.Interval)
			} else {
				r.nextPoll[rc] = started.Add(r.EmptyInterval)
			}
			rows = append(rows, Fresh(got, r.Lines[line][rc], started)...)
		}
		if len(rows) == 0 {
			continue
		}
		fetched := r.Now()
		results := r.Matcher.MatchLine(line, rows)
		for _, m := range results {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO fix VALUES (?,?,?,?,?,?,?,?,?)`,
				m.VehicleID, unix(m.Time), unix(fetched), line, m.LineCode, m.RouteCode, m.Lat, m.Lon, m.Bearing); err != nil {
				return err
			}
			var trip, date, delay, next any
			if m.Matched() {
				trip, date, delay, next = r.Feed.Trips[m.Trip].ID, gtfs.ServiceDate(m.Day), m.Delay, m.NextIndex
				matched++
			}
			waiting := 0
			if m.Waiting {
				waiting = 1
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO match VALUES (?,?,?,?,?,?,?,?,?,?)`,
				unix(started), m.VehicleID, line, m.RouteCode, unix(m.Time), trip, date, delay, next, waiting); err != nil {
				return err
			}
		}
		vehicles += len(results)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cycle VALUES (?,?,?,?,?,?)`, unix(started), polled,
		r.Requests()-req0, vehicles, matched, r.Now().Sub(started).Seconds()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.Logf("%d routes polled, %d vehicles, %d matched, %d requests", polled, vehicles, matched, r.Requests()-req0)

	// Spend the rest of the cycle sampling OASA's arrival predictions.
	for len(r.Stops) > 0 && r.Now().Before(started.Add(r.Interval-time.Second)) && r.Now().Before(end) {
		t0 := r.Now()
		stop := r.Stops[r.stopI%len(r.Stops)]
		r.stopI++
		arrivals, err := r.API.StopArrivals(ctx, stop)
		if err != nil {
			r.Logf("getStopArrivals %s: %v", stop, err)
		}
		polledAt := unix(r.Now())
		for _, a := range arrivals {
			if _, err := r.DB.ExecContext(ctx, `INSERT INTO oasa_eta VALUES (?,?,?,?,?)`,
				polledAt, stop, a.VehCode, a.RouteCode, a.Minutes); err != nil {
				return err
			}
		}
		if err := r.Sleep(ctx, time.Duration(float64(time.Second)/r.ArrivalsRate)-r.Now().Sub(t0)); err != nil {
			return err
		}
	}
	return r.Sleep(ctx, started.Add(r.Interval).Sub(r.Now()))
}

// SleepCtx sleeps for d (no-op when d <= 0) unless ctx ends first.
func SleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SampleStops returns every step'th stop of every route, deduplicated, in order.
func SampleStops(ctx context.Context, api API, lines map[string]RouteCodes, step int) ([]string, error) {
	if step < 1 {
		return nil, fmt.Errorf("stop step must be >= 1")
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range SortedLines(lines) {
		for _, rc := range lines[line].Sorted() {
			stops, err := api.Stops(ctx, rc)
			if err != nil {
				return nil, err
			}
			for i := 0; i < len(stops); i += step {
				if s := stops[i].StopCode; !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
	}
	return out, nil
}
