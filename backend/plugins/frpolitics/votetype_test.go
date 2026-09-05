package frpolitics

import "testing"

// Titles are real prod scrutin titles (legislature 16/17).
func TestClassifyScrutinObject(t *testing.T) {
	cases := []struct {
		titre string
		want  ScrutinObjectType
	}{
		{"l'ensemble de la proposition de loi visant à la nationalisation d'ArcelorMittal France afin de préserver la souveraineté industrielle de la France (première lecture).", ObjectEnsemble},
		{"l'article premier de la proposition de loi visant à la nationalisation d'ArcelorMittal France", ObjectArticle},
		{"l'article 3 de la proposition de loi visant à la nationalisation d'ArcelorMittal France", ObjectArticle},
		{"le sous-amendement n° 372 de M. Verny à l'amendement n° 96 de M. Bernhardt après l'article premier bis de la proposition de loi visant à la nationalisation d'ArcelorMittal France", ObjectSousAmendement},
		{"l'amendement n° 96 de M. Bernhardt après l'article premier bis de la proposition de loi visant à la nationalisation d'ArcelorMittal France", ObjectAmendement},
		{"l'amendement n° 4 de M. Sitzenstuhl et les amendements identiques suivants à l'article premier de la proposition de loi", ObjectAmendement},
		{"la motion de censure déposée en application de l'article 49, alinéa 2, de la Constitution", ObjectMotion},
		{"la motion de rejet préalable, déposée par Mme Mathilde Panot, du projet de loi de finances pour 2026", ObjectMotion},
		{"la déclaration du Gouvernement relative à la politique générale", ObjectDeclaration},
	}
	for _, c := range cases {
		if got := ClassifyScrutinObject(c.titre); got != c.want {
			t.Errorf("ClassifyScrutinObject(%.60q) = %s, want %s", c.titre, got, c.want)
		}
	}

	if IsTextLevelVote(ObjectSousAmendement) || IsTextLevelVote(ObjectAmendement) {
		t.Error("amendment classes must not count as text-level votes")
	}
	if !IsTextLevelVote(ObjectEnsemble) || !IsTextLevelVote(ObjectArticle) || !IsTextLevelVote(ObjectMotion) {
		t.Error("ensemble/article/motion are text-level votes")
	}
}
