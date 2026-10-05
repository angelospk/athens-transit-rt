package gtfs

import (
	"bytes"
	"compress/gzip"
	"reflect"
	"testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
	f := load(t)
	f.Meta = Meta{ETag: `"x"`, LastModified: "Wed, 08 Jul 2026 08:26:04 GMT", Size: 42, BuiltAt: 1}
	var buf bytes.Buffer
	if err := WriteSnapshot(&buf, f); err != nil {
		t.Fatal(err)
	}
	g, err := ReadSnapshot(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Trips, g.Trips) || !reflect.DeepEqual(f.Meta, g.Meta) ||
		!reflect.DeepEqual(f.Exceptions, g.Exceptions) || !reflect.DeepEqual(f.Profiles, g.Profiles) {
		t.Fatal("snapshot changed the feed")
	}
	if g.TripIndex("t2") != f.TripIndex("t2") || len(g.TripsForLine("040")) != 2 {
		t.Fatal("indexes not rebuilt")
	}
}

func TestSnapshotRejectsGarbage(t *testing.T) {
	if _, err := ReadSnapshot(bytes.NewReader([]byte("not gzip"))); err == nil {
		t.Fatal("garbage accepted")
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte("ATRTSNAP\x09rest"))
	zw.Close()
	if _, err := ReadSnapshot(&buf); err == nil {
		t.Fatal("wrong version accepted")
	}
}

func TestVersionAndExpiry(t *testing.T) {
	f := load(t)
	f.Meta.LastModified = "Wed, 08 Jul 2026 08:26:04 GMT"
	if v := f.Version(); v != "2026-07-08" {
		t.Fatalf("version %q", v)
	}
	f.Meta.LastModified = ""
	if v := f.Version(); v != "2026-07-06" {
		t.Fatalf("fallback version %q", v)
	}
}
