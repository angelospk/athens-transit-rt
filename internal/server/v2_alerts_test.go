package server

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/alerts"
	"github.com/angelospk/athens-transit-rt/internal/gtfs"
)

const alertFeed = `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel>
<item><title>Μερική προτροποποίηση της λεωφορειακής γραμμής 101</title>
<link>https://www.oasa.gr/blog/notice-1/</link>
<dc:creator><![CDATA[edoris]]></dc:creator>
<pubDate>Fri, 30 Oct 2026 04:42:27 +0000</pubDate>
<category><![CDATA[Ανακοινώσεις]]></category>
<guid isPermaLink="false">https://www.oasa.gr/?p=101</guid></item>
<item><title>Διακήρυξη ανοιχτής διαδικασίας</title>
<link>https://www.oasa.gr/blog/tender/</link>
<pubDate>Thu, 29 Oct 2026 08:42:24 +0000</pubDate>
<category><![CDATA[Διαγωνισμοί]]></category>
<guid isPermaLink="false">https://www.oasa.gr/?p=102</guid></item>
</channel></rss>`

const alertPage = `<html><body><article><p>Με στόχο την ενημέρωση, στις 31.10.2026 και τη 1.11.2026, ως εξής:</p>
<p>Νέα αφετηρία η στάση με ΚΣ 040054.</p></article></body></html>`

const alertPosts = `[{"id": 7, "date_gmt": "2026-10-29T17:20:07",
 "link": "https://www.athenstransport.com/2026/10/apergia/",
 "title": {"rendered": "Απεργία στο σιδηρόδρομο &#8211; Hellenic Train"},
 "categories": [231, 271]}]`

type v2Alerts struct {
	Alerts    []alerts.Item `json:"alerts"`
	UpdatedAt *int64        `json:"updated_at"`
}

func TestV2Alerts(t *testing.T) {
	now := time.Date(2026, 10, 30, 20, 0, 0, 0, gtfs.Athens)
	a := newApp(t, now)

	// Before the first poll: no Alerts, a null updated_at, cached 5 s.
	res, body := get(t, a, "/v2/alerts")
	var before v2Alerts
	if err := json.Unmarshal(body, &before); err != nil {
		t.Fatalf("%v %s", err, body)
	}
	if res.StatusCode != 200 || maxAge(t, res) != 5 || len(before.Alerts) != 0 || before.UpdatedAt != nil {
		t.Fatalf("before the first poll: %d %d %s", res.StatusCode, maxAge(t, res), body)
	}

	// The GTFS knows line L (a bus) and stops S0..S4.
	if id, mode, ok := a.knownLine("L"); !ok || id != "oasa:L" || mode != "bus" {
		t.Fatalf("knownLine = %q %q %v", id, mode, ok)
	}
	if _, _, ok := a.knownLine("NOPE"); ok || !a.knownStop("S0") || a.knownStop("NOPE") {
		t.Fatal("knownLine/knownStop of an unknown code")
	}

	// One poll of both sources: a notice with its page, a tender to drop, and a strike.
	a.alerts.Get = func(_ context.Context, url string) ([]byte, error) {
		switch url {
		case alerts.Feed:
			return []byte(alertFeed), nil
		case "https://www.oasa.gr/blog/notice-1/":
			return []byte(alertPage), nil
		case alerts.AthensTransportWP:
			return []byte(alertPosts), nil
		}
		return nil, errors.New("404")
	}
	if err := a.alerts.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, body = get(t, a, "/v2/alerts")
	var got v2Alerts
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("%v %s", err, body)
	}
	if ma := maxAge(t, res); ma != 300 {
		t.Fatalf("max-age %d after a poll", ma)
	}
	if got.UpdatedAt == nil || *got.UpdatedAt != now.Unix() {
		t.Fatalf("updated_at %v", got.UpdatedAt)
	}
	if len(got.Alerts) != 2 {
		t.Fatalf("got %d Alerts, want 2: %s", len(got.Alerts), body)
	}
	from, until := "2026-10-31", "2026-11-01"
	want := alerts.Item{ID: "oasa:101", Source: "oasa", Title: "Μερική προτροποποίηση της λεωφορειακής γραμμής 101",
		URL: "https://www.oasa.gr/blog/notice-1/", Published: time.Date(2026, 10, 30, 4, 42, 27, 0, time.UTC).Unix(),
		From: &from, Until: &until, Modes: []string{"bus", "trolley"}, Lines: []string{}, Stops: []string{}} // 040054 is no Stop of the test feed
	if !reflect.DeepEqual(got.Alerts[0], want) {
		t.Fatalf("got  %+v\nwant %+v", got.Alerts[0], want)
	}
	strike := got.Alerts[1]
	if strike.ID != "athenstransport:7" || strike.Source != "athenstransport" || !strike.Urgent ||
		!reflect.DeepEqual(strike.Modes, []string{"suburban"}) || strike.From != nil || strike.Until != nil ||
		len(strike.Lines) != 0 || len(strike.Stops) != 0 {
		t.Fatalf("strike %+v", strike)
	}

	// A source that fails keeps its Alerts; the other one still updates.
	a.alerts.Get = func(_ context.Context, url string) ([]byte, error) {
		if url == alerts.Feed {
			return []byte(alertFeed), nil
		}
		return nil, errors.New("404")
	}
	if err := a.alerts.Poll(context.Background()); err == nil {
		t.Fatal("a failed source must be reported")
	}
	if n := len(a.alerts.Items()); n != 2 {
		t.Fatalf("%d Alerts after a failed source", n)
	}
}
