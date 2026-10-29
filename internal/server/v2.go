package server

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/model"
)

// The /v2 API (docs/CONTRACT-rev8-draft.md): one model for every mode. Each Source has one
// Adapter; the handlers route an id to the adapter of its namespace and only encode.

const (
	v2ListMaxAge    = 3600 // lines, stops, line detail
	v2RunMaxAge     = 15
	v2IncompleteAge = 5  // a response with a Source neither ok nor stale
	v2ETagFrom      = 60 // ETag + 304 for complete responses cached at least this long
)

var (
	errUnknown = errors.New("unknown id")
	errWarming = errors.New("source warming up")
)

// Kind is what an id names, for Resolve.
type Kind int

const (
	KindLine Kind = iota
	KindStop
	KindRun
)

// LineDetail is the body of /v2/lines/{id} without its sources.
type LineDetail struct {
	Line     model.Line
	Patterns []model.Pattern
	Stops    map[string]model.Stop
}

// LineVehicles is a line's vehicles now and when they are next updated.
type LineVehicles struct {
	Vehicles     []model.Vehicle
	UpdatedAt    int64
	NextUpdateAt int64
	MaxAge       int
}

// Adapter turns one Source's data into the model. Lookups return errUnknown for an id the
// Source does not know and errWarming while it has nothing loaded yet; ids are current ids (the
// handler resolves aliases first).
type Adapter interface {
	Namespace() string
	State(now time.Time) model.SourceState
	Resolve(kind Kind, id string) string
	Lines() ([]model.Line, error)
	LineInfo(id string) (*model.Line, error)
	Line(id string) (*LineDetail, error)
	Stops() ([]model.Stop, error)
	Stop(id string) (*model.Stop, error)
	// Arrivals may ask the Source for more (a line watched, a stream opened).
	Arrivals(ctx context.Context, stopID string, now time.Time) ([]model.Arrival, error)
	LineVehicles(id string, now time.Time) (*LineVehicles, error)
	Vehicles(now time.Time) ([]model.Vehicle, error)
	Run(id string, now time.Time) (*model.Run, error)
}

// adapter is the Adapter of an id's namespace, nil when none.
func (a *App) adapter(id string) Adapter {
	ns := model.Namespace(id)
	for _, ad := range a.adapters {
		if ad.Namespace() == ns && len(id) > len(ns) {
			return ad
		}
	}
	return nil
}

func (a *App) states(now time.Time, ads ...Adapter) []model.SourceState {
	out := make([]model.SourceState, len(ads))
	for i, ad := range ads {
		out[i] = ad.State(now)
	}
	return out
}

func (a *App) routeV2(mux *http.ServeMux) {
	mux.HandleFunc("GET /v2/lines", a.handleV2Lines)
	mux.HandleFunc("GET /v2/lines/{id}", a.handleV2Line)
	mux.HandleFunc("GET /v2/lines/{id}/vehicles", a.handleV2LineVehicles)
	mux.HandleFunc("GET /v2/stops", a.handleV2Stops)
	mux.HandleFunc("GET /v2/stops/{id}", a.handleV2Stop)
	mux.HandleFunc("GET /v2/vehicles", a.handleV2Vehicles)
	mux.HandleFunc("GET /v2/vehicles/tiles/{z}/{x}/{y}", a.handleV2Tile)
	mux.HandleFunc("GET /v2/runs/{id}", a.handleV2Run)
}

