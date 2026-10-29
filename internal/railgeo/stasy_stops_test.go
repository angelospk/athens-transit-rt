package railgeo

import (
	"encoding/json"
	"strings"
	"testing"
)

// The name table: every Stop has a name, ids are stasy:<folded name>-<metro|tram>, and every
// alias points at a Stop of the table (an alias to nowhere would 404 a saved Favourite).
func TestSTASYStopTable(t *testing.T) {
	var f struct {
		Stops   map[string]StopName `json:"stops"`
		Aliases map[string]string   `json:"aliases"`
	}
	if err := json.Unmarshal(stasyStopsJSON, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Stops) < 100 {
		t.Fatalf("%d stops", len(f.Stops))
	}
	for id, n := range f.Stops {
		if !strings.HasPrefix(id, "stasy:") || !(strings.HasSuffix(id, "-metro") || strings.HasSuffix(id, "-tram")) {
			t.Errorf("id %q", id)
		}
		if strings.TrimSpace(n.Name) == "" || (n.NameEn != nil && strings.TrimSpace(*n.NameEn) == "") {
			t.Errorf("%s: %+v", id, n)
		}
		// A metro name in capitals is STASY's, not a checked one; ΚΑΤ is an acronym (#39).
		if strings.HasSuffix(id, "-metro") && n.Name == strings.ToUpper(n.Name) && n.Name != "ΚΑΤ" {
			t.Errorf("%s: name %q in capitals", id, n.Name)
		}
	}
	for from, to := range f.Aliases {
		if _, ok := f.Stops[to]; !ok {
			t.Errorf("alias %s -> %s: no such Stop", from, to)
		}
		if _, ok := f.Stops[from]; ok {
			t.Errorf("alias %s is still a Stop", from)
		}
	}
	if n, ok := STASYStopName("stasy:συνταγμα-metro"); !ok || n.Name != "Σύνταγμα" || n.NameEn == nil {
		t.Fatalf("Σύνταγμα: %+v %v", n, ok)
	}
}
