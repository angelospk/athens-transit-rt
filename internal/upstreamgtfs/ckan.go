package upstreamgtfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

// Slice is one dated GTFS zip of a CKAN dataset. From and To are YYYY-MM-DD; To is inclusive.
type Slice struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	From string `json:"from"`
	To   string `json:"to"`
}

// sliceName is the resource name of a dated slice: "GTFS dd/mm/yyyy-dd/mm/yyyy".
var sliceName = regexp.MustCompile(`^GTFS (\d{2})/(\d{2})/(\d{4})-(\d{2})/(\d{2})/(\d{4})$`)

const maxCKANBody = 8 << 20

// ckanPackage is the part of a package_show answer we use.
type ckanPackage struct {
	Success bool `json:"success"`
	Result  struct {
		Resources []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"resources"`
	} `json:"result"`
}

// ParseCKAN returns the dated slices of a package_show answer: those whose name matches and
// that have a url. Other resources (the "all data" zip) are dropped.
func ParseCKAN(body []byte) ([]Slice, error) {
	var p ckanPackage
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("ckan package: %w", err)
	}
	if !p.Success {
		return nil, errors.New("ckan package: success is false")
	}
	var ss []Slice
	for _, r := range p.Result.Resources {
		m := sliceName.FindStringSubmatch(strings.TrimSpace(r.Name))
		if m == nil || r.URL == "" {
			continue
		}
		ss = append(ss, Slice{ID: r.ID, URL: r.URL,
			From: fmt.Sprintf("%s-%s-%s", m[3], m[2], m[1]),
			To:   fmt.Sprintf("%s-%s-%s", m[6], m[5], m[4])})
	}
	return ss, nil
}

// PickSlice returns the slice that covers today in Athens: the newest of those that do, or,
// in a gap, the newest one that already started. ok is false before the first slice.
func PickSlice(ss []Slice, now time.Time) (Slice, bool) {
	today := now.In(gtfs.Athens).Format(time.DateOnly)
	var best Slice
	ok := false
	for _, s := range ss {
		if s.From <= today && today <= s.To {
			if !ok || s.From > best.From || (s.From == best.From && s.To > best.To) {
				best, ok = s, true
			}
		}
	}
	if ok {
		return best, true
	}
	for _, s := range ss {
		if s.From <= today {
			if !ok || s.From > best.From || (s.From == best.From && s.To > best.To) {
				best, ok = s, true
			}
		}
	}
	return best, ok
}

// FetchCKAN reads the package_show answer at url.
func FetchCKAN(ctx context.Context, client *http.Client, url string) ([]Slice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCKANBody))
	if err != nil {
		return nil, err
	}
	return ParseCKAN(body)
}
