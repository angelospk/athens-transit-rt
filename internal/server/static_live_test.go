package server

import (
	"encoding/json"
	"testing"

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
