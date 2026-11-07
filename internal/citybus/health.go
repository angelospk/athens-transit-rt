// Line health: which lines have sent no live bus for DAYS, so the rotation can stop spending its
// probes on them and keep a cheap check on them instead.
// Port of citybus-bridge src/health.ts.

package citybus

import (
	"encoding/json"
	"os"
)

// Days is how long a line must be tracked, without a live bus, before it is timetable-only.
const Days = 7

// LineHealth is one health file: the newest live sighting per line, and when a line was first seen.
type LineHealth struct {
	Since     int64            `json:"since"`     // unix s: when this state file started tracking
	LastLive  map[string]int64 `json:"lastLive"`  // line -> unix s of its newest live sighting
	FirstSeen map[string]int64 `json:"firstSeen"` // line -> unix s it was first in the model
	file      string
}

// loadHealth reads a health file; a missing or unreadable file starts tracking from now.
func loadHealth(file string, now int64) *LineHealth {
	h := &LineHealth{Since: now, LastLive: map[string]int64{}, FirstSeen: map[string]int64{}, file: file}
	b, err := os.ReadFile(file)
	if err != nil {
		return h // no file
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return h // unreadable
	}
	var since int64
	if v, ok := raw["since"]; ok && json.Unmarshal(v, &since) == nil {
		h.Since = since
	}
	var last map[string]int64
	if v, ok := raw["lastLive"]; ok && json.Unmarshal(v, &last) == nil && last != nil {
		h.LastLive = last
	}
	var first map[string]int64
	if v, ok := raw["firstSeen"]; ok && json.Unmarshal(v, &first) == nil && first != nil {
		h.FirstSeen = first
	}
	return h
}

func (h *LineHealth) saw(line string, at int64) {
	h.LastLive[line] = max(h.LastLive[line], at)
}

// track records the lines in the model now: a line not seen before starts its own Days from now.
func (h *LineHealth) track(lines []string, now int64) {
	for _, l := range lines {
		if _, ok := h.FirstSeen[l]; !ok {
			h.FirstSeen[l] = now
		}
	}
}

// timetableOnly reports no live bus for Days, with the line tracked for Days (a new line or a
// fresh start never skips).
func (h *LineHealth) timetableOnly(line string, now int64) bool {
	from := h.Since
	if f, ok := h.FirstSeen[line]; ok && f > from {
		from = f
	}
	return now-from >= Days*86400 && h.LastLive[line] < now-Days*86400
}

// save writes the file through a .tmp then renames it, so a half-written file never appears.
func (h *LineHealth) save() error {
	b, err := json.Marshal(struct {
		Since     int64            `json:"since"`
		LastLive  map[string]int64 `json:"lastLive"`
		FirstSeen map[string]int64 `json:"firstSeen"`
	}{h.Since, h.LastLive, h.FirstSeen})
	if err != nil {
		return err
	}
	if err := os.WriteFile(h.file+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(h.file+".tmp", h.file)
}

// gapFor is the pause between probe boards of one city, in milliseconds, for a rotation over
// probes stops.
func gapFor(probes int) int {
	g := 60000 / max(1, probes)
	if g > 1000 {
		g = 1000
	}
	if g < 250 {
		g = 250
	}
	return g
}
