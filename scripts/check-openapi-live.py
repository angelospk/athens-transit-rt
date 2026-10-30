#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["pyyaml", "jsonschema>=4.18", "gtfs-realtime-bindings"]
# ///
"""Calls every endpoint of docs/openapi/openapi.yaml on the live servers and checks the answers.

For each case: the status code, the Content-Type, CORS (`Access-Control-Allow-Origin: *`) and
the body against the response schema. The check is stricter than the spec: a JSON field that
the spec does not document fails, in every object of every response, nested ones too (the spec
must list every field). `.pb` feeds must parse as
a GTFS-Realtime FeedMessage. Every path of the spec must have a case.

Then the /v2 data checks: every mode has Lines, a bus and a metro Stop have Arrivals, no
Source is down, metro Stops have their proper names (one in capitals is missing from
internal/railgeo/stasy_stops.json), and the rail trains' motion.knots keep the contract's
rules. Run daily by .github/workflows/live-check.yml, which opens an issue on a failure; run it
at a time with service (the Arrival checks need trains and buses running).

    uv run scripts/check-openapi-live.py            # or: scripts/check-openapi-live.py
"""

import copy
import json
import pathlib
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

import yaml
from google.transit import gtfs_realtime_pb2
from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

SPEC = pathlib.Path(__file__).resolve().parent.parent / "docs/openapi/openapi.yaml"

