package server

import (
	"context"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/model"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// #22: OASA's own predictions for a stop (getStopArrivals) are live Arrivals of /v2/stops/{id}
// for the buses the backend has no row for; a bus listed by both stays once, with the backend's
// time. The OASA call never holds up the request: a cold cache starts it in the background.
func TestV2StopOasaPredictions(t *testing.T) {
	now := monday1020
	a := newApp(t, now)
	pollOne(a, now, "w", 23.72, now.Add(-10*time.Second))
	calls := 0
	a.stopArrivals = func(_ context.Context, id string) ([]telematics.Arrival, error) {
		calls++
		if id != "S3" {
			t.Errorf("asked for stop %q", id)
		}
		return []telematics.Arrival{{RouteCode: "SH", VehCode: "w", Minutes: 3}, {RouteCode: "SH", VehCode: "z", Minutes: 7},
			{RouteCode: "999", VehCode: "q", Minutes: 1}}, nil
	}
	byVehicle := func(s v2Stop) map[string][]model.Arrival {
		out := map[string][]model.Arrival{}
		for _, ar := range s.Arrivals {
			if ar.Vehicle != nil {
				out[*ar.Vehicle] = append(out[*ar.Vehicle], ar)
			}
		}
		return out
	}
	var s v2Stop
	v2get(t, a, "/v2/stops/"+esc("oasa:S3"), &s)
	if got := byVehicle(s); len(got["oasa:w"]) != 1 || len(got["oasa:z"]) != 0 {
		t.Fatalf("cold cache: %s", asJSON(s.Arrivals))
	}
	if _, err := a.arrivals(context.Background(), "S3"); err != nil { // the background call
		t.Fatal(err)
	}
	s = v2Stop{}
	v2get(t, a, "/v2/stops/"+esc("oasa:S3"), &s)
	got := byVehicle(s)
	if len(got["oasa:w"]) != 1 || got["oasa:w"][0].Run == nil || len(got["oasa:q"]) != 0 || len(got["oasa:z"]) != 1 {
		t.Fatalf("warm cache: %s", asJSON(s.Arrivals))
	}
	want := model.Arrival{Line: "oasa:L", Stop: "oasa:S3", Headsign: "EAST", At: now.Add(7 * time.Minute).Unix(),
		Source: model.SourceLive, Vehicle: model.Ptr("oasa:z"), VehicleLabel: model.Ptr("z"), Status: model.StatusOf(true, nil)}
	if z := got["oasa:z"][0]; asJSON(z) != asJSON(want) {
		t.Fatalf("OASA row\n got %s\nwant %s", asJSON(z), asJSON(want))
	}
	for i := 1; i < len(s.Arrivals); i++ {
		if s.Arrivals[i].At < s.Arrivals[i-1].At {
			t.Fatalf("not in time order: %s", asJSON(s.Arrivals))
		}
	}
	if calls != 1 {
		t.Fatalf("OASA asked %d times", calls)
	}
}
