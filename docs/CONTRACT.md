# athens-transit contract v1

Shared interface between the backend (`athens-transit-rt`, Go, on a home machine)
and the frontend (`athens-transit-map`, Svelte, on GitHub Pages).
Source of truth: `athens-transit-rt/docs/CONTRACT.md`. The frontend keeps a copy.

## Ownership rules

- The backend owns this contract. v1 changes are **additive only** (new optional
  fields, new endpoints). A breaking change needs a `/v2` path.
- The frontend never edits the backend repo. It asks for a change with a GitHub
  issue on `angelospk/athens-transit-rt`, label `contract`.
- Each change to this file bumps the `Revision` line below and is copied to the
  frontend repo by whoever made it (backend thread opens the copy as a commit or
  issue on the frontend repo).

Revision: 4 (2026-10-06): `path` reaches up to the third stop ahead; `path_stops`
(rev 3: `GET /v1/vehicles`; `speed` and `path` on vehicles)

## Hosts

| Host | Served by | Content |
|---|---|---|
| `https://transit.haroldpoi.dev` | Home machine → Cloudflare Tunnel → Cloudflare cache | Live data (small, changes every ~30 s) |
| `https://angelospk.github.io/athens-transit-rt/static/v1/` | GitHub Pages of the backend repo | Static map data (changes only when OASA publishes a new GTFS) |
| `https://bus.haroldpoi.dev` | GitHub Pages of the frontend repo | The map app |

All JSON is UTF-8. Line ids are the OASA telematics `LineID` in Greek uppercase
(`040`, `550`, `Α1`, `Χ96`), percent-encoded in URLs. The backend also accepts the
Latin look-alikes (`A1` → `Α1`). Times are Unix seconds (integers).
CORS: every live and static response has `Access-Control-Allow-Origin: *`.

## Live API (`transit.haroldpoi.dev`)

### `GET /v1/lines/{line_id}`

Vehicles of one line. One URL per line so all users share one cached copy.
The frontend combines lines in the browser. Max 5 lines per client (enforced in UI).

```json
{
  "line": "040",
  "updated_at": 1791100000,
  "next_update_at": 1791100030,
  "vehicles": [
    {
      "id": "52134",
      "lat": 37.9755, "lon": 23.7348,
      "bearing": 180,
      "position_at": 1791099988,
      "route_code": "2045",
      "variant": "2045",
      "trip_id": "12345-WINTER",
      "trip_label": "00:35 ΠΕΙΡΑΙΑΣ → ΣΥΝΤΑΓΜΑ",
      "delay_s": 140,
      "next_stop_id": "400012",
      "speed": 6.4,
      "path": [[37.9755, 23.7348], [37.97601, 23.73502], [37.9781, 23.7356]],
      "path_stops": [412]
    }
  ]
}
```

- `trip_id`, `trip_label`, `delay_s`, `next_stop_id` are `null` when the vehicle is
  not matched to a scheduled trip. `bearing` may be `null`.
- `variant` keys into `static/v1/lines/{line_id}.json` → `variants` (for shape + stops).
  It may be `null` if no static variant matches.
- `speed` (rev 3): smoothed speed along the route in m/s, one decimal, 0..20. It is the
  median slope over the vehicle's recent fixes (up to 5, within 300 s) as distance along
  its shape; moving less than 20 m over them gives `0` (standing). `null` when unknown:
  fewer than 2 fixes ≥ 20 s apart on the same shape (first sighting, new run, long gap
  between polls), position on the shape ambiguous (a loop or out-and-back passes there
  twice and history cannot tell which), more than 150 m from the shape, no shape.
- `path` (rev 3, rev 4): the route ahead as `[lat, lon]` pairs (5 decimals), from the
  vehicle's projected point on its shape (at `position_at`) to its third stop ahead when
  matched (rev 3: to its next stop; a vehicle waiting to start: to its first stop),
  otherwise `max(speed × 150 s, 300 m)` ahead; at most 1.5 km and never past the
  shape's end. Simplified (5 m), so a straight street has 2 points. `null` when there is
  nothing to move along: no shape, > 150 m from it, ambiguous position, or already at the
  last stop / the shape's end. Use: move the marker along `path` from `position_at` at
  about `speed`, waiting a little at each of `path_stops`; stop at the last point and wait
  for the next update. The static `shape` is not needed for this.
- `path_stops` (rev 4): the stops on `path`, as whole metres along it from its start (the
  last one may be its end). `null` when unknown (not matched to a trip, no `path`) or no
  stop is on it (the 1.5 km limit came first). Older clients ignore it; they then drive
  past the next stop.
