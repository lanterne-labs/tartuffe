package statementday

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A search engine writes the publication date of a result its own way
// ("Mar 18, 2022", "7 déc. 2021", "2021-12-07"). parsePubDate reads the
// forms that name a day; a relative date ("il y a 3 jours") is not read.
var (
	pubMonthFirst = regexp.MustCompile(`^(\p{L}+)\.?\s+(\d{1,2}),?\s+(\d{4})$`)
	pubDayFirst   = regexp.MustCompile(`^(\d{1,2})(?:er)?\s+(\p{L}+)\.?\s+(\d{4})$`)
	pubISO        = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
)

// months by their first letters, French and English; the longer prefixes
// first where one starts another ("juil" before "jui").
var monthPrefixes = []struct {
	prefix string
	month  time.Month
}{
	{"jan", 1}, {"fév", 2}, {"fev", 2}, {"feb", 2}, {"mar", 3}, {"avr", 4}, {"apr", 4},
	{"mai", 5}, {"may", 5}, {"juil", 7}, {"jul", 7}, {"juin", 6}, {"jun", 6},
	{"aoû", 8}, {"aou", 8}, {"aug", 8}, {"sep", 9}, {"oct", 10}, {"nov", 11}, {"déc", 12}, {"dec", 12},
}

func monthOf(name string) (time.Month, bool) {
	name = strings.ToLower(name)
	for _, m := range monthPrefixes {
		if strings.HasPrefix(name, m.prefix) {
			return m.month, true
		}
	}
	return 0, false
}

func parsePubDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	var y, d int
	var m time.Month
	ok := false
	if g := pubISO.FindStringSubmatch(s); g != nil {
		y, _ = strconv.Atoi(g[1])
		mi, _ := strconv.Atoi(g[2])
		d, _ = strconv.Atoi(g[3])
		m, ok = time.Month(mi), mi >= 1 && mi <= 12
	} else if g := pubMonthFirst.FindStringSubmatch(s); g != nil {
		m, ok = monthOf(g[1])
		d, _ = strconv.Atoi(g[2])
		y, _ = strconv.Atoi(g[3])
	} else if g := pubDayFirst.FindStringSubmatch(s); g != nil {
		d, _ = strconv.Atoi(g[1])
		m, ok = monthOf(g[2])
		y, _ = strconv.Atoi(g[3])
	}
	if !ok {
		return time.Time{}, false
	}
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d {
		return time.Time{}, false
	}
	return t, true
}
