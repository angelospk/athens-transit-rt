// Package history keeps every new GPS fix in hourly CSV files, gzipped when the hour ends, for
// later statistics (docs/superpowers/specs/2026-10-06-fix-history.md). Polling never waits for
// the disk: rows go through a buffered channel to one writer goroutine, and are dropped (and
// counted) when it is full.
package history

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	Header     = "fix_t,line,route_code,veh,lat,lon,gtfs,trip_id,shape_id,s_m,delay_s\n"
	hourLayout = "2006-01-02T15" // file names, UTC
	bufferRows = 4096
	flushEvery = 10 * time.Second
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

type Writer struct {
	dir      string
	maxBytes int64
	keep     time.Duration
	now      func() time.Time
	log      *slog.Logger
	ch       chan Row
	stop     chan struct{}
	done     chan struct{}
	dropped  atomic.Int64
	written  atomic.Int64

	last map[string]int64 // veh -> newest fix_t written (writer goroutine only)
	hour string
	f    *os.File
	bw   *bufio.Writer
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
	w.compressLeftovers("")
	w.prune()
	return w, nil
}

// Add queues a row without blocking; a full queue drops it.
func (w *Writer) Add(r Row) {
	select {
	case w.ch <- r:
	default:
		w.dropped.Add(1)
	}
}

func (w *Writer) Dropped() int64 { return w.dropped.Load() }
func (w *Writer) Written() int64 { return w.written.Load() }

// Close writes the queued rows and closes the current file (it is gzipped on the next start).
// Rows added later are dropped.
func (w *Writer) Close() {
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
			if w.f != nil { // idle hours make no files
				w.rotate()
			}
			if w.bw != nil {
				w.bw.Flush()
			}
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

func (w *Writer) write(r Row) {
	if r.FixT <= w.last[r.Veh] {
		return // OASA repeats a fix until the vehicle reports again
	}
	w.last[r.Veh] = r.FixT
	w.rotate()
	if w.bw == nil {
		return
	}
	delay := ""
	if r.DelayS != nil {
		delay = strconv.Itoa(*r.DelayS)
	}
	fmt.Fprintf(w.bw, "%d,%s,%s,%s,%d,%d,%s,%s,%s,%d,%s\n", r.FixT, csv(r.Line), csv(r.RouteCode), csv(r.Veh),
		int64(r.Lat*1e6+0.5*sign(r.Lat)), int64(r.Lon*1e6+0.5*sign(r.Lon)), csv(r.GTFS), csv(r.TripID), csv(r.ShapeID), r.SM, delay)
	w.written.Add(1)
}

// rotate opens the file of the current hour; when the hour changed, the old file is gzipped
// and old files are pruned.
func (w *Writer) rotate() {
	hour := w.now().UTC().Format(hourLayout)
	if hour == w.hour && w.f != nil {
		return
	}
	if w.f != nil {
		w.closeFile()
		w.compressLeftovers(hour)
		w.prune()
	}
	w.hour = hour
	path := filepath.Join(w.dir, hour+".csv")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		w.log.Warn("history: open", "err", err)
		return
	}
	w.f, w.bw = f, bufio.NewWriterSize(f, 64<<10)
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		w.bw.WriteString(Header)
	}
}

func (w *Writer) closeFile() {
	if w.f == nil {
		return
	}
	if err := w.bw.Flush(); err != nil {
		w.log.Warn("history: write", "err", err)
	}
	w.f.Close()
	w.f, w.bw = nil, nil
}

// compressLeftovers gzips every plain .csv except the current hour's (crash or rotation).
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

// prune deletes gzipped hours older than keep, then the oldest while over maxBytes.
func (w *Writer) prune() {
	names, _ := filepath.Glob(filepath.Join(w.dir, "*.csv.gz"))
	sort.Strings(names) // hour names sort by time
	cutoff := w.now().Add(-w.keep)
	type file struct {
		path string
		size int64
	}
	var kept []file
	var total int64
	for _, p := range names {
		t, err := time.Parse(hourLayout, strings.TrimSuffix(filepath.Base(p), ".csv.gz"))
		if err == nil && t.Add(time.Hour).Before(cutoff) {
			os.Remove(p)
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		kept = append(kept, file{p, st.Size()})
		total += st.Size()
	}
	for i := 0; total > w.maxBytes && i < len(kept); i++ {
		os.Remove(kept[i].path)
		total -= kept[i].size
	}
}

// csv keeps a field on one line and out of the column separators (ids never have them; a
// broken feed should not break the file).
func csv(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ',' || r == '\n' || r == '\r' || r == '"' {
			return ' '
		}
		return r
	}, s)
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
