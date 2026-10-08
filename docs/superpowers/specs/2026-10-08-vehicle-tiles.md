# Vehicle tiles (viewport API)

Status: built 2026-10-08 (`internal/server/tiles.go`, CONTRACT rev 5). Changed after the plan
review: the overview zoom is 9, not 10 (below).

## Problem

`GET /v1/vehicles` sends every vehicle to every client: 922 vehicles, 291 KB raw, 64 KB gzip
(prod, 2026-10-08 09:01 Athens). Per field (gzip, 2026-10-07): `path_beyond` 19.5 KB, `path`
12 KB, `path_stops` 6 KB, the rest ~23.5 KB. A map user sees a small area. Per-user bbox URLs
would kill the Cloudflare cache (today: origin sees ~1 request per 30 s), so the viewport API
must use a fixed set of shared URLs.

## Options compared

All numbers: the 922-vehicle prod sample above, gzip -6, viewport centred on Syntagma,
`t` = requests, `v` = vehicles sent, `F` = with paths, `L` = without paths.

| 256 px zoom | phone 400×800 in view | z10 L | z12 L | z13 F | z14 F |
|---|---|---|---|---|---|
| 12 | 587 | 2t 20.6K | 8t 18.0K | 28t 53.7K | 98t 61.7K |
| 13 | 316 | 2t 20.6K | 6t 16.2K | 8t 29.1K | 28t 32.6K |
| 14 | 140 | 2t 20.6K | 4t 14.8K | 6t 26.6K | 8t 15.1K |
| 15 | 49 | 1t 11.3K | 2t 8.3K | 2t 12.4K | 4t 9.7K |
| 16 | 15 | 1t 11.3K | 1t 3.9K | 1t 8.3K | 2t 5.9K |

| 256 px zoom | desktop 1920×1080 in view | z10 L | z12 L | z13 F | z14 F |
|---|---|---|---|---|---|
| 12 | 906 | 6t 22.0K | 40t 27.2K | 144t 82.3K | 558t 126K |
| 13 | 724 | 2t 20.6K | 12t 20.5K | 40t 60.4K | 160t 78.0K |
| 14 | 391 | 2t 20.6K | 4t 14.8K | 12t 38.7K | 40t 38.9K |
| 15 | 176 | 1t 11.3K | 2t 8.3K | 6t 19.4K | 15t 19.8K |
| 16 | 73 | 1t 11.3K | 2t 8.3K | 2t 12.4K | 6t 12.8K |

Whole city: with paths 66 KB, without paths 21 KB. Non-empty tiles over the city:
z10 6, z11 12, z12 30, z13 70, z14 167.

1. **XYZ (Web Mercator slippy) tiles, one tile per request, fixed zoom per LOD.**
   Standard math (MapLibre and every map library have it), square on screen, path-only URL
   (no query string to normalise). Several requests per viewport; HTTP/2 makes them cheap.
2. **Fixed lat/lon cells per LOD (e.g. 0.05°).** Same cache behaviour, simpler integer math,
   but cells are not square (0.05° = 4.4 km × 5.6 km here) and the client needs custom code.
   No gain over 1. Rejected.
3. **Several cells per request (`/tiles/13/{x0}-{x1}/{y0}-{y1}`).** Fewer requests, but every
   viewport shape is another URL: cache hits fall and origin load grows with users, which
   is the constraint we must keep. Rejected.
4. **One city-wide body without paths (`/v1/vehicles/lite`).** 21 KB, one URL. Good for the
   overview, but no use when zoomed in. Option 1 at z9 gives the same bytes in 1-2 requests
   with one URL scheme for both levels.

## Decision

`GET /v1/vehicles/tiles/{z}/{x}/{y}`, XYZ tiles, only two zooms. Map zoom below is the MapLibre
zoom (512 px tiles; the frontend uses MapLibre); the tables above use 256 px zoom = MapLibre + 1.

| z | tile size here | LOD | client uses it at MapLibre zoom |
|---|---|---|---|
| 9 | ~62 km | `line, id, lat, lon, bearing, position_at, variant, delay_s, speed` (no `path*` keys) | < 13 |
| 13 | ~3.9 km | same fields as `/v1/vehicles` (with `path`, `path_beyond`, `path_stops`) | ≥ 13 |

