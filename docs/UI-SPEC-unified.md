# UI spec (DRAFT): one stop card, one line card, one vehicle marker

Status: **draft for Harold's approval, 2026-10-29.** For the app (Flutter, `athens-transit-app`)
and the web (Svelte, `athens-transit-map`). Data: [CONTRACT-rev8-draft.md](CONTRACT-rev8-draft.md).
Terms: [GLOSSARY.md](GLOSSARY.md). Text spec only; the layout follows today's bus StopCard, so no
mockup is needed.

## 0. Shared pieces

**Line badge** (used in all three components):
- `label` present: rounded rectangle, fill `color`, text `label` in `text_color`, bold.
- `label` null: mode icon (white) on `color`, then `name` in plain text beside the badge,
  truncated with an ellipsis at the card width.
- Mode icons: bus, trolley, metro (Μ in a circle), tram, suburban (train). One icon set, both
  clients.

**Freshness tag** (one rule, all modes), shown on each Arrival row and on the vehicle sheet:
| Data | Tag | Style |
|---|---|---|
| Arrival `source: live` | nothing extra; delay as `+3′` / `−1′` when `delay_s` ≠ null and \|delay\| ≥ 60 s | normal |
| Arrival `source: timetable` | «πρόγραμμα» | muted text, minutes in italic |
| Vehicle `position.kind: estimate` | «εκτίμηση από πρόγραμμα» | marker outline dashed |
| GPS fix older than 90 s | «πριν N′» | marker faded |
| Source state ≠ ok | one line under the card title: «Μετρό: ενδεικτικά δρομολόγια», «Προαστιακός: σύνδεση…», «ΟΑΣΑ: χωρίς νέα δεδομένα 6′» | muted banner |

Rule: a field that is `null` is never an empty gap. Either the row drops that one part (no
platform → no "Αποβ." chip) or the tag above says why a live value is absent. The card itself
never hides.

**Time format**: minutes until `at`: «τώρα» under 60 s, «N′» up to 59, then «HH:MM».

## 1. Stop card (any Stop)

Opens on: tap of a stop/station on the map, a Favourite, a search result, an Alert hit.
Data: `GET /v2/stops/{id}`, poll every 20 s while visible (the response is cached 15 s), paused
when hidden.

Layout, top to bottom:
1. **Title row**: mode icon(s) from `stop.modes`, `stop.name`, star button (Favourite toggle).
2. **Source line** (only when a Source state ≠ ok): see §0.
3. **Line chips**: one Line badge per `lines[]`, tap = filter the rows to that Line (tap again =
   all). Tap-and-hold = open the Line card.
4. **Arrival rows**, grouped by Line + headsign, ordered by the first `at`:
   `[badge] → headsign      3′  12′  25′   [bell]`
   - Up to 3 times per group. Second line, small: platform chip «Αποβ. 2» when `platform`
     present; vehicle label «#1226» when present.
   - Tap a time → Line card focused on that Run (`run`); also focus the map on the Vehicle
     when `vehicle` is present (a Run not yet started has none).
   - Bell → create an Alert for this Stop + Line (+ headsign) (§4).
5. **Empty state**: «Καμία άφιξη τις επόμενες 2 ώρες» (and the Source line if a Source is down).
6. **Footer**: «Κοντινές στάσεις»: up to 4 Stops of other modes within 400 m (from the
   `/v2/stops` index), each as icon + name; tap opens its Stop card. This replaces today's
   "nearby bus stops" block of `StationPanel` and the web's "across the street" link.

Mode differences are data, not layout: a metro stop shows rows marked «πρόγραμμα»; a suburban
stop shows platform chips; a bus stop shows delays.

## 2. Line card (any Line, optionally focused on one Run)

Opens on: chip hold in a Stop card, tap of a vehicle marker, search, «Οι γραμμές μου».
Data: `GET /v2/lines/{id}` (cached 1 h) + `GET /v2/lines/{id}/vehicles` (poll at
`next_update_at`); with a Run focused, `GET /v2/runs/{id}` every 15 s.

