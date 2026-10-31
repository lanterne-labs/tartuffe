package categorizer

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// isoDateRe finds an ISO date written into a fact's text ("le 2026-10-28",
// "(déclaration du 2025-11-05)"). The prompt asks for dates in words, but the
// model copies the dates its input shows (the REFERENCE DATE line, a speech
// header): 6.5 percent of the facts of the 2026-10-31 history audit carried
// one, so the rewrite is code, not prompt.
var isoDateRe = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)

var frenchMonthNames = [...]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}

// ISODatesInWords is isoDatesInWords for the repair of stored facts
// (dm facts dates-in-words).
func ISODatesInWords(text, lang string) string { return isoDatesInWords(text, lang) }

// isoDatesInWords rewrites every valid ISO date of text in words, in the
// fact's language: "28 octobre 2026" (French, "1er" for the first day),
// "28 October 2026" otherwise. A string that is not a real date is kept.
func isoDatesInWords(text, lang string) string {
	return isoDateRe.ReplaceAllStringFunc(text, func(s string) string {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return s
		}
		if lang == "fr" || lang == "" {
			day := strconv.Itoa(t.Day())
			if t.Day() == 1 {
				day = "1er"
			}
			return fmt.Sprintf("%s %s %d", day, frenchMonthNames[t.Month()-1], t.Year())
		}
		return t.Format("2 January 2006")
	})
}
