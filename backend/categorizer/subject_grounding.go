package categorizer

import (
	"regexp"
	"strings"
	"unicode"

	"digital-mirror/backend"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/util"

	"github.com/rs/zerolog/log"
)

// The extractor misspells the subject's name now and then ("Édoued Philippe",
// "jean-1uc melenchon", "francois bay's", "manuel b_ompard"), and every
// misspelling became a person of its own: the fact then sat apart from the real
// person, where no contradiction could ever reach it. The source is the ground
// truth for the spelling, so the subject takes the closest spelling found in the
// source or the target name.

// Edit budget, measured on the misspellings found in prod (1 to 3 edits on 11
// to 20 letters) against real near-namesakes that must stay apart (Bayrou and
// Baroin: 3 edits; Le Pen and Le Guen: 2 edits, "pen"/"guen" 2 of 4 letters).
// A well-formed subject may move by maxRepairEdits edits, none of its words by
// a third of its letters or more; a malformed one (a digit, an underscore, an
// escape: never a real name) by maxMalformedEdits. A handle such as "eciotti"
// for "Éric Ciotti" stays out of reach either way.
const (
	maxRepairEdits    = 2
	maxMalformedEdits = 3
	maxRepairRatio    = 0.25
	minRepairKey      = 8
)

// escapedRune matches a JSON escape the model wrote out literally ("\u00ad").
var escapedRune = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)

// nameToken is one run of letters/digits of a text and its byte span there.
type nameToken struct {
	key        string // lowercase, accent-free
	start, end int
}

// nameTokens splits s into letter/digit runs; everything else (spaces, hyphens,
// apostrophes, underscores) separates and invisible format characters vanish,
// so "Jean-Luc", "jean luc" and "jean_luc" tokenize alike.
func nameTokens(s string) []nameToken {
	var out []nameToken
	start := -1
	flush := func(end int) {
		if start >= 0 {
			key := strings.Map(func(r rune) rune {
				if unicode.Is(unicode.Cf, r) {
					return -1
				}
				return r
			}, s[start:end])
			out = append(out, nameToken{key: util.RemoveAccents(strings.ToLower(key)), start: start, end: end})
			start = -1
		}
	}
	for i, r := range s {
		if unicode.Is(unicode.Cf, r) {
			continue // invisible (soft hyphen, zero-width space): part of the word, dropped from its key
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(s))
	return out
}

// nameKey is the spelling-insensitive key of a name: its letters and digits
// only, lowercase and accent-free, with literal escapes and invisible
// characters dropped ("Édou\u00addard Philippe" → "edoudardphilippe").
func nameKey(name string) string {
	var b strings.Builder
	for _, t := range nameTokens(escapedRune.ReplaceAllString(name, "")) {
		b.WriteString(t.key)
	}
	return b.String()
}

// spanKey concatenates the keys of tokens[i:j].
func spanKey(tokens []nameToken, i, j int) string {
	var b strings.Builder
	for _, t := range tokens[i:j] {
		b.WriteString(t.key)
	}
	return b.String()
}

// groundSubject leaves a subject alone when its name (as persons are matched,
// util.NormalizePersonName) is the target or appears in the source. Otherwise
// it replaces the subject with the closest spelling within the edit budget —
// the target, or a run of words in the source of about the subject's length —
// and rewrites the old spelling in the fact texts. It reports whether it
// changed the subject. A subject with no close spelling is left as is: the
// model may legitimately name someone the source only designates (a handle, a
// title).
func groundSubject(fact *backend.Fact, source, target string) bool {
	subject := fact.SubjectEntity
	clean := escapedRune.ReplaceAllString(subject, "")
	key := nameKey(subject)
	if len([]rune(key)) < minRepairKey {
		return false
	}
	norm := util.NormalizePersonName(subject)
	if norm == util.NormalizePersonName(target) {
		return false
	}
	subjTokens := nameTokens(clean)
	words := len(subjTokens)
	srcTokens := nameTokens(source)
	spans := map[string]int{} // source spelling → occurrences
	var order []string        // spellings by first occurrence: deterministic ties
	for i := range srcTokens {
		for n := max(1, words-1); n <= words+1 && i+n <= len(srcTokens); n++ {
			spelling := source[srcTokens[i].start:srcTokens[i+n-1].end]
			if util.NormalizePersonName(spelling) == norm {
				return false
			}
			if spans[spelling] == 0 {
				order = append(order, spelling)
			}
			spans[spelling]++
		}
	}

	malformed := malformedName(subject)
	budget := maxRepairEdits
	if malformed {
		budget = maxMalformedEdits
	}
	best, bestDist, bestCount := "", budget+1, 0
	consider := func(spelling string, count int, fromTarget bool) {
		// A source spelling must itself look like a name: not a cut-off span
		// ("État (DRAC"), not a headline in capitals.
		if !fromTarget && (malformedName(spelling) || allCaps(spelling)) {
			return
		}
		candKey := nameKey(spelling)
		if abs(len([]rune(candKey))-len([]rune(key))) > budget {
			return
		}
		d := util.LevenshteinDistance(key, candKey)
		// Same letters, other separators: a well-formed subject is as likely
		// right as the source ("Ian Brossat" vs a hashtag "IanBrossat", "le
		// Gouvernement" vs a URL slug), so only the target may respell it.
		if d == 0 && !malformed && !fromTarget {
			return
		}
		longer := max(len([]rune(key)), len([]rune(candKey)))
		if d > budget || float64(d) > maxRepairRatio*float64(longer) {
			return
		}
		if budget == maxRepairEdits && !wordsClose(subjTokens, nameTokens(spelling), d) {
			return
		}
		if d < bestDist || (d == bestDist && count > bestCount) {
			best, bestDist, bestCount = spelling, d, count
		}
	}
	// The target first, winning ties: it is the tracked person's name.
	if target != "" {
		consider(target, int(^uint(0)>>1), true)
	}
	for _, spelling := range order {
		consider(spelling, spans[spelling], false)
	}
	if best == "" {
		return false
	}
	// Same person, better display: the source's own spelling of the target.
	if best == target {
		for _, spelling := range order {
			if !allCaps(spelling) && util.NormalizePersonName(spelling) == util.NormalizePersonName(target) {
				best = spelling
				break
			}
		}
	}

	fact.SubjectEntity = best
	fact.NormalizedFactText = replaceName(fact.NormalizedFactText, key, words, best)
	fact.NormalizedFactTextEn = replaceName(fact.NormalizedFactTextEn, key, words, best)
	log.Warn().
		Str("subject_entity", subject).
		Str("repaired_to", best).
		Str("source_url", fact.SourceURL).
		Msg("Subject name misspelled: replaced by the spelling in the source")
	metrics.RecordDecision(metrics.StageFactParse, metrics.OutcomeSubjectRepaired)
	return true
}

// malformedName reports a name no real person carries: a digit, an underscore,
// a literal escape, or any character that is neither a letter nor ordinary
// name punctuation.
func malformedName(name string) bool {
	if escapedRune.MatchString(name) {
		return true
	}
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.Is(unicode.Mn, r), unicode.IsSpace(r):
		case strings.ContainsRune("-'’.()", r):
		default:
			return true
		}
	}
	return false
}

