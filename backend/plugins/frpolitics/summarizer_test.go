package frpolitics

import "testing"

// TestMetaStatement: a statement that talks about the title or what could not
// be determined is a remark on the summarization, not a law statement. The
// feed of 2026-10-17 put "mais le contenu précis de l'article 7 n'est pas
// déterminable à partir du titre" into 299 ballots.
func TestMetaStatement(t *testing.T) {
	for _, s := range []string{
		"Crée un droit à l'aide à mourir, mais le contenu précis de l'article 7 n'est pas déterminable à partir du titre.",
		"Renforce la lutte contre le narcotrafic, mais le contenu précis de l'article 21 bis n'est pas déterminable à partir du titre.",
		"Modifie le code de l'énergie ; il est impossible de déterminer la direction.",
		"Le titre ne permet pas de connaître la mesure.",
	} {
		if !metaStatement(s) {
			t.Errorf("%q: want a meta statement", s)
		}
	}
	for _, s := range []string{
		"Crée un droit à l'aide à mourir.",
		"Renforce la lutte contre le narcotrafic et la criminalité organisée.",
		"Invite le Gouvernement à s'opposer à l'adoption de l'accord commercial entre l'Union européenne et le Mercosur.",
		"Détermine les conditions d'accès au titre de séjour.",
	} {
		if metaStatement(s) {
			t.Errorf("%q: want a law statement", s)
		}
	}
}
