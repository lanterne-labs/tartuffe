package statementday

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/search"
)

type fakeSearch struct{ results []search.SearchResult }

func (f fakeSearch) SearchWeb(string, int) ([]search.SearchResult, error) { return f.results, nil }

func finder(results []search.SearchResult, dayJSON string) *Finder {
	return &Finder{search: fakeSearch{results}, chat: func(_ context.Context, req llm.ChatRequest) (string, error) {
		if strings.Contains(req.Messages[0].Content, "SEARCH RESULTS") {
			return dayJSON, nil
		}
		return `{"queries": ["retailleau thérapies de conversion vote sénat décembre 2021"]}`, nil
	}}
}

func TestFind(t *testing.T) {
	results := []search.SearchResult{
		{URL: "https://a.example/1", Title: "Thérapies de conversion : le Sénat adopte le texte", Date: "7 déc. 2021", Snippet: "Le Sénat a adopté mardi 7 décembre la proposition de loi ; Bruno Retailleau a voté contre."},
		{URL: "https://a.example/2", Title: "Retour sur un vote", Date: "30 juin 2025", Snippet: "En 2021, il avait voté contre."},
	}
	src := time.Date(2025, 6, 29, 0, 0, 0, 0, time.UTC)
	month := backend.WithDatePrecision(time.Date(2021, 12, 1, 0, 0, 0, 0, time.UTC), backend.DatePrecisionMonth)
	year := backend.WithDatePrecision(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), backend.DatePrecisionYear)
	cases := []struct {
		name  string
		known time.Time
		reply string
		want  string // "" : no day
	}{
		{"a day the result states", month, `{"reasoning":"r1","result":1,"how":"stated","evidence":"Le Sénat a adopté mardi 7 décembre","day":"2021-12-07"}`, "2021-12-07"},
		{"typography of the evidence does not count", year, `{"reasoning":"r1","result":1,"how":"stated","evidence":"le sénat a adopté, mardi 7 Décembre","day":"2021-12-07"}`, "2021-12-07"},
		{"the day of a result of that day", month, `{"reasoning":"r1","result":1,"how":"published","evidence":null,"day":"2021-12-07"}`, "2021-12-07"},
		{"the day before a result names", month, `{"reasoning":"r1","result":1,"how":"published","evidence":null,"day":"2021-12-06"}`, "2021-12-06"},
		{"a result published long after", year, `{"reasoning":"r2","result":2,"how":"published","evidence":null,"day":"2021-12-07"}`, ""},
		{"a result published before the day", month, `{"reasoning":"r1","result":1,"how":"published","evidence":null,"day":"2021-12-09"}`, ""},
		{"no day", month, `{"reasoning":"none","result":null,"how":null,"evidence":null,"day":null}`, ""},
		{"outside the known month", month, `{"reasoning":"r1","result":1,"how":"stated","evidence":"mardi 7 décembre","day":"2021-11-07"}`, ""},
		{"outside the known year", year, `{"reasoning":"r2","result":2,"how":"published","evidence":null,"day":"2025-06-30"}`, ""},
		{"evidence not in the result named", month, `{"reasoning":"r2","result":2,"how":"stated","evidence":"mardi 7 décembre","day":"2021-12-07"}`, ""},
		{"evidence invented", month, `{"reasoning":"r1","result":1,"how":"stated","evidence":"voté le 8 décembre 2021","day":"2021-12-08"}`, ""},
		{"evidence without the day", month, `{"reasoning":"r1","result":1,"how":"stated","evidence":"Bruno Retailleau a voté contre","day":"2021-12-07"}`, ""},
		{"evidence of another day", month, `{"reasoning":"r1","result":1,"how":"stated","evidence":"mardi 7 décembre","day":"2021-12-17"}`, ""},
		{"the weekday the evidence names is the day's", month, `{"reasoning":"r1","result":1,"how":"stated","evidence":"mardi 7 décembre","day":"2021-12-07"}`, "2021-12-07"},
		{"no way given", month, `{"reasoning":"r1","result":1,"evidence":"mardi 7 décembre","day":"2021-12-07"}`, ""},
		{"no result named", month, `{"reasoning":"r?","result":9,"how":"stated","evidence":"mardi 7 décembre","day":"2021-12-07"}`, ""},
		{"a month is not a day", year, `{"reasoning":"r1","result":1,"how":"stated","evidence":"mardi 7 décembre","day":"2021-12"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := finder(results, c.reply).Find(context.Background(), Statement{Person: "Bruno Retailleau", Text: "a voté contre", Known: c.known, SourceDate: &src})
			if err != nil {
				t.Fatal(err)
			}
			if have := backend.FormatFactDateISO(got.Day); have != c.want {
				t.Fatalf("day %q, want %q (%s)", have, c.want, got.Reasoning)
			}
			if c.want != "" && (got.URL != "https://a.example/1" || backend.DatePrecisionOf(*got.Day) != backend.DatePrecisionDay) {
				t.Fatalf("found %+v", got)
			}
		})
	}
}

func TestFindRefusesADayAfterTheSource(t *testing.T) {
	results := []search.SearchResult{{URL: "u", Title: "t", Snippet: "a déclaré le 20 mars 2022 sur Brut"}}
	src := time.Date(2022, 3, 10, 0, 0, 0, 0, time.UTC)
	known := backend.WithDatePrecision(time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC), backend.DatePrecisionMonth)
	got, err := finder(results, `{"reasoning":"r","result":1,"how":"stated","evidence":"a déclaré le 20 mars 2022","day":"2022-03-20"}`).Find(context.Background(), Statement{Known: known, SourceDate: &src})
	if err != nil || got.Day != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestFindNeedsAYearOrAMonth(t *testing.T) {
	day := backend.WithDatePrecision(time.Now(), backend.DatePrecisionDay)
	if _, err := finder(nil, "{}").Find(context.Background(), Statement{Known: day}); err == nil {
		t.Fatal("a day was searched again")
	}
}

func TestParsePubDate(t *testing.T) {
	for in, want := range map[string]string{
		"Mar 18, 2022": "2022-03-18", "June 14, 2011": "2011-06-14", "7 déc. 2021": "2021-12-07",
		"1er juil. 2019": "2019-07-01", "29 août 2026": "2026-08-29", "2021-12-07T10:00:00Z": "2021-12-07",
		"12 juin 2024": "2024-06-12", "il y a 3 jours": "", "": "", "31 févr. 2021": "",
	} {
		got, ok := parsePubDate(in)
		have := ""
		if ok {
			have = got.Format("2006-01-02")
		}
		if have != want {
			t.Errorf("parsePubDate(%q) = %q, want %q", in, have, want)
		}
	}
}

type fakeReader map[string]Page

func (f fakeReader) Read(_ context.Context, u string) (Page, error) {
	p, ok := f[u]
	if !ok {
		return Page{}, fmt.Errorf("no page %s", u)
	}
	p.URL = u
	return p, nil
}

// When the search results give no day, the pages are read: the day a page
// writes out is kept, with the page for the caller to queue.
func TestFindReadsThePages(t *testing.T) {
	long := strings.Repeat("Le débat a duré toute la soirée au Palais du Luxembourg. ", 40)
	results := []search.SearchResult{
		{URL: "https://x.com/someone/status/1", Title: "a tweet", Snippet: "…"},
		{URL: "https://a.example/recall", Title: "Retour sur un vote", Date: "30 juin 2025", Snippet: "En 2021, il avait voté contre."},
		{URL: "https://a.example/report", Title: "Le Sénat adopte le texte", Date: "8 déc. 2021", Snippet: "Les sénateurs ont adopté la proposition de loi."},
	}
	pub := time.Date(2021, 12, 8, 9, 0, 0, 0, time.UTC)
	reader := fakeReader{
		"https://a.example/report": {Title: "Le Sénat adopte le texte", Published: &pub, Text: long + "Mardi 7 décembre, Bruno Retailleau a voté contre la proposition de loi. " + long},
		"https://a.example/recall": {Title: "Retour sur un vote", Text: long},
	}
	var pagesAsked []string
	f := &Finder{search: fakeSearch{results}, read: reader, chat: func(_ context.Context, req llm.ChatRequest) (string, error) {
		c := req.Messages[0].Content
		switch {
		case strings.Contains(c, "SEARCH RESULTS"):
			return `{"reasoning":"no result gives it","result":null,"how":null,"evidence":null,"day":null}`, nil
		case strings.Contains(c, "\nPAGE\n"):
			if strings.Contains(c, "Title: Retour sur un vote") {
				pagesAsked = append(pagesAsked, "recall")
				return `{"reasoning":"only recalls","how":null,"evidence":null,"day":null}`, nil
			}
			pagesAsked = append(pagesAsked, "report")
			if !strings.Contains(c, "Published: Wednesday 2021-12-08") {
				t.Errorf("the page prompt lacks the weekday of the publication")
			}
			return `{"reasoning":"the page writes it","how":"stated","evidence":"Mardi 7 décembre, Bruno Retailleau a voté contre","day":"2021-12-07"}`, nil
		}
		return `{"queries": ["q"]}`, nil
	}}
	known := backend.WithDatePrecision(time.Date(2021, 12, 1, 0, 0, 0, 0, time.UTC), backend.DatePrecisionMonth)
	got, err := f.Find(context.Background(), Statement{Person: "Bruno Retailleau", Text: "a voté contre", Known: known})
	if err != nil {
		t.Fatal(err)
	}
	if backend.FormatFactDateISO(got.Day) != "2021-12-07" || got.URL != "https://a.example/report" {
		t.Fatalf("found %+v", got)
	}
	// the page of the known period is read first, the tweet never
	if len(pagesAsked) != 1 || pagesAsked[0] != "report" {
		t.Fatalf("pages read %v, want the report only", pagesAsked)
	}
	if page := articleOf(got); page == nil || page.URL != "https://a.example/report" || page.Published == nil {
		t.Fatalf("no article to queue: %+v", got.Page)
	}
}

type memStore struct {
	pending []Pending
	days    map[int64]string
	queued  []string
}

func (m *memStore) ListFactsForStatementDay(context.Context, bool, int, bool) ([]Pending, error) {
	var out []Pending
	for _, p := range m.pending {
		if _, done := m.days[p.FactID]; !done {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) SetStatementDay(_ context.Context, id int64, day *time.Time) error {
	m.days[id] = backend.FormatFactDateISO(day)
	return nil
}

func (m *memStore) QueueArticle(_ context.Context, personID int64, _ string, page Page) (bool, error) {
	m.queued = append(m.queued, fmt.Sprintf("%d %s %s", personID, page.URL, backend.FormatFactDateISO(page.Published)))
	return true, nil
}

// A run stores the day found and queues the article that gave it; a fact no
// result dates is marked searched and keeps its year.
func TestRunStoresTheDayAndQueuesTheArticle(t *testing.T) {
	results := []search.SearchResult{{URL: "https://a.example/1", Title: "Le Sénat adopte le texte", Date: "7 déc. 2021", Snippet: "Le Sénat a adopté mardi 7 décembre la proposition de loi."}}
	article := strings.Repeat("Le Sénat a adopté mardi 7 décembre la proposition de loi. ", 60)
	f := &Finder{search: fakeSearch{results}, read: fakeReader{"https://a.example/1": {Title: "t", Text: article}}, chat: func(_ context.Context, req llm.ChatRequest) (string, error) {
		c := req.Messages[0].Content
		switch {
		case strings.Contains(c, "SEARCH RESULTS") && strings.Contains(c, "Fact: a voté contre"):
			return `{"reasoning":"r1","result":1,"how":"stated","evidence":"Le Sénat a adopté mardi 7 décembre","day":"2021-12-07"}`, nil
		case strings.Contains(c, "SEARCH RESULTS"), strings.Contains(c, "\nPAGE\n"):
			return `{"reasoning":"none","result":null,"how":null,"evidence":null,"day":null}`, nil
		}
		return `{"queries": ["q"]}`, nil
	}}
	year := backend.WithDatePrecision(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), backend.DatePrecisionYear)
	store := &memStore{days: map[int64]string{}, pending: []Pending{
		{FactID: 1, PersonID: 7, Statement: Statement{Person: "P", Text: "a voté contre", Known: year}},
		{FactID: 2, PersonID: 7, Statement: Statement{Person: "P", Text: "a dit autre chose", Known: year}},
	}}
	res, err := Run(context.Background(), store, f, Options{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Searched != 2 || res.Found != 1 || res.Queued != 1 {
		t.Fatalf("result %+v", res)
	}
	if store.days[1] != "2021-12-07" || store.days[2] != "" {
		t.Fatalf("days %v", store.days)
	}
	if len(store.queued) != 1 || store.queued[0] != "7 https://a.example/1 2021-12-07" {
		t.Fatalf("queued %v", store.queued)
	}
}

// A day written without its year is of the year the text was published: a
// page of 2021 that says "le 26 septembre" does not date a statement the
// site knows as of 2020; nor does "le mardi 23 juillet" date a Saturday.
func TestStatedDayNeedsItsYearOrItsTime(t *testing.T) {
	year := func(y int) time.Time {
		return backend.WithDatePrecision(time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC), backend.DatePrecisionYear)
	}
	str := func(s string) *string { return &s }
	pub := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name     string
		known    time.Time
		evidence string
		day      string
		pub      time.Time
		hasPub   bool
		want     bool
	}{
		{"another year than the page", year(2020), "Le 26 septembre sur Europe 1 et CNews, Eric Zemmour a déclaré", "2020-09-26", pub(2021, 9, 30), true, false},
		{"the page's own year", year(2021), "Le 26 septembre sur Europe 1 et CNews, Eric Zemmour a déclaré", "2021-09-26", pub(2021, 9, 30), true, true},
		{"the year is written", year(2013), "5 avril 2013. Marine Le Pen est l'invitée de la matinale", "2013-04-05", pub(2025, 3, 31), true, true},
		{"no year and no publication date", year(2013), "le 5 avril, Marine Le Pen est l'invitée", "2013-04-05", time.Time{}, false, false},
		{"another weekday", year(2022), "Le mardi 23 juillet 2022, Bruno Retailleau l'assurait", "2022-07-23", pub(2024, 9, 20), true, false},
		{"the weekday of the day", year(2024), "Le mardi 23 juillet, Bruno Retailleau l'assurait", "2024-07-23", pub(2024, 9, 20), true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := checkDay(Statement{Known: c.known}, dayReply{How: str("stated"), Evidence: &c.evidence, Day: &c.day}, "… "+c.evidence+" …", c.pub, c.hasPub)
			if (got.Day != nil) != c.want {
				t.Fatalf("day %v, want kept=%v (%s)", got.Day, c.want, got.Reasoning)
			}
		})
	}
}

// A day taken from the search engine's date for a page falls when the page
// gives another date: the pages are read instead.
func TestPublishedDayNeedsThePageToAgree(t *testing.T) {
	results := []search.SearchResult{{URL: "https://a.example/interview", Title: "Les réponses de Jordan Bardella", Date: "26 mai 2019", Snippet: "Voterez-vous la réforme de la PAC ?"}}
	own := time.Date(2019, 5, 9, 8, 0, 0, 0, time.UTC)
	text := strings.Repeat("Voterez-vous la réforme de la PAC ? Non, répond Jordan Bardella. ", 40)
	f := &Finder{search: fakeSearch{results}, read: fakeReader{"https://a.example/interview": {Title: "Les réponses", Text: text, Published: &own}}, chat: func(_ context.Context, req llm.ChatRequest) (string, error) {
		c := req.Messages[0].Content
		switch {
		case strings.Contains(c, "SEARCH RESULTS"):
			return `{"reasoning":"the interview","result":1,"how":"published","evidence":null,"day":"2019-05-26"}`, nil
		case strings.Contains(c, "\nPAGE\n"):
			if !strings.Contains(c, "Published: Thursday 2019-05-09") {
				t.Errorf("the page is not given with its own date")
			}
			return `{"reasoning":"the interview, published that day","how":"published","evidence":null,"day":"2019-05-09"}`, nil
		}
		return `{"queries": ["q"]}`, nil
	}}
	got, err := f.Find(context.Background(), Statement{Person: "Jordan Bardella", Text: "ne voterait pas la PAC", Known: backend.WithDatePrecision(time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC), backend.DatePrecisionYear)})
	if err != nil {
		t.Fatal(err)
	}
	if backend.FormatFactDateISO(got.Day) != "2019-05-09" {
		t.Fatalf("day %s, want the page's own date (%s)", backend.FormatFactDateISO(got.Day), got.Reasoning)
	}
}
