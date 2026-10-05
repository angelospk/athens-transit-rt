# athens-transit-rt API

Live positions and GTFS-Realtime for OASA buses and trolleys in Athens. Free to use, no key.
This page is for external users. The full field reference is [`CONTRACT.md`](CONTRACT.md).

> **Ελληνικά, σύντομα.** Δημόσιο API χωρίς κλειδί. GTFS-Realtime για όλο το δίκτυο στο
> `/v1/gtfs-rt/`, JSON ανά γραμμή στο `/v1/lines/{γραμμή}`. Σεβαστείτε το caching: τα δεδομένα
> ανανεώνονται κάθε 30-150 s, άρα πιο συχνά αιτήματα δεν φέρνουν νέα δεδομένα.

Base URL: `https://transit.haroldpoi.dev`

The instance runs on one home machine (OASA does not answer data-centre addresses), behind
the Cloudflare cache. There is no uptime guarantee. For anything important, run your own
instance (see [Self-hosting](../README.md#self-hosting-docker)).

## Endpoints

| Method and path | Content | `Cache-Control: max-age` |
|---|---|---|
| `GET /v1/gtfs-rt/vehicle_positions.pb` | GTFS-RT VehiclePositions, whole network (protobuf) | 15 s |
| `GET /v1/gtfs-rt/trip_updates.pb` | GTFS-RT TripUpdates, whole network (protobuf) | 15 s |
| `GET /v1/gtfs-rt/vehicle_positions.json`, `trip_updates.json` | the same feeds as JSON, for debugging | 15 s |
| `GET /v1/lines/{line_id}` | vehicles of one line, simple JSON | until the next poll of the line (5-300 s) |
| `GET /v1/status` | service health and GTFS version | 10 s |

Static data (line list, shapes, stops) is on GitHub Pages:
`https://angelospk.github.io/athens-transit-rt/static/v1/lines.json` and
`.../static/v1/lines/{line_id}.json`.

All responses are UTF-8, send `Access-Control-Allow-Origin: *`, and are gzip-compressed when
the request allows it. Times are Unix seconds.

## Examples

```bash
# Health and GTFS version
curl -s https://transit.haroldpoi.dev/v1/status
# {"ok":true,"gtfs_version":"2026-07-08","gtfs_expires":"2026-10-06","updated_at":1791229867,"lines_active":236,"budget_rps":4}

# Vehicles of line 040
curl -s https://transit.haroldpoi.dev/v1/lines/040

# Whole-network feeds
curl -s --compressed -o vehicle_positions.pb https://transit.haroldpoi.dev/v1/gtfs-rt/vehicle_positions.pb
curl -s --compressed https://transit.haroldpoi.dev/v1/gtfs-rt/trip_updates.json | jq '.entity | length'
```

Python:

```python
import requests
from google.transit import gtfs_realtime_pb2  # pip install gtfs-realtime-bindings

feed = gtfs_realtime_pb2.FeedMessage()
feed.ParseFromString(requests.get("https://transit.haroldpoi.dev/v1/gtfs-rt/vehicle_positions.pb").content)
for e in feed.entity[:5]:
    v = e.vehicle
    print(v.vehicle.id, v.trip.route_id, v.trip.trip_id, v.position.latitude, v.position.longitude)
```

## Per-line JSON

`line_id` is the OASA line number in Greek uppercase (`040`, `550`, `Α1`, `Χ96`),
percent-encoded. Latin look-alikes also work (`A1` → `Α1`).

```json
{
  "line": "040",
  "updated_at": 1791229815,
  "next_update_at": 1791229873,
  "vehicles": [
    {
      "id": "44537",
      "lat": 37.974551, "lon": 23.734165,
      "bearing": 41,
      "position_at": 1791229792,
      "route_code": "3923",
      "variant": "5513",
      "trip_id": "938_day_1_1303_2300",
      "trip_label": "23:00 ΣΥΝΤΑΓΜΑ → ΠΕΙΡΑΙΑΣ",
      "delay_s": 0,
      "next_stop_id": "10341"
    }
  ]
}
```

- `trip_id`, `trip_label`, `delay_s` and `next_stop_id` are `null` when the vehicle is not
  matched to a scheduled trip. `bearing` and `variant` may be `null`.
- Refetch at `next_update_at`, not earlier: until then the answer does not change.
- Asking for a line raises its polling priority for about 10 minutes (about every 30 s).

## GTFS-Realtime details

- `trip_id`, `route_id` and `stop_id` refer to the official OASA static GTFS
  ([osy_gtfs.zip on data.gov.gr](https://data.gov.gr/dataset/fb049bb1-aea6-4443-95fa-8b941dd6a057/resource/119db488-16ea-4c76-b560-41c472872390/download/osy_gtfs.zip)). Use the version in `/v1/status`.
- The feeds are `FULL_DATASET`. `header.timestamp` is the build time, or the newest vehicle fix if that is later.
- VehiclePositions has one entity per vehicle (`vehicle-{id}`). A vehicle that is not matched
  to a trip has a trip descriptor with only `route_id`.
- TripUpdates has one entity per matched trip, with one `stop_time_update` (the next stop)
  that carries the current delay. Consumers propagate it to later stops.
- Matching: each vehicle is assigned to a scheduled trip by its position along the route
  (Hungarian assignment, with departure time and the last 15 minutes of positions to keep
  trips stable when buses bunch). OASA itself publishes no trip ids.

### Validation

On 2026-10-05 (20:06-20:08 UTC, 5 samples 30 s apart, 451 vehicles), the
[MobilityData GTFS Realtime validator](https://github.com/MobilityData/gtfs-realtime-validator)
reported:

| Feed | Errors | Warnings |
|---|---|---|
| TripUpdates | 0 | 0 |
| VehiclePositions | 0 | W006, W009 for unmatched vehicles only (19 of 451, about 4%) |

The warnings come from the `route_id`-only trip descriptor of unmatched vehicles. It is
intended: the line is known even when the trip is not.

To repeat it (batch mode, needs the static GTFS that the feed uses):

```bash
mkdir -p rt/vp && curl -sL -o rt/gtfs.zip https://data.gov.gr/dataset/fb049bb1-aea6-4443-95fa-8b941dd6a057/resource/119db488-16ea-4c76-b560-41c472872390/download/osy_gtfs.zip
for i in 1 2 3 4 5; do
  curl -s -o "rt/vp/VehiclePositions-$(date -u +%Y-%m-%dT%H-%M-%SZ).pb" https://transit.haroldpoi.dev/v1/gtfs-rt/vehicle_positions.pb
  sleep 30
done
docker run --rm -v "$PWD/rt:/w" --entrypoint java ghcr.io/mobilitydata/gtfs-realtime-validator -Xmx5g \
  -cp /app/gtfs-realtime-validator-webapp-1.0.0-SNAPSHOT.jar edu.usf.cutr.gtfsrtvalidator.lib.Main \
  -gtfs /w/gtfs.zip -gtfsRealtimePath /w/vp -sort name
# results: rt/vp/*.pb.results.json (empty list = no errors and no warnings)
```

## Errors and limits

| Status | Body | Meaning |
|---|---|---|
| `404` | `{"error":"unknown_line"}` | no such line |
| `404` | `{"error":"not_found"}` | unknown path or feed name |
| `503` | `{"error":"warming_up"}` | the line was not polled yet after a start; retry in a few seconds |
| `503` | `{"error":"feed_unavailable"}` | the GTFS-RT feed is not built yet after a start |
| `429` | (from Cloudflare) | more than about 30 requests per 10 s from one IP; wait at least 10 s |

- `/v1/status` reports `ok: false` when no line poll succeeded in the last 5 minutes. During an
  OASA outage the last good data stays served; check `updated_at`.
- When the static GTFS has expired (`gtfs_expires` in the past), vehicles still appear with
  their line, but `trip_id` is `null` until OASA publishes a new GTFS.
- Please poll the GTFS-RT feeds at most every 15 s and each line at most once per
  `next_update_at`. Faster polling gets the same cached data.
