// Package telematics is a client for the (undocumented) OASA Telematics API.
//
// Endpoints are described in the community docs:
// https://oasa-telematics-api.readthedocs.io/en/latest/
package telematics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

const (
	BaseURL   = "https://telematics.oasa.gr/api/"
	UserAgent = "athens-transit-rt/1.0 (+https://github.com/angelospk/athens-transit-rt)"
)

var months = map[string]time.Month{"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
	"Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12}

// e.g. "Oct  4 2026 10:15:38:000PM"
var csDate = regexp.MustCompile(`^(\w{3})\s+(\d{1,2})\s+(\d{4})\s+(\d{1,2}):(\d{2}):(\d{2}):\d+\s*([AP]M)`)

// ParseCSDate parses the vehicle timestamp format of getBusLocation (Athens local time).
func ParseCSDate(v string) (time.Time, error) {
	m := csDate.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return time.Time{}, fmt.Errorf("unrecognised CS_DATE %q", v)
	}
	mon, ok := months[m[1]]
	if !ok {
		return time.Time{}, fmt.Errorf("unrecognised CS_DATE %q", v)
	}
	n := func(s string) int { i, _ := strconv.Atoi(s); return i }
	hour := n(m[4]) % 12
	if m[7] == "PM" {
		hour += 12
	}
	return time.Date(n(m[3]), mon, n(m[2]), hour, n(m[5]), n(m[6]), 0, gtfs.Athens), nil
}

// FormatCSDate is the inverse of ParseCSDate (used by replay to feed recorded fixes back).
func FormatCSDate(t time.Time) string {
	return t.In(gtfs.Athens).Format("Jan 02 2006 03:04:05:000PM")
}

// str decodes a JSON string or number into a string (the API is not consistent).
type str string

func (s *str) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = str(v)
		return nil
	}
	*s = str(b)
	return nil
}

type Line struct {
	LineCode, LineID, LineDescr, LineDescrEng string
}

type Route struct {
	RouteCode, LineCode, RouteDescr, RouteDescrEng, RouteType string
}

type RouteStop struct {
	StopCode, StopDescr string
	StopLat, StopLng    float64
	Order               int
}

// Vehicle is one getBusLocation row.
type Vehicle struct {
	VehNo     string
	RouteCode string
	Lat, Lon  float64
	Heading   float64 // 0 often means unknown
	Time      time.Time
	TimeErr   error // set when CS_DATE could not be parsed
}

type Arrival struct {
	RouteCode, VehCode string
	Minutes            int
}

// Client calls the API. With a non-nil Pacer every call waits for its slot first.
type Client struct {
	BaseURL  string
	HTTP     *http.Client
	Pacer    *Pacer
	Log      RequestLog    // every request, per second
	MinGap   time.Duration // never send two requests closer than this (0 = no limit)
	gapMu    chan struct{} // 1-slot lock that a waiter can abandon on cancel
	lastAt   time.Time     // last send, guarded by gapMu
	requests atomic.Int64
	errors   atomic.Int64
}

func New(baseURL string, pacer *Pacer) *Client {
	if baseURL == "" {
		baseURL = BaseURL
	}
	// Redirects are not followed: each request slot must be exactly one request.
	hc := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	c := &Client{BaseURL: baseURL, HTTP: hc, Pacer: pacer, gapMu: make(chan struct{}, 1)}
	c.SetTransport(http.DefaultTransport)
	if pacer != nil {
		c.MinGap = pacer.BaseInterval()
	}
	return c
}

// Requests counts calls made since the client was created.
func (c *Client) Requests() int64 { return c.requests.Load() }

// SetTransport sets the round tripper that sends requests (e.g. one with a proxy). The
// client wraps it: MinGap and the request log apply right before each send.
func (c *Client) SetTransport(rt http.RoundTripper) { c.HTTP.Transport = &gapTransport{c: c, next: rt} }

// gapTransport is the hard guarantee behind the request budget: callers are paced already,
// this absorbs the jitter between a pacer grant and the actual send.
type gapTransport struct {
	c    *Client
	next http.RoundTripper
}

func (g *gapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c, ctx := g.c, req.Context()
	select {
	case c.gapMu <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if wait := time.Until(c.lastAt.Add(c.MinGap)); c.MinGap > 0 && wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			<-c.gapMu
			return nil, ctx.Err()
		}
	}
	c.lastAt = time.Now()
	c.Log.Add(c.lastAt)
	<-c.gapMu
	return g.next.RoundTrip(req)
}

// Errors counts failed calls (transport, HTTP status, error answers, undecodable bodies).
func (c *Client) Errors() int64 { return c.errors.Load() }

// Call performs one request and decodes the JSON answer into out. "No data" answers (empty
// body, "" or null) leave out untouched.
func (c *Client) Call(ctx context.Context, out any, act string, params ...string) error {
	if c.Pacer != nil {
		if err := c.Pacer.Wait(ctx); err != nil {
			return err
		}
	}
	q := url.Values{"act": {act}}
	for i, p := range params {
		q.Set(fmt.Sprintf("p%d", i+1), p)
	}
	start := time.Now()
	err := c.do(ctx, out, act, c.BaseURL+"?"+q.Encode())
	if err != nil {
		c.errors.Add(1)
	}
	if c.Pacer != nil {
		c.Pacer.Report(time.Since(start), err)
	}
	return err
}

