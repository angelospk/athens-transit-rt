# Contract rev 8 (record of the approved draft)

Status: **approved by Harold 2026-10-29 and implemented.** The current text is the
[`/v2` section of CONTRACT.md](CONTRACT.md#unified-api-v2-rev-8) and `openapi/openapi.yaml`;
clients follow those, not this file. Terms: [GLOSSARY.md](GLOSSARY.md).

The approved draft is in git: `git show d05df0d:docs/CONTRACT-rev8-draft.md` (as written) and
`git show 326ceaa:docs/CONTRACT-rev8-draft.md` (as approved). A single copy keeps the contract
from drifting; this file only lists what the implementation changed.

## What changed from the approved draft

- Choices the draft left open, made during the implementation:
  [DECISIONS-phase2.md](DECISIONS-phase2.md) (1-29). The ones a client sees are summed up at the
  top of the `/v2` section of CONTRACT.md.
- Review of 2026-10-29 ([issue #9](https://github.com/angelospk/athens-transit-rt/issues/9)):
  - `stale` and `indicative` responses are cached long; only `warming_up` and `down` make a
    response incomplete. `/v2/stops` has a strong `ETag` (items 1, 12).
  - Suburban trains are in `/v2/vehicles/suburban`; `/v2/vehicles/rail` is metro and tram only
    and opens nothing upstream (item 11).
  - metro and tram Stop names and English names come from a hand-checked OSM table, with
    permanent aliases for renamed Stops (items 4, 7).
  - test cases for the `motion.knots` client rule: `fixtures/v2/motion-cases.json` (item 9).
