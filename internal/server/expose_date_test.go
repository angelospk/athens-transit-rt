package server

import (
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

// A cross-origin fetch() can read Date only when it is exposed (#3): every live response,
// errors included, says so.
func TestExposeDateHeader(t *testing.T) {
	now := time.Date(2026, 10, 5, 10, 25, 0, 0, gtfs.Athens)
	a := newApp(t, now)
	pollOne(a, now, "w", 23.72, now.Add(-10*time.Second))
	for _, p := range []string{"/v1/lines/L", "/v1/status", "/v2/lines", "/v1/lines/nope"} {
		res, _ := get(t, a, p)
		if got := res.Header.Get("Access-Control-Expose-Headers"); got != "Date" {
			t.Errorf("%s: Access-Control-Expose-Headers %q", p, got)
		}
	}
}
