package backend

import (
	"regexp"
	"strings"
)

var passageWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

// QuotePosition returns where quote starts in article, in runes, or -1. The
// quote is found by its first words, whatever the typography between them
// (an extractor's quote has straight apostrophes where the page has curly
// ones, and may trim or join sentences after its first words).
func QuotePosition(article, quote string) int {
	var words []string
	for _, w := range passageWord.FindAllString(quote, 6) {
		words = append(words, regexp.QuoteMeta(w))
	}
	for _, n := range []int{6, 4, 3} {
		if len(words) < n {
			continue
		}
		re, err := regexp.Compile(`(?i)` + strings.Join(words[:n], `[^\p{L}\p{N}]+`))
		if err != nil {
			return -1
		}
		if loc := re.FindStringIndex(article); loc != nil {
			return len([]rune(article[:loc[0]]))
		}
	}
	return -1
}

// QuotePassage returns the part of article from before runes ahead of the
// quote to after runes past its start, and whether the quote was found.
func QuotePassage(article, quote string, before, after int) (string, bool) {
	pos := QuotePosition(article, quote)
	if pos < 0 {
		return "", false
	}
	r := []rune(article)
	return string(r[max(0, pos-before):min(len(r), pos+after)]), true
}
