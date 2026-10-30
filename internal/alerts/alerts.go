// Package alerts builds the service-disruption feed of /v2/alerts (docs/CONTRACT.md, rev 13):
// OASA notices from its news feed and strikes from athenstransport.com, polled in the background.
package alerts

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// Feed is OASA's news feed (WordPress RSS): every notice but the tenders is an Alert.
	Feed = "https://www.oasa.gr/feed/"
	// AthensTransportWP is athenstransport.com's news API: its strike posts (category 231).
	AthensTransportWP = "https://www.athenstransport.com/wp-json/wp/v2/posts?categories=231&per_page=30&_fields=id,date_gmt,link,title,categories"

	pollEvery  = 10 * time.Minute // the contract's poll interval
	keepFor    = 7 * 24 * time.Hour
	dateWindow = 800 // runes of text searched for dates when it has no «ως εξής»

	wpStrike = 231 // athenstransport.com's category id for «Απεργία»
)

// athens is the zone of the contract's dates and of "today"; as gtfs.Athens.
var athens = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Athens"); err == nil {
		return l
	}
	return time.FixedZone("EET", 2*60*60)
}()

// Item is one Alert of the /v2/alerts body. No field is omitted: a client reads from/until and
// modes without a null check.
type Item struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"`
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	Published int64    `json:"published"`
	From      *string  `json:"from"`
	Until     *string  `json:"until"`
	Modes     []string `json:"modes"`
	Lines     []string `json:"lines"`
	Stops     []string `json:"stops"`
	Urgent    bool     `json:"urgent"`
}

// FeedItem is one item of the OASA RSS feed.
type FeedItem struct {
	GUID       string
	Title      string
	Link       string
	Published  time.Time
	Categories []string
}

