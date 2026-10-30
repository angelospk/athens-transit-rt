package rail

import (
	"context"
	"testing"
	"time"
)

// A board past its TTL is served at once while the new one downloads: no request waits for the
// 743 KB board, and a burst of such requests starts one download.
func TestSuburbanStaleBoardRefreshesInBackground(t *testing.T) {
	s, clk, f := newTestSuburban(t)
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("no departures")
	}
	clk.t = clk.t.Add(boardTTL + time.Minute)
	gate := make(chan struct{})
	f.setGate(gate)
	done := make(chan bool)
	go func() {
		for i := 0; i < 5; i++ {
			if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
				done <- false
				return
			}
		}
		done <- true
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("stale board not served")
		}
	case <-time.After(2 * time.Second):
		close(gate)
		t.Fatal("a request waited for the board download")
	}
	close(gate)
	s.refreshes.Wait()
	if n := f.count("/api/public/schedule-board"); n != 2 {
		t.Fatalf("board fetched %d times, want 2 (one refresh for the burst)", n)
	}
	// The refreshed board is fresh again: no further fetch.
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("no departures after the refresh")
	}
	s.refreshes.Wait()
	if n := f.count("/api/public/schedule-board"); n != 2 {
		t.Fatalf("board fetched %d times after the refresh, want 2", n)
	}
	if _, at, _ := s.CachedBoard(clk.now()); !at.Equal(clk.now()) {
		t.Fatalf("board time %v, want the refresh time %v", at, clk.now())
	}
}

// A request that ends does not cancel the refresh it started.
func TestSuburbanRefreshOutlivesRequest(t *testing.T) {
	s, clk, f := newTestSuburban(t)
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("no departures")
	}
	clk.t = clk.t.Add(boardTTL + time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	if _, ok := s.Departures(ctx, "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("stale board not served")
	}
	cancel()
	s.refreshes.Wait()
	if s.BoardFailing() {
		t.Fatal("the refresh failed when its request ended")
	}
	if n := f.count("/api/public/schedule-board"); n != 2 {
		t.Fatalf("board fetched %d times, want 2", n)
	}
}

// The station table past its TTL is served at once too.
func TestSuburbanStaleStationsRefreshInBackground(t *testing.T) {
	s, clk, f := newTestSuburban(t)
	if _, ok := s.Stations(context.Background()); !ok {
		t.Fatal("no stations")
	}
	clk.t = clk.t.Add(stationsTTL + time.Minute)
	gate := make(chan struct{})
	f.setGate(gate)
	done := make(chan int)
	go func() {
		list, _ := s.Stations(context.Background())
		done <- len(list)
	}()
	select {
	case n := <-done:
		if n != 2 {
			t.Fatalf("stale stations: %d, want 2", n)
		}
	case <-time.After(2 * time.Second):
		close(gate)
		t.Fatal("a request waited for the station download")
	}
	close(gate)
	s.refreshes.Wait()
	if n := f.count("/api/public/corridor-stations"); n != 2 {
		t.Fatalf("stations fetched %d times, want 2", n)
	}
}

// A refresh of yesterday's board that waited behind the first fetch of today does not download
// yesterday again: that would drop today's board.
func TestSuburbanRefreshAfterMidnightKeepsToday(t *testing.T) {
	s, clk, f := newTestSuburban(t)
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("no departures")
	}
	clk.t = clk.t.Add(boardTTL + time.Minute)
	tomorrow := clk.now().AddDate(0, 0, 1)
	gate := make(chan struct{})
	f.setGate(gate)
	done := make(chan struct{})
	go func() {
		s.Departures(context.Background(), "ST-PIR", 10, tomorrow) // cold: fetches inside the request
		close(done)
	}()
	for deadline := time.Now().Add(2 * time.Second); f.count("/api/public/schedule-board") < 2; {
		if time.Now().After(deadline) {
			close(gate)
			t.Fatal("tomorrow's fetch did not start")
		}
		time.Sleep(time.Millisecond)
	}
	// Today's stale board starts a refresh, which waits for tomorrow's fetch to end.
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("stale board not served")
	}
	close(gate)
	<-done
	s.refreshes.Wait()
	if _, _, ok := s.CachedBoard(tomorrow); !ok {
		t.Fatal("tomorrow's board was dropped")
	}
	if n := f.count("/api/public/schedule-board"); n != 2 {
		t.Fatalf("board fetched %d times, want 2", n)
	}
}

// A load whose context ended while it waited for another fetch does not fetch, and so does not
// record an upstream failure that never happened.
func TestSuburbanLoadAfterDeadlineFetchesNothing(t *testing.T) {
	s, clk, f := newTestSuburban(t)
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("no departures")
	}
	clk.t = clk.t.Add(stationsTTL + time.Minute) // board and stations both stale
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.loadBoard(ctx, boardDate(suburbanNow))
	s.loadStations(ctx)
	if s.BoardFailing() || !s.stationsFail.IsZero() {
		t.Fatal("an ended context counted as an upstream failure")
	}
	if n := f.count("/api/public/schedule-board") + f.count("/api/public/corridor-stations"); n != 2 {
		t.Fatalf("%d fetches, want 2 (the first load only)", n)
	}
}

// A refresh that waits for the fetch slot past its time limit gives up then: it does not keep its
// goroutine and its refreshing flag while another fetch (an MPass screen, a slow board) holds the slot.
func TestSuburbanRefreshGivesUpWaitingForTheFetchSlot(t *testing.T) {
	s, clk, f := newTestSuburban(t)
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("no departures")
	}
	old := refreshTimeout
	refreshTimeout = 50 * time.Millisecond
	defer func() { refreshTimeout = old }()
	clk.t = clk.t.Add(boardTTL + time.Minute)
	if !s.lockFetch(context.Background()) { // another fetch holds the slot
		t.Fatal("slot not free")
	}
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("stale board not served")
	}
	waited := make(chan struct{})
	go func() { s.refreshes.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		s.unlockFetch()
		t.Fatal("the refresh kept waiting for the fetch slot past its time limit")
	}
	s.unlockFetch()
	s.mu.Lock()
	n := len(s.refreshing)
	s.mu.Unlock()
	if n != 0 || s.BoardFailing() {
		t.Fatalf("after giving up: %d refreshing, failing %v", n, s.BoardFailing())
	}
	if c := f.count("/api/public/schedule-board"); c != 1 {
		t.Fatalf("board fetched %d times, want 1 (the refresh gave up)", c)
	}
	// The next request past the TTL starts a new refresh, which now gets the slot.
	if _, ok := s.Departures(context.Background(), "ST-PIR", 10, clk.now()); !ok {
		t.Fatal("no departures")
	}
	s.refreshes.Wait()
	if c := f.count("/api/public/schedule-board"); c != 2 {
		t.Fatalf("board fetched %d times, want 2", c)
	}
}