Threshold MapLibre 13, with the numbers: there one pixel is ~7.5 m, so a bus at 6 m/s moves
~0.8 px/s and the path animation is visible; at MapLibre 11 (~30 m/px) a 30 s move is ~6 px and a
jump is fine. Paths cost ~3× the bytes per vehicle (71 vs 23 B gzip), and below 13 the view holds
most of the city (desktop MapLibre 12: 724 of 922 vehicles, 60 KB with paths vs 21 KB without).
At ≥ 13 the z13 tiles send 12-39 KB on desktop, 8-27 KB on a phone, vs 64 KB today.
z14 would halve the phone bytes at MapLibre 13 but triple desktop requests (40) and the URLs
the origin serves; z13 is the compromise.

Overview z9, not z10 (changed while building): Cloudflare's rate limit counts cache hits too
(30 requests / 10 s per IP, README), and z10 needs up to 20 tiles for a zoomed-out view (15 on a
desktop at MapLibre 10) for the same ~21 KB. z9 needs 1-2 (at most 9, the whole box); the
city sits in one z9 tile (908 of 922 vehicles), so the overview is in effect the city-wide body
without paths (option 4) under the same URL scheme.

Rules:

- **Domain:** only tiles that touch the box lat 37.5..38.5, lon 22.9..24.5 exist (z9: x 288..290,
  y 196..198, 9 tiles; z13: x 4617..4653, y 3145..3174, 1110 tiles). Every OASA vehicle seen is
  inside (prod 2026-10-08: lat 37.72..38.21, lon 23.32..24.01). A vehicle is in the tiles when
  its z13 tile exists (its z9 parent then exists too), so one just outside the box in an edge
  tile still shows; one farther out is in no tile.
  The client clamps its view to the box.
- A vehicle is in exactly one tile: the tile of its `lat`/`lon` (floor of the tile coordinate).
  Its paths may run into other tiles; they are not cut.
- The client fetches the tiles that intersect the view padded by 300 m (≈ 30 s at 10 m/s, so a
  vehicle that drives into the view along its path is already loaded).
- A vehicle that moves to another tile between builds leaves tile A and appears in tile B.
  Tiles fetched together are normally from one build (same `updated_at`), but caches can mix
  builds within the 5 s max-age floor, so a vehicle can show in two tiles or in none until the
  next refetch. The client merges by vehicle `id`: newer `position_at` wins, on a tie the
  response with the newer `updated_at`. When the LOD changes (crossing MapLibre 13) the client
  replaces the whole set.
- `z` other than 9/13, `x`/`y` outside the domain, not canonical decimal (`013`, `+1`), or an
  escaped path that differs from its decoded form (`%34636`) → `404 {"error":"not_found"}`
  (cached 60 s like other 404s). A query string, even a bare `?` → `400
  {"error":"query_not_allowed"}` with `Cache-Control: no-store`: such a URL is another
  Cloudflare cache key, and a loud error beats a silent cache bypass.
- Body: the same shape as `/v1/vehicles` (`updated_at`, `next_update_at`, `vehicles`, sorted by
  `line`, `id`), so the client can reuse its parser. An empty tile in the domain → `200` with
  `"vehicles":[]`; all empty tiles of a build share one prebuilt body.
- Same snapshot as `/v1/vehicles`: tiles are built in the same lazy build (≤ every 30 s, on the
  first request after it is due, under `vehMu` like today), from the same vehicle list, so
  `updated_at`/`next_update_at` are equal across `/v1/vehicles` and all tiles. No OASA request,
  no line marked watched. `next_update_at` = earliest next build; a cached copy can show it up
  to 5 s in the past.
- Non-empty tiles and the empty body are encoded eagerly at build time (raw + gzip, one reused
  `gzip.Writer`, ETag = hash of raw like `/v1/vehicles`, `-gz` suffix for gzip).
- Headers like `/v1/vehicles`: `Cache-Control: public, max-age=<s until next build, 5..30>`,
  `ETag`, `If-None-Match` → `304`, gzip when asked (bytes compressed once per build),
  `Vary: Accept-Encoding`, CORS `*`, `Content-Length`, HEAD.
- Refetch rule (client): on `moveend` (after the user stops panning/zooming) recompute the tile
  set, fetch tiles not loaded yet, drop vehicles of tiles that left the set. Refetch all tiles
  at `max(min(next_update_at) + 1..3 s, last fetch + 5 s)`.

