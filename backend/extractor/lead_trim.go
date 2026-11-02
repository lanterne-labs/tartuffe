package extractor

import "strings"

// leadTrimMaxPrefix bounds the text dropped before the title: a ticker or a
// "à la une" block is a few headlines; a longer prefix is more likely the
// article itself repeating its title further down.
const leadTrimMaxPrefix = 2500

// leadTrimMinTitle is the shortest title the cut trusts: a short title
// ("Direct", "Vidéo") can occur anywhere in the text.
const leadTrimMinTitle = 25

// stripLeadBeforeTitle drops the text that comes before the article's own
// title. Some sites put their live ticker or "à la une" block above the
// article (publicsenat: the news of 2026-10-24 opens a page of 2026-07-03);
// the extractor keeps it as the first paragraph, and the fact extractor then
// reads it as the article's news, dated by the article (review of
// 2026-10-27, mirror-d3a). The title is matched whole, then without the
// site's suffix (" - Public Sénat", " | Le Figaro").
func stripLeadBeforeTitle(content, title string) string {
	for _, t := range titleForms(title) {
		if len([]rune(t)) < leadTrimMinTitle {
			continue
		}
		i := strings.Index(content, t)
		if i <= 0 || i > leadTrimMaxPrefix {
			continue
		}
		return strings.TrimSpace(content[i:])
	}
	return content
}

// titleForms returns the title, then the title without a site suffix after
// " - ", " | " or " – ".
func titleForms(title string) []string {
	title = strings.Join(strings.Fields(title), " ")
	forms := []string{title}
	for _, sep := range []string{" | ", " - ", " – "} {
		if i := strings.LastIndex(title, sep); i > 0 {
			forms = append(forms, strings.TrimSpace(title[:i]))
		}
	}
	return forms
}
