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

## Session 1 (stasy adapter, rail geometry)

11. **Rail geometry is embedded, not rebuilt here** (`internal/railgeo`: `metro.json`,
    `suburban.json` copied from `athens-transit-map` commit 370af6a, `go:embed`). Alternative:
    port `scripts/metro*.ts` to Go. Reason: smallest step; the files change only when OSM or a
    corridor changes. To refresh: run the scripts in `athens-transit-map`, copy the two files.
    The map repo keeps its copies until phase 4 removes them.
12. **stasy Stop ids use `rail.Fold`**, the fold the `/v1` departures already use: lower case, no
    accents, final sigma as σ, words joined by `-` ("stasy:πειραιασ-metro",
    "stasy:συνταγμα-(κεντρο)-tram"). An unfolded id in a request resolves to the folded one.
13. **stasy Stop `name`** is the first platform's name in the feed as written (metro names are
    upper case, tram names mixed case). `name_en` is `null`: the STASY files have no English names.
14. **stasy Line `name`** is the feed's `route_long_name` ("ΓΡΑΜΜΗ 1"), `"Γραμμή N"` when empty;
    `name_en` null; mode from `route_type` (0 tram, else metro; `T` prefix when missing).
15. **A train's last stop is not an Arrival** (metro/tram and, for the same reason, bus): nobody
    boards there. `/v1` metro departures still list it.
16. **Patterns** are the distinct Stop sequences of a route's trips, most trips first
    (`stasy:M2-0`, `-1`, …). Direction 0 = the most common one's direction; a pattern whose ends
    run the other way along it is 1. Pattern numbers can change with a new timetable (Pattern
    ids are not stored by clients). Shape: the OSM track through the stops; when a stop is
    farther than 150 m from every track (3 of 14 M3 patterns in the 2026-10 feed), straight lines.
17. **stasy Run `passed`** = its timetable time is past (there is no other signal). Its Vehicle is
    the Run itself while the Run is under way.
18. **`/v2/vehicles/rail` is built per request** (75 trains in 2 ms on the real feed), cached
    15 s; the knots move the trains in between.
19. **stasy Source state**: `warming_up` until a timetable loads, `indicative` when it does not
    cover today, else `ok`. `stale` (daily check failed) is not reported yet: the store does not
    expose its last check.

## Session 2 (proastiakos adapter)

20. **proastiakos Lines, Stops and Patterns come from the embedded corridor table**
    (`railgeo.Suburban()`), so the Source is never `warming_up` and `line` is never `null`.
    Alternative: wait for `corridor-stations` (the contract example). Reason: the table is
    already in the binary; a fetch would only delay the same data.
21. **Stop ids are the ids of the embedded table**, not the `corridor-stations` primary. They are
    the same for 44 of 45 stations; Άγιος Γεώργιος is `…cf4f` in the table and `…cfeb` upstream.
    Reason: ids must not depend on a fetch. Both other ids (`…cfeb`, the airport's `…cec7`) are
    permanent aliases in code (`subAliases`); any other id that the cached station table merges
    with a Stop also resolves.
22. **A board run's Line is its own `corridor` field** (the board has one; found on the real
    board of 2026-10-29). Runs of other corridors (ATHLIA, E85, THESLAR …) are not Arrivals,
    even at Athens stations. Only a run with no corridor field falls back to "the corridor with
    most of its stations". Alternative from session 1: always count stations. Reason: 6 regional
    runs share CHALKIDA stations but are not Proastiakos.
23. **A board delay counts only for a run under way** (`in_progress`) or a stop with its own
    delay, and only while the board is ≤ 10 min old. The real board gives `delay: 0` for every
    run not started; that is not an observation, so those Arrivals are `timetable`.
24. **A train on the stream with no delay keeps `source: timetable`** but names its `vehicle`.
    GPS alone does not set `at`.
25. **ose.mpass is not used by `/v2`.** It has no corridor, so no Line can be given. When the
    board fails and none is cached, Arrivals are empty and the Source is `down`.
26. **Stale rules**: board > 30 min old *and* its last refresh failed (a board nobody asked for
    is not stale), or the stream is open and its last event is > 2 min old. A stream that never
    sent an event is not reported stale.
27. **Vehicles**: trains of CHALKIDA, KIATO, PIRAIR only, freight left out. Next station from the
    stream's `nextStationId` (new non-JSON field `SuburbanTrain.NextID`; `/v1` JSON unchanged),
    else its name folded. Direction: where the next station lies ahead on the track; with none,
    the GPS course against the track. A train > 300 m from its corridor (seen: a PIRAIR train
    parked at Κιάτο) keeps its GPS position with `motion: null`. `run` is set only when today's
    cached board has the train.
28. **Direction 0 is the order of the corridor file**: PIRAIR-0 runs Αεροδρόμιο → Πειραιάς,
    CHALKIDA-0 Χαλκίδα → Αθήνα. The Pattern headsign says which.
29. **`/v2/runs/proastiakos:…` does not open the stream** (`TrainStream.Peek`); stop and line
    vehicle requests and `/v2/vehicles/rail` do, as the contract table says. Only today's date is
    a Run (the board is per day).
