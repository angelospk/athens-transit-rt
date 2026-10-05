package feed

import (
	"encoding/json"
	"testing"
	"time"

	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
	"github.com/angelospk/athens-transit-rt/internal/match"
)

func TestBuild(t *testing.T) {
	f := gtfstest.StraightLine{Stops: 5, Trips: 3, First: 10 * 3600, Headway: 10, Leg: 5}.Feed(t)
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, gtfs.Athens)
	now := day.Add(10*time.Hour + 20*time.Minute)
	newest := now.Add(4 * time.Second)
	results := []match.Result{
		{VehicleID: "1", RouteID: "R1", Lat: 37.98, Lon: 23.72, Bearing: 90, Time: newest,
			Trip: f.TripIndex("T01"), Day: day, Delay: 120, NextIndex: 3},
		{VehicleID: "2", RouteID: "R1", Lat: 37.98, Lon: 23.70, Time: now.Add(-time.Minute),
			Trip: f.TripIndex("T02"), Day: day, Delay: 0, NextIndex: 1, Waiting: true},
		{VehicleID: "3", RouteID: "R1", Lat: 37.98, Lon: 23.71, Time: now, Trip: -1},
	}
	vp, tu := Build(f, results, now)
	if vp.GetHeader().GetTimestamp() != uint64(newest.Unix()) || tu.GetHeader().GetTimestamp() != uint64(newest.Unix()) {
		t.Fatal("header must not be older than any entity")
	}
	if vp.GetHeader().GetGtfsRealtimeVersion() != "2.0" || vp.GetHeader().GetIncrementality() != rt.FeedHeader_FULL_DATASET {
		t.Fatal("header fields")
	}
	if len(vp.Entity) != 3 || len(tu.Entity) != 2 {
		t.Fatalf("entities %d %d", len(vp.Entity), len(tu.Entity))
	}
	v1 := vp.Entity[0]
	if v1.GetId() != "vehicle-1" || v1.Vehicle.GetCurrentStatus() != rt.VehiclePosition_IN_TRANSIT_TO ||
		v1.Vehicle.GetStopId() != "S3" || v1.Vehicle.GetCurrentStopSequence() != 4 ||
		v1.Vehicle.Trip.GetTripId() != "T01" || v1.Vehicle.Trip.GetStartDate() != "20261005" ||
		v1.Vehicle.Trip.GetScheduleRelationship() != rt.TripDescriptor_SCHEDULED || v1.Vehicle.GetPosition().GetBearing() != 90 {
		t.Fatalf("vehicle 1: %v", v1)
	}
	v2 := vp.Entity[1].Vehicle
	if v2.GetCurrentStatus() != rt.VehiclePosition_STOPPED_AT || v2.GetStopId() != "S0" {
		t.Fatalf("waiting vehicle: %v", v2)
	}
	v3 := vp.Entity[2].Vehicle
	if v3.Trip.GetRouteId() != "R1" || v3.Trip.TripId != nil || v3.StopId != nil {
		t.Fatalf("unmatched vehicle should carry only route_id: %v", v3)
	}
	t1 := tu.Entity[0]
	if t1.GetId() != "trip-T01-20261005" || t1.TripUpdate.GetVehicle().GetId() != "1" {
		t.Fatalf("trip update id %v", t1)
	}
	stu := t1.TripUpdate.StopTimeUpdate[0]
	if stu.GetArrival().GetDelay() != 120 || stu.Departure != nil || stu.GetStopSequence() != 4 {
		t.Fatalf("stop time update %v", stu)
	}
	if d := tu.Entity[1].TripUpdate.StopTimeUpdate[0]; d.GetDeparture().GetDelay() != 0 || d.Arrival != nil {
		t.Fatalf("waiting trip update %v", d)
	}

	pb, js, err := Encode(vp)
	if err != nil {
		t.Fatal(err)
	}
	var back rt.FeedMessage
	if err := proto.Unmarshal(pb, &back); err != nil || len(back.Entity) != 3 {
		t.Fatalf("pb round trip: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(js, &m); err != nil || m["header"] == nil {
		t.Fatalf("json: %v %s", err, js)
	}
}
