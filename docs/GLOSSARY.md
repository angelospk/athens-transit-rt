# Glossary

One term per concept. Clients, backend, docs and UI copy use these words and no synonyms.

## Language

**Mode**:
The kind of service a passenger rides: bus, trolley, metro, tram or suburban.
_Avoid_: kind, type, transport type

**Line**:
A named service that a passenger recognises and picks, such as bus 040, metro M1 or the suburban service Πειραιάς – Αεροδρόμιο. A Line has exactly one Mode. It always has a name; it has a short **label** (040, M1, T6) only when the operator publishes one.
_Avoid_: route, corridor, γραμμή-κωδικός, route_code

**Label**:
The short public code of a Line, printed on a badge (040, Α1, M2, T6). Optional: suburban Lines have none, and the badge shows the Mode icon, colour and name instead.
_Avoid_: number, line number

**Pattern**:
One ordered sequence of Stops that some runs of a Line follow, in one direction, with its geometry. A Line has one or more Patterns.
_Avoid_: variant, route, direction (a Pattern has a direction; it is not one)

**Stop**:
A place where a passenger boards or leaves a Line, as the passenger picks it on a map. All platforms of the same Mode at one place are one Stop (Σύνταγμα serves M2 and M3 as one Stop); the tram stop Σύνταγμα is another Stop. Bus and trolley Lines share Stops. Two sides of a street are two Stops when the operator numbers them apart (most bus stops).
_Avoid_: station, στάση/σταθμός as separate concepts, platform, stop point

**Platform**:
The track or bay at a Stop where one Arrival takes place. It is a property of an Arrival, never a Stop of its own.
_Avoid_: track, bay, αποβάθρα as a Stop

**Vehicle**:
One bus, trolley or train in service now, shown as one marker on the map. A metro or tram Vehicle is the run itself (no physical train is known); a bus or suburban Vehicle is the physical unit seen by GPS.
_Avoid_: train, bus (as generic terms), unit, car

**Run**:
One journey of a Vehicle along a Pattern from its first to its last Stop at set times.
_Avoid_: trip (except as the upstream field name), δρομολόγιο as a Line

**Position**:
Where a Vehicle is now, with its **position kind**: **gps** when a device on the Vehicle reported it, **estimate** when the timetable places it. An estimate is never shown as a GPS fix.
_Avoid_: location, fix (a fix is one GPS report, a Position may have no fix)

**Arrival**:
One expected moment when a Line's Vehicle is at a Stop, with its headsign. For rail it is the moment the train leaves the Stop; at the last Stop of a Run it is the moment it arrives. Each Arrival has a **source**: **live** when a live signal (GPS or a reported delay) set the time, **timetable** when only the timetable did.
_Avoid_: departure, ETA, prediction, πρόβλεψη as separate concepts

**Delay**:
How much later than the timetable a Vehicle or Arrival is, in seconds; negative when early. Known only from a live signal.
_Avoid_: lateness, καθυστέρηση in minutes as a separate value

**Headsign**:
The destination a passenger reads on the Vehicle or the board for one Arrival or Run.
_Avoid_: destination, to, προς (as a field)

**Source**:
One upstream data supplier (OASA, STASY, railway.gov.gr) and the one adapter that reads it. A Source can fail, change or be replaced; the Lines, Stops and Vehicles it supplies keep their identity.
_Avoid_: feed, provider, upstream (as a model term)

**Source state**:
How fresh a Source's data is now: ok, warming up, stale, indicative (an expired timetable stands in), or down.
_Avoid_: status, health

**Favourite**:
A Stop or Line the passenger saved, of any Mode.
_Avoid_: bookmark, saved stop

**Alert**:
A passenger's request to be told when an Arrival of a chosen Line at a chosen Stop is a set time away, for any Mode.
_Avoid_: rule, notification, reminder (a notification is what an Alert sends)

## Relationships

- A **Line** has one **Mode**, one or more **Patterns**, and a **Label** only when the operator publishes one.
- A **Pattern** is an ordered list of **Stops**; a **Stop** is on one or more Patterns of one or more Lines.
- A **Vehicle** serves one **Line**, runs one **Run** at a time, and has one **Position** with a position kind.
- An **Arrival** joins one **Line**, one **Stop**, one headsign and a time; it names its **Vehicle** only when one is known, and its **Platform** only when the Source gives one.
- Every Line, Stop, Vehicle and Arrival comes from exactly one **Source**; a Source change never changes their identity.
- A **Favourite** and an **Alert** point at a Stop (and, for an Alert, a Line) by identity, whatever the Mode.

## Example dialogue

> **Dev:** "The suburban service to the airport has no number. What does its **Line** badge show?"
> **Domain expert:** "No **Label**, so the Mode icon, its colour and the name Πειραιάς – Αεροδρόμιο."
> **Dev:** "And at Σύνταγμα, is the M2 platform a different **Stop** from the M3 platform?"
> **Domain expert:** "No. One **Stop**; the **Platform** belongs to each **Arrival**."
> **Dev:** "A metro **Vehicle** at Σύνταγμα, is that a GPS **Position**?"
> **Domain expert:** "Never. Its position kind is estimate, and its **Arrivals** have source timetable. The card still shows them, labelled."

## Flagged ambiguities

- "station" was used for metro/suburban places and "stop" for bus places. Resolved: one concept, **Stop**, for every Mode.
- "departure" (rail boards) and "arrival" (bus stop endpoint) named the same thing. Resolved: **Arrival**, defined as the leaving moment for rail.
- "corridor" (railway.gov.gr) and "line" were used for the suburban service. Resolved: **Line**; corridor is only an upstream word.
- "variant" (OASA) and "route" named one stop sequence. Resolved: **Pattern**.
- "trip" was used for both the metro marker and the timetable journey. Resolved: **Run** for the journey, **Vehicle** for the marker.
- One place served by several Modes (Πειραιάς: M1, suburban, tram, buses): separate **Stops**, no wider grouping term (Harold, 2026-10-29).
