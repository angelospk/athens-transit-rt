# Contract rev 8 (DRAFT): one model for every mode

Status: **approved by Harold 2026-10-29 and implemented** (branch `feat/v2-unified`). The
current text is in [CONTRACT.md](CONTRACT.md#unified-api-v2-rev-8) and
`openapi/openapi.yaml`; this draft is kept as the record of what was approved.
Terms: [GLOSSARY.md](GLOSSARY.md).

## 1. Goal and rules

- One model, **Line / Stop / Vehicle / Arrival**, for bus, trolley, metro (M1-M3), tram (T6, T7)
  and suburban (Proastiakos). The app and the web render it with one stop card, one line card and
  one vehicle marker for every mode.
- **Additive only.** Every rev 1-7 endpoint and field stays as it is (app v0.2.0 is out). The new
  model lives under a new path prefix, `/v2/` (Harold, 2026-10-29). `/v1` is not deprecated.
- **The backend owns the unification.** Each Source has one adapter. An adapter turns upstream
  data into the model below. A Source change (new URL, new format, a GTFS instead of a JSON
  board) changes only its adapter; responses keep their shape and their ids.
- **Missing data is labelled, never hidden.** Every optional field is present with `null` when
  not known (keys never disappear, except where noted for tile zoom 9). Clients render the same
  card and say "χωρίς ζωντανά δεδομένα" / "κατά το πρόγραμμα" from `source` and `position.kind`.

## 2. Sources and adapters

| Source id | Upstream | Modes | Adapter (today's code it wraps) | Live signal |
|---|---|---|---|---|
| `oasa` | OASA static GTFS + telematics API | bus, trolley | `internal/gtfs`, `internal/telematics`, `internal/match`, `internal/server/stops.go` | GPS every 30-150 s; delay from the matcher |
| `stasy` | STASY static GTFS (data.gov.gr CKAN) + OSM track geometry | metro, tram | `internal/rail/stasy*.go` | none: timetable only |
| `proastiakos` | railway.gov.gr stations, schedule boards, train-stream (SSE); ose.mpass fallback | suburban | `internal/rail/proastiakos.go`, `trainstream.go` | GPS + delay while the stream is open |

New per adapter (phase 2): a stable id scheme (§3), Line names and colours, geometry, and an
alias table for ids that the upstream renames.

Geometry moves into the backend. Today it lives only in the clients: `athens-transit-map/public/metro.json`
(OSM tracks, built by `scripts/metro*.ts`) and `athens-transit-app/assets/suburban.json`
(corridor paths). The backend repo takes the build scripts and serves the result; clients stop
bundling it.

## 3. Identity

All ids are opaque strings `<namespace>:<native>`, URL-safe after percent-encoding. Clients store
and compare them; they never parse them.

| Entity | `oasa` | `stasy` | `proastiakos` |
|---|---|---|---|
| Line | `oasa:040`, `oasa:Α1` (telematics LineID) | `stasy:M1`, `stasy:T6` (route_short_name) | `proastiakos:PIRAIR` (corridor) |
| Stop | `oasa:400012` (GTFS stop_id = OASA stop code) | `stasy:<folded-name>-<metro\|tram>`, e.g. `stasy:συνταγμα-metro` | `proastiakos:<primary station id>` |
| Vehicle | `oasa:52134` (vehicle no.) | `stasy:run:<trip_id>:<date>` (the Run itself) | `proastiakos:<train no.>` |
| Run | `oasa:run:<trip_id>:<date>` | `stasy:run:<trip_id>:<date>` | `proastiakos:run:<train no.>:<date>` |

- **Namespace = the numbering authority, not the fetch path.** If railway.gov.gr is replaced by
  a Hellenic Train GTFS, the adapter maps the new native ids to the old `proastiakos:` ids.
- **STASY stops are keyed by folded name + mode, not by `stop_id`.** STASY lists one `stop_id`
  per platform and may renumber them in a new feed; a name is what the passenger saves. All
  platforms with one folded name and one mode become one Stop.
- **Aliases.** Each adapter keeps a small table `old id → current id` (renamed stop, Αεροδρόμιο's
  second station id). A request with an alias id answers with the current object; its `id`
  field is the current id, so clients can rewrite stored Favourites. **Aliases are permanent**
  (never removed), and a published Line or Stop id is **never reused** for another Line or Stop,
  so a saved Alert always resolves or fails with `404`, never to the wrong place.
- Run ids (`oasa:run:<trip_id>:<date>`, `stasy:run:<trip_id>:<date>`,
  `proastiakos:run:<train no.>:<date>`) and `stasy:run:*` Vehicle ids live only for their service
  day. Clients never store Vehicle or Run ids.
- `/v1` ids map 1:1: `/v1` line `040` = `oasa:040`; `/v1` stop `400012` = `oasa:400012`.

## 4. Objects

### Line

```json
{
  "id": "proastiakos:PIRAIR",
  "mode": "suburban",
  "label": null,
  "name": "Πειραιάς – Αεροδρόμιο",
  "name_en": "Piraeus – Airport",
  "color": "#2e7d32",
  "text_color": "#ffffff",
  "source": "proastiakos"
}
```

- `mode`: `bus` | `trolley` | `metro` | `tram` | `suburban`. A new mode is an additive change;
  clients show an unknown mode with a generic icon.
- `label`: short public code or `null`. Badge rule: `label` present → coloured badge with the
  label; `null` → mode icon on `color` with `name` (truncated).
- `name`: always present. `name_en`: `null` when the Source has none.
- `color`, `text_color`: always present; the adapter supplies a default per mode when the Source
  has none.

### Stop

```json
{
  "id": "stasy:συνταγμα-metro",
  "name": "Σύνταγμα",
  "name_en": "Syntagma",
  "lat": 37.97527, "lon": 23.73552,
  "modes": ["metro"],
  "lines": ["stasy:M2", "stasy:M3"],
  "source": "stasy"
}
```

- `lat`/`lon`: one point (mean of the platforms for merged STASY stops). 5 decimals.
- `modes`: the modes of `lines`, sorted; a bus stop can be `["bus","trolley"]`.
- `lines`: Line ids that call here, sorted by mode then label/name.
- `name_en`: `null` when unknown (OASA has it for most stops; STASY and railway.gov.gr do).

### Vehicle

```json
{
  "id": "stasy:run:1012345:2026-10-29",
  "line": "stasy:M2",
  "run": "stasy:run:1012345:2026-10-29",
  "mode": "metro",
  "headsign": "Ελληνικό",
  "label": null,
  "position": {"kind": "estimate", "lat": 37.9722, "lon": 23.7325, "bearing": 205, "at": 1793205600},
  "delay_s": null,
  "next_stop": "stasy:ακροπολη-metro",
  "motion": {
    "path": [[37.9722, 23.7325], [37.96882, 23.72963]],
    "path_beyond": [[37.96882, 23.72963], [37.96421, 23.72651]],
    "path_stops": [450, 1010],
    "speed": null,
    "path_speeds": null,
    "knots": [[0, 1793205600], [450, 1793205660], [450, 1793205690], [1010, 1793205760]]
  },
  "source": "stasy"
}
```

- `label`: what the passenger reads on the vehicle: bus fleet no. (`52134`), suburban train no.
  (`1226`); `null` for metro/tram (no physical train is known).
- `position.kind`: `gps` | `estimate`. `position.at`: the GPS fix time, or for an estimate the
  build time of the snapshot. `bearing` may be `null`.
- `delay_s`: `null` when there is no live signal (always for metro/tram).
- `run`: Run id, `null` when the Vehicle is not matched to a Run.
- `next_stop`: Stop id, `null` when unmatched.
- `motion`: how the client moves the marker between snapshots. `null` when there is nothing to
  move along (standing with no Run, no geometry).
  - `path`, `path_beyond`, `path_stops`, `speed`, `path_speeds`: the rev 3-7 meanings, unchanged
    for buses (1.5 km caps included); for suburban they run along the rail geometry of §2.
  - For **estimates** (metro/tram) the 1.5 km caps do not apply: `path` runs to the next Stop
    and `path_beyond` to the third Stop ahead, whatever the distance.
  - `knots` (new, estimates only; `null` for GPS vehicles): `[metres along path + path_beyond,
    unix time]` pairs. The first knot is `[0, position.at]`. Each Stop ahead gives two knots,
    its arrival and its departure, with the same metres (one knot when the two times are equal).
    Metres never decrease; times strictly increase. The last knot is at the end of
    `path_beyond` (or of `path`).
  - **Client rule, one for all modes:** if `knots` is present, place the marker by time: before
    the first knot at metre 0, between two knots linearly in metres (equal metres = dwell at the
    Stop), after the last knot at its metres until the next snapshot. Otherwise use the rev 6
    speed rule (`path_speeds`, then `speed`, then 1.5 m/s) and stop at the end of the points.
    This is the "trains glide along the track" behaviour.
- `source`: the Source id.

### Arrival

```json
{
  "line": "proastiakos:PIRAIR",
  "stop": "proastiakos:6928c3c150ab5bc72b39cee5",
  "headsign": "Αεροδρόμιο",
  "at": 1793205900,
  "scheduled_at": 1793205780,
  "delay_s": 120,
  "source": "live",
  "run": "proastiakos:run:1226:2026-10-29",
  "vehicle": "proastiakos:1226",
  "vehicle_label": "1226",
  "platform": "2",
  "status": "running"
}
```

- `at`: the expected time (leaving time for rail; see the Glossary). Always present.
- `scheduled_at`: the timetable time; `null` when the Source has no timetable for it (an OASA bus
  matched to no trip is not listed at all, as in rev 7).
- `delay_s`: the **observed** delay of the Vehicle (or the Source's reported delay for the
  Run), else `null`. Normally `at = scheduled_at + delay_s`, but `at` is clamped to now (a bus
  already due), so clients must not compute one from the other.
- `source`: `live` | `timetable`. `live` only when GPS or a reported delay set `at`. Suburban
  precedence: stream delay (stream open) → board delay (board ≤ 10 min old) → timetable; the
  first two give `live`.
- `run`: Run id, so the card can open the Run even before it starts; `null` only for an OASA
  bus matched to no trip (not listed today).
- `vehicle`, `vehicle_label`: `null` when no Vehicle is on the Run yet or none is known. Metro/tram:
  `vehicle` is set only while the Run is under way; `vehicle_label` is always `null`.
- `platform`: `null` unless the Source gives one (only railway.gov.gr today).
- `status`: `scheduled` | `running` | `delayed`. Cancelled Runs are left out, as today.

### Source state (in every list response)

```json
"sources": [
  {"id": "oasa", "state": "ok", "updated_at": 1793205590},
  {"id": "stasy", "state": "indicative", "updated_at": 1793205000},
  {"id": "proastiakos", "state": "warming_up", "updated_at": null}
]
```

`state`: `ok` | `warming_up` | `stale` (last good data served) | `indicative` (expired
timetable, the same weekday stands in) | `down` (no data at all). Freshness is per Source,
because their signals differ:

| Source | `stale` when | `indicative` when | `down` when |
|---|---|---|---|
| `oasa` | no successful line poll for 5 min (as `/v1/status` `ok:false`) | static GTFS expired | no data since start |
| `stasy` | the daily check failed and the loaded timetable is from an earlier day | timetable expired (today's `indicative:true`) | no timetable loaded |
| `proastiakos` | stream open but no event for 2 min, or board older than 30 min | never | no board and no ose.mpass answer |

`updated_at`: the last good data of that Source (GPS fix, stream event, board fetch, timetable
load). Only the
Sources that the response draws on are listed. Clients show a one-line note per non-`ok` Source;
they never drop the card.

## 5. Endpoints (`/v2`)

All: UTF-8 JSON, `Access-Control-Allow-Origin: *`, gzip, times in Unix seconds, `ETag` +
`If-None-Match` → `304` where cached ≥ 60 s. **No query strings** on the cached map endpoints
(tiles), as in rev 5. Errors keep the rev 7 shape `{"error": "<code>"}`.

| Endpoint | Body | Cache (`max-age`) | Side effect upstream |
|---|---|---|---|
| `GET /v2/lines` | `{lines: [Line], sources}` all modes | 3600 | none |
| `GET /v2/lines/{id}` | `{line: Line, patterns: [Pattern], stops: {id: Stop}, sources}` | 3600 | none |
| `GET /v2/lines/{id}/vehicles` | `{line, updated_at, next_update_at, vehicles: [Vehicle], sources}` | until `next_update_at`, 5-300 | `oasa`: marks the line watched (as `/v1/lines/{id}`); `proastiakos`: opens the stream |
| `GET /v2/stops` | `{stops: [StopIndexEntry], sources}` every Stop of every mode | 3600 | none |
| `GET /v2/stops/{id}` | `{stop: Stop, lines: [Line], arrivals: [Arrival], updated_at, sources}` | 15 | `oasa`: raises its lines' priority; `proastiakos`: opens the stream |
| `GET /v2/vehicles` | `{updated_at, next_update_at, vehicles: [Vehicle], sources}` bus + trolley | until `next_update_at`, 5-30 | none |
| `GET /v2/vehicles/tiles/{z}/{x}/{y}` | as `/v2/vehicles`, tile-cut as rev 5 | as above | none |
| `GET /v2/vehicles/rail` | as `/v2/vehicles`, metro + tram + suburban (~100 vehicles, one body) | until `next_update_at`, 5-30 | `proastiakos`: opens the stream |
| `GET /v2/runs/{id}` | `{run: Run, sources}` | 15 | none |

Shapes not shown above:

```json
// Pattern
{"id": "oasa:2045", "headsign": "Σύνταγμα", "direction": 0,
 "stops": ["oasa:400012", "oasa:400013"], "shape": [[37.9420, 23.6466], [37.9431, 23.6480]]}

// StopIndexEntry (compact: ~9000 stops, target < 250 KB gzip)
["oasa:400012", "Πλ. Καραϊσκάκη", 37.94200, 23.64660, ["bus"], ["oasa:040", "oasa:049"]]

// Run (also before it starts: vehicle null, every stop passed:false)
{"id": "stasy:run:1012345:2026-10-29", "line": "stasy:M2", "headsign": "Ελληνικό",
 "pattern": "stasy:M2-0", "vehicle": "stasy:run:1012345:2026-10-29", "vehicle_label": null,
 "status": "running",
 "stops": [{"stop": "stasy:ακροπολη-metro", "scheduled_at": 1793205660, "at": 1793205660,
            "delay_s": null, "source": "timetable", "platform": null, "passed": false}]}
```

Rules:

- `/v2/stops/{id}` arrivals: soonest first, at most 2 h ahead, at most 4 per Line + headsign, at
  most 30 in all. Bus arrivals: those of rev 7 (`/v1/stops/{id}`, `live`), plus the timetable
  Arrivals of Runs no matched bus has started yet (`timetable`, `vehicle: null`; Harold,
  2026-10-29). Metro/tram: the STASY
  timetable. Suburban: the board, with the stream's delay when it is open.
- `/v2/vehicles` and tiles carry bus and trolley only, like rev 3/5: map traffic must not keep
  the railway.gov.gr stream open. Rail vehicles come from `/v2/vehicles/rail`, which a client
  loads only while its rail layer is on; that request is the explicit suburban demand. Clients
  merge both into one vehicles layer (one marker component). Tile zoom 9 entries drop `motion`
  (key absent), as rev 5 drops the path keys.
- Metro/tram vehicles are placed at build time (every 30 s); their `knots` keep them moving.
- `/v2/runs/{id}` replaces `/v1/rail/stasy/trips/{id}` and `/v1/rail/suburban/trains/{no}` for
  the line card. It answers for any Run of the service day, started or not (`vehicle: null`
  before it starts). OASA: the matched trip's stops, timetable time plus observed delay.
  `404 {"error":"unknown_run"}` outside the service day.
- Unknown ids: `404` `unknown_line` / `unknown_stop` / `unknown_run`. A Source still loading:
  `200` with the Source `warming_up` in `sources`, empty lists, and `null` for a single object
  that Source should fill (e.g. `line: null` in `/v2/lines/{id}` for a suburban Line before the
  corridor table loads, never `404` then). Never `503`, so one slow Source does not blank a card
  that also shows other Sources.
- **Incomplete responses are cached at most 5 s** (`max-age=5`, no `ETag`): any response with a
  Source not `ok` or `stale`. The long `max-age` above applies only to complete responses, so a
  Source that recovers shows within seconds, not after an hour.

## 6. Mode × field availability

`✓` from the Source, `≈` estimate, `—` missing (`null`), `↻` only while the suburban stream is open.

| Field | bus / trolley (`oasa`) | metro / tram (`stasy`) | suburban (`proastiakos`) |
|---|---|---|---|
| Line `label` | ✓ | ✓ | — (badge falls back) |
| Line `color` | ✓ (GTFS / default) | ✓ GTFS `route_color` | adapter table |
| Stop `name_en` | ✓ most | ✓ | ✓ |
| Pattern `shape` | ✓ GTFS | ✓ OSM track | ✓ corridor path |
| Vehicle `position.kind` | `gps` | `estimate` | `gps` ↻ |
| Vehicle `label` | ✓ fleet no. | — | ✓ train no. |
| Vehicle `delay_s` | ✓ when matched | — | ✓ ↻ |
| Vehicle `motion.path*` | ✓ when matched | ≈ from timetable + track | ≈ snapped to track, ahead at `speed` |
| Vehicle `motion.speed` | ✓ | — | ✓ (`speed_kmh` / 3.6) |
| Vehicle `motion.path_speeds` | ✓ | — | — |
| Vehicle `motion.knots` | — | ≈ | — |
| Arrival `source` | `live`; `timetable` before a bus starts its Run | `timetable` | `live` ↻ or board ≤ 10 min, else `timetable` |
| Arrival `scheduled_at` | ✓ | ✓ | ✓ |
| Arrival `delay_s` | ✓ | — | ✓ ↻ / board (≤ 10 min old) |
| Arrival `vehicle_label` | ✓ | — | ✓ |
| Arrival `platform` | — | — | ✓ when the board has it |
| Run (`/v2/runs/{id}`) | ✓ when matched | ✓ | ✓ |

## 7. Which adapter fills what

- **`oasa`**: Line, Pattern, Stop from the OASA GTFS (today's static `lines.json` /
  `lines/{id}.json` data); Vehicle from the matcher's vehicles (today's `/v1/lines/{id}` fields
  renamed: `bearing`, `position_at` → `position`; `variant` → Pattern id; `trip_label` dropped
  (the card builds it); `path*` → `motion`); Arrival from `internal/server/stops.go`.
- **`stasy`**: Line from `routes.txt` (+ default names "Γραμμή 1" … when the feed leaves them
  empty); Stop by folding `stops.txt` names per mode; Pattern from the distinct stop sequences of
  `trips.txt` + the OSM track cut between stops; Vehicle from `STASY.Vehicles` (`from`/`to`
  become `next_stop`, `path`, `knots`); Arrival from the departures index; `indicative` →
  Source state.
- **`proastiakos`**: Line from the corridor table (names and colours in the adapter, as
  `suburban.json` has them); Stop from `corridor-stations` (ids merged, aliases kept); Pattern
  from the corridor path and its ordered stations; Vehicle from the train-stream (snapped to the
  corridor path; `course` → `bearing`); Arrival from the board + stream delay; ose.mpass when the
  board fails.

## 8. Not in rev 8

- Cross-mode interchange grouping (Πειραιάς M1 + suburban + tram + buses as one place): not
  planned. Separate Stops; the Stop card lists nearby Stops of other modes (Harold, 2026-10-29).
- Rail vehicles inside the shared tiles: rejected (keeps the suburban stream open for bus-only
  map traffic, ~80 KB/s upstream; Codex review 2026-10-29).
- Server-side Alerts or push: Alerts stay client-side and poll `/v2/stops/{id}`.
- MCP tools keep their names; they may switch to the `/v2` model inside without a contract change.
