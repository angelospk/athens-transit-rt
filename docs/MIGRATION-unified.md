# Migration plan: one model for every mode

Status: **phase 1 (design) approved and phase 2 (backend) done, 2026-10-29**; phases 3 (app)
and 4 (web) are next. Design: [GLOSSARY.md](GLOSSARY.md), the `/v2` section of
[CONTRACT.md](CONTRACT.md#unified-api-v2-rev-8) and [UI-SPEC-unified.md](UI-SPEC-unified.md);
review and follow-ups: [issue #9](https://github.com/angelospk/athens-transit-rt/issues/9). Estimates are working hours of agent-assisted work,
already multiplied by 1.5-2.

## Phase 2: backend (`athens-transit-rt`), 33-45 h

Order (each step ships alone; `/v1` untouched throughout):

1. **Model package** `internal/model`: Line, Stop, Pattern, Vehicle, Arrival, RunStop,
   SourceState; JSON golden fixtures in `docs/fixtures/v2/` first (tests before code). 3-4 h.
2. **Adapter interface** per Source: `Lines()`, `Line(id)`, `Stops()`, `Arrivals(stopID, now)`,
   `Vehicles(now)`, `Run(runID, now)`, `State()`, `Resolve(alias)`. 2-3 h.
3. **`oasa` adapter** wrapping today's GTFS, matcher and `stops.go`. Proof: `/v2` bus output is
   field-for-field the same data as `/v1` (diff test on the fixtures). 5-7 h.
3b. **Timetable bus Arrivals** for Runs no matched bus has started (`source: timetable`). 3-5 h.
4. **Rail geometry into the backend**: move `athens-transit-map/scripts/metro*.ts` output and
   `suburban.json` corridor paths into the repo (generated file + build script, GitHub Pages
   static). Cut tracks between stops; snap points. 5-6 h.
5. **`stasy` adapter**: stop folding per mode, Patterns, Vehicles with `motion` + `knots`,
   Arrivals from the departures index; `knots` with arrival+departure pairs. 6-8 h.
6. **`proastiakos` adapter**: corridor Lines + names/colours table, merged Stops + aliases,
   stream Vehicles snapped to the track, Arrivals from board + stream delay. 5-7 h.
7. **`/v2` handlers**: bus snapshot for `/v2/vehicles` + tiles, rail snapshot for
   `/v2/vehicles/rail`, `/v2/runs/{id}`, cache headers (5 s for incomplete responses),
   `sources` block, OpenAPI, `docs/API.md`, CHANGELOG, contract rev 8. 4-5 h.
8. **Final review** (Codex `review` lane, CodeRabbit), deploy. 2-3 h.

Done when: every `/v2` endpoint answers on `https://transit.haroldpoi.dev` for one id of each mode,
fixtures and diff tests pass, `/v1` fixtures unchanged.

## Phase 3: app (`athens-transit-app`, Flutter), 36-48 h

1. `lib/core/model_v2.dart` + API client for `/v2`; parse the contract fixtures in tests. 4-6 h.
2. One mover for every mode (port of the contract client rule; unit tests on `knots`, dwell
   and speed paths). 5-7 h.
3. **Trains on the map**: one vehicles layer from bus tiles + `/v2/vehicles/rail`, mode toggles,
   dashed estimate outline. Drop bundled `assets/suburban.json` after `/v2/lines/{id}` gives
   geometry. 6-8 h.
4. Unified Stop card replacing `StationPanel` + `StopPanel` (keep feature-id parsing for old map
   layers until step 6). 6-8 h.
5. Unified Line card replacing `LinePanel`. 5-7 h.
6. **Favourites and Alerts on any Stop**: migrate `state_v1` → `state_v2` once at start
   (`FavStop` name|lat|lon → nearest `/v2/stops` entry by folded name within 200 m; unresolved ones
   kept and marked); new Alert kind "minutes before at Stop" polling `/v2/stops/{id}` in the
   foreground service; old bus Rules keep running on `/v1`. 7-9 h.
7. Device test (`docs/TEST-CHECKLIST.md` gains: metro Stop card, suburban Stop card with
   platform, metro train glides and dwells, Alert on a metro Stop fires with «πρόγραμμα»,
   Favourite metro Stop survives a restart), release v0.3.0. 3-4 h.

## Phase 4: web (`athens-transit-map`, Svelte), 21-28 h

1. `src/lib/v2.ts` types + fetchers; one poll helper (today it is copied in four cards). 3-4 h.
2. One mover (`motion.ts` gains the `knots` rule; delete `railSegment`/`RailGlide` paths that
   the backend now feeds). 4-5 h.
3. One `StopCard` replacing `StopCard`, `StationCard`, `RailCard`; one `LineCard` replacing
   `TrainCard` + InfoCard's stop list. 7-9 h.
4. Alerts on any Stop (client and push bot share `AlertWatch`; add the Stop-arrival rule). 4-6 h.
5. Remove bundled `public/metro.json`, `public/suburban.json` once `/v2` serves geometry; QA in a
   browser. 3-4 h.

## Risks

| Risk | Effect | Mitigation |
|---|---|---|
| **Single host** (one backend instance, on the MVPS VPS in Greece since 2026-10-29; see below) | All modes go dark together; today metro/tram on the web survive a backend outage only because geometry is bundled | Keep `/v1` and bundled assets until phase 3/4 end; clients cache the last `/v2/lines` and `/v2/stops` for offline display |
| **OASA answers only addresses in Greece** (2026-10-28: 10 of 10 Greek probes, data centres included, answered; 7 of 7 outside Greece timed out) | A host outside Greece gets no bus data; `/v2` adds no new OASA calls but depends on the same polling | The host is a VPS in Thessaloniki (MVPS) and reaches OASA directly. Moving it abroad needs a Greek egress again. `/v2` `sources` shows `oasa: stale` honestly if OASA stops answering |
| **Cloudflare cache** (edge `max-age`; `/v2/stops/{id}` 15 s, vehicles ≤ 30 s, lines/stops 1 h) | An Arrival can be ~15 s + client poll old; an empty "warming up" list could stick for an hour | `at` is absolute and clients count down on their own clock; metro uses `knots`; incomplete responses are cached ≤ 5 s |
| **Cloudflare rate limit** (~30 requests / 10 s per IP) | Shared IPs (mobile CGNAT, offices) may hit `429` with more clients | Budget per client at a z13 view: ~15 tiles / 30 s + 1 stop / 20 s + alerts 1 stop / 30 s each ≈ 0.7 req/s. Back off ≥ 10 s on `429`, as today |
| **Suburban stream load** (~80 KB/s upstream while open) | Kept open by bus-only map traffic if rail were in the tiles | Decided: rail vehicles only in `/v2/vehicles/rail`, loaded only with a rail layer on; stream closes 3 min after the last request, as today |
| **STASY renames stops / railway.gov.gr ids change** | Stored Favourites and Alerts point at dead ids | Name-based STASY ids, alias tables, client repair rule (UI spec §4) |
| **Geometry drift** (OSM track vs STASY stop points) | Trains glide off the drawn line | Snap stops to the track in the build script; test that every STASY stop is within 60 m of its track |

## Hosting

Since 2026-10-29 00:44 Athens, `transit.haroldpoi.dev` is served by a VPS in Thessaloniki (MVPS,
1 vCPU, 2 GB) through the same Cloudflare Tunnel. The mini PC is a cold standby: its tunnel
connector is stopped (two connectors would split traffic), and its backend container must stay
stopped too (two backends poll OASA twice).

Effect on this plan:
- **Deploys are automatic.** A timer on the VPS pulls `ghcr.io/angelospk/athens-transit-rt:latest`
  every 10 minutes and recreates the container when the image changed, so every push to `main`
  is live within about 10 minutes, as CHANGELOG.md says. It waits for `/v1/status` `ok` and rolls
  back to the previous image when that fails; it skips 03:00-06:00 Athens, when the daily stats
  are written. No hand-off message per release.
- `/v2` adds no new state files (geometry and the Stop name table are embedded, aliases are in
  code), so the state directory does not change.
- New outbound calls from the host: none beyond today (STASY CKAN, railway.gov.gr, OASA).

## Decisions (Harold, 2026-10-29)

- **Q1** Path prefix: `/v2/...`.
- **Q2** Cross-mode places: separate Stops + "nearby Stops" list in the Stop card.
- **Q3** Timetable bus Arrivals: yes, in phase 2 (step 3b).
- **Frequent lines** (headway ≤ 20′): the Line card shows a frequency summary instead of
  seen/missed per run: «X από Y σήμερα», runs per hour, and the longest gap. With buses every
  few minutes, the seen window ([-3, +20] min) cannot tell which run a departure was, so
  per-run flags say «some bus left», not «this run ran». Phase 3 (app) and phase 4 (web) build
  this card; the `/v2` backend must give the numbers (runs seen and planned today, per hour,
  longest gap between departures). (Harold, 2026-10-29.)
- (Decided, not asked: rail vehicles come from `/v2/vehicles/rail`, not the bus tiles; Codex
  review 2026-10-29.)
