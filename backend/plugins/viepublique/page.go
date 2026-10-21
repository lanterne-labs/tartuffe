package viepublique

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"

	"digital-mirror/backend/util"

	"golang.org/x/net/html"
)

const userAgent = "digital-mirror/1.0 (+https://github.com/lanterne-labs/mirror)"

// errGone marks a page that does not exist (404, 410): final, not retried.
var errGone = errors.New("page gone")

// Turn is one speaking turn of the text: the speaker's label as the
// transcript prints it ("LAURENT NUNEZ"), empty when the text has none.
type Turn struct {
	Label string
	Text  string
}

// fetchPage returns the turns of the full text of a speech page.
func fetchPage(ctx context.Context, client *http.Client, pageURL string) ([]Turn, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", pageURL, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, fmt.Errorf("fetching %s: HTTP %d: %w", pageURL, resp.StatusCode, errGone)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("fetching %s: HTTP %d", pageURL, resp.StatusCode)
	}
	return parsePage(io.LimitReader(resp.Body, 16<<20))
}

// parsePage reads the full text block of a speech page (the field
// "texte intégral") into turns. A paragraph whose first line is a label in
// capitals, followed by a line break, starts the turn of that speaker; any
// other paragraph goes on the current turn.
func parsePage(r io.Reader) ([]Turn, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing the page: %w", err)
	}
	body := findClass(doc, "field--name-field-texte-integral")
	if body == nil {
		return nil, fmt.Errorf("the page has no full text")
	}
	var turns []Turn
	for _, lines := range paragraphs(body) {
		if len(lines) > 1 && isLabel(lines[0]) {
			turns = append(turns, Turn{Label: lines[0], Text: strings.Join(lines[1:], "\n")})
			continue
		}
		text := strings.Join(lines, "\n")
		if len(turns) == 0 {
			turns = append(turns, Turn{Text: text})
			continue
		}
		turns[len(turns)-1].Text += "\n" + text
	}
	return turns, nil
}

func findClass(n *html.Node, class string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "class" && strings.Contains(" "+a.Val+" ", " "+class+" ") {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findClass(c, class); f != nil {
			return f
		}
	}
	return nil
}

// paragraphs returns the lines of each block (p, li, h*) under n, split on
// <br>, without the empty ones.
func paragraphs(n *html.Node) [][]string {
	var out [][]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && isBlock(n.Data) {
			var lines []string
			var cur strings.Builder
			flush := func() {
				if s := strings.Join(strings.Fields(cur.String()), " "); s != "" {
					lines = append(lines, s)
				}
				cur.Reset()
			}
			var text func(*html.Node)
			text = func(n *html.Node) {
				switch {
				case n.Type == html.TextNode:
					cur.WriteString(n.Data)
				case n.Type == html.ElementNode && n.Data == "br":
					flush()
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					text(c)
				}
			}
			text(n)
			flush()
			if len(lines) > 0 {
				out = append(out, lines)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func isBlock(tag string) bool {
	switch tag {
	case "p", "li", "h2", "h3", "h4", "blockquote":
		return true
	}
	return false
}

// isLabel reports whether a line is a speaker label: short, without a
// lowercase letter, with at least two letters ("LAURENT NUNEZ", "M. LE
// PRÉSIDENT").
func isLabel(s string) bool {
	if len([]rune(s)) > 60 {
		return false
	}
	letters := 0
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters >= 2
}

// ownTurns returns, for each turn of the person, the turn before it when
// someone else spoke it (the question), then the person's turn. A text
// without labels is the person's own when the person is its only speaker
// besides journalists; so is a turn whose label names no speaker of the
// index (LE PRÉSIDENT), in that case.
func ownTurns(turns []Turn, speech Speech, person string) []exchange {
	key := util.PersonNameKey(person)
	solo := true
	var others []string
	for _, s := range speech.Speakers {
		k := util.PersonNameKey(s.Name)
		if k == key {
			continue
		}
		others = append(others, k)
		if !s.IsJournalist() {
			solo = false
		}
	}
	owner := func(t Turn) bool {
		if t.Label == "" {
			return solo
		}
		lk := util.PersonNameKey(t.Label)
		if strings.HasPrefix(lk, "question") || strings.HasPrefix(lk, "journaliste") {
			return false
		}
		if namesSpeaker(lk, key) {
			return true
		}
		for _, o := range others {
			if namesSpeaker(lk, o) {
				return false
			}
		}
		return solo
	}
	var out []exchange
	for i, t := range turns {
		if !owner(t) {
			continue
		}
		ex := exchange{Answer: t}
		if i > 0 && !owner(turns[i-1]) {
			ex.Question = turns[i-1]
		}
		out = append(out, ex)
	}
	return out
}

// namesSpeaker reports whether a label key names the speaker of key: the
// full name, or a label ending in it ("M. LAURENT NUNEZ").
func namesSpeaker(label, key string) bool {
	return key != "" && strings.HasSuffix(label, key)
}

// exchange is one turn of the person with the question it answers.
type exchange struct {
	Question Turn
	Answer   Turn
}
