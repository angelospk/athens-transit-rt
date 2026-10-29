package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/model"
)

// A matched bus waiting at its first stop has at_terminus true on /v1/lines/{id} and on /v2;
// once it runs along the route, false. The key is always present.
func TestVehicleAtTerminusFlag(t *testing.T) {
	for _, c := range []struct {
		name string
		at   time.Time
		lon  float64
		want bool
	}{
		{"waiting at the first stop", time.Date(2026, 10, 5, 10, 25, 0, 0, gtfs.Athens), 23.7001, true},
		{"running", time.Date(2026, 10, 5, 10, 33, 0, 0, gtfs.Athens), 23.72, false},
	} {
		a := newApp(t, c.at)
		pollOne(a, c.at, "w", c.lon, c.at.Add(-10*time.Second))
		_, body := get(t, a, "/v1/lines/L")
		var lr LineResponse
		if err := json.Unmarshal(body, &lr); err != nil || len(lr.Vehicles) != 1 {
			t.Fatalf("%s: %v %s", c.name, err, body)
		}
		if lr.Vehicles[0].TripID == nil {
			t.Fatalf("%s: not matched: %s", c.name, body)
		}
		if !strings.Contains(string(body), `"at_terminus":`) || lr.Vehicles[0].AtTerminus != c.want {
			t.Fatalf("%s: v1 at_terminus want %v: %s", c.name, c.want, body)
		}
		var lv struct{ Vehicles []model.Vehicle }
		v2get(t, a, "/v2/lines/"+esc("oasa:L")+"/vehicles", &lv)
		if len(lv.Vehicles) != 1 || lv.Vehicles[0].AtTerminus != c.want {
			t.Fatalf("%s: v2 at_terminus want %v: %+v", c.name, c.want, lv.Vehicles)
		}
		if got := lv.Vehicles[0].Lite(); got.AtTerminus != c.want {
			t.Fatalf("%s: lite at_terminus %v", c.name, got.AtTerminus)
		}
	}
}
