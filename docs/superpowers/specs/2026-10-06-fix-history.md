# Fix history (storage design, not built)

Status: design, written 2026-10-06. Build only when Harold asks. It is the raw input for
[route speed statistics](2026-10-06-route-speed-stats.md) and for later delay statistics.

## What to keep

One row per **new** GPS fix of a matched or unmatched vehicle, written after `onLine` matched
a line poll:

| column | type | note |
|---|---|---|
| `fix_t` | int64 unix s | OASA `CS_DATE` (the fix time, not the poll time) |
| `line` | string | canonical line id |
| `route_code` | string | OASA route code |
| `veh` | string | vehicle number |
| `lat`, `lon` | int32 | degrees × 1e6 |
| `gtfs` | string | GTFS version (`gtfs_version` of `/v1/status`) |
| `trip_id`, `shape_id` | string | empty when unmatched |
| `s_m` | int32 | distance along the shape, m (−1 when unmatched) |
| `delay_s` | int32 | null when unmatched |

**Dedup:** OASA repeats a fix until the vehicle reports again, so each poll returns many fixes
already seen. Keep a map `veh → last fix_t` (bounded by the ~1,000 vehicles on the road) and
write a row only when `fix_t` is newer. Watched lines are polled every ~30 s, so the vehicle
side, not the poll rate, limits how many rows we get.

**Identity:** `trip_id` and `shape_id` belong to one GTFS version. Every row carries `gtfs`,
so statistics can join on the right shapes after a feed change. Stop pairs (see the speed
statistics spec) are stable across versions; shape indexes are not.

## Volume

Upper bound at 4 req/s: about 2.4 vehicles per answer (≈ 900 vehicles over ≈ 370 polled
routes) gives about 10 fixes/s before dedup, so ≤ 860 k rows/day. As CSV, a row is about
80 B → ≤ 70 MB/day raw, about 10-15 MB/day gzip. The real number after dedup is lower.
Measure it in the first week before you choose the retention.

## Storage

- Hourly CSV files in the state dir: `history/2026-10-06T08.csv` (append, no fsync per
  row). When the hour ends, gzip the file to `.csv.gz` and delete the plain file. A crash
  loses at most the unflushed buffer of the current hour.
- No database in the serve path. SQLite (`modernc.org/sqlite`, already used by `record`) is
  for analysis: `atrt history-import` loads the gzip files on a dev machine.
- **Retention and disk cap:** delete files older than 30 days, and the oldest files first
  when `history/` exceeds a size limit (default 500 MB, flag `--history-max-mb`). Check once
  per hour.
- **Non-blocking:** `onLine` sends rows to a buffered channel (e.g. 4,096 rows). One writer
  goroutine owns the file. When the channel is full, drop the rows and count them
  (`history_dropped` in `/metrics`). Polling and publishing never wait for the disk.
- Off by default (`--history`), because self-hosters may not want the disk use. Note that
  `mem_limit: 200m` is not affected: the buffer is about 4,096 × 100 B.

## Not decided

- Whether to also keep OASA ETA answers (`oasa_eta` in `record`) for arrival-time checks.
- Whether to keep unmatched fixes (they help find mapping gaps, but they double the volume).
