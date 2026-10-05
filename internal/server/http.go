package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/sched"
)

const (
	minMaxAge        = 5
	maxMaxAge        = 300
	inactiveMaxAge   = 120
	unknownMaxAge    = 300
	statusMaxAge     = 10
	gtfsRTMaxAge     = 15
	okRecentPollSecs = 5 * 60
)

type LineResponse struct {
	Line         string    `json:"line"`
	UpdatedAt    int64     `json:"updated_at"`
	NextUpdateAt int64     `json:"next_update_at"`
	Vehicles     []Vehicle `json:"vehicles"`
}

type StatusResponse struct {
	OK          bool    `json:"ok"`
	GTFSVersion string  `json:"gtfs_version"`
	GTFSExpires string  `json:"gtfs_expires"`
	UpdatedAt   int64   `json:"updated_at"`
	LinesActive int     `json:"lines_active"`
	BudgetRPS   float64 `json:"budget_rps"`
}

// Handler serves the live API of docs/CONTRACT.md.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/lines/{id}", a.handleLine)
	mux.HandleFunc("GET /v1/status", a.handleStatus)
	mux.HandleFunc("GET /v1/gtfs-rt/{name}", a.handleGTFSRT)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, unknownMaxAge, map[string]string{"error": "not_found"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status, maxAge int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	w.WriteHeader(status)
	w.Write(b)
}

func clamp(v, lo, hi int64) int64 { return max(lo, min(hi, v)) }

func (a *App) handleLine(w http.ResponseWriter, r *http.Request) {
	line := CanonicalLine(r.PathValue("id"))
	now := a.now()
	a.mu.RLock()
	known, warming := a.known[line], a.warming[line]
	d := a.lines[line]
	a.mu.RUnlock()
	if !known {
		writeJSON(w, http.StatusNotFound, unknownMaxAge, map[string]string{"error": "unknown_line"})
		return
	}
	a.sched.Watch(line)
	next, polled := a.sched.NextUpdate(line)
	if warming {
		writeJSON(w, http.StatusServiceUnavailable, minMaxAge, map[string]string{"error": "warming_up"})
		return
	}
	if !polled {
		// No scheduled service now: not polled. Recent results (a run ending) still show.
		resp := LineResponse{Line: line, UpdatedAt: now.Unix(), NextUpdateAt: now.Unix() + inactiveMaxAge, Vehicles: []Vehicle{}}
		if d != nil && now.Sub(d.updated) <= lineKeep {
			resp.UpdatedAt, resp.Vehicles = d.updated.Unix(), d.vehicles
		}
		writeJSON(w, http.StatusOK, inactiveMaxAge, resp)
		return
	}
	if d == nil {
		writeJSON(w, http.StatusServiceUnavailable, minMaxAge, map[string]string{"error": "warming_up"})
		return
	}
	vehicles := d.vehicles
	if vehicles == nil {
		vehicles = []Vehicle{}
	}
	maxAge := clamp(int64(next.Sub(now)/time.Second), minMaxAge, maxMaxAge)
	writeJSON(w, http.StatusOK, int(maxAge), LineResponse{Line: line, UpdatedAt: d.updated.Unix(),
		NextUpdateAt: max(next.Unix(), now.Unix()+minMaxAge), Vehicles: vehicles})
}

// Status is the /v1/status body.
func (a *App) Status() StatusResponse {
	now := a.now()
	a.mu.RLock()
	lastOK, lastPublish, active, loaded := a.lastOK, a.lastPublish, a.active, a.w != nil
	a.mu.RUnlock()
	st := a.sched.Stats()
	polledLines := st.LinesByTier[sched.Watched] + st.LinesByTier[sched.Dense] + st.LinesByTier[sched.Other]
	ok := loaded && (polledLines == 0 || (!lastOK.IsZero() && now.Sub(lastOK) <= okRecentPollSecs*time.Second))
	var updated int64
	if !lastPublish.IsZero() {
		updated = lastPublish.Unix()
	}
	return StatusResponse{OK: ok, GTFSVersion: a.gtfsVersion(), GTFSExpires: a.gtfsExpires(), UpdatedAt: updated,
		LinesActive: active, BudgetRPS: a.pacer.BaseRPS()}
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statusMaxAge, a.Status())
}

func (a *App) handleGTFSRT(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	switch name {
	case "vehicle_positions.pb", "trip_updates.pb", "vehicle_positions.json", "trip_updates.json":
	default:
		writeJSON(w, http.StatusNotFound, unknownMaxAge, map[string]string{"error": "not_found"})
		return
	}
	b, ok := a.gtfsRT(name)
	if !ok {
		http.Error(w, "feed unavailable", http.StatusServiceUnavailable)
		return
	}
	if name[len(name)-3:] == ".pb" {
		w.Header().Set("Content-Type", "application/x-protobuf")
	} else {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", gtfsRTMaxAge))
	w.Write(b)
}
