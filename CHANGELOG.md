# Changelog

The public instance (`https://transit.haroldpoi.dev`) runs `main`; a push to `main` deploys it.
Version tags (`v*`) build an image with that version (`ghcr.io/angelospk/athens-transit-rt:<version>`)
for self-hosting. API reference: [docs/API.md](docs/API.md), OpenAPI 3.1:
[docs/openapi/openapi.yaml](docs/openapi/openapi.yaml), viewer with try-it:
<https://angelospk.github.io/athens-transit-rt/>. Contract for clients: [docs/CONTRACT.md](docs/CONTRACT.md).

## Unreleased

### Live API
- Live responses send `Access-Control-Expose-Headers: Date` (contract rev 10), so a browser
  `fetch()` can read the server time and a new tab sees a stopped feed on its first response
  ([#3](https://github.com/angelospk/athens-transit-rt/issues/3)).
- `at_terminus` (contract rev 9) on `/v1/lines/{id}` vehicles and on `/v2` Vehicles: `true`
  when a bus is matched and waiting at the first stop of its trip, so clients can show
  "waiting at the terminus" and leave its departure delay out of the line's running delay
  ([#1](https://github.com/angelospk/athens-transit-rt/issues/1)).
- `docs/fixtures/v2/motion-cases.json`: shared test cases for the `motion.knots` client rule
  (where a marker stands at a time), so the app and the web map test their movers against the
  same cases. The backend checks the knot rules on every train of its test feed through a day.
- `/v2/vehicles/rail` is metro and tram only and opens nothing upstream. Suburban trains moved
  to the new `/v2/vehicles/suburban`, which opens the railway.gov.gr stream: a map showing the
  metro no longer keeps that stream (~80 KB/s) open.
- `/v2` caching: a Source that is `indicative` (expired timetable) or `stale` no longer makes a
  response incomplete, so `/v2/lines`, `/v2/stops` and `/v2/lines/{id}` keep `max-age=3600` with
  an `ETag` while the OASA GTFS is expired, and the `/v2/vehicles` snapshot keeps its 30 s life.
  Only `warming_up` and `down` cut a response to `max-age=5`. `/v2/stops` (about 760 KB raw) is
  built and gzipped once and served with a strong `ETag` while the stops and Source states stay
  the same.
- **`/v2`: one model for every mode** (contract rev 8): buses, trolleys, metro, tram and the
  suburban railway as Line, Stop, Pattern, Vehicle, Arrival and Run, with `<source>:<native>`
  ids, permanent aliases and a `sources` freshness block in every body. Endpoints:
  `/v2/lines`, `/v2/lines/{id}`, `/v2/lines/{id}/vehicles`, `/v2/stops`, `/v2/stops/{id}`,
  `/v2/vehicles`, `/v2/vehicles/tiles/{z}/{x}/{y}`, `/v2/vehicles/rail`, `/v2/runs/{id}`. Bus
  Arrivals add the timetable trips no bus has started; metro and tram trains are placed from the
  timetable on their OSM track and glide between snapshots (`motion.knots`); suburban trains come
  from the railway.gov.gr stream, snapped to their corridor. Rail geometry is now embedded in the
  backend. `/v1` is unchanged. Choices made on the way: `docs/DECISIONS-phase2.md`.
- `/v2` on an expired OASA GTFS: bus timetable Arrivals and `/v2/runs` come from OASA's daily
  schedule of today (cached, never asked per request): the departure plus the stop's offset in
  the nearest GTFS trip of that direction. Run id `oasa:run:<trip_id>@<HHMM>:<date>`. `oasa` is
  `indicative` only for lines without a cached daily schedule.
- `GET /v1/stops/{stop_id}` (contract rev 8): arrival times now come from each bus's position
  and the typical driving speed to the stop (else the timetable's running time) instead of the
  timetable time plus delay; unmatched buses placed on their shape are listed too, and while
  the static GTFS is valid so are the trips that have not left yet (`source: schedule`). New
  fields `source`, `trip_id`, `scheduled`, `stops_away`, `distance_m`; up to 60 min ahead
  (was 2 h), at most 3 per line and variant. Existing fields are unchanged. Stop boards by
  @imertz (#6).
- `GET /v1/stops/{stop_id}/arrivals`: OASA's own predictions for a stop (line, route code,
  vehicle, minutes), soonest first, cached 30 s per stop with `Cache-Control` until that cache
  expires. The OASA calls take at most one request slot in four, so the line polls keep at least
  three quarters of the budget; when OASA or the queue cannot answer, the last good answer up to
  2 minutes old is served. Shares its cache with the MCP tool `stop_arrivals`.

## v0.1.0 (2026-10-28)

First tagged version. Everything below is live on the public instance.

### Live API
- Every active OASA route polled under one global budget (4–5 req/s), with backoff on errors and
  slow answers; background jobs report only their errors to the pacer, so a slow prefetch no longer
  holds the pace down.
- Vehicles matched to scheduled trips, with smoothed `speed`, the `path` ahead, `path_stops` and
  `path_speeds` (contract rev 7).
- `path_speeds` follow today's traffic: recent passes of every line per 100 m piece of road and
  heading, blended into the typical speed of the hour (`segspeed`).
- Vehicle tiles for whole-network maps (`/v1/vehicles/tiles/{z}/{x}/{y}`), GTFS-Realtime
  VehiclePositions and TripUpdates.

### Timetables
- `GET /v1/lines/{id}/schedule`: today from OASA's daily schedule (`getDailySchedule`,
  `source: oasa-daily`), the real timetable even while the static GTFS is expired; other days and
  fallback from the GTFS (`source: gtfs`, `expired` when outside its validity).
- Every line's daily schedule is prefetched once a day through the scheduler's background slots.
- `seen` per run for the daily schedule too (new): the daily schedule has no trip ids, so the
  server watches buses leave the start of their shape (within 300 m, then 600 m along within
  15 min) and gives each start to the run due from 3 min before to 20 min after it. `false` once
  20 min pass without one. Loop lines, late or out-of-order fixes and a feed swap are handled.
  A bus whose route code changes late, at the end of one shape and up to 1.5 km into the next
  (909, Χ96), counts too. Direction names match GTFS headsigns without spaces, dots or hyphens
  and with Latin look-alike letters read as Greek; a loop line takes every shape.

### Rail
- Metro and tram: `GET /v1/rail/stasy/departures?stop=<name>` (STASY GTFS timetable, not live).
- Suburban rail (Proastiakos): `GET /v1/rail/suburban/stations` and
  `/v1/rail/suburban/stations/{id}/departures` (live departures and delays).

### Statistics
- `/v1/stats` and `/v1/stats/days/{date}`: trips planned, run, partial, missed per line; long-term
  views on the web map.

### MCP
- `/mcp`: a read-only MCP server (Streamable HTTP) with tools for lines, buses, stops, timetables,
  statistics, metro and suburban rail. One OASA call per stop, bounded queue, read deadlines.

### Clients
- Web map: <https://bus.haroldpoi.dev> ([athens-transit-map](https://github.com/angelospk/athens-transit-map)).
- Android app: [athens-transit-app](https://github.com/angelospk/athens-transit-app) (signed APKs on
  its releases page).
