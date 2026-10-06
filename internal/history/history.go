// Package history keeps every new GPS fix in hourly CSV files, gzipped when the hour ends, for
// later statistics (docs/superpowers/specs/2026-10-06-fix-history.md). Polling never waits for
// the disk: rows go through a buffered channel to one writer goroutine, and are dropped (and
// counted) when it is full.
package history

import (
	"bufio"
	"compress/gzip"
	"encoding/csv"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Header     = "fix_t,line,route_code,veh,lat,lon,gtfs,trip_id,shape_id,s_m,delay_s\n"
	hourLayout = "2006-01-02T15" // file names, UTC
	bufferRows = 4096
	flushEvery = 10 * time.Second
	dedupKeep  = time.Hour // a vehicle not seen for this long is forgotten by the dedup
)

// Row is one GPS fix. Lat and Lon are degrees; SM is metres along the shape, -1 when unknown;
// TripID, ShapeID and DelayS are empty / nil when the vehicle is not matched to a trip.
type Row struct {
	FixT                 int64
	Line, RouteCode, Veh string
	Lat, Lon             float64
	GTFS                 string
	TripID, ShapeID      string
	SM                   int32
	DelayS               *int
}

// SM converts metres along a shape for Row.SM: -1 when not a usable distance.
func SM(m float64) int32 {
	if math.IsNaN(m) || m < 0 || m > math.MaxInt32 {
		return -1
	}
	return int32(math.Round(m))
}

type Writer struct {
	dir      string
	maxBytes int64
	keep     time.Duration
	now      func() time.Time
	log      *slog.Logger

	mu     sync.RWMutex // Add holds it shared; Close takes it to stop new rows
	closed bool
	ch     chan Row
	stop   chan struct{}
	done   chan struct{}

	dropped atomic.Int64 // queue full or closed
	written atomic.Int64 // rows on disk (flushed)
	lost    atomic.Int64 // rows a disk error lost

	// writer goroutine only
	last    map[string]int64 // veh -> newest fix_t written
	hour    string           // hour of the open file
	f       *os.File
	bw      *bufio.Writer
	cw      *csv.Writer
	pending int64 // rows written to the buffer since the last flush
}

// Open starts a writer in dir (created if needed). Files older than keep are deleted, then the
// oldest ones while the directory holds more than maxBytes.
func Open(dir string, maxBytes int64, keep time.Duration, now func() time.Time, log *slog.Logger) (*Writer, error) {
	w, err := newWriter(dir, maxBytes, keep, now, log)
	if err != nil {
		return nil, err
	}
	go w.run()
	return w, nil
}

func newWriter(dir string, maxBytes int64, keep time.Duration, now func() time.Time, log *slog.Logger) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, maxBytes: maxBytes, keep: keep, now: now, log: log,
		ch: make(chan Row, bufferRows), stop: make(chan struct{}), done: make(chan struct{}), last: map[string]int64{}}
	// The current hour's file stays plain: new rows are appended to it.
	w.compressLeftovers(w.nowHour())
	w.prune()
	return w, nil
}

// Add queues a row without blocking. A full queue or a closed writer drops it (counted).
func (w *Writer) Add(r Row) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		w.dropped.Add(1)
		return
	}
	select {
	case w.ch <- r:
	default:
		w.dropped.Add(1)
	}
}

func (w *Writer) Dropped() int64 { return w.dropped.Load() }
func (w *Writer) Written() int64 { return w.written.Load() }
func (w *Writer) Lost() int64    { return w.lost.Load() }

// Close writes the queued rows and closes the current file (it is gzipped when its hour is
// over, at the next start at the latest). Rows added later are dropped.
func (w *Writer) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	close(w.stop)
	<-w.done
}

func (w *Writer) run() {
	defer close(w.done)
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	for {
		select {
		case r := <-w.ch:
			w.write(r)
		case <-tick.C:
			w.tick()
		case <-w.stop:
			for {
				select {
				case r := <-w.ch:
					w.write(r)
				default:
					w.closeFile()
					return
				}
			}
		}
	}
}

// tick flushes, or ends a past hour; an idle hour makes no file.
func (w *Writer) tick() {
	if w.f != nil && w.nowHour() != w.hour {
		w.endHour()
	} else {
		w.flush()
	}
}

func (w *Writer) nowHour() string { return w.now().UTC().Format(hourLayout) }

