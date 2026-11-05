package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/release"
	"github.com/angelospk/athens-transit-rt/internal/upstreamgtfs"
)

const (
	ckanZipName  = "gtfs-ckan.zip"  // the feed of the slice in use
	ckanJSONName = "gtfs-ckan.json" // the slice it came from
)

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
	zipPath := filepath.Join(a.cfg.StateDir, ckanZipName)
	jsonPath := filepath.Join(a.cfg.StateDir, ckanJSONName)
	if old, err := readCKANSlice(jsonPath); err == nil && old.ID == slice.ID && a.world() != nil {
		return nil
	}
	// The new zip replaces the kept one only after it loads, so a bad file never does.
	newPath := zipPath + ".new"
	defer os.Remove(newPath)
	if _, err := upstreamgtfs.Download(ctx, client, slice.URL, newPath); err != nil {
		return err
	}
	f, err := gtfs.LoadZip(newPath, gtfs.LoadOptions{})
	if err != nil {
		return err
	}
	if err := os.Rename(newPath, zipPath); err != nil {
		return err
	}
	if err := writeCKANSlice(jsonPath, slice); err != nil {
		return err
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
	f, err := gtfs.LoadZip(filepath.Join(a.cfg.StateDir, ckanZipName), gtfs.LoadOptions{})
	if err != nil {
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
