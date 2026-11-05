package server

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/angelospk/athens-transit-rt/internal/geo"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

// TileBox is a lat/lon rectangle.
type TileBox struct{ South, West, North, East float64 }

// City is what differs between the cities one build can serve.
type City struct {
	ID      string
	Lat0    float64 // reference latitude of the geo projection
	TileBox TileBox // the area vehicle tiles exist for
	// Rail turns on everything Athens-only besides the buses: STASY metro and tram, Proastiakos,
	// the OASA alerts poller and the municipal bus layer.
	Rail          bool
	TelematicsURL string // the telematics API base URL
	GTFSCKANURL   string // CKAN package_show URL of the city's GTFS ("" = no CKAN feed)
	// GTFSStopIDs: the GTFS stop_id is the telematics StopID, not its StopCode (OASTH).
	GTFSStopIDs bool
}

// CityByID returns the city of a --city value ("" = athens).
func CityByID(id string) (City, error) {
	switch id {
	case "", "athens":
		return City{ID: "athens", Lat0: 37.98, TileBox: TileBox{37.5, 22.9, 38.5, 24.5}, Rail: true,
			TelematicsURL: telematics.BaseURL}, nil
	case "thessaloniki":
		return City{ID: "thessaloniki", Lat0: 40.63, TileBox: TileBox{40.3, 22.6, 41.0, 23.4},
			TelematicsURL: "https://telematics.oasth.gr/api/", GTFSStopIDs: true,
			// OSETH slices, one zip per period; the server picks the one that covers today.
			GTFSCKANURL: "https://data.gov.gr/api/3/action/package_show?id=42c9a7da-c86c-48b1-914c-f340e8bef00d"}, nil
	}
	return City{}, fmt.Errorf("unknown city %q (athens or thessaloniki)", id)
}

// UseCityGlobals sets the package-level state that depends on the city: the tile box and the
// projection origin. Both are process-wide, so call it once at startup (serve), before New, and
// never from New: tests build many Apps in one process.
func UseCityGlobals(c City) {
	tileBox = c.TileBox
	geo.SetOriginLat(c.Lat0)
}

// greekToLatin maps the Greek capitals that look like a Latin letter to it. Thessaloniki's
// telematics writes line numbers with Latin letters ("39S"), its GTFS with Greek ones ("39Σ").
var greekToLatin = map[rune]rune{'Α': 'A', 'Β': 'B', 'Ε': 'E', 'Ζ': 'Z', 'Η': 'H', 'Ι': 'I', 'Κ': 'K',
	'Μ': 'M', 'Ν': 'N', 'Ο': 'O', 'Ρ': 'P', 'Τ': 'T', 'Υ': 'Y', 'Χ': 'X', 'Σ': 'S'}

func foldLine(s string) string {
	return strings.Map(func(r rune) rune {
		r = unicode.ToUpper(r)
		if l, ok := greekToLatin[r]; ok {
			return l
		}
		return r
	}, s)
}

// lineAlias returns a function that maps a telematics line number to the spelling of the GTFS
// feed's route_short_name (feedLines), or the trimmed number when the feed has no such line.
func lineAlias(feedLines []string) func(string) string {
	m := make(map[string]string, len(feedLines))
	for _, l := range feedLines {
		m[foldLine(l)] = l
	}
	return func(id string) string {
		id = strings.TrimSpace(id)
		if g, ok := m[foldLine(id)]; ok {
			return g
		}
		return id
	}
}