# (spec path, concrete path, expected status[, schema of the closed spec to use instead of the
# response schema: a tile must hold only entries of its zoom]). Error cases use answers that do not depend
# on the moment (no warming_up, no feed_unavailable). A {name} in a path is a value an earlier
# case's answer gave (CAPTURE); an optional one (OPTIONAL) that is missing skips the case.
SYNTAGMA_METRO = "/v2/stops/stasy:%CF%83%CF%85%CE%BD%CF%84%CE%B1%CE%B3%CE%BC%CE%B1-metro"
CASES = [
    ("/v1/status", "/v1/status", 200),
    ("/healthz", "/healthz", 200),
    ("/v1/lines/{line_id}", "/v1/lines/040", 200),
    ("/v1/lines/{line_id}", "/v1/lines/A1", 200),
    ("/v1/lines/{line_id}", "/v1/lines/%CE%A796", 200),
    ("/v1/lines/{line_id}", "/v1/lines/ZZZ", 404),
    ("/v1/vehicles", "/v1/vehicles", 200),
    ("/v1/vehicles/tiles/{z}/{x}/{y}", "/v1/vehicles/tiles/13/4636/3160", 200, "TileZ13"),
    ("/v1/vehicles/tiles/{z}/{x}/{y}", "/v1/vehicles/tiles/9/289/197", 200, "TileZ9"),
    ("/v1/vehicles/tiles/{z}/{x}/{y}", "/v1/vehicles/tiles/13/1/1", 404),
    ("/v1/vehicles/tiles/{z}/{x}/{y}", "/v1/vehicles/tiles/13/4636/3160?x", 400),
    ("/v1/gtfs-rt/{feed}", "/v1/gtfs-rt/vehicle_positions.pb", 200),
    ("/v1/gtfs-rt/{feed}", "/v1/gtfs-rt/trip_updates.pb", 200),
    ("/v1/gtfs-rt/{feed}", "/v1/gtfs-rt/vehicle_positions.json", 200),
    ("/v1/gtfs-rt/{feed}", "/v1/gtfs-rt/trip_updates.json", 200),
    ("/v1/gtfs-rt/{feed}", "/v1/gtfs-rt/alerts.pb", 404),
    ("/v1/stats", "/v1/stats", 200),
    ("/v1/stats/days/{date}", "/v1/stats/days/{latest}", 200),
    ("/v1/stats/days/{date}", "/v1/stats/days/2026-13-01", 400),
    ("/v1/stats/days/{date}", "/v1/stats/days/2020-01-01", 404),
    ("/static/v1/lines.json", "/static/v1/lines.json", 200),
    ("/static/v1/lines/{line_id}.json", "/static/v1/lines/040.json", 200),
    ("/static/v1/lines/{line_id}.json", "/static/v1/lines/%CE%911.json", 200),
    ("/v1/lines/{line_id}/schedule", "/v1/lines/040/schedule", 200),
    ("/v1/lines/{line_id}/schedule", "/v1/lines/040/schedule?date=1999-01-01", 400),
    ("/v1/stops/{stop_id}", "/v1/stops/240084", 200),
    ("/v1/stops/{stop_id}", "/v1/stops/nosuchstop", 404),
    ("/v1/stops/{stop_id}/arrivals", "/v1/stops/240084/arrivals", 200),
    ("/v1/stops/{stop_id}/arrivals", "/v1/stops/nosuchstop/arrivals", 404),
    ("/v1/rail/stasy/vehicles", "/v1/rail/stasy/vehicles", 200),
    ("/v1/rail/stasy/departures", "/v1/rail/stasy/departures?stop=%CE%A3%CE%A5%CE%9D%CE%A4%CE%91%CE%93%CE%9C%CE%91", 200),
    ("/v1/rail/stasy/departures", "/v1/rail/stasy/departures", 400),
    ("/v1/rail/stasy/trips/{id}", "/v1/rail/stasy/trips/{stasy_trip}", 200),
    ("/v1/rail/stasy/trips/{id}", "/v1/rail/stasy/trips/nosuchtrip", 404),
    ("/v1/rail/suburban/stations", "/v1/rail/suburban/stations", 200),
    ("/v1/rail/suburban/stations/{id}/departures", "/v1/rail/suburban/stations/{sub_station}/departures", 200),
    ("/v1/rail/suburban/vehicles", "/v1/rail/suburban/vehicles", 200),
    ("/v1/rail/suburban/trains/{no}", "/v1/rail/suburban/trains/{sub_train}", 200),
    ("/v2/lines", "/v2/lines", 200),
    ("/v2/lines/{id}", "/v2/lines/oasa:040", 200),
    ("/v2/lines/{id}", "/v2/lines/stasy:M2", 200),
    ("/v2/lines/{id}", "/v2/lines/proastiakos:PIRAIR", 200),
    ("/v2/lines/{id}", "/v2/lines/oasa:NOPE", 404),
    ("/v2/lines/{id}/vehicles", "/v2/lines/oasa:040/vehicles", 200),
    ("/v2/lines/{id}/vehicles", "/v2/lines/stasy:M2/vehicles", 200),
    ("/v2/stops", "/v2/stops", 200),
    ("/v2/stops/{id}", "/v2/stops/oasa:240084", 200),
    ("/v2/stops/{id}", SYNTAGMA_METRO, 200),
    ("/v2/stops/{id}", "/v2/stops/zzz:1", 404),
    ("/v2/runs/{id}", "/v2/runs/{run}", 200),
    ("/v2/runs/{id}", "/v2/runs/stasy:run:nosuchtrip:2020-01-01", 404),
    ("/v2/alerts", "/v2/alerts", 200),
    ("/v2/vehicles", "/v2/vehicles", 200),
    ("/v2/vehicles/tiles/{z}/{x}/{y}", "/v2/vehicles/tiles/13/4636/3160", 200),
    ("/v2/vehicles/tiles/{z}/{x}/{y}", "/v2/vehicles/tiles/13/1/1", 404),
    ("/v2/vehicles/rail", "/v2/vehicles/rail", 200),
    ("/v2/vehicles/suburban", "/v2/vehicles/suburban", 200),  # no train at night: an empty list
]

# Values that later cases need, taken from the answer of a case: path -> (name, getter).
CAPTURE = {
    "/v1/stats": ("latest", lambda d: d.get("latest")),
    "/v1/rail/stasy/vehicles": ("stasy_trip", lambda d: next((v["trip"] for v in d.get("vehicles") or []), None)),
    "/v1/rail/suburban/stations": ("sub_station", lambda d: next((s["id"] for s in d.get("stations") or []), None)),
    "/v1/rail/suburban/vehicles": ("sub_train", lambda d: next((t["train"] for t in d.get("trains") or []), None)),
    SYNTAGMA_METRO: ("run", lambda d: next((a["run"] for a in d.get("arrivals") or [] if a.get("run")), None)),
}
OPTIONAL = {"sub_train"}  # no suburban train runs at night

