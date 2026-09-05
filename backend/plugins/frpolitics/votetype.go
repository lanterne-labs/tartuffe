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

// scrutinNumeroRe pulls the amendment number out of a scrutin title
// ("le sous-amendement n° 372 de M. Verny", "l'amendement n°30 de Mme Lebec").
var scrutinNumeroRe = regexp.MustCompile(`n°\s*(\d+)`)

// describeVotedObject returns the phrase the vote-statement templates wrap
// ("a voté CONTRE %s") and whether facts may be generated at all.
//
//   - text-level votes (ensemble/article/motion/declaration) and unclassified
//     scrutins keep the bill phrase: the ballot IS a position on the text.
//   - amendement / sous-amendement votes describe the modification itself —
//     number, author group, purpose from the AN amendments archive — because
//     the ballot judges only that modification. Without resolvable content the
//     scrutin is gated (ok=false): better no fact than a fact that misstates
//     what was voted.
func describeVotedObject(ctx context.Context, law Law, summary LawSummary, orgCache map[string]OrganeInfo) (string, bool) {
	objType := ClassifyScrutinObject(law.Title)
	billPhrase := prepareLawPhrase(summary.SimpleStatement)
	if IsTextLevelVote(objType) || objType == ObjectOther {
		return billPhrase, true
	}

	m := scrutinNumeroRe.FindStringSubmatch(law.Title)
	if m == nil {
		return "", false
	}
	numero := 0
	fmt.Sscanf(m[1], "%d", &numero)
	info := LookupAmendement(ctx, law.Legislature, numero, law.VoteDate)
	if info == nil {
		return "", false
	}
	purpose := purposePhrase(info)
	if purpose == "" {
		return "", false
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
	return fmt.Sprintf("%s n° %d%s %s, déposé sur %s", label, numero, group, purpose, billPhrase), true
}
