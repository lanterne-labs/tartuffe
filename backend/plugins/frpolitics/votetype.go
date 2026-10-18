package frpolitics

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// ScrutinObjectType classifies WHAT a scrutin voted on. Scrutin titles are
// rigidly formulaic, so this is deterministic — no LLM.
//
// The class decides how much a ballot means (mirror-f28): a vote on the text
// itself (ensemble, article, motion) is a direct position on the bill; a vote
// on an amendement/sous-amendement judges only that modification, in context,
// and must never be read as a position on the parent bill (prod scrutins
// 17-4373/17-4375: LFI voted against BOTH lengthening an audit delay AND the
// audit itself — one coherent position, invisible at title level).
type ScrutinObjectType string

const (
	ObjectEnsemble       ScrutinObjectType = "ensemble"
	ObjectArticle        ScrutinObjectType = "article"
	ObjectMotion         ScrutinObjectType = "motion"
	ObjectAmendement     ScrutinObjectType = "amendement"
	ObjectSousAmendement ScrutinObjectType = "sous_amendement"
	ObjectDeclaration    ScrutinObjectType = "declaration"
	ObjectOther          ScrutinObjectType = "other"
)

// ClassifyScrutinObject returns the voted-object class for a scrutin title.
// Order matters: "sous-amendement" contains "amendement", and amendment titles
// name the parent article ("à l'article premier"), so amendment checks come
// first and article detection requires the title to START with the article.
func ClassifyScrutinObject(titre string) ScrutinObjectType {
	t := strings.ToLower(strings.TrimSpace(titre))
	switch {
	case strings.Contains(t, "sous-amendement"):
		return ObjectSousAmendement
	case strings.Contains(t, "l'amendement"), strings.Contains(t, "l’amendement"),
		strings.Contains(t, "les amendements"):
		return ObjectAmendement
	case strings.Contains(t, "motion "), strings.HasPrefix(t, "la motion"):
		return ObjectMotion
	case strings.Contains(t, "l'ensemble"), strings.Contains(t, "l’ensemble"):
		return ObjectEnsemble
	case strings.HasPrefix(t, "l'article"), strings.HasPrefix(t, "l’article"),
		strings.HasPrefix(t, "la première partie"), strings.HasPrefix(t, "la seconde partie"):
		return ObjectArticle
	case strings.Contains(t, "déclaration du gouvernement"), strings.Contains(t, "declaration du gouvernement"):
		return ObjectDeclaration
	default:
		return ObjectOther
	}
}

// IsTextLevelVote reports whether the ballot is a direct position on the text
// itself — the classes whose votes may break a stated position on the bill.
func IsTextLevelVote(t ScrutinObjectType) bool {
	switch t {
	case ObjectEnsemble, ObjectArticle, ObjectMotion, ObjectDeclaration:
		return true
	}
	return false
}

// articleTitleRe splits the title of a scrutin on an article: the article
// ("l'article 19 bis", "l'article 24 (examen prioritaire)") and the bill it
// belongs to ("la proposition de loi visant à ...").
var articleTitleRe = regexp.MustCompile(`^(?i)(l['’]article\s+.+?)\s+(?:de|du|des)\s+((?:la |l['’])?(?:projet|proposition)\s.+)$`)

// partTitleRe splits the title of a scrutin on a part of a budget bill ("la
// première partie du projet de loi de finances pour 2026").
var partTitleRe = regexp.MustCompile(`^(?i)(la (?:première|seconde) partie)\s+(?:de|du)\s+((?:la |l['’])?(?:projet|proposition)\s.+)$`)

// splitArticle splits the title of a scrutin on an article or a part of a
// bill into the voted object ("l'article 7") and the bill ("la proposition
// de loi relative au droit à l'aide à mourir (première lecture)."). ok is
// false for another title shape.
func splitArticle(titre string) (article, bill string, ok bool) {
	t := strings.TrimSpace(titre)
	for _, re := range []*regexp.Regexp{articleTitleRe, partTitleRe} {
		if m := re.FindStringSubmatch(t); m != nil {
			return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), true
		}
	}
	return "", "", false
}

