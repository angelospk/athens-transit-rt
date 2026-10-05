package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
)

func TestCheckDownloadsVerifiesAndCaches(t *testing.T) {
	f := gtfstest.StraightLine{Stops: 3, Trips: 2, First: 36000, Headway: 10, Leg: 5}.Feed(t)
	var snap bytes.Buffer
	if err := gtfs.WriteSnapshot(&snap, f); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(snap.Bytes())
	m := Manifest{GTFSVersion: "2026-07-08", SHA256: hex.EncodeToString(sum[:]), Size: int64(snap.Len()), Snapshot: "snapshot.bin"}
	corrupt := false
	manifestHits, snapHits := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			manifestHits++
			if r.Header.Get("If-None-Match") == `"m1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"m1"`)
			json.NewEncoder(w).Encode(m)
		case "/snapshot.bin":
			snapHits++
			b := snap.Bytes()
			if corrupt {
				b = bytes.Clone(b)
				b[len(b)/2] ^= 0xff
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := &Fetcher{BaseURL: srv.URL + "/", Dir: dir, HTTP: srv.Client()}
	ctx := context.Background()

	corrupt = true
	if _, _, err := r.Check(ctx); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
	corrupt = false
	r.manifestETag = "" // the failed attempt must not have remembered the manifest
	got, man, err := r.Check(ctx)
	if err != nil || got == nil || man.GTFSVersion != "2026-07-08" || len(got.Trips) != 2 {
		t.Fatalf("check: %v %v", err, man)
	}
	// Unchanged manifest (304): nothing new.
	if got, _, err := r.Check(ctx); err != nil || got != nil {
		t.Fatalf("second check %v %v", got, err)
	}
	if snapHits != 2 {
		t.Fatalf("snapshot downloads %d", snapHits)
	}
	// A new process finds the snapshot on disk without downloading.
	r2 := &Fetcher{BaseURL: srv.URL + "/", Dir: dir, HTTP: srv.Client()}
	local, lm, err := r2.LoadLocal()
	if err != nil || local == nil || lm.SHA256 != m.SHA256 {
		t.Fatalf("load local: %v", err)
	}
	if got, _, err := r2.Check(ctx); err != nil || got != nil {
		t.Fatalf("same sha re-downloaded: %v %v", got, err)
	}
	if snapHits != 2 {
		t.Fatalf("snapshot downloads %d", snapHits)
	}

	// A tampered local snapshot is rejected at startup (and then downloaded again).
	files, _ := filepath.Glob(filepath.Join(dir, "snapshot-*.bin"))
	if len(files) != 1 {
		t.Fatalf("local snapshot files %v", files)
	}
	b, _ := os.ReadFile(files[0])
	b[len(b)/2] ^= 0xff
	os.WriteFile(files[0], b, 0o644)
	r3 := &Fetcher{BaseURL: srv.URL + "/", Dir: dir, HTTP: srv.Client()}
	if f, _, err := r3.LoadLocal(); err == nil || f != nil {
		t.Fatal("tampered local snapshot loaded")
	}
	if got, _, err := r3.Check(ctx); err != nil || got == nil {
		t.Fatalf("re-download after tampering: %v %v", got, err)
	}
}
