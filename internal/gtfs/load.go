package gtfs

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RequiredFiles must all be present in a usable feed.
var RequiredFiles = []string{"stops.txt", "routes.txt", "trips.txt", "stop_times.txt", "calendar.txt"}

type LoadOptions struct {
	// Lines limits the feed to these line numbers (route_short_name); nil loads everything.
	Lines map[string]bool
	Meta  Meta
}

// LoadZip parses a GTFS zip. stop_times.txt (~225 MB for OASA) is streamed.
func LoadZip(path string, opt LoadOptions) (*Feed, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	for _, name := range RequiredFiles {
		if files[name] == nil {
			return nil, fmt.Errorf("gtfs: %s missing from %s", name, path)
		}
	}
	b := &builder{feed: &Feed{Meta: opt.Meta, Exceptions: map[int64]int8{}}, lines: opt.Lines}
	steps := []struct {
		name     string
		optional bool
		fn       func(*table) error
	}{
		{"stops.txt", false, b.stops},
		{"calendar.txt", false, b.calendar},
		{"calendar_dates.txt", true, b.calendarDates},
		{"routes.txt", false, b.routes},
		{"trips.txt", false, b.trips},
		{"stop_times.txt", false, b.stopTimes},
		{"shapes.txt", true, b.shapes},
	}
	for _, s := range steps {
		zf := files[s.name]
		if zf == nil {
			if s.optional {
				continue
			}
			return nil, fmt.Errorf("gtfs: %s missing", s.name)
		}
		if err := readTable(zf, s.fn); err != nil {
			return nil, fmt.Errorf("gtfs: %s: %w", s.name, err)
		}
	}
	if err := b.finish(); err != nil {
		return nil, err
	}
	return b.feed, nil
}

// table is a CSV reader that addresses columns by header name.
type table struct {
	r      *csv.Reader
	cols   map[string]int
	record []string
}

func (t *table) next() (bool, error) {
	rec, err := t.r.Read()
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t.record = rec
	return true, nil
}

// get returns the named column, or "" when the column does not exist.
func (t *table) get(name string) string {
	if i, ok := t.cols[name]; ok && i < len(t.record) {
		return t.record[i]
	}
	return ""
}

func (t *table) require(names ...string) error {
	for _, n := range names {
		if _, ok := t.cols[n]; !ok {
			return fmt.Errorf("column %s missing", n)
		}
	}
	return nil
}

func readTable(zf *zip.File, fn func(*table) error) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(bufio.NewReaderSize(rc, 1<<16))
	r.ReuseRecord = true
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return err
	}
	cols := map[string]int{}
	for i, h := range header {
		cols[strings.TrimSpace(strings.TrimPrefix(h, "\xef\xbb\xbf"))] = i
	}
	return fn(&table{r: r, cols: cols})
}

type stRow struct{ seq, stop, arr, dep int32 }

type builder struct {
	feed      *Feed
	lines     map[string]bool
	stopIdx   map[string]int32
	routeIdx  map[string]int32
	svcIdx    map[string]int32
	shapeIdx  map[string]int32
	tripIdx   map[string]int32
	headsigns map[string]int32
	tripShape []string  // shape_id per trip, resolved after shapes are read
	rows      [][]stRow // stop_times per trip
	shapePts  map[string][]shapePt
}

type shapePt struct {
	seq      int
	lat, lon float64
}

func (b *builder) stops(t *table) error {
	if err := t.require("stop_id", "stop_lat", "stop_lon"); err != nil {
		return err
	}
	b.stopIdx = map[string]int32{}
	for {
		ok, err := t.next()
		if !ok || err != nil {
			return err
		}
		lat, err1 := strconv.ParseFloat(t.get("stop_lat"), 64)
		lon, err2 := strconv.ParseFloat(t.get("stop_lon"), 64)
		if err := errors.Join(err1, err2); err != nil {
			return fmt.Errorf("stop %s: %w", t.get("stop_id"), err)
		}
		b.stopIdx[t.get("stop_id")] = int32(len(b.feed.Stops))
		b.feed.Stops = append(b.feed.Stops, Stop{ID: t.get("stop_id"), Name: t.get("stop_name"), Lat: lat, Lon: lon})
	}
}

var weekdays = [7]string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

func (b *builder) calendar(t *table) error {
	if err := t.require("service_id", "start_date", "end_date"); err != nil {
		return err
	}
	b.svcIdx = map[string]int32{}
	for {
		ok, err := t.next()
		if !ok || err != nil {
			return err
		}
		s := Service{ID: t.get("service_id")}
		for i, d := range weekdays {
			s.Days[i] = t.get(d) == "1"
		}
		start, err1 := parseDate(t.get("start_date"))
		end, err2 := parseDate(t.get("end_date"))
		if err := errors.Join(err1, err2); err != nil {
			return err
		}
		s.Start, s.End = start, end
		b.svcIdx[s.ID] = int32(len(b.feed.Services))
		b.feed.Services = append(b.feed.Services, s)
	}
}

