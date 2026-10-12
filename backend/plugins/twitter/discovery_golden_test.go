package twitter

import (
	"sync"
	"testing"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/search"
)

// TestHandleDiscoveryGolden pins the handle judge of the Twitter discovery
// (askHandle): from the search results, the live bios and the Wikipedia
// summary, which handle is the official account of the person, or none?
//
// Source: the prod DB holds the 24 handles linked on 2026-05-25 and no
// evidence of any discovery call (search results and bios are not stored).
// So the evidence of every case is written for the test, in the shape that
// discover builds: the web results go through extractHandlesFromWeb, and the
// live bio of a direct profile hit comes from bios. The person names are prod
// names. In the positive cases the expected handle is the handle linked in
// prod. Every other handle is invented for the test. Each case states the
// label and the reason.
//
// The golden checks the LLM decision only; the bio guardrail
// (handleConfirmedByBio) runs after it in discover.
//
// LOCAL ONLY: it skips when OPEN_AI_KEY is not set.
//
//	go test -v -run TestHandleDiscoveryGolden ./backend/plugins/twitter/
func TestHandleDiscoveryGolden(t *testing.T) {
	llmtest.Setup(t)

	client, err := llm.NewClientFromEnv("twitter-discovery")
	if err != nil {
		t.Fatalf("NewClientFromEnv: %v", err)
	}

	type bios map[string]*userInfo // handle (lower case) -> live profile
	wikiFR := func(title, description, extract string) *search.WikipediaSummary {
		return &search.WikipediaSummary{Title: title, Lang: "fr", Description: description, Extract: extract}
	}
	hit := func(handle, title, snippet string) search.SearchResult {
		return search.SearchResult{URL: "https://x.com/" + handle, Title: title, Snippet: snippet}
	}
	page := func(url, title, snippet string) search.SearchResult {
		return search.SearchResult{URL: url, Title: title, Snippet: snippet}
	}

	cases := []struct {
		name    string
		person  string
		wiki    *search.WikipediaSummary
		results []search.SearchResult
		bios    bios
		want    string // expected handle in lower case; "" is NONE
		reason  string
	}{
		// The official account is in the evidence.
		{
			name:   "bardella_official_and_parody",
			person: "jordan bardella",
			wiki:   wikiFR("Jordan Bardella", "homme politique français", "Jordan Bardella est un homme politique français. Il est président du Rassemblement national depuis 2022 et député européen depuis 2019."),
			results: []search.SearchResult{
				hit("J_Bardella", "Jordan Bardella (@J_Bardella) / X", "Président du @RNational_off. Député européen. Président du groupe Patriotes pour l'Europe."),
				hit("BardellaParodie", "Jordan Bardella Parodie (@BardellaParodie) / X", "Compte parodique."),
			},
			bios: bios{
				"j_bardella":      {Name: "Jordan Bardella", Description: "Président du @RNational_off. Député européen. Président du groupe @PatriotsEP.", IsVerified: true, Followers: 1600000},
				"bardellaparodie": {Name: "Jordan Bardella (parodie)", Description: "Compte parodique. Non affilié à Jordan Bardella.", Followers: 3200},
			},
			want:   "j_bardella",
			reason: "the bio names his office and party; the other account says it is a parody",
		},
		{
			name:   "glucksmann_short_handle_and_fan",
			person: "raphael glucksmann",
			wiki:   wikiFR("Raphaël Glucksmann", "essayiste et homme politique français", "Raphaël Glucksmann est un essayiste et homme politique français. Coprésident de Place publique, il est député européen depuis 2019."),
			results: []search.SearchResult{
				hit("rglucks1", "Raphaël Glucksmann (@rglucks1) / X", "Député européen. Coprésident de @placepublique_."),
				hit("TeamGlucksmann", "Team Glucksmann (@TeamGlucksmann) / X", "Compte de soutien à Raphaël Glucksmann."),
			},
			bios: bios{
				"rglucks1":       {Name: "Raphaël Glucksmann", Description: "Député européen. Coprésident de @placepublique_.", IsVerified: true, Followers: 420000},
				"teamglucksmann": {Name: "Team Glucksmann", Description: "Compte de soutien, non officiel. Pour une gauche sociale et européenne.", Followers: 2100},
			},
			want:   "rglucks1",
			reason: "the handle does not spell his name, but the verified bio names his offices; the other is a fan account",
		},
		{
			name:   "faure_official_and_homonym",
			person: "olivier faure",
			wiki:   wikiFR("Olivier Faure", "homme politique français", "Olivier Faure est un homme politique français. Député de Seine-et-Marne, il est premier secrétaire du Parti socialiste depuis 2018."),
			results: []search.SearchResult{
				hit("faureolivier", "Olivier Faure (@faureolivier) / X", "Premier secrétaire du @partisocialiste. Député de Seine-et-Marne."),
				hit("OlivierFaurePhoto", "Olivier Faure (@OlivierFaurePhoto) / X", "Photographe à Lyon. Mariages et portraits."),
			},
			bios: bios{
				"faureolivier":      {Name: "Olivier Faure", Description: "Premier secrétaire du @partisocialiste. Député de Seine-et-Marne.", IsVerified: true, Followers: 310000},
				"olivierfaurephoto": {Name: "Olivier Faure", Description: "Photographe à Lyon. Mariages, portraits, événements.", Followers: 800},
			},
			want:   "faureolivier",
			reason: "the bio matches his party office and seat; the homonym is a photographer",
		},
		{
			name:   "darmanin_official",
			person: "gerald darmanin",
			wiki:   wikiFR("Gérald Darmanin", "homme politique français", "Gérald Darmanin est un homme politique français. Ministre de l'Intérieur de 2020 à 2024, il est garde des Sceaux, ministre de la Justice depuis 2024."),
			results: []search.SearchResult{
				hit("GDarmanin", "Gérald Darmanin (@GDarmanin) / X", "Garde des Sceaux, ministre de la Justice."),
				page("https://www.lefigaro.fr/politique/darmanin-reagit", "Darmanin réagit sur X", "Le garde des Sceaux a réagi sur son compte @GDarmanin."),
			},
			bios: bios{
				"gdarmanin": {Name: "Gérald Darmanin", Description: "Garde des Sceaux, ministre de la Justice.", IsVerified: true, Followers: 900000},
			},
			want:   "gdarmanin",
			reason: "a direct hit with a matching bio, confirmed by a news snippet",
		},
		{
			name:   "ciotti_official",
			person: "eric ciotti",
			wiki:   wikiFR("Éric Ciotti", "homme politique français", "Éric Ciotti est un homme politique français. Président de l'Union des droites pour la République, il est maire de Nice depuis 2026."),
			results: []search.SearchResult{
				hit("ECiotti", "Eric Ciotti (@ECiotti) / X", "Maire de Nice. Président de l'@UDR_officiel."),
			},
			bios: bios{
				"eciotti": {Name: "Eric Ciotti", Description: "Maire de Nice. Président de l'@UDR_officiel. Député des Alpes-Maritimes.", IsVerified: true, Followers: 450000},
			},
			want:   "eciotti",
			reason: "the bio matches his mayoralty and party in the Wikipedia summary",
		},
		{
			name:   "lecornu_official",
			person: "sebastien lecornu",
			wiki:   wikiFR("Sébastien Lecornu", "homme politique français", "Sébastien Lecornu est un homme politique français. Ancien ministre des Armées, il est Premier ministre depuis 2025."),
			results: []search.SearchResult{
				hit("SebLecornu", "Sébastien Lecornu (@SebLecornu) / X", "Premier ministre."),
				hit("Matignon", "Matignon (@Matignon) / X", "Compte officiel des services du Premier ministre."),
			},
			bios: bios{
				"seblecornu": {Name: "Sébastien Lecornu", Description: "Premier ministre.", IsVerified: true, Followers: 250000},
				"matignon":   {Name: "Matignon", Description: "Compte officiel des services du Premier ministre.", IsVerified: true, Followers: 700000},
			},
			want:   "seblecornu",
			reason: "his personal account; @Matignon is the account of the office, not of the person",
		},

		// No account of the person is in the evidence.
		{
			name:   "homonyms_only",
			person: "patrice martin",
			wiki:   wikiFR("Patrice Martin (ski nautique)", "skieur nautique français", "Patrice Martin est un skieur nautique français, plusieurs fois champion du monde."),
			results: []search.SearchResult{
				hit("PatriceMartinMTL", "Patrice Martin (@PatriceMartinMTL) / X", "Coach sportif à Montréal."),
			},
			bios: bios{
				"patricemartinmtl": {Name: "Patrice Martin", Description: "Coach sportif 🇨🇦 Montréal. Course à pied et triathlon.", Followers: 640},
			},
			want:   "",
			reason: "a common name; the bio (a coach in Montreal) and the Wikipedia page (a water skier) are two homonyms with no link",
		},
		{
			name:   "parody_only",
			person: "jean-philippe tanguy",
			wiki:   wikiFR("Jean-Philippe Tanguy", "homme politique français", "Jean-Philippe Tanguy est un homme politique français, député de la Somme depuis 2022 et membre du Rassemblement national."),
			results: []search.SearchResult{
				hit("TanguyParodie", "JP Tanguy Parodie (@TanguyParodie) / X", "Parodie. Je ne suis pas Jean-Philippe Tanguy."),
			},
			bios: bios{
				"tanguyparodie": {Name: "JP Tanguy (parodie)", Description: "Compte parodique. Je ne suis pas Jean-Philippe Tanguy.", Followers: 5400},
			},
			want:   "",
			reason: "the only candidate says it is a parody",
		},
		{
			name:   "fan_account_only",
			person: "manon bouquin",
			wiki:   nil,
			results: []search.SearchResult{
				hit("SoutienBouquin", "Soutien Manon Bouquin (@SoutienBouquin) / X", "Compte de soutien à Manon Bouquin, députée RN."),
			},
			bios: bios{
				"soutienbouquin": {Name: "Soutiens de Manon Bouquin", Description: "Compte de soutien à Manon Bouquin, députée RN. Non officiel.", Followers: 310},
			},
			want:   "",
			reason: "the only candidate is a fan account that says it is not official",
		},
		{
			name:   "no_candidate",
			person: "alexandra martin (gironde)",
			wiki:   nil,
			results: []search.SearchResult{
				page("https://www.assemblee-nationale.fr/dyn/deputes/alexandra-martin", "Mme Alexandra Martin - Assemblée nationale", "Députée de la Gironde. Groupe Rassemblement National."),
				page("https://www.sudouest.fr/gironde/alexandra-martin", "Gironde : la députée Alexandra Martin réagit", "La députée a réagi sur les réseaux sociaux à l'annonce du budget."),
			},
			bios:   bios{},
			want:   "",
			reason: "no result holds a handle; the model must not guess one",
		},
		{
			name:   "party_account_not_the_person",
			person: "olivier faure",
			wiki:   wikiFR("Olivier Faure", "homme politique français", "Olivier Faure est un homme politique français. Député de Seine-et-Marne, il est premier secrétaire du Parti socialiste depuis 2018."),
			results: []search.SearchResult{
				hit("partisocialiste", "Parti socialiste (@partisocialiste) / X", "Compte officiel du Parti socialiste. Premier secrétaire : Olivier Faure."),
			},
			bios: bios{
				"partisocialiste": {Name: "Parti socialiste", Description: "Compte officiel du Parti socialiste. Premier secrétaire : @faureolivier.", IsVerified: true, Followers: 380000},
			},
			want:   "",
			reason: "hard: the only profile is the party account; it names the person but is not his account",
		},
		{
			name:   "similar_name_other_politician",
			person: "christophe bex",
			wiki:   wikiFR("Christophe Bex", "homme politique français", "Christophe Bex est un homme politique français. Membre de La France insoumise, il est député de la Haute-Garonne depuis 2022."),
			results: []search.SearchResult{
				hit("ChristopheBechu", "Christophe Béchu (@ChristopheBechu) / X", "Maire d'Angers. Ancien ministre de la Transition écologique."),
			},
			bios: bios{
				"christophebechu": {Name: "Christophe Béchu", Description: "Maire d'Angers. Ancien ministre de la Transition écologique. Secrétaire général d'Horizons.", IsVerified: true, Followers: 60000},
			},
			want:   "",
			reason: "hard: a verified politician with a close name, but another person and another party",
		},
		{
			name:   "foreign_homonym",
			person: "andreas jung",
			wiki:   &search.WikipediaSummary{Title: "Andreas Jung", Lang: "en", Description: "German politician", Extract: "Andreas Jung is a German politician of the Christian Democratic Union (CDU) who has been serving as a member of the Bundestag since 2005."},
			results: []search.SearchResult{
				hit("andreasjungmusic", "Andreas Jung (@andreasjungmusic) / X", "Musician. Berlin. New album out now."),
			},
			bios: bios{
				"andreasjungmusic": {Name: "Andreas Jung", Description: "Musician 🎸 Berlin. New album out now.", Followers: 2300},
			},
			want:   "",
			reason: "the bio is a musician; the person is a CDU member of the Bundestag",
		},
	}

	ctx, cancel := llmtest.Context(t)
	defer cancel()

	// The cases run concurrently with -llm-claude: mu guards the counts.
	var mu sync.Mutex
	var passed, failed int
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				llmtest.Parallel(t)
				candidates := extractHandlesFromWeb(tc.results)
				for i, c := range candidates {
					if c.Direct {
						candidates[i].UserInfo = tc.bios[c.Handle]
					}
				}
				got, reply, err := askHandle(ctx, client, tc.person, tc.results, candidates, tc.wiki)
				if err != nil {
					t.Fatalf("askHandle: %v", err)
				}
				mu.Lock()
				defer mu.Unlock()
				if got != tc.want {
					failed++
					t.Errorf("got handle=%q want %q (\"\" is NONE)\n    reply: %s\n    label reason: %s", got, tc.want, reply, tc.reason)
					return
				}
				passed++
				t.Logf("handle=%q ✓", got)
			})
		}
	})
	t.Logf("=== HANDLE GOLDEN: %d/%d passed (%d failed) ===", passed, passed+failed, failed)
}
