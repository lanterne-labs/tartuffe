package news

import (
	"regexp"
	"strconv"
	"time"
)

// The date many news sites write in the article's URL: /2025/10/15/
// (lemonde, liberation), -29-10-2025- (leparisien), -20251025- (20minutes,
// lefigaro), _AV-202602260332 (bfmtv). It is the last fallback of a page's
// date, after the page's own metadata and the search result's: a page read
// on a retry after a bot check often carries no metadata (lejdd.fr,
// leparisien.fr since 2026-10-28, mirror-rxd).
var (
	urlDateSlashes = regexp.MustCompile(`/((?:19|20)\d{2})/(\d{2})/(\d{2})(?:/|$)`)
	urlDateISO     = regexp.MustCompile(`[/_-]((?:19|20)\d{2})-(\d{2})-(\d{2})(?:[/_.-]|$)`)
	urlDateDMY     = regexp.MustCompile(`[/_-](\d{2})-(\d{2})-((?:19|20)\d{2})(?:[/_.-]|$)`)
	urlDateCompact = regexp.MustCompile(`[/_-]((?:19|20)\d{2})(\d{2})(\d{2})(?:\d{4})?(?:[/_.-]|$)`)
)

// earliestURLYear bounds a date read in a URL: before it, the digits are an
// id, not a date.
const earliestURLYear = 1995

// DateFromURL returns the publication date written in the article's URL, or
// nil when the URL carries none (or a date after now, which is an id).
func DateFromURL(u string, now time.Time) *time.Time {
	type match struct {
		re      *regexp.Regexp
		y, m, d int
	}
	for _, c := range []match{
		{urlDateSlashes, 1, 2, 3},
		{urlDateISO, 1, 2, 3},
		{urlDateDMY, 3, 2, 1},
		{urlDateCompact, 1, 2, 3},
	} {
		for _, sm := range c.re.FindAllStringSubmatch(u, -1) {
			year, _ := strconv.Atoi(sm[c.y])
			month, _ := strconv.Atoi(sm[c.m])
			day, _ := strconv.Atoi(sm[c.d])
			if year < earliestURLYear || month < 1 || month > 12 || day < 1 {
				continue
			}
			t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
			if t.Day() != day || t.After(now) {
				continue
			}
			return &t
		}
	}
	return nil
}

// documentDate is a news page's publication date: the page's own metadata,
// else the date written in its URL, else the search result's. The URL comes
// before the search result: a search engine dates a page by when it indexed
// it again, so an old article without metadata took that day
// (lemonde.fr/en/.../2022/03/14/... stored as 2026-10-25, audit 2026-11-04).
func documentDate(page, search *time.Time, url string, now time.Time) *time.Time {
	if page != nil {
		return page
	}
	if d := DateFromURL(url, now); d != nil {
		return d
	}
	return search
}
