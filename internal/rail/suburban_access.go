package rail

import (
	"context"
	"strings"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

// Read access to the day's board and the station table for the /v2 adapter. The /v1 JSON types
// stay as they are; these are plain Go values.

// SuburbanBoardStop is one stop of a board run. Arr and Dep are zero when the board has none.
type SuburbanBoardStop struct {
	StationID, Name, Role, Status string
	Arr, Dep                      time.Time
	Platform                      *string
	Delay                         *int // minutes
}

// SuburbanSchedule is one run of the day's board.
type SuburbanSchedule struct {
	Train, Corridor, Status, Destination string
	Delay                                *int // minutes
	Stops                                []SuburbanBoardStop
}

// Board returns the board of now's day (fetched as Departures does) and when it was fetched.
// ok is false when no board of that day is available.
func (s *Suburban) Board(ctx context.Context, now time.Time) ([]SuburbanSchedule, time.Time, bool) {
	if _, ok := s.board(ctx, boardDate(now)); !ok {
		return nil, time.Time{}, false
	}
	return s.CachedBoard(now)
}

// CachedBoard is Board without a fetch: the board of now's day as last fetched, whatever its age.
func (s *Suburban) CachedBoard(now time.Time) ([]SuburbanSchedule, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.boards[boardDate(now)]
	if e == nil {
		return nil, time.Time{}, false
	}
	if e.conv == nil {
		e.conv = convertBoard(e.board)
	}
	return e.conv, e.at, true
}

// BoardFailing reports whether the last board fetch failed.
func (s *Suburban) BoardFailing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.boardFail.IsZero()
}

// StationPrimary returns the canonical id of any station id from the cached station table,
// without a fetch; ok is false for an unknown id or before the table loads.
func (s *Suburban) StationPrimary(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.stations.primary[id]
	return p, ok
}

func boardDate(now time.Time) string { return now.In(gtfs.Athens).Format(time.DateOnly) }

func convertBoard(b *scheduleBoard) []SuburbanSchedule {
	out := make([]SuburbanSchedule, 0, len(b.Schedules))
	for _, sc := range b.Schedules {
		c := SuburbanSchedule{Train: strings.TrimSpace(sc.TrainNumber), Corridor: sc.Corridor, Status: sc.Status,
			Destination: sc.Destination.NameGreek, Delay: sc.Delay, Stops: make([]SuburbanBoardStop, len(sc.RouteStops))}
		for i, st := range sc.RouteStops {
			arr, _ := boardTime(st.ScheduledArrival, "")
			dep, _ := boardTime(st.ScheduledDeparture, "")
			c.Stops[i] = SuburbanBoardStop{StationID: st.StationID, Name: st.NameGreek, Role: st.Role, Status: st.Status,
				Arr: arr, Dep: dep, Platform: st.Platform, Delay: st.Delay}
		}
		out = append(out, c)
	}
	return out
}