// service returns the index of a service id, creating a calendar-less one if needed
// (services defined only in calendar_dates.txt).
func (b *builder) service(id string) int32 {
	if i, ok := b.svcIdx[id]; ok {
		return i
	}
	i := int32(len(b.feed.Services))
	b.svcIdx[id] = i
	b.feed.Services = append(b.feed.Services, Service{ID: id})
	return i
}

func (b *builder) calendarDates(t *table) error {
	if err := t.require("service_id", "date", "exception_type"); err != nil {
		return err
	}
	for {
		ok, err := t.next()
		if !ok || err != nil {
			return err
		}
		d, err := parseDate(t.get("date"))
		if err != nil {
			return err
		}
		typ, err := strconv.Atoi(t.get("exception_type"))
		if err != nil {
			return err
		}
		b.feed.Exceptions[int64(b.service(t.get("service_id")))<<32|int64(d)] = int8(typ)
	}
}

func (b *builder) routes(t *table) error {
	if err := t.require("route_id", "route_short_name"); err != nil {
		return err
	}
	b.routeIdx = map[string]int32{}
	for {
		ok, err := t.next()
		if !ok || err != nil {
			return err
		}
		short := strings.TrimSpace(t.get("route_short_name"))
		if b.lines != nil && !b.lines[short] {
			continue
		}
		typ, _ := strconv.Atoi(t.get("route_type"))
		b.routeIdx[t.get("route_id")] = int32(len(b.feed.Routes))
		b.feed.Routes = append(b.feed.Routes, Route{
			ID: t.get("route_id"), ShortName: short, LongName: strings.TrimSpace(t.get("route_long_name")),
			Type: typ, Color: t.get("route_color"), TextColor: t.get("route_text_color"),
		})
	}
}

func (b *builder) trips(t *table) error {
	if err := t.require("route_id", "service_id", "trip_id"); err != nil {
		return err
	}
	b.tripIdx = map[string]int32{}
	b.headsigns = map[string]int32{}
	for {
		ok, err := t.next()
		if !ok || err != nil {
			return err
		}
		route, ok := b.routeIdx[t.get("route_id")]
		if !ok {
			continue
		}
		hs := strings.TrimSpace(t.get("trip_headsign"))
		h, ok := b.headsigns[hs]
		if !ok {
			h = int32(len(b.feed.Headsigns))
			b.headsigns[hs] = h
			b.feed.Headsigns = append(b.feed.Headsigns, hs)
		}
		dir, _ := strconv.Atoi(t.get("direction_id"))
		b.tripIdx[t.get("trip_id")] = int32(len(b.feed.Trips))
		b.feed.Trips = append(b.feed.Trips, Trip{
			ID: t.get("trip_id"), Route: route, Service: b.service(t.get("service_id")),
			Shape: -1, Dir: int8(dir), Headsign: h,
		})
		b.tripShape = append(b.tripShape, t.get("shape_id"))
	}
}

func (b *builder) stopTimes(t *table) error {
	if err := t.require("trip_id", "arrival_time", "departure_time", "stop_id", "stop_sequence"); err != nil {
		return err
	}
	b.rows = make([][]stRow, len(b.feed.Trips))
	iTrip, iArr, iDep, iStop, iSeq := t.cols["trip_id"], t.cols["arrival_time"], t.cols["departure_time"],
		t.cols["stop_id"], t.cols["stop_sequence"]
	for {
		ok, err := t.next()
		if !ok || err != nil {
			return err
		}
		r := t.record
		ti, ok := b.tripIdx[r[iTrip]]
		if !ok {
			continue
		}
		stop, ok := b.stopIdx[r[iStop]]
		if !ok {
			return fmt.Errorf("trip %s: unknown stop %s", r[iTrip], r[iStop])
		}
		seq, err1 := strconv.Atoi(r[iSeq])
		arr, err2 := ParseHMS(r[iArr])
		dep, err3 := ParseHMS(r[iDep])
		if err := errors.Join(err1, err2, err3); err != nil {
			return fmt.Errorf("trip %s: %w", r[iTrip], err)
		}
		b.rows[ti] = append(b.rows[ti], stRow{int32(seq), stop, arr, dep})
	}
}

