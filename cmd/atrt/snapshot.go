package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
	"github.com/angelospk/athens-transit-rt/internal/release"
	"github.com/angelospk/athens-transit-rt/internal/staticsite"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
	"github.com/angelospk/athens-transit-rt/internal/upstreamgtfs"
)

// snapshot builds the release assets (snapshot + manifest) and the static map data.
func snapshot(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	gtfsZip := fs.String("gtfs", "", "GTFS zip to use (default: download from data.gov.gr into --work)")
	work := fs.String("work", "work", "scratch directory for the download")
	out := fs.String("out", "dist", "directory for snapshot.bin and manifest.json")
	site := fs.String("site", "site", "directory for the static map data (static/v1/...)")
	ifChanged := fs.Bool("if-changed", false, "do nothing when the published GTFS equals the one in --manifest-url")
	manifestURL := fs.String("manifest-url", release.DefaultBaseURL+release.ManifestName, "current manifest, for --if-changed")
	names := fs.Bool("names", true, "fetch English line names from OASA telematics (1 request)")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Minute}
	setOutput := func(changed bool) {
		if p := os.Getenv("GITHUB_OUTPUT"); p != "" {
			f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
			if err == nil {
				fmt.Fprintf(f, "changed=%v\n", changed)
				f.Close()
			}
		}
	}

	var v upstreamgtfs.Version
	path := *gtfsZip
	if path == "" {
		var err error
		if v, err = upstreamgtfs.Head(ctx, client, upstreamgtfs.URL); err != nil {
			return err
		}
		if *ifChanged {
			if cur, err := currentManifest(ctx, client, *manifestURL); err != nil {
				fmt.Fprintln(os.Stderr, "no usable current manifest, building:", err)
			} else if v.Same(upstreamgtfs.Version{ETag: cur.ETag, LastModified: cur.LastModified, Size: cur.GTFSSize}) {
				fmt.Println("GTFS unchanged:", v.ETag, v.LastModified)
				setOutput(false)
				return nil
			}
		}
		if err := os.MkdirAll(*work, 0o755); err != nil {
			return err
		}
		path = filepath.Join(*work, "osy_gtfs.zip")
		if v, err = upstreamgtfs.Download(ctx, client, upstreamgtfs.URL, path); err != nil {
			return err
		}
	}
	start := time.Now()
	f, err := gtfs.LoadZip(path, gtfs.LoadOptions{Meta: gtfs.Meta{ETag: v.ETag, LastModified: v.LastModified,
		Size: v.Size, BuiltAt: time.Now().Unix()}})
	if err != nil {
		return err
	}
	m, err := release.Build(f, *out)
	if err != nil {
		return err
	}
	var tel []telematics.Line
	if *names {
		if tel, err = telematics.New("", nil).Lines(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "English line names unavailable:", err)
		}
	}
	if err := staticsite.Write(f, tel, *site); err != nil {
		return err
	}
	fmt.Printf("built GTFS %s (expires %s): %d trips, %d lines, snapshot %d bytes, in %s\n",
		m.GTFSVersion, m.GTFSExpires, len(f.Trips), len(f.Lines()), m.Size, time.Since(start).Round(time.Millisecond))
	setOutput(true)
	return nil
}

func currentManifest(ctx context.Context, client *http.Client, url string) (*release.Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var m release.Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}