// allCaps reports a spelling with letters and no lowercase one: a headline.
func allCaps(s string) bool {
	letters := false
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
		letters = letters || unicode.IsLetter(r)
	}
	return letters
}

// wordsClose guards a well-formed subject against a near-namesake: word by
// word, no word may change by a third of its letters or more ("pen" → "guen"
// is 2 of 4). Words that split or merge ("bardel la" → "bardella") cannot be
// aligned, so the whole name may then differ by one edit at most.
func wordsClose(a, b []nameToken, dist int) bool {
	if len(a) != len(b) {
		return dist <= 1
	}
	for i := range a {
		d := util.LevenshteinDistance(a[i].key, b[i].key)
		if 3*d >= max(len([]rune(a[i].key)), len([]rune(b[i].key))) && d > 0 {
			return false
		}
	}
	return true
}

// replaceName rewrites every run of up to words+1 words of text whose key is
// key with spelling. Literal escapes are blanked out byte for byte (each
// 6-byte "\uXXXX" becomes three 2-byte soft hyphens, which nameTokens drops)
// so the token spans still index into text.
func replaceName(text, key string, words int, spelling string) string {
	tokens := nameTokens(escapedRune.ReplaceAllStringFunc(text, func(m string) string { return strings.Repeat("\u00ad", len(m)/2) }))
	var b strings.Builder
	last := 0
	for i := 0; i < len(tokens); {
		matched := 0
		for j := i + 1; j <= len(tokens) && j-i <= words+1; j++ {
			if spanKey(tokens, i, j) == key {
				matched = j
				break
			}
		}
		if matched == 0 {
			i++
			continue
		}
		b.WriteString(text[last:tokens[i].start])
		b.WriteString(spelling)
		last = tokens[matched-1].end
		i = matched
	}
	b.WriteString(text[last:])
	return b.String()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
