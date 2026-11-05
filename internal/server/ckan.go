package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/release"
	"github.com/angelospk/athens-transit-rt/internal/upstreamgtfs"
)

// ckanJSONName holds the slice in use. Its feed is gtfs-ckan-<slice id>.zip, so writing this
// file is the commit: a crash before it leaves the old slice and its zip in use.
const ckanJSONName = "gtfs-ckan.json"

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9-]`)

func ckanZipPath(dir string, s upstreamgtfs.Slice) string {
	return filepath.Join(dir, "gtfs-ckan-"+unsafeID.ReplaceAllString(s.ID, "_")+".zip")
}

// ckanURL is the CKAN package_show URL of the city's GTFS.
func (a *App) ckanURL() string {
	if a.cfg.GTFSCKANURL != "" {
		return a.cfg.GTFSCKANURL
	}
	return a.city.GTFSCKANURL
}

// ckanMode: the static GTFS comes from a CKAN slice, not from the release snapshot.
func (a *App) ckanMode() bool { return a.cfg.GTFSZip == "" && a.ckanURL() != "" }

// checkCKAN keeps the state dir on the slice that covers today. The old feed stays on any error.
func (a *App) checkCKAN(ctx context.Context) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	ss, err := upstreamgtfs.FetchCKAN(ctx, client, a.ckanURL())
	if err != nil {
		return err
	}
	slice, ok := upstreamgtfs.PickSlice(ss, a.now())
	if !ok {
		return errors.New("ckan: no GTFS slice has started yet")
	}
	jsonPath := filepath.Join(a.cfg.StateDir, ckanJSONName)
	if old, err := readCKANSlice(jsonPath); err == nil && old.ID == slice.ID && a.world() != nil {
		return nil
	}
	if err := os.MkdirAll(a.cfg.StateDir, 0o755); err != nil {
		return err
	}
	zipPath := ckanZipPath(a.cfg.StateDir, slice)
	// Download validates the zip and renames it into place only then.
	if _, err := upstreamgtfs.Download(ctx, client, slice.URL, zipPath); err != nil {
		return err
	}
	f, err := gtfs.LoadZip(zipPath, gtfs.LoadOptions{})
	if err == nil && len(f.Trips) == 0 {
		err = errors.New("ckan: GTFS slice has no trips")
	}
	if err != nil {
		os.Remove(zipPath)
		return err
	}
	if err := writeCKANSlice(jsonPath, slice); err != nil {
		return err
	}
	// The other slices' zips are not needed any more.
	old, _ := filepath.Glob(filepath.Join(a.cfg.StateDir, "gtfs-ckan-*.zip"))
	for _, p := range old {
		if p != zipPath {
			os.Remove(p)
		}
	}
	a.SetFeed(f, &release.Manifest{GTFSVersion: slice.From, GTFSExpires: slice.To})
	a.log.Info("GTFS slice loaded", "id", slice.ID, "from", slice.From, "to", slice.To)
	return nil
}

// loadCKANLocal loads the feed kept by an earlier run, if any.
func (a *App) loadCKANLocal() bool {
	s, err := readCKANSlice(filepath.Join(a.cfg.StateDir, ckanJSONName))
	if err != nil {
		return false
	}
	f, err := gtfs.LoadZip(ckanZipPath(a.cfg.StateDir, s), gtfs.LoadOptions{})
	if err != nil || len(f.Trips) == 0 {
		return false
	}
	a.SetFeed(f, &release.Manifest{GTFSVersion: s.From, GTFSExpires: s.To})
	return true
}

func readCKANSlice(path string) (upstreamgtfs.Slice, error) {
	var s upstreamgtfs.Slice
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// writeCKANSlice writes via a temp file and a rename, so a crash leaves the old slice in place.
func writeCKANSlice(path string, s upstreamgtfs.Slice) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
