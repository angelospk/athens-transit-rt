package gtfs

import (
	"bufio"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Snapshot file: gzip( "ATRTSNAP" + version byte + gob(Feed) ). The magic and version let a
// server refuse a snapshot written by an incompatible build instead of misreading it.
const (
	snapshotMagic   = "ATRTSNAP"
	snapshotVersion = 1
)

func WriteSnapshot(w io.Writer, f *Feed) error {
	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(append([]byte(snapshotMagic), snapshotVersion)); err != nil {
		return err
	}
	if err := gob.NewEncoder(zw).Encode(f); err != nil {
		return err
	}
	return zw.Close()
}

func ReadSnapshot(r io.Reader) (*Feed, error) {
	zr, err := gzip.NewReader(bufio.NewReader(r))
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	defer zr.Close()
	head := make([]byte, len(snapshotMagic)+1)
	if _, err := io.ReadFull(zr, head); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	if string(head[:len(snapshotMagic)]) != snapshotMagic || head[len(snapshotMagic)] != snapshotVersion {
		return nil, fmt.Errorf("snapshot: unsupported format (want %s v%d)", snapshotMagic, snapshotVersion)
	}
	f := &Feed{}
	if err := gob.NewDecoder(zr).Decode(f); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	if f.Exceptions == nil {
		f.Exceptions = map[int64]int8{}
	}
	f.index()
	return f, nil
}

// Version is the publication date of the GTFS (its Last-Modified), as YYYY-MM-DD, falling
// back to the first calendar start date.
func (f *Feed) Version() string {
	if t, err := http.ParseTime(f.Meta.LastModified); err == nil {
		return t.In(Athens).Format(time.DateOnly)
	}
	if s := f.FeedStart(); !s.IsZero() {
		return s.Format(time.DateOnly)
	}
	return ""
}
