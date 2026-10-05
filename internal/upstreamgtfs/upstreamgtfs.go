// Package upstreamgtfs checks and downloads OASA's static GTFS from data.gov.gr.
package upstreamgtfs

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

// URL is the official OSY (bus/trolley) feed. It redirects to a signed blob URL; the version
// headers are those of the final response.
const URL = "https://data.gov.gr/dataset/fb049bb1-aea6-4443-95fa-8b941dd6a057/resource/" +
	"119db488-16ea-4c76-b560-41c472872390/download/osy_gtfs.zip"

const userAgent = "athens-transit-rt/1.0 (+https://github.com/angelospk/athens-transit-rt)"

type Version struct {
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
	Size         int64  `json:"size"`
}

// Same reports whether two versions describe the same published file.
func (v Version) Same(o Version) bool {
	if v.ETag != "" && o.ETag != "" {
		return v.ETag == o.ETag
	}
	return v.LastModified == o.LastModified && v.Size == o.Size
}

func Head(ctx context.Context, client *http.Client, url string) (Version, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return Version{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return Version{}, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Version{}, fmt.Errorf("HEAD %s: HTTP %d", url, resp.StatusCode)
	}
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return Version{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), Size: size}, nil
}

// Download saves the feed to path, refusing files that are not a usable GTFS zip.
func Download(ctx context.Context, client *http.Client, url, path string) (Version, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Version{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return Version{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Version{}, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return Version{}, err
	}
	n, err := io.Copy(out, resp.Body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = validate(tmp)
	}
	if err != nil {
		os.Remove(tmp)
		return Version{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return Version{}, err
	}
	return Version{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), Size: n}, nil
}

func validate(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("downloaded GTFS is not a zip: %w", err)
	}
	defer zr.Close()
	have := map[string]bool{}
	for _, f := range zr.File {
		have[f.Name] = true
	}
	for _, name := range gtfs.RequiredFiles {
		if !have[name] {
			return fmt.Errorf("downloaded GTFS is unusable (missing %s)", name)
		}
	}
	return nil
}