// summaryTitle is the title the law summarizer reads: the bill for a
// scrutin on an article of it, the scrutin title otherwise. The summarizer
// describes a text it can see from its title; the article's content is not
// in the title, and a model asked about it says so in its statement.
func summaryTitle(law Law) string {
	if ClassifyScrutinObject(law.Title) == ObjectArticle {
		if _, bill, ok := splitArticle(law.Title); ok {
			return bill
		}
	}
	return law.Title
}

// scrutinNumeroRe pulls the amendment number out of a scrutin title
// ("le sous-amendement n° 372 de M. Verny", "l'amendement n°30 de Mme Lebec").
var scrutinNumeroRe = regexp.MustCompile(`n°\s*(\d+)`)

// resolveVotedObject resolves what a scrutin voted on, from the scrutin alone
// (no summary needed, so a gated scrutin costs no LLM call), and whether facts
// may be generated at all. votedObjectPhrase turns it into the phrase the
// vote-statement templates wrap ("a voté CONTRE %s").
//
//   - ensemble/motion/declaration votes and unclassified scrutins keep the
//     bill phrase: the ballot IS a position on the text.
//   - article votes name the article and what it does before the bill
//     phrase ("l'article 7 (« Permet ... ») du texte de loi : « ... »"): the
//     ballot judges that article of the bill. articleResolver reads the
//     article in the text of the bill; here the article alone.
//   - amendement / sous-amendement votes describe the modification itself —
//     number, author group, purpose from the AN amendments archive, returned
//     as amendment — because the ballot judges only that modification.
//     Without resolvable content, or an article title that does not split,
//     the scrutin is gated (ok=false): better no fact than a fact that
//     misstates what was voted.
//
// The error is non-nil only when the amendments archive is not available.
func resolveVotedObject(ctx context.Context, law Law, orgCache map[string]OrganeInfo) (object string, ok bool, err error) {
	objType := ClassifyScrutinObject(law.Title)
	if objType == ObjectArticle {
		// An article vote is resolved by articleResolver, which reads what
		// the article does in the text. Without it the ballot would carry
		// the bill's purpose for one article: gated.
		return "", false, nil
	}
	if IsTextLevelVote(objType) || objType == ObjectOther {
		return "", true, nil
	}

	m := scrutinNumeroRe.FindStringSubmatch(law.Title)
	if m == nil {
		return "", false, nil
	}
	numero := 0
	fmt.Sscanf(m[1], "%d", &numero)
	info, err := LookupAmendement(ctx, law.Legislature, numero, law.VoteDate)
	if err != nil {
		return "", false, err
	}
	if info == nil {
		return "", false, nil
	}
	purpose := purposePhrase(info)
	if purpose == "" {
		return "", false, nil
	}

	label := "l'amendement"
	if objType == ObjectSousAmendement {
		label = "le sous-amendement"
	}
	group := ""
	if gi, ok := orgCache[info.AuthorGroupRef]; ok {
		name := gi.ShortName
		if name == "" {
			name = gi.LongName
		}
		if name != "" {
			group = fmt.Sprintf(" (%s)", name)
		}
	}
	return fmt.Sprintf("%s n° %d%s %s", label, numero, group, purpose), true, nil
}

// votedObjectPhrase is the voted object's phrase: the bill phrase, preceded
// by the article or the amendment when the ballot judged one (see
// resolveVotedObject).
func votedObjectPhrase(object string, summary LawSummary) string {
	billPhrase := prepareLawPhrase(summary.SimpleStatement)
	switch {
	case object == "":
		return billPhrase
	case isArticlePhrase(object):
		return object + " du " + strings.TrimPrefix(billPhrase, "le ")
	default:
		return object + ", déposé sur " + billPhrase
	}
}

// isArticlePhrase reports whether object names an article or a part of the
// bill (splitArticle), not an amendment.
func isArticlePhrase(object string) bool {
	o := strings.ToLower(object)
	return strings.HasPrefix(o, "l'article") || strings.HasPrefix(o, "l’article") || strings.HasPrefix(o, "la première partie") || strings.HasPrefix(o, "la seconde partie")
}
