package upstreamgtfs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelospk/athens-transit-rt/internal/gtfs/gtfstest"
)

func TestHeadFollowsRedirectAndDownloadValidates(t *testing.T) {
	zipPath := gtfstest.Zip(t, gtfstest.StraightLine{Stops: 2, Trips: 1, First: 36000, Headway: 10, Leg: 5}.Files())
	good, _ := os.ReadFile(zipPath)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download":
			http.Redirect(w, r, "/blob", http.StatusFound)
		case "/blob":
			w.Header().Set("ETag", `"0xABC"`)
			w.Header().Set("Last-Modified", "Wed, 08 Jul 2026 08:26:04 GMT")
			w.Write(good)
		case "/bad":
			w.Write([]byte("<html>maintenance</html>"))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	v, err := Head(ctx, srv.Client(), srv.URL+"/download")
	if err != nil || v.ETag != `"0xABC"` || v.Size != int64(len(good)) {
		t.Fatalf("head %+v %v", v, err)
	}
	dst := filepath.Join(t.TempDir(), "g.zip")
	got, err := Download(ctx, srv.Client(), srv.URL+"/download", dst)
	if err != nil || !got.Same(v) {
		t.Fatalf("download %+v %v", got, err)
	}
	if _, err := Download(ctx, srv.Client(), srv.URL+"/bad", dst+"2"); err == nil {
		t.Fatal("html accepted as GTFS")
	}
	if _, err := os.Stat(dst + "2"); !os.IsNotExist(err) {
		t.Fatal("bad download left a file")
	}
}

func TestSame(t *testing.T) {
	a := Version{ETag: `"1"`, LastModified: "x", Size: 1}
	if !a.Same(Version{ETag: `"1"`, LastModified: "y", Size: 2}) || a.Same(Version{ETag: `"2"`}) {
		t.Fatal("etag comparison")
	}
	if !(Version{LastModified: "x", Size: 1}).Same(Version{LastModified: "x", Size: 1}) {
		t.Fatal("fallback comparison")
	}
}
