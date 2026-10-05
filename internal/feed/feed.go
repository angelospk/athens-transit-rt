// Package feed builds GTFS-Realtime VehiclePositions and TripUpdates from matched vehicles.
package feed

import (
	"time"

	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/match"
)

func message(ts time.Time) *rt.FeedMessage {
	return &rt.FeedMessage{Header: &rt.FeedHeader{
		GtfsRealtimeVersion: proto.String("2.0"),
		Incrementality:      rt.FeedHeader_FULL_DATASET.Enum(),
		Timestamp:           proto.Uint64(uint64(ts.Unix())),
	}}
}

func tripDescriptor(f *gtfs.Feed, r *match.Result) *rt.TripDescriptor {
	t := f.Trip(r.Trip)
	return &rt.TripDescriptor{
		TripId:               proto.String(t.ID),
		RouteId:              proto.String(f.Routes[t.Route].ID),
		StartDate:            proto.String(gtfs.ServiceDate(r.Day)),
		ScheduleRelationship: rt.TripDescriptor_SCHEDULED.Enum(),
	}
}

// NextStop is the stop a matched vehicle is heading to (the first stop while waiting there).
func NextStop(f *gtfs.Feed, r *match.Result) gtfs.StopTime {
	t := f.Trip(r.Trip)
	if r.Waiting {
		return f.StopTime(t, 0)
	}
	return f.StopTime(t, r.NextIndex)
}

// Build returns (vehicle positions, trip updates).
func Build(f *gtfs.Feed, results []match.Result, now time.Time) (*rt.FeedMessage, *rt.FeedMessage) {
	// The header must not be older than any entity; OASA's GPS fixes can be a few seconds
	// newer than the start of the polling cycle.
	newest := now
	for i := range results {
		if results[i].Time.After(newest) {
			newest = results[i].Time
		}
	}
	vehicles, updates := message(newest), message(newest)
	for i := range results {
		r := &results[i]
		vp := &rt.VehiclePosition{
			Vehicle: &rt.VehicleDescriptor{Id: proto.String(r.VehicleID), Label: proto.String(r.VehicleID)},
			Position: &rt.Position{Latitude: proto.Float32(float32(r.Lat)), Longitude: proto.Float32(float32(r.Lon)),
				Bearing: proto.Float32(float32(r.Bearing))},
			Timestamp: proto.Uint64(uint64(r.Time.Unix())),
		}
		vehicles.Entity = append(vehicles.Entity, &rt.FeedEntity{Id: proto.String("vehicle-" + r.VehicleID), Vehicle: vp})
		if !r.Matched() {
			// Partial descriptor: the line is known even when the exact trip is not.
			vp.Trip = &rt.TripDescriptor{RouteId: proto.String(r.RouteID)}
			continue
		}
		vp.Trip = tripDescriptor(f, r)
		stop := NextStop(f, r)
		if r.Waiting {
			vp.CurrentStatus = rt.VehiclePosition_STOPPED_AT.Enum()
		} else {
			vp.CurrentStatus = rt.VehiclePosition_IN_TRANSIT_TO.Enum()
		}
		stopID := f.Stops[stop.Stop].ID
		vp.CurrentStopSequence = proto.Uint32(uint32(stop.Seq))
		vp.StopId = proto.String(stopID)

		stu := &rt.TripUpdate_StopTimeUpdate{
			StopSequence:         proto.Uint32(uint32(stop.Seq)),
			StopId:               proto.String(stopID),
			ScheduleRelationship: rt.TripUpdate_StopTimeUpdate_SCHEDULED.Enum(),
		}
		// A single delay propagates to all downstream stops (GTFS-RT spec).
		ev := &rt.TripUpdate_StopTimeEvent{Delay: proto.Int32(int32(r.Delay))}
		if r.Waiting {
			stu.Departure = ev
		} else {
			stu.Arrival = ev
		}
		updates.Entity = append(updates.Entity, &rt.FeedEntity{
			Id: proto.String("trip-" + f.Trip(r.Trip).ID + "-" + gtfs.ServiceDate(r.Day)),
			TripUpdate: &rt.TripUpdate{
				Trip:           tripDescriptor(f, r),
				Vehicle:        &rt.VehicleDescriptor{Id: proto.String(r.VehicleID)},
				Timestamp:      proto.Uint64(uint64(r.Time.Unix())),
				StopTimeUpdate: []*rt.TripUpdate_StopTimeUpdate{stu},
			},
		})
	}
	return vehicles, updates
}

// Encode returns the binary protobuf and an indented JSON rendering (for debugging).
func Encode(msg *rt.FeedMessage) (pb, js []byte, err error) {
	if pb, err = proto.Marshal(msg); err != nil {
		return nil, nil, err
	}
	js, err = protojson.MarshalOptions{Multiline: true, Indent: " "}.Marshal(msg)
	return pb, js, err
}