def allof_parts(spec):
    """Names of the component schemas used as a part of an allOf (VehicleBase, Motion)."""
    parts = set()

    def walk(node):
        if isinstance(node, dict):
            for x in node.get("allOf", []):
                if "$ref" in x:
                    parts.add(x["$ref"].rsplit("/", 1)[-1])
            for v in node.values():
                walk(v)
        elif isinstance(node, list):
            for v in node:
                walk(v)

    walk(spec)
    return parts


def close(node, part=False):
    """Closes every object schema under node, nested ones too: a field the spec does not list
    fails. A part of an allOf stays open (it cannot see its siblings' fields); the allOf
    schema itself is closed, and unevaluatedProperties sees through its parts."""
    if isinstance(node, dict):
        if not part and ("properties" in node or "allOf" in node):
            node.setdefault("unevaluatedProperties", False)
        for k, v in node.items():
            if k == "allOf":
                for x in v:
                    close(x, part=True)
            else:
                close(v)
    elif isinstance(node, list):
        for v in node:
            close(v)


def closed_spec(spec):
    s = copy.deepcopy(spec)
    sch = s["components"]["schemas"]
    open_parts = allof_parts(s)
    for name, schema in sch.items():
        close(schema, part=name in open_parts)
    close(s["paths"])  # response schemas written inline
    for name, item in (("TileZ13", "CityVehicle"), ("TileZ9", "CityVehicleLite")):
        sch[name] = copy.deepcopy(sch["TileResponse"])
        sch[name]["properties"]["vehicles"]["items"] = {"$ref": f"#/components/schemas/{item}"}
    return s


