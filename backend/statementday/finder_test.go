package statementday

import (
	"context"
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
