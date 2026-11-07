package server

import (
	"encoding/json"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/release"
	"github.com/angelospk/athens-transit-rt/internal/staticsite"
)

func TestStaticLinesFromFeed(t *testing.T) {
	a := newApp(t, monday1020)
	res, body := get(t, a, "/static/v1/lines.json")
	if res.StatusCode != 200 {
		t.Fatalf("lines.json: %d %s", res.StatusCode, body)
	}
	var lf staticsite.LinesFile
	if err := json.Unmarshal(body, &lf); err != nil || len(lf.Lines) == 0 {
		t.Fatalf("lines.json: %v %s", err, body)
	}
	id := lf.Lines[0].ID
	res, body = get(t, a, "/static/v1/lines/"+id+".json")
	if res.StatusCode != 200 {
		t.Fatalf("line %s: %d %s", id, res.StatusCode, body)
	}
	var one staticsite.LineFile
	if err := json.Unmarshal(body, &one); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/static/v1/lines/nope.json", "/static/v1/lines/" + id} {
		if res, _ := get(t, a, p); res.StatusCode != 404 {
			t.Fatalf("%s: %d, want 404", p, res.StatusCode)
		}
	}
}

// A feed from CKAN (Thessaloniki) has its version only in the manifest: lines.json must say the same
// version as /v1/status, or clients that compare the two (the web's stop index, the alert bot) see none.
func TestStaticLinesVersionFromManifest(t *testing.T) {
	a := newApp(t, monday1020)
	a.SetFeed(a.world().feed, &release.Manifest{GTFSVersion: "2026-10-27"})
	_, body := get(t, a, "/static/v1/lines.json")
	var lf staticsite.LinesFile
	if err := json.Unmarshal(body, &lf); err != nil {
		t.Fatal(err)
	}
	if lf.GTFSVersion != "2026-10-27" || lf.GTFSVersion != a.gtfsVersion() {
		t.Fatalf("lines.json gtfs_version %q, status %q, want 2026-10-27", lf.GTFSVersion, a.gtfsVersion())
	}
}
