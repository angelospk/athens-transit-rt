package server

import (
	"net/http"
	"slices"

	"github.com/angelospk/athens-transit-rt/internal/staticsite"
)

// staticMaxAge: the static line files change only with a new GTFS feed.
const staticMaxAge = 3600

// The files of docs/CONTRACT.md static/v1, built from the loaded feed. A city without a snapshot
// release on GitHub Pages (Thessaloniki) serves them from here; Athens serves them too.
func (a *App) handleStaticLines(w http.ResponseWriter, r *http.Request) {
	wd := a.world()
	if wd == nil {
		writeJSON(w, http.StatusServiceUnavailable, minMaxAge, map[string]string{"error": "warming_up"})
		return
	}
	out := staticsite.LinesFile{GTFSVersion: wd.feed.Version(), Lines: []staticsite.Line{}}
	tel := a.meta.Snapshot().Lines
	for _, id := range wd.feed.Lines() {
		out.Lines = append(out.Lines, staticsite.LineInfo(wd.feed, tel, id))
	}
	writeJSON(w, http.StatusOK, staticMaxAge, out)
}

func (a *App) handleStaticLine(w http.ResponseWriter, r *http.Request) {
	wd := a.world()
	if wd == nil {
		writeJSON(w, http.StatusServiceUnavailable, minMaxAge, map[string]string{"error": "warming_up"})
		return
	}
	id := r.PathValue("file")
	if len(id) <= len(".json") || id[len(id)-len(".json"):] != ".json" {
		writeJSON(w, http.StatusNotFound, unknownMaxAge, map[string]string{"error": "not_found"})
		return
	}
	id = id[:len(id)-len(".json")]
	if !slices.Contains(wd.feed.Lines(), id) {
		writeJSON(w, http.StatusNotFound, unknownMaxAge, map[string]string{"error": "unknown_line"})
		return
	}
	writeJSON(w, http.StatusOK, staticMaxAge, staticsite.LineFileOf(wd.feed, id))
}
