package viepublique

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"digital-mirror/backend/util"
	"digital-mirror/backend/webfetch"

	"golang.org/x/net/html"
)

// errGone marks a page that does not exist (404, 410): final, not retried.
var errGone = webfetch.ErrGone

// Turn is one speaking turn of the text: the speaker's label as the
// transcript prints it ("LAURENT NUNEZ"), empty when the text has none.
type Turn struct {
	Label string
	Text  string
}

// fetchPage returns the turns of the full text of a speech page.
func fetchPage(ctx context.Context, pageURL, person string) ([]Turn, error) {
	p, err := webfetch.Fetch(ctx, webfetch.Request{URL: pageURL, Source: sourceType, Purpose: "speech", Person: person})
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", pageURL, err)
	}
	turns, err := parsePage(bytes.NewReader(p.Body))
	if err != nil {
		webfetch.Note(ctx, sourceType, pageURL, "no_text")
		return nil, fmt.Errorf("%s: %w", pageURL, err)
	}
	return turns, nil
}

// parsePage reads the full text block of a speech page (the field
// "texte intégral") into turns. A paragraph whose first line is a speaker
// label, alone or followed by a line break, starts the turn of that speaker;
// any other paragraph goes on the current turn.
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
		if isLabel(lines[0]) {
			turns = append(turns, Turn{Label: lines[0], Text: strings.Join(lines[1:], "\n")})
			continue
		}
		text := strings.Join(lines, "\n")
		if len(turns) == 0 {
			turns = append(turns, Turn{Text: text})
			continue
		}
		if turns[len(turns)-1].Text != "" {
			text = turns[len(turns)-1].Text + "\n" + text
		}
		turns[len(turns)-1].Text = text
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

// isLabel reports whether a line is a speaker label: a name in which the
// surname is in capitals ("LAURENT NUNEZ", "Gérald DARMANIN", "M. LE
// PRÉSIDENT"), optionally followed by a comma and the speaker's role ("Maud
// BREGEON, ministre déléguée, porte-parole du Gouvernement").
func isLabel(s string) bool {
	name := labelName(s)
	if genericLabels[util.PersonNameKey(name)] {
		return true
	}
	if len([]rune(s)) > 160 || len([]rune(name)) > 50 {
		return false
	}
	words := strings.Fields(name)
	// A name has two words at least: a lone "OK" or "NON" is an answer.
	if len(words) < 2 || len(words) > 6 {
		return false
	}
	capitals := false
	for _, w := range words {
		letters, upper := 0, 0
		for _, r := range w {
			if unicode.IsLetter(r) {
				letters++
				if unicode.IsUpper(r) {
					upper++
				}
			}
		}
		first, _ := utf8.DecodeRuneInString(w)
		switch {
		case letters >= 2 && upper == letters:
			capitals = true
		case letters == 0 || unicode.IsUpper(first) || particles[strings.ToLower(w)]:
		default:
			return false
		}
	}
	return capitals && !strings.ContainsAny(name, "?!;«»\"")
}

// genericLabels are the labels of a speaker the transcript does not name:
// never the person's.
var genericLabels = map[string]bool{"journaliste": true, "journalistes": true, "unjournaliste": true, "unejournaliste": true,
	"question": true, "questions": true, "intervenant": true, "intervenante": true, "public": true, "uneintervenante": true, "unintervenant": true}

var particles = map[string]bool{"de": true, "du": true, "des": true, "le": true, "la": true, "d'": true, "von": true, "van": true}

// labelName is the name part of a label, before the role.
func labelName(label string) string {
	name, _, _ := strings.Cut(label, ",")
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(name), ":"))
}

// roleLabels are the labels that name a function, not a person: the
// person's own when the person is the text's only speaker besides
// journalists.
var roleLabels = []string{"lepresident", "lapresidente", "lepremierministre", "lapremiereministre", "leministre", "laministre", "leporteparole", "laporteparole", "lasecretairedetat", "lesecretairedetat"}

func isRoleLabel(key string) bool {
	key = strings.TrimPrefix(strings.TrimPrefix(key, "mme"), "m")
	for _, r := range roleLabels {
		if strings.HasPrefix(key, r) {
			return true
		}
	}
	return false
}

// ownTurns returns, for each turn of the person, the turn before it when
// someone else spoke it (the question), then the person's turn. A label is
// the person's when it names the person, or names a function (LE
// PRÉSIDENT) while the person is the only speaker besides journalists; any
// other label is someone else's, listed in the index or not (a journalist).
// A text without any label is the person's in that case too.
func ownTurns(turns []Turn, speech Speech, person string) []exchange {
	key := util.PersonNameKey(person)
	surname := surnameKey(person)
	solo := true
	for _, s := range speech.Speakers {
		if util.PersonNameKey(s.Name) != key && !s.IsJournalist() {
			solo = false
		}
	}
	owner := func(t Turn) bool {
		if t.Label == "" {
			return solo
		}
		lk := util.PersonNameKey(labelName(t.Label))
		switch {
		case lk == key || (surname != "" && strings.HasSuffix(lk, surname)):
			return true
		case isRoleLabel(lk):
			return solo
		}
		return false
	}
	var out []exchange
	for i, t := range turns {
		if !owner(t) || strings.TrimSpace(t.Text) == "" {
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

// surnameKey is the key of the person's surname: the last word, with the
// particle before it ("Marine Le Pen" → "lepen").
func surnameKey(person string) string {
	words := strings.Fields(person)
	if len(words) < 2 {
		return ""
	}
	last := words[len(words)-1]
	if len(words) >= 3 && particles[strings.ToLower(words[len(words)-2])] {
		last = words[len(words)-2] + last
	}
	return util.PersonNameKey(last)
}

// exchange is one turn of the person with the question it answers.
type exchange struct {
	Question Turn
	Answer   Turn
}
