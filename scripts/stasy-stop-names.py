#!/usr/bin/env python3
"""Build internal/railgeo/stasy_stops.json: the display name and English name of every STASY
(metro, tram) Stop of /v2, keyed by its Stop id.

STASY writes metro names in capitals without accents ("ΣΥΝΤΑΓΜΑ") and has no English names.
OpenStreetMap has both. A Stop takes OSM's name when its folded STASY name matches an OSM metro
or tram node within 800 m; the entries in OVERRIDES below were checked by hand (abbreviations,
renamed or merged stations, missing name:en). Tram names stay as STASY writes them: they are
already in proper case, and OSM's differ in places ("Εδέμ" / "Έδεμ").

Run it when the STASY feed gains or renames a Stop (the server logs "stasy: stops not in the
name table", and the daily live check fails), then review the diff:

    python3 scripts/stasy-stop-names.py [https://transit.haroldpoi.dev]

It reads the Stop ids from /v2/stops and the names from the Overpass API.
"""

import json
import math
import sys
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request

OUT = "internal/railgeo/stasy_stops.json"
UA = "athens-transit-rt/1.0 (github.com/angelospk/athens-transit-rt)"
OVERPASS = "https://overpass-api.de/api/interpreter"
QUERY = """[out:json][timeout:80];
(node["railway"="station"](37.80,23.50,38.15,24.05);
 node["railway"="tram_stop"](37.80,23.50,38.15,24.05););
out;"""

# Checked by hand on 2026-10-29: [name, name_en]; None keeps the STASY name / leaves no English.
OVERRIDES = {
    "stasy:αγ.-δημητριοσ-metro": ["Άγιος Δημήτριος", "Agios Dimitrios"],
    "stasy:δ.-πλακεντιασ-metro": ["Δουκίσσης Πλακεντίας", "Doukissis Plakentias"],
    "stasy:δημοτικο-θεατρο-metro": [None, "Dimotiko Theatro"],
    "stasy:ελ.-βενιζελοσ-metro": ["Αεροδρόμιο", "Airport"],
    "stasy:καντζα-metro": ["Παιανία-Κάντζα", "Peania-Kantza"],
    "stasy:κατεχακη-metro": ["Κατεχάκη", "Katechaki"],
    "stasy:κεραμικοσ-metro": ["Κεραμεικός", "Kerameikos"],
    "stasy:μανιατικα-metro": [None, "Maniatika"],
    "stasy:ν.κοσμοσ-metro": ["Νέος Κόσμος", "Neos Kosmos"],
    "stasy:νικαια-metro": [None, "Nikaia"],
    "stasy:παλληνη-metro": [None, "Pallini"],
    "stasy:σ.-λαρισησ-metro": ["Σταθμός Λαρίσης", "Larissa Station"],
    "stasy:ταυροσ-metro": ["Ταύρος", "Tavros"],
    "stasy:34ου-συνταγματοσ-tram": [None, "34ou Syntagmatos"],
    "stasy:αγγ.-μεταξα-tram": [None, "Angelou Metaxa"],
    "stasy:αγιασ-φωτεινησ-tram": [None, "Agias Fotinis"],
    "stasy:βουλα-tram": [None, "Voula"],
    "stasy:κολυμβητηριο-(γλυφαδα)-tram": [None, "Kolymvitirio (Glyfada)"],
    "stasy:μουσσων-tram": [None, "Mousson"],
    "stasy:πλατεια-κατρακη-tram": [None, "Platia Katraki"],
    "stasy:σεφ-(νεο-φαληρο)-tram": [None, "SEF (Neo Faliro)"],
    "stasy:σκυλιτση-tram": [None, "Skylitsi"],
    "stasy:συνταγμα-(κεντρο)-tram": [None, "Syntagma (Kentro)"],
}

# Points of the feed that are no station passengers know, merged into a Stop: the server keeps
# them out of /v2/stops and counts their times as the Stop's. ΣΥΝΔΕΣΗ ΠΡΟΑΣΤΙΑΚΟΥ is where M3
# airport trains move onto the suburban tracks, 91 m from Δουκίσσης Πλακεντίας (#12).
MERGED = {
    "stasy:συνδεση-προαστιακου-metro": "stasy:δ.-πλακεντιασ-metro",
}


def fold(s):
    s = s.lower().replace("ς", "σ")
    s = "".join(c for c in unicodedata.normalize("NFD", s) if unicodedata.category(c) != "Mn")
    return " ".join(s.split())


def metres(a, b, c, d):
    return math.hypot((a - c) * 111320, (b - d) * 111320 * math.cos(math.radians(a)))


def get(url, data=None):
    """GET (or POST data) url as JSON; Overpass answers 502/504 when busy, so try 3 times."""
    for attempt in range(3):
        try:
            req = urllib.request.Request(url, data=data, headers={"User-Agent": UA})
            with urllib.request.urlopen(req, timeout=120) as r:
                return json.load(r)
        except urllib.error.HTTPError as e:
            if e.code not in (429, 502, 503, 504) or attempt == 2:
                raise
            time.sleep(20 * (attempt + 1))


def main():
    api = sys.argv[1] if len(sys.argv) > 1 else "https://transit.haroldpoi.dev"
    stops = [e for e in get(api + "/v2/stops")["stops"] if e[0].startswith("stasy:")]
    osm = get(OVERPASS, urllib.parse.urlencode({"data": QUERY}).encode())["elements"]
    out, unmatched = {}, []
    for sid, name, lat, lon, modes, _ in stops:
        if sid in MERGED:
            continue
        mode = modes[0]
        hits = [(metres(lat, lon, x["lat"], x["lon"]), x["tags"]) for x in osm
                if "name" in x["tags"] and fold(x["tags"]["name"]) == fold(name)]
        hits = [h for h in hits if h[0] < 800]
        tags = min(hits, key=lambda h: h[0])[1] if hits else {}
        disp = tags.get("name") if mode == "metro" and tags else None
        en = tags.get("name:en")
        if sid in OVERRIDES:
            o = OVERRIDES[sid]
            disp, en = o[0], o[1]
        elif not tags:
            unmatched.append(sid)
        out[sid] = {"name": disp or name, "name_en": en}
    for sid in OVERRIDES:
        if sid not in out:
            print("override for a stop that is gone:", sid, file=sys.stderr)
    body = {
        "source": "Names © OpenStreetMap contributors (ODbL), checked by hand; ids from STASY GTFS",
        "stops": dict(sorted(out.items())),
        "aliases": {},
    }
    old = {}
    try:
        old = json.load(open(OUT, encoding="utf-8")).get("aliases", {})
    except FileNotFoundError:
        pass
    body["aliases"] = {**old, **MERGED}  # renamed and merged stops: old id -> current id, kept forever
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(body, f, ensure_ascii=False, indent=1)
        f.write("\n")
    print(f"{len(out)} stops, {sum(1 for v in out.values() if v['name_en'])} with name_en")
    if unmatched:
        print("no OSM match, add to OVERRIDES:", ", ".join(unmatched), file=sys.stderr)


if __name__ == "__main__":
    main()