// ParseFeed reads OASA's RSS feed. An unparsable pubDate leaves the zero time, not an error.
func ParseFeed(body []byte) ([]FeedItem, error) {
	var f struct {
		Items []struct {
			Title      string   `xml:"title"`
			Link       string   `xml:"link"`
			GUID       string   `xml:"guid"`
			PubDate    string   `xml:"pubDate"`
			Creator    string   `xml:"creator"` // dc:creator, the author, first as the site lists it
			Categories []string `xml:"category"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &f); err != nil {
		return nil, err
	}
	out := make([]FeedItem, 0, len(f.Items))
	for _, it := range f.Items {
		pub, err := time.Parse(time.RFC1123Z, strings.TrimSpace(it.PubDate))
		if err != nil {
			pub = time.Time{}
		}
		cats := make([]string, 0, len(it.Categories)+1)
		if c := strings.TrimSpace(it.Creator); c != "" {
			cats = append(cats, c)
		}
		for _, c := range it.Categories {
			cats = append(cats, strings.TrimSpace(c))
		}
		out = append(out, FeedItem{GUID: strings.TrimSpace(it.GUID), Title: html.UnescapeString(strings.TrimSpace(it.Title)),
			Link: strings.TrimSpace(it.Link), Published: pub, Categories: cats})
	}
	return out, nil
}

var (
	parenRe   = regexp.MustCompile(`\([^)]*\)`)
	articleRe = regexp.MustCompile(`(?is)<article\b[^>]*>(.*?)</article>`)
	dropRe    = regexp.MustCompile(`(?is)<(script|style|header)\b[^>]*>.*?</(script|style|header)>`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe   = regexp.MustCompile(`\s+`)
	numericRe = regexp.MustCompile(`\b(\d{1,2})[./](\d{1,2})[./](\d{4}|\d{2})\b`)
	monthRe   = regexp.MustCompile(`\b(\d{1,2})\s+(ιανουαρι|φεβρουαρι|μαρτι|απριλι|μαι|ιουνι|ιουλι|αυγουστ|σεπτεμβρι|οκτωβρι|νοεμβρι|δεκεμβρι)[α-ω]*\s+(\d{4})\b`)
	stopRe    = regexp.MustCompile(`Κ(?:\.)?Σ\.?\s*(\d+)`)
)

var monthNumber = map[string]int{"ιανουαρι": 1, "φεβρουαρι": 2, "μαρτι": 3, "απριλι": 4, "μαι": 5,
	"ιουνι": 6, "ιουλι": 7, "αυγουστ": 8, "σεπτεμβρι": 9, "οκτωβρι": 10, "νοεμβρι": 11, "δεκεμβρι": 12}

// accents strips Greek accents one rune for one rune, so that month names match either way.
var accents = strings.NewReplacer("ά", "α", "έ", "ε", "ή", "η", "ί", "ι", "ϊ", "ι", "ΐ", "ι",
	"ό", "ο", "ύ", "υ", "ϋ", "υ", "ΰ", "υ", "ώ", "ω")

// latinToGreek maps the Latin capitals that look like Greek ones, as server.CanonicalLine does.
var latinToGreek = strings.NewReplacer("A", "Α", "B", "Β", "E", "Ε", "Z", "Ζ", "H", "Η", "I", "Ι",
	"K", "Κ", "M", "Μ", "N", "Ν", "O", "Ο", "P", "Ρ", "T", "Τ", "Y", "Υ", "X", "Χ")

// LineCodes are the line codes that follow «γραμμή»/«γραμμών» in a title, deduped, in order. A
// parenthetical note is skipped; the list ends at the first other word. nil when none.
func LineCodes(title string) []string {
	var out []string
	seen := map[string]bool{}
	words := strings.Fields(strings.ReplaceAll(parenRe.ReplaceAllString(title, " "), ",", ", "))
	for i := 0; i < len(words); i++ {
		if !strings.HasPrefix(strings.ToLower(words[i]), "γραμμ") {
			continue
		}
		for _, w := range words[i+1:] {
			w = strings.Trim(w, ",.;:·()«»-–")
			if w == "" || w == "&" || strings.EqualFold(w, "και") {
				continue // a separator
			}
			code, ok := lineCode(w)
			if !ok {
				break
			}
			if !seen[code] {
				seen[code] = true
				out = append(out, code)
			}
		}
	}
	return out
}

// lineCode is an optional letter, 1-3 digits and an optional letter, Greek capitals.
func lineCode(w string) (string, bool) {
	r := []rune(w)
	if len(r) == 0 || len(r) > 5 {
		return "", false
	}
	i, code := 0, ""
	if unicode.IsLetter(r[0]) {
		code, i = strings.ToUpper(string(r[0])), 1
	}
	j := i
	for j < len(r) && r[j] >= '0' && r[j] <= '9' {
		j++
	}
	if d := j - i; d < 1 || d > 3 {
		return "", false
	}
	code = latinToGreek.Replace(code) + string(r[i:j])
	if j < len(r) {
		if !unicode.IsLetter(r[j]) || j+1 != len(r) {
			return "", false
		}
		code += latinToGreek.Replace(strings.ToUpper(string(r[j])))
	}
	return code, true
}

// ArticleText is the text of the page's <article> element (of the whole page when it has none):
// scripts, styles and the post header (author, dates) dropped, tags stripped, entities
// unescaped, whitespace (also &nbsp;) collapsed.
func ArticleText(page []byte) string {
	s := string(page)
	if m := articleRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	s = dropRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(html.UnescapeString(s), "\u00a0", " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// Dates are the earliest and latest date (YYYY-MM-DD) of a notice's opening paragraph: from the
// first «Με στόχο» or «Ενημερώνουμε» up to «ως εξής», or 800 runes. ok is false when it names none.
func Dates(text string) (from, until string, ok bool) {
	r := []rune(text)
	start, found := 0, false
	for _, m := range []string{"Με στόχο", "Ενημερώνουμε"} {
		if i := strings.Index(text, m); i >= 0 {
			if ri := utf8.RuneCountInString(text[:i]); !found || ri < start {
				start, found = ri, true
			}
		}
	}
	end := start + dateWindow
	if i := strings.Index(text, "ως εξής"); i >= 0 {
		if ri := utf8.RuneCountInString(text[:i]); ri > start {
			end = ri
		}
	}
	if end > len(r) {
		end = len(r)
	}
	region := string(r[start:end])

	var days []time.Time
	for _, m := range numericRe.FindAllStringSubmatch(region, -1) {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if y < 100 {
			y += 2000
		}
		if t, ok := calendar(y, mo, d); ok {
			days = append(days, t)
		}
	}
	plain := accents.Replace(strings.ToLower(region))
	for _, m := range monthRe.FindAllStringSubmatch(plain, -1) {
		d, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[3])
		if t, ok := calendar(y, monthNumber[m[2]], d); ok {
			days = append(days, t)
		}
	}
	if len(days) == 0 {
		return "", "", false
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	return days[0].Format(time.DateOnly), days[len(days)-1].Format(time.DateOnly), true
}

// calendar validates a real day of a month.
func calendar(y, m, d int) (time.Time, bool) {
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t, t.Day() == d && int(t.Month()) == m
}

// StopCodes are the «ΚΣ <code>» stop codes of a text, deduped, in order. nil when none.
func StopCodes(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range stopRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// modeOfCategory is athenstransport.com's category id → mode.
var modeOfCategory = map[int]string{224: "bus", 252: "trolley", 227: "metro", 218: "metro",
	250: "tram", 236: "suburban", 271: "suburban"}

// modeOrder is the order of the modes in an Alert.
var modeOrder = []string{"bus", "trolley", "metro", "tram", "suburban"}

// ModesOf is the modes of athenstransport.com's category ids; none of them means every mode.
func ModesOf(categories []int) []string {
	seen := map[string]bool{}
	for _, c := range categories {
		if m, ok := modeOfCategory[c]; ok {
			seen[m] = true
		}
	}
	out := []string{}
	for _, m := range modeOrder {
		if seen[m] {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return append([]string{}, modeOrder...)
	}
	return out
}

// FromWP are the strikes of athenstransport.com published in the last 7 days before now.
func FromWP(body []byte, now time.Time) ([]Item, error) {
	var posts []struct {
		ID         int64                     `json:"id"`
		DateGMT    string                    `json:"date_gmt"`
		Link       string                    `json:"link"`
		Title      struct{ Rendered string } `json:"title"`
		Categories []int                     `json:"categories"`
	}
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, err
	}
	out := []Item{}
	for _, p := range posts {
		if !containsInt(p.Categories, wpStrike) {
			continue
		}
		at, err := time.Parse("2006-01-02T15:04:05", p.DateGMT)
		if err != nil {
			continue
		}
		if now.Sub(at) > keepFor {
			continue
		}
		out = append(out, Item{ID: "athenstransport:" + strconv.FormatInt(p.ID, 10), Source: "athenstransport",
			Title: html.UnescapeString(strings.TrimSpace(p.Title.Rendered)), URL: p.Link, Published: at.Unix(),
			Modes: ModesOf(p.Categories), Lines: []string{}, Stops: []string{}, Urgent: true})
	}
	return out, nil
}

func containsInt(vs []int, v int) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

// OASAItem is one OASA notice as an Alert; keep is false for a tender. article is the notice
// page, nil when it could not be read (the Alert then has no dates and no stops).
func OASAItem(fi FeedItem, article []byte, knownLine func(code string) (id, mode string, ok bool), knownStop func(code string) bool) (it Item, keep bool) {
	for _, c := range fi.Categories {
		if c == "Διαγωνισμοί" {
			return Item{}, false
		}
	}
	it = Item{ID: "oasa:" + postID(fi.GUID), Source: "oasa", Title: fi.Title, URL: fi.Link,
		Published: fi.Published.Unix(), Modes: []string{}, Lines: []string{}, Stops: []string{}}
	if article != nil {
		text := ArticleText(article)
		if from, until, ok := Dates(text); ok {
			it.From, it.Until = &from, &until
		}
		for _, code := range StopCodes(text) {
			if knownStop != nil && knownStop(code) {
				it.Stops = append(it.Stops, "oasa:"+code)
			}
		}
	}
	// Buses, plus trolleys when one of the lines is one; a network-wide notice has both.
	trolley := false
	for _, code := range LineCodes(fi.Title) {
		if knownLine == nil {
			continue
		}
		id, mode, ok := knownLine(code)
		if !ok {
			continue
		}
		it.Lines = append(it.Lines, id)
		trolley = trolley || mode == "trolley"
	}
	it.Modes = []string{"bus"}
	if trolley || len(it.Lines) == 0 {
		it.Modes = append(it.Modes, "trolley")
	}
	return it, true
}

// postID is the p= number of an OASA GUID ("https://www.oasa.gr/?p=14909"), else the GUID.
func postID(guid string) string {
	if u, err := url.Parse(guid); err == nil {
		if p := u.Query().Get("p"); p != "" {
			return p
		}
	}
	return guid
}

// Poller keeps the Alerts of both sources and refreshes them. Items and Updated are safe for
// concurrent use; Poll must not run twice at once.
type Poller struct {
	Get       func(ctx context.Context, url string) ([]byte, error)
	KnownLine func(code string) (id, mode string, ok bool)
	KnownStop func(code string) bool
	Now       func() time.Time
	Log       *slog.Logger // nil: slog.Default()

	mu       sync.Mutex
	oasa     []Item
	wp       []Item
	articles map[string][]byte // notice page by GUID: each one is read once
	updated  int64
}

// Updated is the Unix time of the last poll with no failure, 0 before it.
func (p *Poller) Updated() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.updated
}

// Items are both sources' Alerts, newest first: published within 7 days, or still valid by until.
func (p *Poller) Items() []Item {
	items, _ := p.Snapshot()
	return items
}

// Snapshot is Items and Updated of the same poll.
func (p *Poller) Snapshot() ([]Item, int64) {
	now := p.now()
	p.mu.Lock()
	all := make([]Item, 0, len(p.oasa)+len(p.wp))
	all = append(all, p.oasa...)
	all = append(all, p.wp...)
	updated := p.updated
	p.mu.Unlock()
	out := []Item{}
	for _, it := range all {
		if live(it, now) {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published > out[j].Published })
	return out, updated
}

// live reports whether an Alert is still listed at now (as Snapshot filters).
func live(it Item, now time.Time) bool {
	return now.Sub(time.Unix(it.Published, 0)) <= keepFor || (it.Until != nil && *it.Until >= now.In(athens).Format(time.DateOnly))
}

// Poll refreshes both sources. A source that fails keeps its previous Alerts and its error is
// returned; a notice page that fails only loses its dates until the next poll.
func (p *Poller) Poll(ctx context.Context) error {
	now := p.now()
	var errs []error

	// OASA: the feed, then the page of every notice not read yet.
	var oasa []Item
	oasaOK := false
	if b, err := p.get(ctx, Feed); err != nil {
		errs = append(errs, fmt.Errorf("oasa feed: %w", err))
	} else if items, err := ParseFeed(b); err != nil {
		errs = append(errs, fmt.Errorf("oasa feed: %w", err))
	} else {
		oasaOK = true
		seen := map[string]bool{}
		for _, fi := range items {
			if tender(fi) {
				continue
			}
			seen[fi.GUID] = true
			p.mu.Lock()
			article, read := p.articles[fi.GUID]
			p.mu.Unlock()
			if !read && fi.Link != "" {
				if b, err := p.get(ctx, fi.Link); err == nil {
					article, read = b, true
					p.mu.Lock()
					if p.articles == nil {
						p.articles = map[string][]byte{}
					}
					p.articles[fi.GUID] = b
					p.mu.Unlock()
				}
			}
			if !read {
				article = nil
			}
			if it, keep := OASAItem(fi, article, p.KnownLine, p.KnownStop); keep {
				oasa = append(oasa, it)
			}
		}
		// A notice that left the feed is never read again.
		p.mu.Lock()
		for g := range p.articles {
			if !seen[g] {
				delete(p.articles, g)
			}
		}
		p.mu.Unlock()
	}

	// athenstransport.com: the strike posts.
	var wp []Item
	wpOK := false
	if b, err := p.get(ctx, AthensTransportWP); err != nil {
		errs = append(errs, fmt.Errorf("athenstransport: %w", err))
	} else if items, err := FromWP(b, now); err != nil {
		errs = append(errs, fmt.Errorf("athenstransport: %w", err))
	} else {
		wp, wpOK = items, true
	}

	p.mu.Lock()
	if oasaOK {
		// The feed shows only the last 10 notices: one that left it stays until it expires.
		inFeed := map[string]bool{}
		for _, it := range oasa {
			inFeed[it.ID] = true
		}
		for _, it := range p.oasa {
			if !inFeed[it.ID] && live(it, now) {
				oasa = append(oasa, it)
			}
		}
		p.oasa = oasa
	}
	if wpOK {
		p.wp = wp
	}
	if len(errs) == 0 {
		p.updated = now.Unix()
	}
	p.mu.Unlock()
	return errors.Join(errs...)
}

// Run polls at once, then every 10 minutes until ctx ends, and logs the failures.
func (p *Poller) Run(ctx context.Context) {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}
	for {
		if err := p.Poll(ctx); err != nil {
			log.Warn("alerts poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollEvery):
		}
	}
}

// tender reports whether the feed item is a tender, not a service notice.
func tender(fi FeedItem) bool {
	for _, c := range fi.Categories {
		if c == "Διαγωνισμοί" {
			return true
		}
	}
	return false
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) get(ctx context.Context, url string) ([]byte, error) {
	if p.Get == nil {
		return nil, errors.New("alerts: no Get")
	}
	return p.Get(ctx, url)
}