- Headers: `Cache-Control: public, max-age=<seconds until next_update_at, min 5, max 300>`.
  The frontend refetches a line at `next_update_at + 1..3 s` (random jitter), never faster
  than every 5 s, and at most every 60 s while the tab is hidden.
- Asking for a line marks it "watched" for ~10 min: the backend polls it at the top
  priority tier (~30 s), within a capped share of the OASA request budget.
- `404` `{"error":"unknown_line"}` for an unknown line. `503` `{"error":"warming_up"}`
  before the first poll of that line finished.
- `429` may come from Cloudflare rate limiting (per IP). The frontend backs off ≥ 10 s.

### `GET /v1/vehicles` (rev 3)

Every vehicle the backend knows now, from all lines, for a citywide map. Data comes from
the normal polling (watched lines ~30 s, busy ~60 s, others ~150 s); this endpoint does
**not** mark any line watched and causes no OASA request, so positions here can be up
to ~2.5 min staler than `/v1/lines/{id}` for lines nobody watches.

```json
{
  "updated_at": 1791100000,
  "next_update_at": 1791100030,
  "vehicles": [
    {
      "line": "040",
      "id": "52134",
      "lat": 37.9755, "lon": 23.7348,
      "bearing": 180,
      "position_at": 1791099988,
      "variant": "2045",
      "delay_s": 140,
      "speed": 6.4,
      "path": [[37.9755, 23.7348], [37.9781, 23.7356]],
      "path_stops": [412]
    }
  ]
}
```

- Same field meanings as `/v1/lines/{id}`; `line` is the line id. Coordinates are rounded
  to 5 decimals. No `route_code`, `trip_id`, `trip_label`, `next_stop_id`: fetch
  `/v1/lines/{line}` when the user selects a vehicle.
- Vehicles with a GPS fix older than 5 min are left out. A vehicle seen on two lines
  appears once (newest fix). Sorted by `line`, then `id`. `vehicles` may be `[]`.
- One snapshot for everyone, built at most every 30 s (`updated_at` = build time,
  `next_update_at` = earliest next build). Refetch at `next_update_at + 1..3 s`.
- Headers: `Cache-Control: public, max-age=<seconds until next_update_at, min 5, max 30>`
  (the 5 s floor can keep a copy up to 5 s past `next_update_at`). `ETag`;
  `If-None-Match` gives `304`. In a browser, let the HTTP cache revalidate (plain
  `fetch`); do not set `If-None-Match` from JS: it is not a CORS-safelisted header and
  there is no `OPTIONS` preflight support. gzip when asked (~50 KB for 1500 vehicles).

### `GET /v1/status`

```json
{ "ok": true, "gtfs_version": "2026-10-06", "gtfs_expires": "2027-01-06",
  "updated_at": 1791100000, "lines_active": 312, "budget_rps": 4 }
```

`Cache-Control: public, max-age=10`. The frontend shows a banner if `ok` is false or
`gtfs_expires` is in the past.

### GTFS-Realtime feeds (whole network, for journey planners)

- `GET /v1/gtfs-rt/vehicle_positions.pb`
- `GET /v1/gtfs-rt/trip_updates.pb`
- `.json` variants of both, for debugging.

`Cache-Control: public, max-age=15`. The frontend does not use these.

## Static data (`.../athens-transit-rt/static/v1/`)

Published by GitHub Actions in the backend repo when OASA publishes a new GTFS.

### `lines.json`

```json
{
  "gtfs_version": "2026-10-06",
  "lines": [
    { "id": "040", "name": "ΠΕΙΡΑΙΑΣ - ΣΥΝΤΑΓΜΑ", "name_en": "PIRAEUS - SYNTAGMA",
      "color": "#1a73e8", "text_color": "#ffffff", "kind": "bus" }
  ]
}
```

`kind` is `bus` or `trolley`.

### `lines/{line_id}.json`

```json
{
  "id": "040",
  "variants": {
    "2045": {
      "headsign": "ΣΥΝΤΑΓΜΑ",
      "direction": 0,
      "shape": [[37.9420, 23.6466], [37.9431, 23.6480]],
      "stops": ["400012", "400013"]
    }
  },
  "stops": {
    "400012": { "name": "ΠΛ. ΚΑΡΑΪΣΚΑΚΗ", "lat": 37.9420, "lon": 23.6466 }
  }
}
```

`shape` is `[lat, lon]` pairs, rounded to 5 decimals. `variants[*].stops` is ordered.

## Fixtures

`docs/fixtures/` in the backend repo holds one example of every response above
(`line-040.json`, `vehicles.json`, `status.json`, `lines.json`, `lines-040.json`). The frontend
develops against copies of these until the live API is up.