Origin load is bounded by the domain, not by users: per Cloudflare PoP and 30 s at most ~2
requests per requested tile (the 5 s floor can allow one more right after a build), so ≤ 2 ×
(1110 + 9) in the worst case and, with views clustered on the centre (70 non-empty z13 tiles
at rush hour), ~100-200 in practice; each is a copy of prebuilt bytes. Cloudflare already caches
`/v1/status` and `/v1/lines/*` (host-wide rule), so tiles should be cached too; verified after
deploy (`cf-cache-status: HIT`, 304, gzip).

Deltas (`since=`) are not built. The design leaves room: a later
`/v1/vehicles/tiles/{z}/{x}/{y}?since=<updated_at>` keys only on the previous build, which all
clients share, so it stays cacheable (the 400 rule would allow exactly that one parameter).

Review (Codex, plan round): adopted all 8 findings: Cloudflare/query 400 `no-store`, escaped-path
aliases and bare `?`, a fixed tile domain and origin budget with empty tiles, the 300 m halo,
`next_update_at` and 5 s refetch floor, merge tie-break and LOD switch, reused gzip writer and
build-time measurement, MapLibre 512 px zoom (the threshold is MapLibre 13, = 256 px 14).

## Implementation

- `internal/server/tiles.go`: `tileOf(lat, lon, z)`, domain, grouping, encoding, `handleTile`.
  `vehiclesSnapshot` gets `tiles map[tileKey]*encoded` and `empty *encoded` (raw, gz, etags). `buildVehicles`
  calls `collectVehicles` once and builds the full body and the tiles.
- `http.go`: route `GET /v1/vehicles/tiles/{z}/{x}/{y}`; skip the gzip middleware for it like
  `/v1/vehicles`.
- Tests first (`tiles_test.go`): tile math vs known values (Syntagma at z13/z9, edges);
  vehicle in exactly one tile and union of tiles = `/v1/vehicles`; z9 has no `path*` keys, z13
  equals `/v1/vehicles` entries; same `updated_at` as `/v1/vehicles`; empty tile 200; bad z/x/y
  outside domain, non-canonical numbers and escaped aliases 404; query and bare `?` 400 no-store;
  ETag/304/gzip/HEAD/Content-Length/CORS/Vary;
  `/v1/vehicles` body unchanged; no line watched.
- Benchmark: a new benchmark over a recorded prod payload (`ATRT_BENCH_VEHICLES=<file>`) reports bytes per tile at several
  viewports, build time and allocations, compared with the build before this change.
- Docs: CONTRACT rev 5, API.md, README, fixtures `vehicles-tile-13-4636-3160.json`,
  `vehicles-tile-9-289-197.json` (cut from `vehicles.json`).

## Measured (after)

`ATRT_BENCH_VEHICLES=<recorded body> go test ./internal/server -bench BuildProd -benchmem`, the
922-vehicle prod snapshot of 2026-10-08 09:01, Ryzen 7 7840HS:

| | before | after |
|---|---|---|
| build (every ≤ 30 s, on demand) | 11.5 ms | 23.4 ms |
| allocated per build | 2.0-2.4 MB | 3.7-3.8 MB |
| held by the snapshot | 355 KB | 874 KB (73 non-empty tiles) |
| `/v1/vehicles` | 290.9 KB raw, 63.6 KB gzip | unchanged |

A view centred on Syntagma, padded 300 m (MapLibre zoom; requests, vehicles sent, gzip bytes):

| zoom | phone 400×800 | desktop 1920×1080 |
|---|---|---|
| 10 | 1, 908, 20.1 KB | 6, 922, 21.0 KB |
| 11 | 1, 908, 20.1 KB | 2, 921, 20.6 KB |
| 12 | 1, 908, 20.1 KB | 1, 908, 20.1 KB |
| 13 | 6, 361, 26.3 KB | 15, 551, 40.7 KB |
| 14 | 4, 245, 17.8 KB | 9, 420, 30.6 KB |
| 15 | 2, 170, 12.3 KB | 2, 170, 12.3 KB |

Today every client gets 63.6 KB. The task's 2026-10-07 numbers (box around Syntagma, all fields:
~1 km 1.6 KB, ~2 km 4.7 KB, ~6 km 15.9 KB, ~20 km 49 KB) are the lower bound of an exact bbox;
tiles cost more because a tile is larger than the view, but stay shared and cacheable.
