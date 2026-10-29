# Phase 2 decisions (for Harold to read)

Choices made while Harold was away, during the unattended phase 2 sessions (2026-10-29). Each
one: choice, alternative, reason. The docs did not settle these points.

## Session 1 (oasa adapter, `/v2` framework)

1. **Adapters live in `internal/server`; only the model has its own package (`internal/model`).**
   Alternative: one package per adapter. Reason: the oasa adapter reads the App's line polls,
   matcher and stop boards under the App's locks; another package would need a wide new API.
   The `Adapter` interface (`internal/server/v2.go`) still keeps one adapter per Source.
2. **An unmatched bus on its way is still an Arrival** (`run`, `scheduled_at`, `delay_s` null,
   `source: live`). Alternative: drop it, as the contract text "not listed at all, as in rev 7"
   says. Reason: rev 7 (`/v1/stops/{id}`, PR #6) does list unmatched buses, and the migration
   plan requires `/v2` to carry the same data as `/v1`.
3. **`status: delayed` from 5 minutes of delay** (`model.DelayedFrom`, every Source); below
   that `running`; without a live signal `scheduled`. Alternative: oasa never `delayed`. Reason:
   one rule for every mode, so clients need no per-mode logic.
4. **Vehicle `headsign` is nullable.** An unmatched bus that cannot be placed on a shape has no
   headsign. Matched: the trip's headsign; unmatched but placed: the headsign of the shape's most
   common trip (as the stop board).
5. **oasa Stop `name_en` is `null`.** The OASA GTFS and the telematics metadata in this repo have
   no English stop names. Alternative: a new telematics call. Reason: `/v2` adds no OASA calls
   (data-centre ban risk).
6. **oasa Stops in `/v2/stops` are the stops some line calls at**; stops with no trips are left
   out.
7. **oasa Source state is never `down`**: the static feed always stands in. `warming_up` until
   the feed loads and the first poll answers; `stale` when no poll succeeded for 5 min (as
   `/v1/status ok:false`); `indicative` on an expired GTFS.
8. **Bus Run (`/v2/runs/oasa:run:<trip>:<date>`)**: any trip of yesterday, today or tomorrow that
   runs that day. Stop times are the arrival times. With a matched bus (fix < 5 min old): its
   delay on every stop, `passed` for the stops behind it, `at` never before now ahead of it.
   Without a bus: the timetable, no stop `passed` (no signal says the bus left).
9. **ETags on `/v2` JSON are weak** (`W/"…"`): the Handler may gzip the same body. Prebuilt
   `/v2/vehicles` and tiles keep the `/v1` strong ETags per encoding.
10. **Step 3b (timetable bus Arrivals) reuses the rev 7 board rows** (`scheduleArrivals`, PR #6):
    `/v2` only widens them to 2 h and cuts 4 per Line + headsign, 30 in all. `/v1` keeps 1 h and
    3 per variant (a shared `stopRows` feeds both).