def fetch(url):
    req = urllib.request.Request(url, headers={"User-Agent": "athens-transit-rt openapi check"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, r.headers, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers, e.read()


def check_knots(knots):
    """The contract's motion.knots rules (as model.CheckKnots): "" when they hold."""
    if not knots:
        return "no knots"
    if knots[0][0] != 0:
        return f"first knot at {knots[0][0]} m"
    for i in range(1, len(knots)):
        if knots[i][1] <= knots[i - 1][1]:
            return f"knot {i}: time {knots[i][1]} not after {knots[i - 1][1]}"
        if knots[i][0] < knots[i - 1][0]:
            return f"knot {i}: {knots[i][0]} m before {knots[i - 1][0]} m"
    return ""


def v2_checks(bodies):
    """What the schema cannot say: the /v2 data is there for every mode and well formed."""
    problems = []
    lines = bodies.get("/v2/lines", {}).get("lines", [])
    modes = {ln["mode"] for ln in lines}
    for m in ("bus", "trolley", "metro", "tram", "suburban"):
        if m not in modes:
            problems.append(f"/v2/lines: no {m} Line")
    for path, data in bodies.items():
        for s in data.get("sources") or []:
            if s.get("state") == "down":
                problems.append(f"{path}: Source {s['id']} is down")
    for path in ("/v2/stops/oasa:240084", SYNTAGMA_METRO):
        if path in bodies and not bodies[path].get("arrivals"):
            problems.append(f"{path}: no Arrivals")
    # Metro names that are written in capitals: an acronym, not a name the table is missing.
    METRO_ACRONYMS = {"ΚΑΤ"}
    caps = [e[1] for e in bodies.get("/v2/stops", {}).get("stops", [])
            if e[0].startswith("stasy:") and e[0].endswith("-metro") and e[1] == e[1].upper()
            and e[1] != e[1].lower() and e[1] not in METRO_ACRONYMS]
    if caps:
        problems.append(f"/v2/stops: {len(caps)} metro Stop(s) in capitals, missing from "
                        f"internal/railgeo/stasy_stops.json (run scripts/stasy-stop-names.py): "
                        + ", ".join(caps[:10]))
    trains = bodies.get("/v2/vehicles/rail", {}).get("vehicles", [])
    if not any(v["mode"] == "metro" for v in trains):
        problems.append("/v2/vehicles/rail: no metro train")
    for v in trains:
        knots = (v.get("motion") or {}).get("knots")
        if knots is None:
            continue
        msg = check_knots(knots)
        if not msg and knots[0][1] != v["position"]["at"]:
            msg = f"first knot at {knots[0][1]}, position.at {v['position']['at']}"
        if msg:
            problems.append(f"/v2/vehicles/rail: {v['id']}: {msg}")
    print(f"/v2 data: {len(lines)} lines ({', '.join(sorted(modes))}), {len(trains)} rail trains")
    return problems


def main():
    spec = yaml.safe_load(SPEC.read_text())
    root = "urn:openapi"
    registry = Registry().with_resource(root, Resource(closed_spec(spec), DRAFT202012))
    default_server = spec["servers"][0]["url"]

    missing = set(spec["paths"]) - {c[0] for c in CASES}
    errors = [f"no case for spec path {p}" for p in sorted(missing)]
    got, bodies = {}, {}
    for spec_path, path, want, *override in CASES:
        item = spec["paths"][spec_path]
        server = item.get("servers", [{"url": default_server}])[0]["url"]
        need = [n for n in got.keys() | {c[0] for c in CAPTURE.values()} if "{" + n + "}" in path]
        if any(got.get(n) is None for n in need):
            n = next(n for n in need if got.get(n) is None)
            print(f"skip {path}: no {n} from an earlier answer")
            if n not in OPTIONAL:
                errors.append(f"{path}: skipped, no {n} from an earlier answer")
            continue
        for n in need:
            path = path.replace("{" + n + "}", urllib.parse.quote(str(got[n]), safe=":"))
        url = server + path
        time.sleep(0.5)  # stay well under Cloudflare's ~30 requests per 10 s
        status, headers, body = fetch(url)
        if status == 503 and want == 200 and b"warming_up" in body:
            # A line or stream not polled until this request asked for it: ask again once.
            time.sleep(15)
            status, headers, body = fetch(url)
        ctype = (headers.get("Content-Type") or "").split(";")[0].strip()
        where = f"{path} -> {status} {ctype}"
        resp = item["get"]["responses"].get(str(status))
        problems = []
        if status != want:
            problems.append(f"status {status}, want {want}")
        if resp is None:
            problems.append(f"status {status} not in the spec")
        if headers.get("Access-Control-Allow-Origin") != "*":
            problems.append("no Access-Control-Allow-Origin: *")
        content = (resp or {}).get("content", {})
        if ctype == "" and "text/plain" in content:
            ctype = "text/plain"  # /healthz sends no Content-Type
        if content and ctype not in content:
            problems.append(f"Content-Type {ctype!r} not in {sorted(content)}")
        elif ctype == "application/json" and content:
            ref = "/".join(["#/paths", spec_path.replace("~", "~0").replace("/", "~1"), "get/responses",
                            str(status), "content/application~1json/schema"])
            if override and status == want:
                ref = "#/components/schemas/" + override[0]
            v = Draft202012Validator({"$ref": root + ref}, registry=registry, format_checker=FormatChecker())
            data = json.loads(body)
            for e in list(v.iter_errors(data))[:5]:
                problems.append(f"schema: {'/'.join(map(str, e.absolute_path))}: {e.message[:200]}")
            if status == 200 and isinstance(data, dict):
                bodies[path] = data
                if path in CAPTURE:
                    name, get = CAPTURE[path]
                    got[name] = get(data)
            items = next((data[k] for k in ("vehicles", "entity", "lines", "days") if isinstance(data.get(k), list)), None)
            where += f" ({len(body)} bytes" + (f", {len(items)} items)" if items is not None else ")")
        elif ctype == "application/x-protobuf":
            feed = gtfs_realtime_pb2.FeedMessage()
            feed.ParseFromString(body)  # may have no entity (no vehicle now)
            if not feed.IsInitialized():
                problems.append("FeedMessage without required fields (header)")
            where += f" ({len(feed.entity)} entities)"
        elif ctype == "text/plain" and body != b"ok\n":
            problems.append(f"body {body[:40]!r}")
        print(("FAIL " if problems else "ok   ") + where)
        errors += [f"{path}: {p}" for p in problems]
    errors += v2_checks(bodies)
    if errors:
        print("\n".join(["", f"{len(errors)} problem(s):"] + errors))
        sys.exit(1)
    print(f"\nall {len(CASES)} cases match the spec")


if __name__ == "__main__":
    main()
