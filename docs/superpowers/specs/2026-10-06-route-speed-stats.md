# Route speed statistics (later project, not built)

Status: idea, written 2026-10-06. Build only when Harold asks.

## Why

`speed` (contract rev 3) is the vehicle's own recent speed. It is good for the next 30-150 s
on the same street, but it does not know that the next 400 m are a jammed avenue at 08:30 or
an empty one at 23:00. Typical speeds per road segment and time of week would let the map (and
`delay_s` forecasts later) predict motion better, especially for vehicles seen only every 150 s.

## Idea

- **Segment** = a pair of consecutive stops (`from_stop`, `to_stop`) of any stop pattern. Lines
  that share a street share the segment, so data accumulates faster than per shape. Real feed
  (2026-10-06): 9,566 unique stop pairs, 509 patterns, 515 shapes, 8,504 km of shape.
- **Time slot** = day type (weekday, Saturday, Sunday/holiday) × hour = 72 slots. Hour-of-week
  (168 slots) splits the data too thinly for most segments; start with 72.
- **Observation**: the motion tracker already keeps up to 5 fixes per vehicle as distance `s`
  along its shape. For two consecutive accepted fixes on the same track (Δt 20-300 s, both on
  a shape with snapped stops), the mean speed `Δs / Δt` is credited to every segment the
  interval overlaps, weighted by the overlap length. Standing time at stops stays in (it is
  part of the real travel time). Intervals with a reset (new run, implausible jump) are
  skipped.
- **Aggregate** per (segment, slot): exponentially weighted mean speed (half-life ~4 weeks,
  so timetable or road changes fade in) and a saturating sample count. Use a cell only after
  ≥ 10 samples.

## Storage

Per cell: mean speed in dm/s (`uint16`) + count (`uint16`) = 4 bytes.

- Dense: 9,566 segments × 72 slots × 4 B ≈ 2.8 MB. Fine in memory (the service runs at
  ~45 MB of 200 MB).
- Sparse (only cells ever observed; most segments run only some hours): expect ≈ 1-1.5 MB.
- On disk: one file in the state dir, written every 10 min (atomic rename, like the
  telematics metadata), gzip ≈ 0.5 MB. No database.
- Segments are keyed by stop ids, not internal indexes, so a new GTFS keeps the data for
  every unchanged stop pair; pairs that disappear are dropped after the half-life.

## Use

- `/v1/vehicles` and `/v1/lines/{id}`: with a known cell, `path` could carry per-point
  expected speeds, or the backend could return a predicted `s(t)` curve. Keep the contract
  change additive (a new optional field), decided when built.
- Fallback order for motion: own recent `speed` (≤ 2 min old) → segment typical speed →
  no prediction.

## Open questions

- Holidays: use the GTFS calendar exceptions to pick the day type.
- Whether 15-min slots in the peak hours are worth 2× the storage.
