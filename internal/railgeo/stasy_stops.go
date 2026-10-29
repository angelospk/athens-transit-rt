package railgeo

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// stasyStopsJSON is the display name and English name of every STASY Stop of /v2, by Stop id,
// and the ids of renamed Stops (aliases, kept forever). Built by scripts/stasy-stop-names.py
// from OpenStreetMap, checked by hand. Names © OpenStreetMap contributors (ODbL).
//
//go:embed stasy_stops.json
var stasyStopsJSON []byte

// StopName is a Stop's name as passengers read it (proper case, accents) and in English.
type StopName struct {
	Name   string  `json:"name"`
	NameEn *string `json:"name_en"`
}

var (
	stasyOnce    sync.Once
	stasyNames   map[string]StopName
	stasyAliases map[string]string
)

func loadSTASY() {
	stasyOnce.Do(func() {
		var f struct {
			Stops   map[string]StopName `json:"stops"`
			Aliases map[string]string   `json:"aliases"`
		}
		if err := json.Unmarshal(stasyStopsJSON, &f); err != nil {
			panic("railgeo: stasy_stops.json: " + err.Error())
		}
		stasyNames, stasyAliases = f.Stops, f.Aliases
	})
}

// STASYStopName is the table entry of a STASY Stop id; ok false when the table lacks it (a
// Stop new in the feed, or a renamed one: run scripts/stasy-stop-names.py).
func STASYStopName(id string) (StopName, bool) {
	loadSTASY()
	n, ok := stasyNames[id]
	return n, ok
}

// STASYAlias is the current id of a renamed STASY Stop's old id.
func STASYAlias(id string) (string, bool) {
	loadSTASY()
	to, ok := stasyAliases[id]
	return to, ok
}