Layout:
1. **Header**: Line badge (§0), `name`, star (Favourite Line), mode name in small text.
2. **Pattern switch**: one segmented control per headsign of `patterns` (two directions
   usually; more when the Line has branches).
3. **Stop list** of the chosen Pattern, vertical line in `color`, one dot per Stop:
   - With a focused Run: each row shows `at` (and «πρόγραμμα» if `source: timetable`), passed
     Stops greyed, the Vehicle's place as a marker between rows.
   - Without: vehicles of this Pattern drawn as small markers between the rows they are between
     (from `next_stop`).
   - Tap a Stop → Stop card.
4. **Timetable link** (bus only today: `/v1/lines/{id}/schedule`): «Πρόγραμμα ημέρας». Hidden
   for modes without a day timetable endpoint; adding one later is additive.

This one card replaces web `TrainCard` + the bus InfoCard stop list, and the app's `LinePanel`.

## 3. Vehicle marker (any Vehicle)

- **Shape**: circle with the mode icon, fill Line `color`, a heading wedge when `bearing` ≠ null.
  Size: bus 18 px, rail 22 px (rail vehicles are fewer and longer).
- **Label** (zoom ≥ 14): Line `label`, or for a label-less Line the mode icon only.
- **Position kind**: `gps` solid outline; `estimate` dashed outline (§0). Stale GPS fades at
  90 s and hides at 600 s (today's web rule for trains, now for all).
- **Motion**: one mover for every mode, the client rule of the contract (`knots` by time,
  else `path_speeds`/`speed`). Metro and tram glide along the track between stations and dwell at
  each station; nothing jumps on the 30 s refresh unless the new snapshot disagrees by > 150 m.
- **Tap**: bottom sheet: badge, → headsign, freshness tag, delay, vehicle label, «Επόμενη:
  <Stop>», button «Διαδρομή» → Line card focused on this Run.
- **Layers**: one vehicles layer, fed by two requests: bus tiles (`/v2/vehicles/tiles`) and
  `/v2/vehicles/rail`. Mode toggles (bus / metro+tram / suburban) in the map menu; the rail
  request runs only while a rail toggle is on (it opens the suburban stream). Default on: all.

## 4. Favourites and Alerts (any Stop)

- **Favourite Stop**: stored as `{id, name, modes, lat, lon}`. `id` is the key; `name`/`lat`/`lon`
  are a snapshot for display offline and for repair: if `/v2/stops/{id}` answers `404`, the
  client looks up the nearest Stop with the same folded name within 200 m in `/v2/stops` and
  rewrites the id. If the response `stop.id` differs from the stored id (alias), rewrite it.
- **Favourite Line**: `{id, label, name, mode, color}`, same repair rule. Today's 5-line cap
  stays for bus Lines only (it protects the OASA budget); rail Lines cost no upstream request.
- **Alert**: `{stop, line, headsign?, lead_min, days?, window?, until?}`. The client polls
  `/v2/stops/{stop}` every 30 s inside the window and fires when the first matching Arrival has
  `at − now ≤ lead_min`. The notification text includes «(πρόγραμμα)» when the Arrival's
  `source` is `timetable`, so a metro Alert is honest that it is a timetable time.
- Today's bus "stops away" Alerts (count of stops before the boarding stop) stay as they are on
  `/v1`; they need GPS and `next_stop`. In the new editor that option is offered only when the
  Line's mode is bus/trolley.

## 5. Copy (Greek, the app's tone)

| Key | Text |
|---|---|
| timetable tag | πρόγραμμα |
| estimate tag | εκτίμηση από πρόγραμμα |
| no arrivals | Καμία άφιξη τις επόμενες 2 ώρες |
| source warming | {Πηγή}: σύνδεση… |
| source stale | {Πηγή}: χωρίς νέα δεδομένα {N}′ |
| source indicative | {Πηγή}: ενδεικτικά δρομολόγια |
| source down | {Πηγή}: μη διαθέσιμο |
| platform chip | Αποβ. {n} |