func (b *builder) shapes(t *table) error {
	if err := t.require("shape_id", "shape_pt_lat", "shape_pt_lon", "shape_pt_sequence"); err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, s := range b.tripShape {
		wanted[s] = true
	}
	b.shapePts = map[string][]shapePt{}
	for {
		ok, err := t.next()
		if !ok || err != nil {
			return err
		}
		id := t.get("shape_id")
		if !wanted[id] {
			continue
		}
		seq, err1 := strconv.Atoi(t.get("shape_pt_sequence"))
		lat, err2 := strconv.ParseFloat(t.get("shape_pt_lat"), 64)
		lon, err3 := strconv.ParseFloat(t.get("shape_pt_lon"), 64)
		if err := errors.Join(err1, err2, err3); err != nil {
			return fmt.Errorf("shape %s: %w", id, err)
		}
		b.shapePts[id] = append(b.shapePts[id], shapePt{seq, lat, lon})
	}
}

// finish sorts stop times, drops trips with fewer than two stops, deduplicates patterns and
// profiles, and resolves shapes.
func (b *builder) finish() error {
	f := b.feed
	ids := make([]string, 0, len(b.shapePts))
	for id := range b.shapePts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	shapeIdx := map[string]int32{}
	for _, id := range ids {
		pts := b.shapePts[id]
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].seq < pts[j].seq })
		s := Shape{ID: id, Points: make([][2]float64, len(pts))}
		for i, p := range pts {
			s.Points[i] = [2]float64{p.lat, p.lon}
		}
		shapeIdx[id] = int32(len(f.Shapes))
		f.Shapes = append(f.Shapes, s)
	}

	patterns, profiles := map[string]int32{}, map[string]int32{}
	var key []byte
	kept := f.Trips[:0]
	for i, t := range f.Trips {
		rows := b.rows[i]
		if len(rows) < 2 {
			continue
		}
		sort.SliceStable(rows, func(a, c int) bool { return rows[a].seq < rows[c].seq })
		key = key[:0]
		for _, r := range rows {
			key = binary.AppendVarint(key, int64(r.stop))
			key = binary.AppendVarint(key, int64(r.seq))
		}
		p, ok := patterns[string(key)]
		if !ok {
			p = int32(len(f.Patterns))
			patterns[string(key)] = p
			pat := Pattern{Stops: make([]int32, len(rows)), Seq: make([]int32, len(rows))}
			for j, r := range rows {
				pat.Stops[j], pat.Seq[j] = r.stop, r.seq
			}
			f.Patterns = append(f.Patterns, pat)
		}
		base := rows[0].arr
		prof := Profile{Arr: make([]uint16, len(rows)), Dep: make([]uint16, len(rows))}
		sameDep := true
		for j, r := range rows {
			a, d := r.arr-base, r.dep-base
			if a < 0 || d < 0 || a > 65535 || d > 65535 {
				return fmt.Errorf("gtfs: trip %s: stop times out of range", t.ID)
			}
			prof.Arr[j], prof.Dep[j] = uint16(a), uint16(d)
			sameDep = sameDep && a == d
		}
		if sameDep {
			prof.Dep = nil
		}
		key = key[:0]
		for j := range prof.Arr {
			key = binary.AppendUvarint(key, uint64(prof.Arr[j]))
			if prof.Dep != nil {
				key = binary.AppendUvarint(key, uint64(prof.Dep[j]))
			}
		}
		key = append(key, byte(len(prof.Arr)), boolByte(prof.Dep != nil))
		pr, ok := profiles[string(key)]
		if !ok {
			pr = int32(len(f.Profiles))
			profiles[string(key)] = pr
			f.Profiles = append(f.Profiles, prof)
		}
		t.Pattern, t.Profile, t.Base = p, pr, base
		if s, ok := shapeIdx[b.tripShape[i]]; ok {
			t.Shape = s
		}
		kept = append(kept, t)
	}
	f.Trips = kept
	b.rows = nil
	if f.Meta.BuiltAt == 0 {
		f.Meta.BuiltAt = time.Now().Unix()
	}
	f.index()
	return nil
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// ParseHMS parses a GTFS time ("25:10:00" allowed) into seconds.
func ParseHMS(v string) (int32, error) {
	v = strings.TrimSpace(v)
	h, rest, ok1 := strings.Cut(v, ":")
	m, s, ok2 := strings.Cut(rest, ":")
	if !ok1 || !ok2 {
		return 0, fmt.Errorf("bad time %q", v)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	ss, err3 := strconv.Atoi(s)
	if err := errors.Join(err1, err2, err3); err != nil {
		return 0, fmt.Errorf("bad time %q", v)
	}
	return int32(hh*3600 + mm*60 + ss), nil
}

func parseDate(v string) (int32, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 19000101 || n > 29991231 {
		return 0, fmt.Errorf("bad date %q", v)
	}
	return int32(n), nil
}
