// Package release moves the preprocessed GTFS snapshot from the GitHub Actions pipeline to
// the server: a manifest plus the snapshot, both assets of one rolling GitHub release.
package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

const (
	DefaultBaseURL = "https://github.com/angelospk/athens-transit-rt/releases/download/gtfs-snapshot/"
	ManifestName   = "manifest.json"
	SnapshotName   = "snapshot.bin"
	maxSnapshot    = 64 << 20
)

// Manifest describes the published snapshot. GTFS fields identify the data.gov.gr file it
// was built from, so the pipeline can tell whether a new download is needed.
type Manifest struct {
	GTFSVersion  string `json:"gtfs_version"`
	GTFSExpires  string `json:"gtfs_expires"`
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
	GTFSSize     int64  `json:"gtfs_size"`
	Snapshot     string `json:"snapshot"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	BuiltAt      int64  `json:"built_at"`
}

// Fetcher keeps the newest snapshot in Dir.
type Fetcher struct {
	BaseURL string
	Dir     string
	HTTP    *http.Client

	manifestETag string
	current      string // sha256 of the snapshot in use
}

// LoadLocal reads the snapshot saved by an earlier run, if any (nil, nil, nil when none).
func (r *Fetcher) LoadLocal() (*gtfs.Feed, *Manifest, error) {
	b, err := os.ReadFile(filepath.Join(r.Dir, ManifestName))
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(filepath.Join(r.Dir, localName(m)))
	if err != nil {
		return nil, nil, err
	}
	if err := verify(data, m); err != nil {
		return nil, nil, fmt.Errorf("local %w", err)
	}
	f, err := gtfs.ReadSnapshot(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	r.current = m.SHA256
	return f, &m, nil
}

// Check fetches the manifest and, when it names a snapshot other than the one in use,
// downloads, verifies and stores it. It returns (nil, nil, nil) when nothing changed.
func (r *Fetcher) Check(ctx context.Context) (*gtfs.Feed, *Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+ManifestName, nil)
	if err != nil {
		return nil, nil, err
	}
	if r.manifestETag != "" {
		req.Header.Set("If-None-Match", r.manifestETag)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("manifest: HTTP %d", resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, nil, fmt.Errorf("manifest: %w", err)
	}
	etag := resp.Header.Get("ETag")
	if m.SHA256 == r.current {
		r.manifestETag = etag
		return nil, nil, nil
	}
	data, err := r.download(ctx, m)
	if err != nil {
		return nil, nil, err
	}
	f, err := gtfs.ReadSnapshot(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	if err := r.save(data, m); err != nil {
		return nil, nil, err
	}
	r.current, r.manifestETag = m.SHA256, etag
	return f, &m, nil
}

func (r *Fetcher) download(ctx context.Context, m Manifest) ([]byte, error) {
	name := m.Snapshot
	if name == "" {
		name = SnapshotName
	}
	if filepath.Base(name) != name {
		return nil, fmt.Errorf("snapshot: bad asset name %q", name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSnapshot))
	if err != nil {
		return nil, err
	}
	// The manifest may be uploaded before the snapshot finished replacing; retry later.
	if err := verify(data, m); err != nil {
		return nil, err
	}
	return data, nil
}

func verify(data []byte, m Manifest) error {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != m.SHA256 || int64(len(data)) != m.Size {
		return fmt.Errorf("snapshot: checksum mismatch")
	}
	return nil
}

// localName: each snapshot generation has its own file, so the manifest (written last,
// atomically) always names a complete file; a crash leaves the old pair in place.
func localName(m Manifest) string {
	if len(m.SHA256) < 16 {
		return SnapshotName
	}
	return "snapshot-" + m.SHA256[:16] + ".bin"
}

func (r *Fetcher) save(data []byte, m Manifest) error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	keep := localName(m)
	if err := writeAtomic(filepath.Join(r.Dir, keep), data); err != nil {
		return err
	}
	b, _ := json.Marshal(m)
	if err := writeAtomic(filepath.Join(r.Dir, ManifestName), b); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(r.Dir, "snapshot*.bin"))
	for _, p := range old {
		if filepath.Base(p) != keep {
			os.Remove(p)
		}
	}
	return nil
}

// writeAtomic writes via a synced temp file and a rename.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Build writes the snapshot and its manifest for a feed into dir (used by the pipeline).
func Build(f *gtfs.Feed, dir string) (*Manifest, error) {
	var buf bytes.Buffer
	if err := gtfs.WriteSnapshot(&buf, f); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(buf.Bytes())
	m := &Manifest{
		GTFSVersion: f.Version(), ETag: f.Meta.ETag, LastModified: f.Meta.LastModified, GTFSSize: f.Meta.Size,
		Snapshot: SnapshotName, SHA256: hex.EncodeToString(sum[:]), Size: int64(buf.Len()), BuiltAt: f.Meta.BuiltAt,
	}
	if end := f.FeedEnd(); !end.IsZero() {
		m.GTFSExpires = end.Format("2006-01-02")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(dir, SnapshotName), buf.Bytes()); err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(m, "", " ")
	return m, writeAtomic(filepath.Join(dir, ManifestName), b)
}
