// Package history keeps every new GPS fix in hourly CSV files, gzipped when the hour ends, for
// later statistics (docs/superpowers/specs/2026-10-06-fix-history.md). Polling never waits for
// the disk: rows go through a buffered channel to one writer goroutine, and are dropped (and
// counted) when it is full.
package history

import (
	"bufio"
	"bytes"
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
	w.cw.Write([]string{strconv.FormatInt(r.FixT, 10), oneLine(r.Line), oneLine(r.RouteCode), oneLine(r.Veh),
		micro(r.Lat), micro(r.Lon), oneLine(r.GTFS), oneLine(r.TripID), oneLine(r.ShapeID), strconv.Itoa(int(r.SM)), delay})
	w.pending++
}

// oneLine drops line breaks (ids never have them), so every newline in a file ends a row and a
// row stays far below the 64 KiB cutPartialRow looks at.
func oneLine(v string) string {
	if len(v) > 256 {
		v = v[:256]
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, v)
}

func micro(deg float64) string { return strconv.FormatInt(int64(math.Round(deg*1e6)), 10) }

// open opens (appends to) the current hour's file.
func (w *Writer) open() bool {
	w.hour = w.nowHour()
	f, err := os.OpenFile(filepath.Join(w.dir, w.hour+".csv"), os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		w.log.Warn("history: open", "err", err)
		return false
	}
	if err := cutPartialRow(f); err != nil {
		w.log.Warn("history: open", "err", err)
		f.Close()
		return false
	}
	w.f, w.bw = f, bufio.NewWriterSize(f, 64<<10)
	w.cw = csv.NewWriter(w.bw)
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		w.bw.WriteString(Header)
	}
	return true
}

// cutPartialRow drops an unfinished last row (a write that failed half way), so the next row
// starts on its own line.
func cutPartialRow(f *os.File) error {
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return err
	}
	n := min(st.Size(), 64<<10)
	tail := make([]byte, n)
	if _, err := f.ReadAt(tail, st.Size()-n); err != nil {
		return err
	}
	if tail[n-1] == '\n' {
		return nil
	}
	keep := st.Size() - n + int64(bytes.LastIndexByte(tail, '\n')+1)
	return f.Truncate(keep)
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

// compressLeftovers gzips every plain .csv except the current hour's. Each gets its own
// archive (hour.csv.gz, then hour.1.csv.gz, ... after a restart within the hour), each with its
// header. Retries are idempotent: the CSV is first renamed to .part, and a .part whose archive
// exists was already gzipped.
func (w *Writer) compressLeftovers(current string) {
	names, _ := filepath.Glob(filepath.Join(w.dir, "*.csv"))
	for _, p := range names {
		hour := strings.TrimSuffix(filepath.Base(p), ".csv")
		if hour == current {
			continue
		}
		for k := 0; ; k++ {
			base := hour
			if k > 0 {
				base += "." + strconv.Itoa(k)
			}
			part := filepath.Join(w.dir, base+".csv.part")
			if exists(part) || exists(filepath.Join(w.dir, base+".csv.gz")) {
				continue
			}
			if err := os.Rename(p, part); err != nil {
				w.log.Warn("history: seal", "file", p, "err", err)
			}
			break
		}
	}
	parts, _ := filepath.Glob(filepath.Join(w.dir, "*.csv.part"))
	for _, part := range parts {
		gz := strings.TrimSuffix(part, ".part") + ".gz"
		if !exists(gz) {
			if err := gzipFile(part, gz); err != nil {
				w.log.Warn("history: gzip", "file", part, "err", err)
				continue
			}
		}
		os.Remove(part)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// gzipFile writes src gzipped to dst (via a temporary file and a rename).
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
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
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
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
