// Package server runs the live service: it keeps the static GTFS snapshot current, polls
// OASA through the scheduler, matches vehicles to trips and serves the contract API.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"

	"github.com/angelospk/athens-transit-rt/internal/feed"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/meta"
	"github.com/angelospk/athens-transit-rt/internal/release"
	"github.com/angelospk/athens-transit-rt/internal/sched"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

const (
	ExpiryWarningDays = 7
	feedMaxAge        = 5 * time.Minute // vehicles older than this leave the GTFS-RT feeds
	lineKeep          = 5 * time.Minute // results of a line that stopped being polled
)

type Config struct {
	Listen        string
	MetricsListen string // loopback address for /metrics ("" = off)
	StateDir      string
	RPS           float64
	Matcher       match.Kind
	ReleaseURL    string        // snapshot release base URL ("" = release.DefaultBaseURL)
	GTFSZip       string        // load this zip instead of the release snapshot (local use)
	TelematicsURL string        // "" = telematics.BaseURL
	ReleaseCheck  time.Duration // how often to look for a new snapshot
	Sched         sched.Config
}

type world struct {
	feed     *gtfs.Feed
	manifest *release.Manifest
	matcher  *match.Matcher
	mapper   *match.RouteMapper
}

// lineData is the last published state of one line.
type lineData struct {
	updated  time.Time
	feed     *gtfs.Feed
	results  []match.Result
	vehicles []Vehicle
}

type App struct {
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	pacer  *telematics.Pacer
	client *telematics.Client
	sched  *sched.Scheduler
	meta   *meta.Store
	fetch  *release.Fetcher

	matchMu sync.Mutex // serialises matching (matchers are not concurrency-safe)

	mu           sync.RWMutex
	w            *world
	lines        map[string]*lineData
	known        map[string]bool
	warming      map[string]bool // scheduled now, but its route codes are not known yet
	active       int
	lastPublish  time.Time
	lastOK       time.Time
	warnedDay    string
	lastRequests int64

	rtMu    sync.Mutex
	rtBuilt time.Time
	rtFeeds map[string][]byte
}

func New(cfg Config, log *slog.Logger) *App {
	a := &App{cfg: cfg, log: log, now: time.Now, lines: map[string]*lineData{}, known: map[string]bool{}}
	a.pacer = telematics.NewPacer(cfg.RPS)
	a.client = telematics.New(cfg.TelematicsURL, nil) // the scheduler paces every request
	a.sched = sched.New(cfg.Sched, a.pacer, a.client, a.onLine)
	a.meta = meta.Open(filepath.Join(cfg.StateDir, "telematics-meta.json"), a.client, a.sched.Submit,
		func() { a.refreshLines() })
	base := cfg.ReleaseURL
	if base == "" {
		base = release.DefaultBaseURL
	}
	a.fetch = &release.Fetcher{BaseURL: base, Dir: cfg.StateDir, HTTP: &http.Client{Timeout: 2 * time.Minute}}
	return a
}

// SetFeed installs a static feed (new matcher state; published results stay until replaced).
func (a *App) SetFeed(f *gtfs.Feed, m *release.Manifest) {
	mapper := match.NewRouteMapper(f, a.meta.Stops)
	w := &world{feed: f, manifest: m, mapper: mapper, matcher: match.New(f, mapper, a.cfg.Matcher)}
	a.matchMu.Lock()
	a.mu.Lock()
	a.w = w
	a.mu.Unlock()
	a.matchMu.Unlock()
	a.log.Info("static GTFS loaded", "version", a.gtfsVersion(), "trips", len(f.Trips), "lines", len(f.Lines()))
	a.warnExpiry(true)
	a.refreshLines()
}

func (a *App) world() *world {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.w
}

func (a *App) gtfsVersion() string {
	w := a.world()
	if w == nil {
		return ""
	}
	if w.manifest != nil && w.manifest.GTFSVersion != "" {
		return w.manifest.GTFSVersion
	}
	return w.feed.Version()
}

func (a *App) gtfsExpires() string {
	w := a.world()
	if w == nil || w.feed.FeedEnd().IsZero() {
		return ""
	}
	return w.feed.FeedEnd().Format(time.DateOnly)
}

