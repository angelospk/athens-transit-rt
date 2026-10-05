# Regenerates internal/match/testdata/upstream.json: runs upstream oasa_rt matchers on the
# recorded cycles. Run from the repo root: UPSTREAM=<clone> GTFS_ZIP=<zip> python3 scripts/parity/upstream_match.py
# (needs numpy + scipy).
import json, os, sys
sys.path.insert(0, os.environ.get("UPSTREAM", "../athens-gtfs-realtime"))
from oasa_rt.static import StaticGTFS, service_date_str
from oasa_rt.matcher import Matcher, HungarianMatcher, MemoryMatcher, RouteMapper
rec = json.load(open("internal/match/testdata/cycles.json"))
class FakeTel:
    def route_stops(self, rc): return rec["stops"].get(rc, [])
gtfs = StaticGTFS(os.environ.get("GTFS_ZIP", "data/osy_gtfs.zip"), ["040", "Α1"])
mapper = RouteMapper(gtfs, FakeTel())
methods = {"greedy": Matcher(gtfs, mapper), "hungarian": HungarianMatcher(gtfs, mapper), "memory": MemoryMatcher(gtfs, mapper)}
out = {k: [] for k in methods}
for cyc in rec["cycles"]:
    by_line = {}
    for v in cyc["rows"]:
        by_line.setdefault(v["LINE"], []).append(dict(v))
    for name, m in methods.items():
        res = []
        for line in sorted(by_line):
            for r in m.match_line(line, [dict(v) for v in by_line[line]]):
                res.append([r.vehicle_id, r.line, r.route_id, r.trip.trip_id if r.trip else None,
                            service_date_str(r.service_day) if r.trip else None, r.delay if r.trip else 0,
                            r.next_index if r.trip else 0, r.waiting_at_start])
        out[name].append(res)
json.dump(out, open("./internal/match/testdata/upstream.json", "w"), ensure_ascii=False)
for k, v in out.items(): print(k, [sum(1 for r in c if r[3]) for c in v], "matched of", [len(c) for c in v])
