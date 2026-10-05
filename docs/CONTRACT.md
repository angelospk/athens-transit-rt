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

Revision: 2 (2026-10-05)

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
      "next_stop_id": "400012"
    }
  ]
}
```

- `trip_id`, `trip_label`, `delay_s`, `next_stop_id` are `null` when the vehicle is
  not matched to a scheduled trip. `bearing` may be `null`.
- `variant` keys into `static/v1/lines/{line_id}.json` → `variants` (for shape + stops).
  It may be `null` if no static variant matches.
- Headers: `Cache-Control: public, max-age=<seconds until next_update_at, min 5, max 300>`.
  The frontend refetches a line at `next_update_at + 1..3 s` (random jitter), never faster
  than every 5 s, and at most every 60 s while the tab is hidden.
- Asking for a line marks it "watched" for ~10 min: the backend polls it at the top
  priority tier (~30 s), within a capped share of the OASA request budget.
- `404` `{"error":"unknown_line"}` for an unknown line. `503` `{"error":"warming_up"}`
  before the first poll of that line finished.
- `429` may come from Cloudflare rate limiting (per IP). The frontend backs off ≥ 10 s.

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
(`line-040.json`, `status.json`, `lines.json`, `lines-040.json`). The frontend
develops against copies of these until the live API is up.