func (w *Writer) write(r Row) {
	if r.FixT <= w.last[r.Veh] {
		return // OASA repeats a fix until the vehicle reports again
	}
	w.last[r.Veh] = r.FixT
	if w.f != nil && w.nowHour() != w.hour {
		w.endHour()
	}
	if w.f == nil && !w.open() {
		w.lost.Add(1)
		return
	}
	delay := ""
	if r.DelayS != nil {
		delay = strconv.Itoa(*r.DelayS)
	}
	w.cw.Write([]string{strconv.FormatInt(r.FixT, 10), r.Line, r.RouteCode, r.Veh, micro(r.Lat), micro(r.Lon),
		r.GTFS, r.TripID, r.ShapeID, strconv.Itoa(int(r.SM)), delay})
	w.pending++
}

func micro(deg float64) string { return strconv.FormatInt(int64(math.Round(deg*1e6)), 10) }

// open opens (appends to) the current hour's file.
func (w *Writer) open() bool {
	w.hour = w.nowHour()
	f, err := os.OpenFile(filepath.Join(w.dir, w.hour+".csv"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		w.log.Warn("history: open", "err", err)
		return false
	}
	w.f, w.bw = f, bufio.NewWriterSize(f, 64<<10)
	w.cw = csv.NewWriter(w.bw)
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		w.bw.WriteString(Header)
	}
	return true
}

// flush moves the buffered rows to the file. On an error they count as lost and the file is
// reopened with the next row (a bufio.Writer keeps its first error).
func (w *Writer) flush() {
	if w.f == nil {
		return
	}
	w.cw.Flush()
	err := w.cw.Error()
	if err == nil {
		err = w.bw.Flush()
	}
	if err != nil {
		w.log.Warn("history: write", "err", err)
		w.lost.Add(w.pending)
		w.pending = 0
		w.f.Close()
		w.f, w.bw, w.cw = nil, nil, nil
		return
	}
	w.written.Add(w.pending)
	w.pending = 0
}

func (w *Writer) closeFile() {
	w.flush()
	if w.f != nil {
		w.f.Close()
		w.f, w.bw, w.cw = nil, nil, nil
	}
}

// endHour closes the file of a past hour, gzips it, prunes, and forgets vehicles not seen lately.
func (w *Writer) endHour() {
	w.closeFile()
	w.compressLeftovers(w.nowHour())
	w.prune()
	cut := w.now().Add(-dedupKeep).Unix()
	for v, t := range w.last {
		if t < cut {
			delete(w.last, v)
		}
	}
}

// compressLeftovers gzips every plain .csv except the current hour's.
func (w *Writer) compressLeftovers(current string) {
	names, _ := filepath.Glob(filepath.Join(w.dir, "*.csv"))
	for _, p := range names {
		if strings.TrimSuffix(filepath.Base(p), ".csv") == current {
			continue
		}
		if err := gzipFile(p); err != nil {
			w.log.Warn("history: gzip", "file", p, "err", err)
		}
	}
}

// gzipFile replaces path with path.gz. An existing path.gz (the same hour written before a
// restart) is kept: the new rows follow it as another gzip member (readers see one stream).
func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := path + ".gz.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if old, err := os.Open(path + ".gz"); err == nil {
		_, err = io.Copy(out, old)
		old.Close()
		if err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
	}
	zw := gzip.NewWriter(out)
	_, err = io.Copy(zw, in)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path+".gz")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(path)
}

// prune deletes hours older than keep, then the oldest files while the directory holds more
// than maxBytes. Every file counts (plain, gzipped, left-over temporary) except the open one.
func (w *Writer) prune() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	open := ""
	if w.f != nil {
		open = w.hour + ".csv"
	}
	type file struct {
		name string
		size int64
	}
	var files []file
	var total int64
	cutoff := w.now().Add(-w.keep)
	for _, e := range entries {
		name := e.Name()
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if name == open {
			total += info.Size()
			continue
		}
		hour, _, _ := strings.Cut(name, ".")
		if t, err := time.Parse(hourLayout, hour); err == nil && t.Add(time.Hour).Before(cutoff) {
			if os.Remove(filepath.Join(w.dir, name)) == nil {
				continue
			}
		}
		files = append(files, file{name, info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name }) // hour names sort by time
	for i := 0; total > w.maxBytes && i < len(files); i++ {
		if os.Remove(filepath.Join(w.dir, files[i].name)) == nil {
			total -= files[i].size
		}
	}
}