// writeV2 writes a /v2 body: a response with a Source not ok or stale is cached at most 5 s
// with no ETag; a complete one cached a minute or more gets an ETag and answers 304.
func writeV2(w http.ResponseWriter, r *http.Request, maxAge int, sources []model.SourceState, v any) {
	if !model.Complete(sources) {
		writeJSON(w, http.StatusOK, min(maxAge, v2IncompleteAge), v)
		return
	}
	if maxAge < v2ETagFrom {
		writeJSON(w, http.StatusOK, maxAge, v)
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(b)
	// Weak: the same body gzipped by the Handler or not.
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h := w.Header()
	h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	h.Set("ETag", "W/"+tag)
	if etagMatch(r.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
}

func notFound(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusNotFound, unknownMaxAge, map[string]string{"error": code})
}

// lookup resolves a path id to its adapter and current id; ok is false when no adapter has it.
func (a *App) lookup(r *http.Request, kind Kind) (Adapter, string, bool) {
	id := r.PathValue("id")
	ad := a.adapter(id)
	if ad == nil {
		return nil, "", false
	}
	return ad, ad.Resolve(kind, id), true
}

func (a *App) handleV2Lines(w http.ResponseWriter, r *http.Request) {
	now := a.now()
	out := []model.Line{}
	for _, ad := range a.adapters {
		if ls, err := ad.Lines(); err == nil {
			out = append(out, ls...)
		}
	}
	src := a.states(now, a.adapters...)
	writeV2(w, r, v2ListMaxAge, src, map[string]any{"lines": out, "sources": src})
}

func (a *App) handleV2Line(w http.ResponseWriter, r *http.Request) {
	ad, id, ok := a.lookup(r, KindLine)
	if !ok {
		notFound(w, "unknown_line")
		return
	}
	d, err := ad.Line(id)
	if errors.Is(err, errUnknown) {
		notFound(w, "unknown_line")
		return
	}
	src := a.states(a.now(), ad)
	body := map[string]any{"line": nil, "patterns": []model.Pattern{}, "stops": map[string]model.Stop{}, "sources": src}
	if err == nil {
		body["line"], body["patterns"], body["stops"] = d.Line, d.Patterns, d.Stops
	}
	writeV2(w, r, v2ListMaxAge, src, body)
}

func (a *App) handleV2LineVehicles(w http.ResponseWriter, r *http.Request) {
	ad, id, ok := a.lookup(r, KindLine)
	if !ok {
		notFound(w, "unknown_line")
		return
	}
	now := a.now()
	lv, err := ad.LineVehicles(id, now)
	if errors.Is(err, errUnknown) {
		notFound(w, "unknown_line")
		return
	}
	if err != nil {
		lv = &LineVehicles{UpdatedAt: now.Unix(), NextUpdateAt: now.Unix() + minMaxAge, MaxAge: minMaxAge}
	}
	if lv.Vehicles == nil {
		lv.Vehicles = []model.Vehicle{}
	}
	src := a.states(now, ad)
	writeV2(w, r, lv.MaxAge, src, map[string]any{"line": id, "updated_at": lv.UpdatedAt,
		"next_update_at": lv.NextUpdateAt, "vehicles": lv.Vehicles, "sources": src})
}

func (a *App) handleV2Stops(w http.ResponseWriter, r *http.Request) {
	out := []model.IndexEntry{}
	for _, ad := range a.adapters {
		if ss, err := ad.Stops(); err == nil {
			for _, s := range ss {
				out = append(out, s.Entry())
			}
		}
	}
	src := a.states(a.now(), a.adapters...)
	writeV2(w, r, v2ListMaxAge, src, map[string]any{"stops": out, "sources": src})
}

func (a *App) handleV2Stop(w http.ResponseWriter, r *http.Request) {
	ad, id, ok := a.lookup(r, KindStop)
	if !ok {
		notFound(w, "unknown_stop")
		return
	}
	now := a.now()
	st, err := ad.Stop(id)
	if errors.Is(err, errUnknown) {
		notFound(w, "unknown_stop")
		return
	}
	lines, arr := []model.Line{}, []model.Arrival{}
	if err == nil {
		for _, l := range st.Lines {
			if li, err := ad.LineInfo(l); err == nil {
				lines = append(lines, *li)
			}
		}
		if got, err := ad.Arrivals(r.Context(), id, now); err == nil && got != nil {
			arr = got
		}
	}
	src := a.states(now, ad)
	writeV2(w, r, stopMaxAge, src, map[string]any{"stop": st, "lines": lines, "arrivals": arr,
		"updated_at": now.Unix(), "sources": src})
}

func (a *App) handleV2Run(w http.ResponseWriter, r *http.Request) {
	ad, id, ok := a.lookup(r, KindRun)
	if !ok {
		notFound(w, "unknown_run")
		return
	}
	now := a.now()
	run, err := ad.Run(id, now)
	if errors.Is(err, errUnknown) {
		notFound(w, "unknown_run")
		return
	}
	src := a.states(now, ad)
	writeV2(w, r, v2RunMaxAge, src, map[string]any{"run": run, "sources": src})
}

// v2Response is the body of /v2/vehicles and its tiles.
type v2Response[V any] struct {
	UpdatedAt    int64               `json:"updated_at"`
	NextUpdateAt int64               `json:"next_update_at"`
	Vehicles     []V                 `json:"vehicles"`
	Sources      []model.SourceState `json:"sources"`
}

// v2Snapshot is the encoded /v2/vehicles body and tiles, built like the /v1 snapshot.
type v2Snapshot struct {
	built    time.Time
	complete bool
	all      *encoded
	tiles    map[tileKey]*encoded
	empty    *encoded
}

// busAdapters are the Sources of /v2/vehicles and its tiles: bus and trolley only, so map
// traffic never opens the suburban stream.
func (a *App) busAdapters() []Adapter {
	var out []Adapter
	for _, ad := range a.adapters {
		if ad.Namespace() == "oasa" {
			out = append(out, ad)
		}
	}
	return out
}

func (a *App) v2Vehicles() (*v2Snapshot, error) {
	a.v2Mu.Lock()
	defer a.v2Mu.Unlock()
	now := a.now()
	if s := a.v2Snap; s != nil && now.Sub(s.built) < vehiclesEvery && !now.Before(s.built) {
		return s, nil
	}
	ads := a.busAdapters()
	vs := []model.Vehicle{}
	for _, ad := range ads {
		if got, err := ad.Vehicles(now); err == nil {
			vs = append(vs, got...)
		}
	}
	src := a.states(now, ads...)
	next := now.Add(vehiclesEvery).Unix()
	zw := gzip.NewWriter(nil)
	all, err := encode(zw, v2Response[model.Vehicle]{now.Unix(), next, vs, src})
	if err != nil {
		return nil, err
	}
	s := &v2Snapshot{built: now, complete: model.Complete(src), all: all, tiles: map[tileKey]*encoded{}}
	detail := map[tileKey][]model.Vehicle{}
	overview := map[tileKey][]model.LiteVehicle{}
	for _, v := range vs {
		k := tileKey{z: tileDetailZ}
		if k.x, k.y = tileOf(v.Position.Lat, v.Position.Lon, k.z); !inTileRange(k) {
			continue
		}
		detail[k] = append(detail[k], v)
		k = tileKey{z: tileOverviewZ}
		k.x, k.y = tileOf(v.Position.Lat, v.Position.Lon, k.z)
		overview[k] = append(overview[k], v.Lite())
	}
	for k, vs := range detail {
		if s.tiles[k], err = encode(zw, v2Response[model.Vehicle]{now.Unix(), next, vs, src}); err != nil {
			return nil, err
		}
	}
	for k, vs := range overview {
		if s.tiles[k], err = encode(zw, v2Response[model.LiteVehicle]{now.Unix(), next, vs, src}); err != nil {
			return nil, err
		}
	}
	if s.empty, err = encode(zw, v2Response[model.Vehicle]{now.Unix(), next, []model.Vehicle{}, src}); err != nil {
		return nil, err
	}
	a.v2Snap = s
	return s, nil
}

func (a *App) handleV2Vehicles(w http.ResponseWriter, r *http.Request) {
	a.serveV2Snapshot(w, r, func(s *v2Snapshot) *encoded { return s.all })
}

func (a *App) handleV2Tile(w http.ResponseWriter, r *http.Request) {
	k, ok := tileRequest(w, r)
	if !ok {
		return
	}
	a.serveV2Snapshot(w, r, func(s *v2Snapshot) *encoded {
		if e := s.tiles[k]; e != nil {
			return e
		}
		return s.empty
	})
}

// serveV2Snapshot serves prebuilt bytes like serveSnapshot; an incomplete snapshot is cached
// at most 5 s, with no ETag.
func (a *App) serveV2Snapshot(w http.ResponseWriter, r *http.Request, pick func(*v2Snapshot) *encoded) {
	s, err := a.v2Vehicles()
	if err != nil {
		a.log.Error("building /v2/vehicles", "err", err)
		writeJSON(w, http.StatusInternalServerError, minMaxAge, map[string]string{"error": "internal"})
		return
	}
	e := pick(s)
	body, etag, gz := e.raw, e.etag, false
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		body, etag, gz = e.gz, e.gzETag, true
	}
	maxAge := clamp(int64(s.built.Add(vehiclesEvery).Sub(a.now())/time.Second), minMaxAge, vehiclesMaxAge)
	h := w.Header()
	if !s.complete {
		maxAge = min(maxAge, v2IncompleteAge)
	} else {
		h.Set("ETag", etag)
		if etagMatch(r.Header.Get("If-None-Match"), etag) {
			h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	h.Set("Content-Type", "application/json; charset=utf-8")
	if gz {
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// sortLines orders a Stop's Lines by mode, then label (or name).
func sortLines(ids []string, info func(string) (mode, key string)) {
	sort.SliceStable(ids, func(i, j int) bool {
		mi, ki := info(ids[i])
		mj, kj := info(ids[j])
		if model.ModeRank(mi) != model.ModeRank(mj) {
			return model.ModeRank(mi) < model.ModeRank(mj)
		}
		return ki < kj
	})
}
