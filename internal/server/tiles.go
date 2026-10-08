package server

import (
	"compress/gzip"
	"math"
	"net/http"
	"strconv"
)

// Vehicle tiles (docs/CONTRACT.md rev 5): /v1/vehicles cut into XYZ (Web Mercator) tiles at two
// zooms, so a map asks only for what it shows and every client shares the same URLs.
const (
	tileOverviewZ = 9  // no path fields
	tileDetailZ   = 13 // the /v1/vehicles entries
)

// tileBox is the area tiles exist for: Attica with a margin. Fixed, so the set of URLs (and the
// load on the origin behind the cache) does not grow with clients.
var tileBox = struct{ south, west, north, east float64 }{37.5, 22.9, 38.5, 24.5}

type tileKey struct{ z, x, y int }

// liteVehicle is a /v1/vehicles entry without path, path_beyond and path_stops.
type liteVehicle struct {
	Line       string   `json:"line"`
	ID         string   `json:"id"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
	Bearing    *float64 `json:"bearing"`
	PositionAt int64    `json:"position_at"`
	Variant    *string  `json:"variant"`
	DelayS     *int     `json:"delay_s"`
	Speed      *float64 `json:"speed"`
}

type liteResponse struct {
	UpdatedAt    int64         `json:"updated_at"`
	NextUpdateAt int64         `json:"next_update_at"`
	Vehicles     []liteVehicle `json:"vehicles"`
}

// tileOf is the XYZ tile at zoom z that holds lat/lon.
func tileOf(lat, lon float64, z int) (x, y int) {
	n := float64(int(1) << z)
	r := lat * math.Pi / 180
	return int(math.Floor((lon + 180) / 360 * n)), int(math.Floor((1 - math.Asinh(math.Tan(r))/math.Pi) / 2 * n))
}

// tileRange is the inclusive range of tiles at zoom z that touch tileBox.
func tileRange(z int) (x0, x1, y0, y1 int) {
	x0, y0 = tileOf(tileBox.north, tileBox.west, z)
	x1, y1 = tileOf(tileBox.south, tileBox.east, z)
	return x0, x1, y0, y1
}

func inTileRange(k tileKey) bool {
	x0, x1, y0, y1 := tileRange(k.z)
	return k.x >= x0 && k.x <= x1 && k.y >= y0 && k.y <= y1
}

// buildTiles encodes every non-empty tile of both zooms and the shared empty body. A vehicle is
// in the one tile of its position (its paths may run on into others), when its z13 tile is in
// the domain (its z9 parent then is too); otherwise in none. The vehicles keep their /v1/vehicles order.
func (s *vehiclesSnapshot) buildTiles(zw *gzip.Writer, resp VehiclesResponse) error {
	detail := map[tileKey][]CityVehicle{}
	overview := map[tileKey][]liteVehicle{}
	for _, v := range resp.Vehicles {
		k := tileKey{z: tileDetailZ}
		if k.x, k.y = tileOf(v.Lat, v.Lon, k.z); !inTileRange(k) {
			continue
		}
		detail[k] = append(detail[k], v)
		k = tileKey{z: tileOverviewZ}
		k.x, k.y = tileOf(v.Lat, v.Lon, k.z)
		overview[k] = append(overview[k], liteVehicle{Line: v.Line, ID: v.ID, Lat: v.Lat, Lon: v.Lon,
			Bearing: v.Bearing, PositionAt: v.PositionAt, Variant: v.Variant, DelayS: v.DelayS, Speed: v.Speed})
	}
	s.tiles = make(map[tileKey]*encoded, len(detail)+len(overview))
	var err error
	for k, vs := range detail {
		if s.tiles[k], err = encode(zw, VehiclesResponse{resp.UpdatedAt, resp.NextUpdateAt, vs}); err != nil {
			return err
		}
	}
	for k, vs := range overview {
		if s.tiles[k], err = encode(zw, liteResponse{resp.UpdatedAt, resp.NextUpdateAt, vs}); err != nil {
			return err
		}
	}
	s.empty, err = encode(zw, VehiclesResponse{resp.UpdatedAt, resp.NextUpdateAt, []CityVehicle{}})
	return err
}

// parseTileNum accepts only the canonical decimal form, so each tile has one URL (one cache key).
func parseTileNum(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && strconv.Itoa(n) == s
}

// handleTile serves one tile of the current snapshot like /v1/vehicles (prebuilt bytes, same
// cache headers). Only canonical URLs are served: anything else would be another cache key.
func (a *App) handleTile(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"query_not_allowed"}`))
		return
	}
	z, okZ := parseTileNum(r.PathValue("z"))
	x, okX := parseTileNum(r.PathValue("x"))
	y, okY := parseTileNum(r.PathValue("y"))
	k := tileKey{z, x, y}
	if !okZ || !okX || !okY || (z != tileOverviewZ && z != tileDetailZ) || !inTileRange(k) ||
		r.URL.EscapedPath() != r.URL.Path {
		writeJSON(w, http.StatusNotFound, unknownMaxAge, map[string]string{"error": "not_found"})
		return
	}
	a.serveSnapshot(w, r, func(s *vehiclesSnapshot) *encoded {
		if e := s.tiles[k]; e != nil {
			return e
		}
		return s.empty
	})
}
