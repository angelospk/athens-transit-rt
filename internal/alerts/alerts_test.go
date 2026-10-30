package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLineCodes(t *testing.T) {
	cases := map[string][]string{
		"Μερική προσωρινή τροποποίηση της διαδρομής  των λεωφορειακών γραμμών  305, 319 και 326,  στον Δήμο Σπάτων &#8211; Αρτέμιδος": {"305", "319", "326"},
		"Μερική προσωρινή τροποποίηση της λεωφ. γραμμής 418 λόγω έργων στην οδό Σαρανταπόρου":                                         {"418"},
		"Μερική προσωρινή τροποποίηση της λεωφορειακής γραμμής 046 στον Δήμο Αθηναίων":                                                {"046"},
		"Τροποποίηση των γραμμών 724 και 727, λόγω εργασιών στην λεωφόρο Καραμανλή μεταξύ των οδών 3 και 5":                           {"724", "727"},
		"Λειτουργία των ΜΜΜ λόγω των εορταστικών εκδηλώσεων για την Εθνική Επέτειο της 28ης Οκτωβρίου":                                nil,
		"Τροποποίηση της γραμμής Α1 και της γραμμής Χ95":                                                                              {"Α1", "Χ95"},
		"Τροποποίηση της γραμμής A1 (Latin A) και B12":                                                                                {"Α1", "Β12"},
		"Τροποποίηση των γραμμών 040, 040 και 049":                                                                                    {"040", "049"},
	}
	for title, want := range cases {
		if got := LineCodes(title); !reflect.DeepEqual(got, want) {
			t.Errorf("LineCodes(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestDatesAndStops(t *testing.T) {
	cases := []struct {
		file, from, until string
		stops             []string
	}{
		{"post-112.html", "2026-10-31", "2026-11-01", []string{"040054"}}, // «στις 31.10.2026 και τη 1.11.2026», «ΚΣ 040054»
		{"post-109.html", "2026-10-31", "2026-10-31", nil},                // «Σάββατο 31.10.26»
		{"post-046.html", "2026-10-31", "2027-04-30", nil},                // «από 31/10/2026 έως 30/04/2027»
		{"post-409.html", "2026-10-28", "2026-10-28", nil},                // «Τετάρτη 28 Οκτωβρίου 2026»
	}
	for _, c := range cases {
		text := ArticleText(read(t, c.file))
		if !strings.Contains(text, "Με στόχο") {
			t.Fatalf("%s: ArticleText lost the body: %.200q", c.file, text)
		}
		if strings.Contains(text, "<") {
			t.Errorf("%s: ArticleText kept a tag", c.file)
		}
		from, until, ok := Dates(text)
		if !ok || from != c.from || until != c.until {
			t.Errorf("%s: Dates = %q %q %v, want %q %q", c.file, from, until, ok, c.from, c.until)
		}
		if got := StopCodes(text); !reflect.DeepEqual(got, c.stops) {
			t.Errorf("%s: StopCodes = %q, want %q", c.file, got, c.stops)
		}
	}
	if _, _, ok := Dates("Ενημερώνουμε το επιβατικό κοινό ότι λόγω των εκδηλώσεων της 28ης Οκτωβρίου, ως εξής"); ok {
		t.Error("a day without month and year must not be a date")
	}
	// Only the opening paragraph counts: a date after «ως εξής» is part of the route text.
	from, until, ok := Dates("Ο ΟΑΣΑ ανακοινώνει για τις 2/11/2026 ως εξής: η στάση του 2010 και 5/5/2030")
	if !ok || from != "2026-11-02" || until != "2026-11-02" {
		t.Errorf("Dates after «ως εξής» = %q %q %v", from, until, ok)
	}
}

func TestParseFeed(t *testing.T) {
	items, err := ParseFeed(read(t, "oasa-feed.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 10 {
		t.Fatalf("got %d items, want 10", len(items))
	}
	it := items[1]
	if it.GUID != "https://www.oasa.gr/?p=14909" || !strings.HasPrefix(it.Link, "https://www.oasa.gr/blog/") {
		t.Errorf("item 2 = %+v", it)
	}
	if !strings.Contains(it.Title, "γραμμής 112") {
		t.Errorf("title %q", it.Title)
	}
	if want := time.Date(2026, 10, 30, 4, 42, 0, 0, time.UTC); !it.Published.Truncate(time.Minute).Equal(want) {
		t.Errorf("published %v, want %v", it.Published, want)
	}
	if !reflect.DeepEqual(items[8].Categories[:2], []string{"helpdesk", "Διαγωνισμοί"}) {
		t.Errorf("categories %q", items[8].Categories)
	}
	if strings.Contains(items[5].Title, "&#8211;") {
		t.Errorf("title not unescaped: %q", items[5].Title)
	}
}

func TestFromWP(t *testing.T) {
	now := time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
	items, err := FromWP(read(t, "athenstransport-posts.json"), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d strike items, want 1: %+v", len(items), items)
	}
	it := items[0]
	want := Item{
		ID: "athenstransport:80765", Source: "athenstransport",
		Title:     "Hellenic Train: Απεργία στο σιδηρόδρομο την Παρασκευή παρά την απόφαση του δικαστηρίου",
		URL:       "https://www.athenstransport.com/2026/10/apergia-hellenic-train-30-oktobriou/",
		Published: time.Date(2026, 10, 29, 17, 20, 7, 0, time.UTC).Unix(),
		Modes:     []string{"suburban"}, Lines: []string{}, Stops: []string{}, Urgent: true,
	}
	if !reflect.DeepEqual(it, want) {
		t.Errorf("got  %+v\nwant %+v", it, want)
	}
	// Category ids → modes; a strike with no mode category concerns every mode.
	if got := ModesOf([]int{231, 224, 227, 218}); !reflect.DeepEqual(got, []string{"bus", "metro"}) {
		t.Errorf("ModesOf = %q", got)
	}
	if got := ModesOf([]int{231}); !reflect.DeepEqual(got, []string{"bus", "trolley", "metro", "tram", "suburban"}) {
		t.Errorf("ModesOf(strike only) = %q", got)
	}
}

var lines = map[string]string{"101": "bus", "109": "bus", "112": "bus", "046": "bus", "418": "bus", "409": "bus", "724": "bus", "727": "bus", "305": "bus", "319": "bus", "326": "bus", "10": "trolley"}

func knownLine(code string) (string, string, bool) {
	m, ok := lines[code]
	return "oasa:" + code, m, ok
}

func knownStop(code string) bool { return code == "040054" }

func TestOASAItem(t *testing.T) {
	items, _ := ParseFeed(read(t, "oasa-feed.xml"))
	it, keep := OASAItem(items[1], read(t, "post-112.html"), knownLine, knownStop)
	if !keep {
		t.Fatal("item 2 dropped")
	}
	from, until := "2026-10-31", "2026-11-01"
	want := Item{
		ID: "oasa:14909", Source: "oasa", Title: items[1].Title, URL: items[1].Link,
		Published: items[1].Published.Unix(), From: &from, Until: &until,
		Modes: []string{"bus"}, Lines: []string{"oasa:112"}, Stops: []string{"oasa:040054"},
	}
	if !reflect.DeepEqual(it, want) {
		t.Errorf("got  %+v\nwant %+v", it, want)
	}
	if _, keep := OASAItem(items[8], nil, knownLine, knownStop); keep {
		t.Error("a tender (Διαγωνισμοί) must be dropped")
	}
	// No article (fetch failed): kept, without dates or stops.
	it, keep = OASAItem(items[4], nil, knownLine, knownStop)
	if !keep || it.From != nil || it.Until != nil || !reflect.DeepEqual(it.Lines, []string{"oasa:046"}) || len(it.Stops) != 0 {
		t.Errorf("no article: %+v %v", it, keep)
	}
	// Network-wide notice: no lines, both road modes.
	it, _ = OASAItem(items[7], nil, knownLine, knownStop)
	if len(it.Lines) != 0 || !reflect.DeepEqual(it.Modes, []string{"bus", "trolley"}) {
		t.Errorf("network-wide: %+v", it)
	}
	// Unknown line codes are dropped; a trolley line adds the trolley mode.
	fi := items[1]
	fi.Title = "Τροποποίηση των γραμμών 112, 999 και 10"
	it, _ = OASAItem(fi, nil, knownLine, knownStop)
	if !reflect.DeepEqual(it.Lines, []string{"oasa:112", "oasa:10"}) || !reflect.DeepEqual(it.Modes, []string{"bus", "trolley"}) {
		t.Errorf("mixed: %+v", it)
	}
}

func TestPoller(t *testing.T) {
	feed := read(t, "oasa-feed.xml")
	parsed, _ := ParseFeed(feed)
	pages := map[string][]byte{
		Feed:              feed,
		parsed[0].Link:    read(t, "post-109.html"),
		parsed[1].Link:    read(t, "post-112.html"),
		parsed[4].Link:    read(t, "post-046.html"),
		parsed[9].Link:    read(t, "post-409.html"),
		AthensTransportWP: read(t, "athenstransport-posts.json"),
	}
	gets := map[string]int{}
	now := time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
	p := &Poller{
		Get: func(_ context.Context, url string) ([]byte, error) {
			gets[url]++
			if b, ok := pages[url]; ok {
				return b, nil
			}
			return nil, errors.New("404")
		},
		KnownLine: knownLine, KnownStop: knownStop,
		Now: func() time.Time { return now },
	}
	if p.Updated() != 0 || len(p.Items()) != 0 {
		t.Fatal("not empty before the first poll")
	}
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := p.Items()
	if len(items) != 10 { // 9 OASA notices (no tender) + 1 strike
		t.Fatalf("got %d items, want 10", len(items))
	}
	for i := 1; i < len(items); i++ {
		if items[i].Published > items[i-1].Published {
			t.Fatal("items not newest first")
		}
	}
	if items[0].ID != "oasa:14914" || items[0].From == nil || *items[0].From != "2026-10-31" {
		t.Errorf("first item %+v", items[0])
	}
	if p.Updated() != now.Unix() {
		t.Errorf("Updated %d", p.Updated())
	}

	// A second poll fetches only the feeds and the articles that failed, never a parsed one again.
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gets[parsed[1].Link] != 1 || gets[Feed] != 2 || gets[AthensTransportWP] != 2 {
		t.Errorf("gets after two polls: %v", gets)
	}
	if gets[parsed[2].Link] != 2 {
		t.Errorf("a failed article is retried: %d", gets[parsed[2].Link])
	}

	// 9 days later: only the notice that is still valid (046 until 2027-04-30) is left.
	now = now.Add(9 * 24 * time.Hour)
	items = p.Items()
	if len(items) != 1 || !reflect.DeepEqual(items[0].Lines, []string{"oasa:046"}) {
		t.Errorf("after 9 days: %+v", items)
	}

	// One source failing keeps the other's items and the old ones.
	now = time.Date(2026, 10, 30, 21, 0, 0, 0, time.UTC)
	delete(pages, AthensTransportWP)
	if err := p.Poll(context.Background()); err == nil {
		t.Error("a failed source must be reported")
	}
	if len(p.Items()) != 10 {
		t.Errorf("after a failed source: %d items", len(p.Items()))
	}
}

func TestItemJSON(t *testing.T) {
	b, _ := json.Marshal(Item{ID: "x", Modes: []string{}, Lines: []string{}, Stops: []string{}})
	for _, k := range []string{`"from":null`, `"until":null`, `"lines":[]`, `"urgent":false`, `"published":0`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("%s missing in %s", k, b)
		}
	}
}
