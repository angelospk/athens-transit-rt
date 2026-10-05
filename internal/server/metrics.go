package server

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/sched"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// Metrics is the body of the local-only GET /metrics: what the OASA budget is spent on.
// It is served on a separate loopback listener and is not part of the public contract.
type Metrics struct {
	Now           int64   `json:"now"`
	BudgetRPS     float64 `json:"budget_rps"`
	RPSNow        float64 `json:"rps_now"` // after backoff
	RequestsTotal int64   `json:"requests_total"`
	ErrorsTotal   int64   `json:"errors_total"`
	// OASA requests started in each of the last 3600 seconds, oldest first.
	RequestsPerSecond []int32          `json:"requests_per_second"`
	Max1s             int32            `json:"max_1s"`       // over that hour
	Max10s            int32            `json:"max_10s"`      // over that hour
	SentByTier        map[string]int64 `json:"sent_by_tier"` // since start; "background" = metadata
	LinesByTier       map[string]int   `json:"lines_by_tier"`
	RSSBytes          int64            `json:"rss_bytes"`
	Lines             []MetricsLine    `json:"lines"`
}

type MetricsLine struct {
	ID         string `json:"id"`
	Tier       string `json:"tier"`
	IntervalS  int    `json:"interval_s"`
	NextDue    int64  `json:"next_due"`
	LastPollAt int64  `json:"last_poll_at"` // start of the last poll, 0 = never
	UpdatedAt  int64  `json:"updated_at"`   // last published data, 0 = none
	Polling    bool   `json:"polling"`
}

func (a *App) Metrics() Metrics {
	now := a.now()
	per := a.client.Log.PerSecond(now, telematics.RequestLogSeconds)
	st := a.sched.Stats()
	m := Metrics{Now: now.Unix(), BudgetRPS: a.pacer.BaseRPS(), RPSNow: a.pacer.RPS(),
		RequestsTotal: a.client.Requests(), ErrorsTotal: a.client.Errors(), RequestsPerSecond: per,
		Max1s: telematics.MaxWindow(per, 1), Max10s: telematics.MaxWindow(per, 10),
		SentByTier: map[string]int64{"background": st.SentBackground}, LinesByTier: map[string]int{},
		RSSBytes: rss()}
	for _, t := range []sched.Tier{sched.Inactive, sched.Other, sched.Dense, sched.Watched} {
		m.LinesByTier[t.String()] = st.LinesByTier[t]
		if t != sched.Inactive {
			m.SentByTier[t.String()] = st.Sent[t]
		}
	}
	lines := a.sched.Lines()
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, l := range lines {
		ml := MetricsLine{ID: l.ID, Tier: l.Tier.String(), IntervalS: int(l.Interval / time.Second),
			NextDue: unixOrZero(l.NextDue), LastPollAt: unixOrZero(l.LastStart), Polling: l.Polling}
		if d := a.lines[l.ID]; d != nil {
			ml.UpdatedAt = d.updated.Unix()
		}
		m.Lines = append(m.Lines, ml)
	}
	return m
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// rss reads the resident set size on Linux (0 elsewhere).
func rss() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}

// MetricsHandler serves /metrics to loopback clients only (it also listens on loopback).
func (a *App) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() || r.URL.Path != "/metrics" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(a.Metrics())
	})
}
