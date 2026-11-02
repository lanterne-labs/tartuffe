package frpolitics

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testObjectPhrase = "le sous-amendement n° 372 au projet de loi de finances"

// A deputy's vote fact names its voter, like any other fact; the embedding
// text stays the name-free object phrase shared by every ballot.
func TestDeriveDeputyFactsNameTheVoter(t *testing.T) {
	law := Law{Key: "17-1", URL: "https://an.test/17-1"}
	events := []VoteEvent{
		{DisplayName: "Jean Dupont", Position: VoteFor},
		{DisplayName: "Marie Curie", Position: VoteAgainst},
	}

	facts := deriveDeputyFacts(law, events, LawSummary{}, testObjectPhrase)

	require.Len(t, facts, 2)
	require.Equal(t, "Jean Dupont a voté POUR "+testObjectPhrase, facts[0].FactText)
	require.Equal(t, "Marie Curie a voté CONTRE "+testObjectPhrase, facts[1].FactText)
	for i, f := range facts {
		require.Equal(t, events[i].DisplayName, f.PersonName)
		require.True(t, strings.HasPrefix(f.FactText, f.PersonName+" "), f.FactText)
		require.Equal(t, testObjectPhrase, f.EmbeddingText)
	}
}

// A deputy who took no part in the vote gets no fact: it states no position.
func TestDeriveDeputyFactsSkipNonVoting(t *testing.T) {
	law := Law{Key: "17-1", URL: "https://an.test/17-1"}
	facts := deriveDeputyFacts(law, []VoteEvent{
		{DisplayName: "Jean Dupont", Position: VoteNonVotant},
		{DisplayName: "Marie Curie", Position: VoteAbstention},
	}, LawSummary{}, testObjectPhrase)

	require.Len(t, facts, 1)
	require.Equal(t, "Marie Curie a choisi l'abstention lors du vote sur "+testObjectPhrase, facts[0].FactText)
}

// A party's majority fact names the party.
func TestDerivePartyMajorityFactsNameTheParty(t *testing.T) {
	law := Law{Key: "17-1", URL: "https://an.test/17-1"}
	stats := LawGroupStats{"PO1": {Position: VoteAgainst, Against: 9, For: 1}}
	organes := map[string]OrganeInfo{"PO1": {OrganeRef: "PO1", ShortName: "RN", LongName: "Rassemblement National"}}

	facts := derivePartyMajorityFacts(law, stats, 0.5, organes, LawSummary{}, testObjectPhrase)

	require.Len(t, facts, 1)
	require.Equal(t, "Rassemblement National", facts[0].PersonName)
	require.Equal(t, "party", facts[0].EntityType)
	require.Equal(t, "Rassemblement National a collectivement voté CONTRE "+testObjectPhrase, facts[0].FactText)
	require.Equal(t, testObjectPhrase, facts[0].EmbeddingText)
}

// A group whose members mostly did not vote gives no collective fact
// (2 voters of 23 LIOT deputies, 2026-11-01).
func TestDerivePartyMajorityFactsParticipationFloor(t *testing.T) {
	law := Law{Key: "17-1", URL: "https://an.test/17-1"}
	organes := map[string]OrganeInfo{"PO1": {OrganeRef: "PO1", ShortName: "LIOT", LongName: "Libertés, Indépendants, Outre-mer et Territoires"}}
	tooFew := LawGroupStats{"PO1": {Position: VoteFor, For: 2, NonVot: 21}}
	require.Empty(t, derivePartyMajorityFacts(law, tooFew, 0.5, organes, LawSummary{}, testObjectPhrase))
	enough := LawGroupStats{"PO1": {Position: VoteFor, For: 5, NonVot: 18}}
	require.Len(t, derivePartyMajorityFacts(law, enough, 0.5, organes, LawSummary{}, testObjectPhrase), 1)
	// nonVotants is usually 0: the group's size is the base (7 of 69 PS).
	minority := LawGroupStats{"PO1": {Position: VoteFor, For: 7, Members: 69}}
	require.Empty(t, derivePartyMajorityFacts(law, minority, 0.5, organes, LawSummary{}, testObjectPhrase))
	half := LawGroupStats{"PO1": {Position: VoteFor, For: 20, Members: 69}}
	require.Len(t, derivePartyMajorityFacts(law, half, 0.5, organes, LawSummary{}, testObjectPhrase), 1)
	ni := map[string]OrganeInfo{"PO2": {OrganeRef: "PO2", ShortName: "NI", LongName: "Non inscrit"}}
	require.Empty(t, derivePartyMajorityFacts(law, LawGroupStats{"PO2": {Position: VoteFor, For: 8, Members: 9}}, 0.5, ni, LawSummary{}, testObjectPhrase))
}

// A vote on a declaration of the Government (art. 50-1) is not "le texte de
// loi"; the phrase is the declaration itself (one scrutin, 468 ballots on
// 2026-11-01).
func TestLawPhraseForDeclaration(t *testing.T) {
	title := "la déclaration du Gouvernement portant sur la lutte contre le narcotrafic (application de l'article 50-1 de la Constitution)"
	got := lawPhraseFor(title, "Approuve la déclaration du Gouvernement visant à renforcer la lutte contre le narcotrafic.")
	require.Equal(t, "la déclaration du Gouvernement visant à renforcer la lutte contre le narcotrafic.", got)
	require.Equal(t, "la déclaration du Gouvernement : « Autre résumé »", lawPhraseFor(title, "Autre résumé"))
}

// Every record becomes its own single-subject input; records on one scrutin
// share the embedding of their common EmbeddingText and the vote subject.
func TestBuildVoteFactInputsOnePerRecord(t *testing.T) {
	law := Law{Key: "17-1", URL: "https://an.test/17-1", SeanceRef: "S1"}
	records := deriveDeputyFacts(law, []VoteEvent{
		{DisplayName: "Jean Dupont", Position: VoteFor},
		{DisplayName: "Marie Curie", Position: VoteFor},
	}, LawSummary{}, testObjectPhrase)
	emb := []float32{1, 0, 0}

	inputs, err := buildVoteFactInputs(records, map[string][]float32{testObjectPhrase: emb}, "politique budgétaire")
	require.NoError(t, err)

	require.Len(t, inputs, 2)
	for i, in := range inputs {
		require.Equal(t, records[i].PersonName, in.SubjectEntity)
		require.Equal(t, "human", in.SubjectEntityType)
		require.Equal(t, records[i].FactText, in.NormalizedFactText)
		require.Equal(t, emb, in.Embedding)
		require.Equal(t, "17-1", in.Metadata["law_key"])
		require.Equal(t, "S1", in.Metadata["seance_ref"])
		require.Equal(t, "politique budgétaire", in.Metadata["vote_subject"])
	}
	require.NotEqual(t, inputs[0].NormalizedFactText, inputs[1].NormalizedFactText, "same ballot, two voters, two facts")
}

// A record whose embedding was not generated is an error, not a fact stored
// without a vector.
func TestBuildVoteFactInputsMissingEmbedding(t *testing.T) {
	records := []FactRecord{{PersonName: "Jean Dupont", FactText: "Jean Dupont a voté POUR x", EmbeddingText: "x", LawKey: "17-1"}}
	_, err := buildVoteFactInputs(records, map[string][]float32{}, "s")
	require.Error(t, err)
}
