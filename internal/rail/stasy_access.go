package rail

import "time"

// Read access to the parsed timetable for the /v2 stasy adapter, which builds its own Stops,
// Patterns and Runs from it. Indices are stable for one *STASY; callers must not modify what
// these return.

// STASYStopTime is one stop of a trip: Stop index, arrival A and departure T in seconds after
// midnight of the service day.
type STASYStopTime = stasyStopTime

// STASYRow is one call at a stop: Trip index and departure Dep (seconds of the service day).
type STASYRow = stasyRow

// TripInfo is a trip's id, route index and headsign (trip indices run from 0 to NumTrips).
func (s *STASY) TripInfo(ti int32) (id string, route int32, headsign string) {
	t := &s.trips[ti]
	return t.ID, t.Route, t.Headsign
}

// TripIndex is the index of a trip id.
func (s *STASY) TripIndex(id string) (int32, bool) {
	ti, ok := s.tripIdx[id]
	return ti, ok
}

// TripStops is a trip's stops in stop_sequence order.
func (s *STASY) TripStops(ti int32) []STASYStopTime { return s.tripStops[ti] }

// StopRows are the calls at a stop index.
func (s *STASY) StopRows(stop int32) []STASYRow { return s.byStop[stop] }

// ServiceOn reports whether trip ti runs on the service day of noon (a local noon), and whether
// another date of the same weekday stood in for it (the timetable does not cover that day).
func (s *STASY) ServiceOn(ti int32, noon time.Time) (runs, indicative bool) {
	date, ind := s.timetableDate(noon)
	return s.active(s.trips[ti].Service, date), ind
}

// Indicative reports whether the timetable does not cover the day of noon (another date of the
// same weekday stands in).
func (s *STASY) Indicative(noon time.Time) bool {
	_, ind := s.timetableDate(noon)
	return ind
}

// RouteColor is the route's colour ("#rrggbb"), from the feed or the published one.
func (s *STASY) RouteColor(route int32) string { return s.color(route) }

// Updated is when the current timetable was loaded, zero before.
func (s *STASYStore) Updated() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updated
}
