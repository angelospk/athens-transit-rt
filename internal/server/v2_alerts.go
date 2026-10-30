package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/alerts"
	"github.com/angelospk/athens-transit-rt/internal/rail"
)

// The /v2/alerts feed (contract rev 13): OASA notices and strikes, polled in the background.
// Unlike the other /v2 bodies it has no sources: a failed source keeps its last Alerts and
// updated_at says when the last poll worked.

const (
	alertsMaxAge   = 300 // after the first poll with no failure
	alertBodyLimit = 2 << 20
	alertTimeout   = 20 * time.Second // a notice page or a 30-post list is small
)

// handleV2Alerts serves the Alerts now. Before the first good poll: an empty list, a null
// updated_at and max-age=5.
func (a *App) handleV2Alerts(w http.ResponseWriter, r *http.Request) {
	items, updated := a.alerts.Items(), a.alerts.Updated()
	body := map[string]any{"alerts": items, "updated_at": nil}
	maxAge := v2IncompleteAge
	if updated != 0 {
		body["updated_at"], maxAge = updated, alertsMaxAge
	}
	writeJSON(w, http.StatusOK, maxAge, body)
}

// alertGet fetches one alert source: a page or a JSON document, with the polite User-Agent.
func (a *App) alertGet(ctx context.Context, from string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, from, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", rail.UserAgent)
	resp, err := a.alertHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", from, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, alertBodyLimit))
}

// knownLine is an OASA line code as a /v2 Line id and mode, for /v2/alerts: only a line of the
// current GTFS, and only a bus or a trolley.
func (a *App) knownLine(code string) (string, string, bool) {
	w := a.world()
	if w == nil {
		return "", "", false
	}
	mode, ok := w.lineModes()[code]
	if !ok || (mode != "bus" && mode != "trolley") {
		return "", "", false
	}
	return oasaPrefix + code, mode, true
}

// knownStop reports whether code is a Stop id of the current GTFS.
func (a *App) knownStop(code string) bool {
	w := a.world()
	return w != nil && w.feed.StopIndex(code) >= 0
}

// newAlertsPoller is the /v2/alerts poller of this App.
func (a *App) newAlertsPoller() *alerts.Poller {
	return &alerts.Poller{Get: a.alertGet, KnownLine: a.knownLine, KnownStop: a.knownStop,
		Now: func() time.Time { return a.now() }, Log: a.log}
}