func (c *Client) do(ctx context.Context, out any, act, u string) error {
	c.requests.Add(1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", act, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%s: %w", act, err)
	}
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Act: act, Status: resp.StatusCode}
	}
	b := strings.TrimSpace(string(body))
	if b == "" || b == `""` || b == "null" {
		return nil
	}
	if strings.HasPrefix(b, "{") {
		var e struct {
			Error *string `json:"error"`
		}
		if json.Unmarshal([]byte(b), &e) == nil && e.Error != nil {
			return fmt.Errorf("%s: %s", act, *e.Error)
		}
	}
	if err := json.Unmarshal([]byte(b), out); err != nil {
		return fmt.Errorf("%s: %w", act, err)
	}
	return nil
}

type HTTPError struct {
	Act    string
	Status int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.Act, e.Status) }

func (c *Client) Lines(ctx context.Context) ([]Line, error) {
	var raw []struct{ LineCode, LineID, LineDescr, LineDescrEng str }
	if err := c.Call(ctx, &raw, "webGetLines"); err != nil {
		return nil, err
	}
	out := make([]Line, len(raw))
	for i, r := range raw {
		out[i] = Line{string(r.LineCode), string(r.LineID), string(r.LineDescr), string(r.LineDescrEng)}
	}
	return out, nil
}

func (c *Client) Routes(ctx context.Context, lineCode string) ([]Route, error) {
	var raw []struct{ RouteCode, LineCode, RouteDescr, RouteDescrEng, RouteType str }
	if err := c.Call(ctx, &raw, "webGetRoutes", lineCode); err != nil {
		return nil, err
	}
	out := make([]Route, len(raw))
	for i, r := range raw {
		out[i] = Route{string(r.RouteCode), string(r.LineCode), string(r.RouteDescr), string(r.RouteDescrEng), string(r.RouteType)}
	}
	return out, nil
}

func (c *Client) Stops(ctx context.Context, routeCode string) ([]RouteStop, error) {
	var raw []struct{ StopCode, StopDescr, StopLat, StopLng, RouteStopOrder str }
	if err := c.Call(ctx, &raw, "webGetStops", routeCode); err != nil {
		return nil, err
	}
	out := make([]RouteStop, len(raw))
	for i, r := range raw {
		lat, _ := strconv.ParseFloat(string(r.StopLat), 64)
		lon, _ := strconv.ParseFloat(string(r.StopLng), 64)
		order, _ := strconv.Atoi(string(r.RouteStopOrder))
		out[i] = RouteStop{string(r.StopCode), string(r.StopDescr), lat, lon, order}
	}
	return out, nil
}

func (c *Client) BusLocations(ctx context.Context, routeCode string) ([]Vehicle, error) {
	var raw []struct {
		VehNo   str `json:"VEH_NO"`
		CSDate  str `json:"CS_DATE"`
		Lat     str `json:"CS_LAT"`
		Lon     str `json:"CS_LNG"`
		Route   str `json:"ROUTE_CODE"`
		Heading str `json:"VEH_HEADING"`
	}
	if err := c.Call(ctx, &raw, "getBusLocation", routeCode); err != nil {
		return nil, err
	}
	out := make([]Vehicle, 0, len(raw))
	for _, r := range raw {
		lat, err1 := strconv.ParseFloat(string(r.Lat), 64)
		lon, err2 := strconv.ParseFloat(string(r.Lon), 64)
		if err1 != nil || err2 != nil || !inGreece(lat, lon) || r.VehNo == "" || r.Route == "" {
			continue // also drops 0,0 and NaN
		}
		heading, err := strconv.ParseFloat(string(r.Heading), 64)
		if err != nil || math.IsNaN(heading) || math.IsInf(heading, 0) {
			heading = 0 // unknown
		}
		ts, err := ParseCSDate(string(r.CSDate))
		out = append(out, Vehicle{VehNo: string(r.VehNo), RouteCode: string(r.Route), Lat: lat, Lon: lon,
			Heading: heading, Time: ts, TimeErr: err})
	}
	return out, nil
}

func (c *Client) StopArrivals(ctx context.Context, stopCode string) ([]Arrival, error) {
	var raw []struct {
		Route str `json:"route_code"`
		Veh   str `json:"veh_code"`
		Mins  str `json:"btime2"`
	}
	if err := c.Call(ctx, &raw, "getStopArrivals", stopCode); err != nil {
		return nil, err
	}
	out := make([]Arrival, 0, len(raw))
	for _, r := range raw {
		m, err := strconv.Atoi(string(r.Mins))
		if err != nil {
			continue
		}
		out = append(out, Arrival{string(r.Route), string(r.Veh), m})
	}
	return out, nil
}

// inGreece is a loose bounding box; OASA sends 0,0 (and worse) for vehicles without a fix.
func inGreece(lat, lon float64) bool {
	return lat >= 34 && lat <= 42 && lon >= 19 && lon <= 30
}
