package news

import (
	"testing"

	"digital-mirror/backend"
	"digital-mirror/backend/core"
	"digital-mirror/backend/plugins"
)

// An article that never names the target gives them no fact: the first-person
// words of an interview are the interviewee's (prod fact 257496, 2026-11-12).
func TestDropUnnamedTarget(t *testing.T) {
	target := plugins.Target{Name: "nicolas dupont-aignan"}
	facts := []backend.Fact{
		{SubjectEntity: "Nicolas Dupont-Aignan", NormalizedFactText: "Nicolas Dupont-Aignan a déclaré soutenir Bruno Retailleau."},
		{SubjectEntity: "Philippe Juvin", NormalizedFactText: "Philippe Juvin soutient Bruno Retailleau."},
	}
	juvin := core.SourceDocument{
		URL:     "https://www.lopinion.fr/politique/philippe-juvin-lr-pourquoi-je-soutiens-bruno-retailleau",
		Title:   "Philippe Juvin (LR) : « Pourquoi je soutiens Bruno Retailleau »",
		Content: "Je soutiens Bruno Retailleau parce qu'il a fait le choix de devenir ministre.",
	}
	got := dropUnnamedTarget(target, juvin, facts)
	if len(got) != 1 || got[0].SubjectEntity != "Philippe Juvin" {
		t.Fatalf("want only the fact of Philippe Juvin, got %+v", got)
	}
	named := juvin
	named.Content += " Nicolas Dupont-Aignan a réagi."
	if got := dropUnnamedTarget(target, named, facts); len(got) != 2 {
		t.Fatalf("an article naming the target keeps its facts, got %d", len(got))
	}
}