// warnExpiry logs once a day when the static GTFS expires within ExpiryWarningDays.
func (a *App) warnExpiry(force bool) {
	w := a.world()
	if w == nil {
		return
	}
	end := w.feed.FeedEnd()
	if end.IsZero() {
		return
	}
	today := gtfs.Midnight(a.now())
	day := today.Format(time.DateOnly)
	if !force && day == a.warnedDay {
		return
	}
	a.warnedDay = day
	left := int(end.Sub(today).Hours() / 24)
	switch {
	case left < 0:
		a.log.Warn("static GTFS expired; no trips can be matched until OASA publishes a new one", "end", end.Format(time.DateOnly))
	case left <= ExpiryWarningDays:
		a.log.Warn("static GTFS expires soon", "end", end.Format(time.DateOnly), "days_left", left)
	}
}

// refreshLines recomputes which lines and route codes have scheduled service now.
func (a *App) refreshLines() {
	w := a.world()
	if w == nil {
		return
	}
	now := a.now()
	routes := a.meta.LineRoutes()
	known := map[string]bool{}
	for _, l := range w.feed.Lines() {
		known[l] = true
	}
	for _, l := range a.meta.LineIDs() {
		known[l] = true
	}
	var specs []sched.LineSpec
	active := 0
	warming := map[string]bool{}
	bootstrapping := a.meta.Snapshot().FetchedAt == 0
	plan := w.feed.PlanningTime(now) // which lines run now, even when the feed expired
	for line := range known {
		cands := w.feed.CandidateTrips(line, plan, match.CandidateBefore, match.CandidateAfter)
		shapesNow := map[int32]bool{}
		for _, c := range cands {
			shapesNow[w.feed.Trips[c.Trip].Shape] = true
		}
		spec := sched.LineSpec{ID: line, Active: len(cands) > 0 && len(routes[line]) > 0}
		if len(cands) > 0 && len(routes[line]) == 0 && bootstrapping {
			warming[line] = true
		}
		for _, r := range routes[line] {
			on := spec.Active
			if on {
				if shapes := w.mapper.ShapesFor(line, r.Code); len(shapes) > 0 {
					on = false
					for s := range shapes {
						on = on || shapesNow[s]
					}
				}
			}
			spec.Routes = append(spec.Routes, sched.Route{Code: r.Code, LineCode: r.LineCode, Active: on})
		}
		if spec.Active {
			active++
		}
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	a.sched.SetLines(specs)
	a.mu.Lock()
	a.known, a.active, a.warming = known, active, warming
	a.mu.Unlock()
}

// onLine matches one finished line poll and publishes it.
func (a *App) onLine(p sched.LinePoll) {
	a.matchMu.Lock()
	w := a.world()
	if w == nil {
		a.matchMu.Unlock()
		return
	}
	results := w.matcher.MatchLine(p.Line, p.Obs)
	vehicles := make([]Vehicle, len(results))
	for i := range results {
		vehicles[i] = a.vehicleView(w, &results[i])
	}
	a.matchMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	if p.Failed == p.Routes {
		return // keep the previous data; nothing new is known
	}
	a.lines[p.Line] = &lineData{updated: p.Done, feed: w.feed, results: results, vehicles: vehicles}
	a.lastPublish, a.lastOK = p.Done, p.Done
}

// Vehicle is one entry of the /v1/lines/{id} response (docs/CONTRACT.md).
type Vehicle struct {
	ID         string   `json:"id"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	Bearing    *float64 `json:"bearing"`
	PositionAt int64    `json:"position_at"`
	RouteCode  string   `json:"route_code"`
	Variant    *string  `json:"variant"`
	TripID     *string  `json:"trip_id"`
	TripLabel  *string  `json:"trip_label"`
	DelayS     *int     `json:"delay_s"`
	NextStopID *string  `json:"next_stop_id"`
}

func ptr[T any](v T) *T { return &v }

func (a *App) vehicleView(w *world, r *match.Result) Vehicle {
	f := w.feed
	v := Vehicle{ID: r.VehicleID, Lat: r.Lat, Lon: r.Lon, PositionAt: r.Time.Unix(), RouteCode: r.RouteCode}
	if r.Bearing != 0 { // 0 means "unknown" more often than "due north"
		v.Bearing = ptr(r.Bearing)
	}
	if !r.Matched() {
		if s := bestShape(f, w.mapper.ShapesFor(r.Line, r.RouteCode), r.Line); s >= 0 {
			v.Variant = ptr(f.Shapes[s].ID)
		}
		return v
	}
	t := f.Trip(r.Trip)
	if t.Shape >= 0 {
		v.Variant = ptr(f.Shapes[t.Shape].ID)
	}
	v.TripID = ptr(t.ID)
	v.TripLabel = ptr(TripLabel(f, t))
	v.DelayS = ptr(r.Delay)
	v.NextStopID = ptr(f.Stops[feed.NextStop(f, r).Stop].ID)
	return v
}

// bestShape picks the shape with the most trips of the line among a route code's shapes.
func bestShape(f *gtfs.Feed, shapes map[int32]bool, line string) int32 {
	if len(shapes) == 0 {
		return -1
	}
	count := map[int32]int{}
	for _, ti := range f.TripsForLine(line) {
		if s := f.Trips[ti].Shape; shapes[s] {
			count[s]++
		}
	}
	best := int32(-1)
	for s, n := range count {
		if s >= 0 && (best < 0 || n > count[best] || (n == count[best] && s < best)) {
			best = s
		}
	}
	return best
}

// TripLabel is "HH:MM FIRST STOP → LAST STOP" (GTFS 24:35 shows as 00:35).
func TripLabel(f *gtfs.Feed, t *gtfs.Trip) string {
	first, last := f.StopTime(t, 0), f.StopTime(t, f.NumStops(t)-1)
	mins := first.Dep / 60
	return fmt.Sprintf("%02d:%02d %s → %s", mins/60%24, mins%60, f.Stops[first.Stop].Name, f.Stops[last.Stop].Name)
}

var latinToGreek = strings.NewReplacer("A", "Α", "B", "Β", "E", "Ε", "Z", "Ζ", "H", "Η", "I", "Ι", "K", "Κ",
	"M", "Μ", "N", "Ν", "O", "Ο", "P", "Ρ", "T", "Τ", "Y", "Υ", "X", "Χ")

// CanonicalLine normalises a line id as users type it (Latin look-alikes for Greek letters).
func CanonicalLine(raw string) string {
	return latinToGreek.Replace(strings.ToUpper(strings.TrimSpace(raw)))
}

// gtfsRT returns the encoded feeds, rebuilt at most every 10 s.
func (a *App) gtfsRT(name string) ([]byte, bool) {
	a.rtMu.Lock()
	defer a.rtMu.Unlock()
	now := a.now()
	if a.rtFeeds == nil || now.Sub(a.rtBuilt) >= 10*time.Second {
		feeds, err := a.buildRT(now)
		if err != nil {
			a.log.Error("building GTFS-RT", "err", err)
			return nil, false
		}
		a.rtFeeds, a.rtBuilt = feeds, now
	}
	b, ok := a.rtFeeds[name]
	return b, ok
}

func (a *App) buildRT(now time.Time) (map[string][]byte, error) {
	w := a.world()
	var results []match.Result
	a.mu.RLock()
	ids := make([]string, 0, len(a.lines))
	for id := range a.lines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		d := a.lines[id]
		if w == nil || d.feed != w.feed {
			continue
		}
		for _, r := range d.results {
			if now.Sub(r.Time) <= feedMaxAge {
				results = append(results, r)
			}
		}
	}
	a.mu.RUnlock()
	var vp, tu *rt.FeedMessage
	if w == nil {
		vp, tu = feed.Build(&gtfs.Feed{}, nil, now)
	} else {
		vp, tu = feed.Build(w.feed, results, now)
	}
	out := map[string][]byte{}
	for name, msg := range map[string]*rt.FeedMessage{"vehicle_positions": vp, "trip_updates": tu} {
		pb, js, err := feed.Encode(msg)
		if err != nil {
			return nil, err
		}
		out[name+".pb"], out[name+".json"] = pb, js
	}
	return out, nil
}

// Run loads the static feed, then polls and serves until ctx ends.
func (a *App) Run(ctx context.Context) error {
	if err := a.loadInitial(ctx); err != nil {
		return err
	}
	if a.meta.Due() {
		a.meta.Refresh()
	}
	srv := &http.Server{Addr: a.cfg.Listen, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- srv.ListenAndServe() }()
	var msrv *http.Server
	if a.cfg.MetricsListen != "" {
		msrv = &http.Server{Addr: a.cfg.MetricsListen, Handler: a.MetricsHandler(), ReadHeaderTimeout: 10 * time.Second}
		go func() { errc <- msrv.ListenAndServe() }()
	}
	go a.sched.Run(ctx)
	go a.housekeeping(ctx)
	a.log.Info("listening", "addr", a.cfg.Listen, "metrics", a.cfg.MetricsListen, "rps", a.pacer.BaseRPS())
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if msrv != nil {
			msrv.Shutdown(sctx)
		}
		return srv.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}

func (a *App) loadInitial(ctx context.Context) error {
	if a.cfg.GTFSZip != "" {
		f, err := gtfs.LoadZip(a.cfg.GTFSZip, gtfs.LoadOptions{})
		if err != nil {
			return err
		}
		a.SetFeed(f, nil)
		return nil
	}
	if f, m, err := a.fetch.LoadLocal(); err != nil {
		a.log.Warn("local snapshot unusable", "err", err)
	} else if f != nil {
		a.SetFeed(f, m)
	}
	for a.world() == nil {
		f, m, err := a.fetch.Check(ctx)
		if err == nil && f != nil {
			a.SetFeed(f, m)
			break
		}
		a.log.Warn("waiting for a GTFS snapshot", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Minute):
		}
	}
	return nil
}

func (a *App) housekeeping(ctx context.Context) {
	minute := time.NewTicker(time.Minute)
	defer minute.Stop()
	check := a.cfg.ReleaseCheck
	if check <= 0 {
		check = 30 * time.Minute
	}
	release := time.NewTicker(check)
	defer release.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-minute.C:
			a.refreshLines()
			if a.meta.Due() {
				a.meta.Refresh()
			}
			a.warnExpiry(false)
			a.forget()
			a.logStats()
		case <-release.C:
			if a.cfg.GTFSZip != "" {
				continue
			}
			f, m, err := a.fetch.Check(ctx)
			switch {
			case err != nil:
				a.log.Warn("snapshot check failed", "err", err)
			case f != nil:
				a.SetFeed(f, m)
			}
		}
	}
}

// logStats writes one line a minute about the polling plan and its cost.
func (a *App) logStats() {
	st := a.sched.Stats()
	a.mu.RLock()
	vehicles, matched := 0, 0
	for _, d := range a.lines {
		if a.now().Sub(d.updated) > feedMaxAge {
			continue
		}
		for i := range d.results {
			vehicles++
			if d.results[i].Matched() {
				matched++
			}
		}
	}
	a.mu.RUnlock()
	reqs := a.client.Requests()
	a.log.Info("stats", "requests", reqs, "requests_last_min", reqs-a.lastRequests,
		"watched", st.LinesByTier[sched.Watched], "dense", st.LinesByTier[sched.Dense], "other", st.LinesByTier[sched.Other],
		"stretch_other", fmt.Sprintf("%.2f", st.Stretch[sched.Other]), "rps_now", fmt.Sprintf("%.2f", a.pacer.RPS()),
		"vehicles", vehicles, "matched", matched)
	a.lastRequests = reqs
}

// forget drops lines not polled for a while and old matcher state.
func (a *App) forget() {
	now := a.now()
	a.matchMu.Lock()
	if w := a.world(); w != nil {
		w.matcher.Forget(now)
	}
	a.matchMu.Unlock()
	a.mu.Lock()
	for id, d := range a.lines {
		if now.Sub(d.updated) > time.Hour {
			delete(a.lines, id)
		}
	}
	a.mu.Unlock()
}
