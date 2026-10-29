# Migration plan (DRAFT): one model for every mode

Status: **draft for Harold's approval, 2026-10-29.** Phase 1 (this design) is done when Harold
approves [GLOSSARY.md](GLOSSARY.md), [CONTRACT-rev8-draft.md](CONTRACT-rev8-draft.md) and
[UI-SPEC-unified.md](UI-SPEC-unified.md). Estimates are working hours of agent-assisted work,
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
| **Single host** (one backend instance; now moving to the Oracle VPS, see below) | All modes go dark together; today metro/tram on the web survive a backend outage only because geometry is bundled | Keep `/v1` and bundled assets until phase 3/4 end; clients cache the last `/v2/lines` and `/v2/stops` for offline display |
| **OASA blocks data-centre IPs** (tested 2026-10-05: every cloud host timed out) | If the VPS cannot reach `telematics.oasa.gr`, bus data stops; `/v2` adds no new OASA calls but depends on the same polling | Not a phase 2 task: the hosting thread must confirm OASA works from the VPS. `/v2` `sources` shows `oasa: stale/down` honestly if it does not |
| **Cloudflare cache** (edge `max-age`; `/v2/stops/{id}` 15 s, vehicles ≤ 30 s, lines/stops 1 h) | An Arrival can be ~15 s + client poll old; an empty "warming up" list could stick for an hour | `at` is absolute and clients count down on their own clock; metro uses `knots`; incomplete responses are cached ≤ 5 s |
| **Cloudflare rate limit** (~30 requests / 10 s per IP) | Shared IPs (mobile CGNAT, offices) may hit `429` with more clients | Budget per client at a z13 view: ~15 tiles / 30 s + 1 stop / 20 s + alerts 1 stop / 30 s each ≈ 0.7 req/s. Back off ≥ 10 s on `429`, as today |
| **Suburban stream load** (~80 KB/s upstream while open) | Kept open by any map viewer if rail were in the tiles, or metro and suburban in one body | Decided: suburban vehicles only in `/v2/vehicles/suburban`, loaded only with the suburban layer on and a corridor in view; metro and tram in `/v2/vehicles/rail`, which opens nothing (review #9, item 11). The stream closes 3 min after the last request, as today |
| **STASY renames stops / railway.gov.gr ids change** | Stored Favourites and Alerts point at dead ids | Name-based STASY ids, alias tables, client repair rule (UI spec §4) |
| **Geometry drift** (OSM track vs STASY stop points) | Trains glide off the drawn line | Snap stops to the track in the build script; test that every STASY stop is within 60 m of its track |

## Hosting impact (thread «Transit backend: state tar και token για μεταφορά στο VPS»)

Read on 2026-10-29, not touched. State on 2026-10-28 22:43 UTC: the backend state tar (260 MB) and
the tunnel token went to the Mac for hand-off; `cloudflared-transit` on the mini PC is stopped
and disabled; `transit.haroldpoi.dev` returned `530` until the VPS connects; the mini PC
backend container still runs and still polls OASA in parallel.

Effect on this plan:
- Phase 2 deploys go to whatever host runs the image after the move. The deploy step ("pull +
  up" in `~/services/atrt`) may become the VPS owner's step, so each phase 2 release needs a
  hand-off message instead of a self-serve deploy.
- `/v2` adds no new state files (geometry is generated into the repo/GitHub Pages, aliases are in
  code), so the state tar format does not change.
- New outbound calls from the host: none beyond today (STASY CKAN, railway.gov.gr, OASA). The
  OASA data-centre block is the one open hosting risk (table above).
- Two backends polling OASA in parallel raise the ban risk until the mini PC container stops;
  that is the hosting thread's decision, not this plan's.

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
