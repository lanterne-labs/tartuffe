package backend

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// A statement's OWN date (facts.statement_date) is the date the person said
// or did what the fact states, when the text gives it: a 2019 tweet quoted in
// a 2025 article, "en mars 2024, il avait affirmé…". The source's date
// (fact_sources.published_at) is when the page was published; the judges
// read the statement's date first, so their three-month window and their
// ORDER rule work on the date of the words, not of the article (review of
// 2026-11-01, mirror-fmf: 4.6 pct of the text facts).
//
// The text often gives only a year or a month. The precision travels in the
// time's zone: a statement date is a UTC midnight in a zone named
// DatePrecisionYear, DatePrecisionMonth or DatePrecisionDay (offset 0, so
// every comparison and gap is unchanged), and FormatFactDate prints only what
// is known ("2019", "March 2024"). A time from anywhere else (a source date)
// prints as a full date.
const (
	DatePrecisionYear  = "year"
	DatePrecisionMonth = "month"
	DatePrecisionDay   = "day"
)

// WithDatePrecision returns t as a statement date of the given precision.
func WithDatePrecision(t time.Time, precision string) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.FixedZone(precision, 0))
}

// DatePrecisionOf returns the precision a statement date carries, or "" for
// any other time.
func DatePrecisionOf(t time.Time) string {
	switch name, _ := t.Zone(); name {
	case DatePrecisionYear, DatePrecisionMonth, DatePrecisionDay:
		return name
	}
	return ""
}

// FormatFactDate prints a fact's date for a judge's prompt: only the year or
// the month of a statement date known to that precision, else the full date;
// "unknown date" when there is none.
func FormatFactDate(t *time.Time) string {
	if t == nil {
		return "unknown date"
	}
	switch DatePrecisionOf(*t) {
	case DatePrecisionYear:
		return t.Format("2006")
	case DatePrecisionMonth:
		return t.Format("January 2006")
	}
	return t.Format("January 2, 2006")
}

// FormatFactDateISO prints a fact's date as "2019", "2019-03" or
// "2019-03-14", as precise as it is known; "" when there is none.
func FormatFactDateISO(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	switch DatePrecisionOf(*t) {
	case DatePrecisionYear:
		return t.Format("2006")
	case DatePrecisionMonth:
		return t.Format("2006-01")
	}
	return t.UTC().Format("2006-01-02")
}

// FactDate returns the date the judges read for a fact: its statement date
// (with its precision) when it has one, else the source's date.
func FactDate(statement *time.Time, precision *string, source *time.Time) *time.Time {
	if statement == nil || precision == nil {
		return source
	}
	d := WithDatePrecision(*statement, *precision)
	return &d
}

var statementDateRe = regexp.MustCompile(`^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?$`)

// ParseStatementDate reads "YYYY", "YYYY-MM" or "YYYY-MM-DD" as a statement
// date of that precision.
func ParseStatementDate(s string) (time.Time, error) {
	m := statementDateRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, fmt.Errorf("statement date %q: want YYYY, YYYY-MM or YYYY-MM-DD", s)
	}
	year, _ := strconv.Atoi(m[1])
	month, day, precision := 1, 1, DatePrecisionYear
	if m[2] != "" {
		month, _ = strconv.Atoi(m[2])
		precision = DatePrecisionMonth
	}
	if m[3] != "" {
		day, _ = strconv.Atoi(m[3])
		precision = DatePrecisionDay
	}
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if month < 1 || month > 12 || t.Day() != day {
		return time.Time{}, fmt.Errorf("statement date %q: no such day", s)
	}
	return WithDatePrecision(t, precision), nil
}
