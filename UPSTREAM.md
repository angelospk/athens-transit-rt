# Upstream tracking

This project is a Go port of
[foivospro/athens-gtfs-realtime](https://github.com/foivospro/athens-gtfs-realtime) (MIT).
It is not a git fork. The `upstream-watch` workflow compares the SHA below with upstream
`main` every day and opens one issue per batch of new upstream commits.

After you port upstream changes, update this line (the workflow reads it):

last-ported: 6dfd8dccf1b93158d6348c63f9942f21ca05619a

Reviewed, not ported (2026-10-29, #2): 0c444b5..6dfd8dc add an HMM matcher (behind our memory
matcher in upstream's own evaluation) and a recent-segment arrival model (on our history it
does not beat the 14-day typical speeds we use: median error 0.79 vs 0.75 min); 6dfd8dc moves
upstream's live feed to the memory matcher, which we already run.
