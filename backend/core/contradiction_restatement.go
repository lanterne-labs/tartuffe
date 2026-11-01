package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend/llm"
)

// A contradiction RESTATES another when both pair one fact of the person with
// two reports of the same position, claim or act: the denial said again on
// another day, the same verdict reported by two outlets, the candidacy
// announced and then confirmed. One thing is contradicted; the second row is
// the first counted again (fan-out, mirror-mtn: one fact made up to 26 rows;
// 344 of the 378 pairs of rows sharing a fact were labelled the same
// contradiction on 2026-10-31). A restating row keeps its evidence but is not
// counted again: it is not recorded.
//
// Two rows sharing a fact are DIFFERENT contradictions when the other facts
// are distinct claims or acts: a verdict against a denial of the facts and
// against a claim that the case was rigged; a pledge broken by two criticisms
// made on different days; the same candidacy against a hypocrisy rule and
// against a "last campaign" pledge.
//
// On the 375 labelled pairs (TestRestatementEval, Sonnet, 2026-11-01) the
// prompt keeps the different pairs apart 31/31 and catches 322/343 of the
// same ones (264/343 before the rules on old messages denied in one go,
// variants of one opposite position and self-exemption).
//
// The check runs BEFORE a new contradiction is recorded: a restating pair is
// not stored, it is recorded as a judged pair (fact_pair_checks), so the
// detector does not raise it again.

// SharedContradiction is an existing contradiction of the person that shares
// a fact with a new one.
type SharedContradiction struct {
	ID         int64
	SharedFact int64 // the fact both rows hold
	OtherFact  int64 // the existing row's other fact
	OtherText  string
	OtherDate  *time.Time
	Reason     string
	CreatedAt  time.Time
	SharedText string
	SharedDate *time.Time
	NewOther   int64 // the new row's other fact
	NewText    string
	NewDate    *time.Time
	NewReason  string
}

// RestatementStorer finds the rows sharing a fact with a new pair and
// records a restating pair as judged.
type RestatementStorer interface {
	// ContradictionsSharingFact returns the person's contradictions that
	// hold factA or factB (and not both), oldest first, with both pairs'
	// facts and dates filled in (NewOther/NewText: the new pair's other fact).
	ContradictionsSharingFact(ctx context.Context, personID, factA, factB int64, newReason string) ([]SharedContradiction, error)
	// RecordRestatedPair records the pair as judged, not a contradiction
	// of its own (fact_pair_checks, dismissed), so it is not raised again.
	RecordRestatedPair(ctx context.Context, personID, factA, factB int64) error
}

// RestatementJudge asks whether two contradictions sharing a fact are one.
type RestatementJudge struct {
	client *llm.Client
}

// NewRestatementJudgeFromEnv builds the judge from the LLM env.
func NewRestatementJudgeFromEnv() (*RestatementJudge, error) {
	client, err := llm.NewClientFromEnv("contradiction-restatement")
	if err != nil {
		return nil, err
	}
	return &RestatementJudge{client: client}, nil
}

const restatementPrompt = `You check a political honesty tracker for a contradiction counted twice.

One fact of a person (the SHARED fact) is paired with two other facts of the same person, giving two contradictions. Decide whether the two other facts state the SAME thing — one position, one claim, one denial or one act of the person, reported twice or said again on another day — so that the shared fact contradicts ONE thing and the second contradiction is the first counted again; or whether they state DIFFERENT things, so that there are two contradictions.

SAME (answer "same"):
- The same position or claim in other words, in another language, or repeated on another day or in another interview ("j'ai les mains propres" and "je suis innocente" against one verdict; "la primaire n'est pas possible" said in May and again in June).
- The same act reported twice or by two outlets (one candidacy announcement, one accord signed, one vote reported twice); a first-instance ruling and the appeal ruling that confirms it in the same case.
- Messages of one thread or one speech set against a single later statement; also old messages of ONE period brought out together (the posts of one account in one year, one leak) against ONE later denial ("ce n'a jamais été ma position"): whatever each message says, the person denies them in one go, so the denial is one inconsistency, however many messages.
- VARIANTS of one opposite position, both against the shared fact: a narrower and a broader version of one proposal (a two-person tie-break vote and a large open primary are both a vote to choose the candidate, against "a primary is not possible"); two wordings of one thesis ("Vichy protected the Jews" and "Vichy sacrificed foreign Jews to save French Jews", against "I have not studied it"); an opinion and the campaign promise that carries it out (favourable to Corsican autonomy, then promising it, against "better lose Corsica"); a rule of one's own and an attack on opponents for breaking it ("never by taxes", "the RN prefers taxes", against joining a government that raises them). A statement and an ACT that take the same side ("against the primary" said and a manifesto for a candidate without a primary), set against one later act (taking part in the primary), are one reversal.
- Against a rule or sanction the person demanded for others, everything by which they exempt THEMSELF once it applies to them is one self-exemption: attacking the sanction's principle ("l'inéligibilité pose un problème démocratique"), claiming eligibility, announcing or defending a candidacy, in whatever election.
- Different sentences that take the SAME SIDE against the shared fact, even with different content: what matters is the position each states RELATIVE TO the shared fact, not the wording. Against a court verdict, "je suis innocente", "j'ai les mains propres", "mes arguments me disculpent" and a cassation appeal "pour établir mon innocence" are one PLAIN claim of innocence (but see DIFFERENT for specific denials); against "the convicted must be ineligible for life", "je suis éligible", "rien ne m'empêchera d'être candidate", "mon exclusion serait un vol démocratique" and the candidacy announcement are one self-exemption; against a pledge, the same breach announced and then confirmed is one breach. Ask: would a reader count these as two separate inconsistencies of the person, or as one inconsistency reported several times?

DIFFERENT (answer "different"):
- Two distinct claims, even on one subject and on the same side: against a verdict, a plain claim of innocence ("je suis innocente", "j'ai les mains propres") is one claim, but a denial of a SPECIFIC fact the court found ("il n'y a jamais eu de système mis en place par mon père"), a dispute of the LEGAL QUALIFICATION ("ce n'est pas du détournement de fonds publics") and a claim that the case was rigged by opponents are each a claim of their own; "l'islam est une religion de terrorisme" and "l'islam est incompatible avec la République".
- Two distinct ACTS, each its own breach of one pledge: two criticisms made on different days, two votes on different texts.
- Two different kinds of contradiction with the shared fact: the same candidacy against a hypocrisy rule ("the convicted must be ineligible") and against a pledge ("my last campaign").
- Two different later positions (regulating crypto, then mining it).
- A claim about what a text or an accord SAYS ("le protocole ne prévoit pas que les migrants soient renvoyés en France") and a WISH of the person on the same subject ("je souhaite qu'ils soient renvoyés dans leur pays d'origine"), against signing that accord: a factual claim and a demand are two claims, not variants of one position.

Person: %s
SHARED fact (%s): %s
Contradiction 1 — other fact (%s): %s
  flagged because: %s
Contradiction 2 — other fact (%s): %s
  flagged because: %s

Return ONLY strict JSON, the reasoning first:
{"reasoning": "<one sentence: what each other fact states, and whether it is one thing>", "verdict": "same" | "different"}`

type restatementReply struct {
	Reasoning string `json:"reasoning"`
	Verdict   string `json:"verdict"`
}

func dateOrUnknown(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "undated"
	}
	return t.Format("2006-01-02")
}

// SameContradiction reports whether the new contradiction (shared fact with
// NewText) restates the existing one (shared fact with OtherText).
func (j *RestatementJudge) SameContradiction(ctx context.Context, person string, c SharedContradiction) (bool, string, error) {
	prompt := fmt.Sprintf(restatementPrompt, person,
		dateOrUnknown(c.SharedDate), c.SharedText,
		dateOrUnknown(c.OtherDate), c.OtherText, c.Reason,
		dateOrUnknown(c.NewDate), c.NewText, c.NewReason)
	content, _, err := j.client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.0,
		MaxTokens:      400,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskRestatement,
	})
	if err != nil {
		return false, "", fmt.Errorf("restatement judge: %w", err)
	}
	reply, err := llm.ParseJSONResponse[restatementReply](content)
	if err != nil {
		return false, "", fmt.Errorf("restatement judge: parsing %q: %w", content, err)
	}
	switch strings.ToLower(strings.TrimSpace(reply.Verdict)) {
	case "same":
		return true, reply.Reasoning, nil
	case "different":
		return false, reply.Reasoning, nil
	default:
		return false, reply.Reasoning, fmt.Errorf("restatement judge: unknown verdict %q", reply.Verdict)
	}
}

// RestatesExisting reports whether the pair (factA, factB) of the person
// restates one of their recorded contradictions; when it does, the pair is
// recorded as judged and the caller does not record it. It returns the id it
// restates (0: none). A failed judge call answers no (logged): a duplicate
// counted is better than a contradiction lost.
func RestatesExisting(ctx context.Context, storer RestatementStorer, judge *RestatementJudge, person string, personID, factA, factB int64, reason string) (int64, error) {
	if judge == nil || storer == nil {
		return 0, nil
	}
	cands, err := storer.ContradictionsSharingFact(ctx, personID, factA, factB, reason)
	if err != nil {
		return 0, err
	}
	for _, c := range cands {
		same, why, err := judge.SameContradiction(ctx, person, c)
		if err != nil {
			log.Warn().Err(err).Int64("fact_a", factA).Int64("fact_b", factB).Int64("candidate", c.ID).Msg("restatement check failed; the contradiction is recorded")
			return 0, nil
		}
		if !same {
			continue
		}
		if err := storer.RecordRestatedPair(ctx, personID, factA, factB); err != nil {
			return 0, err
		}
		log.Info().Int64("fact_a", factA).Int64("fact_b", factB).Int64("restates", c.ID).Str("why", why).Msg("contradiction restates a recorded one; not recorded again")
		return c.ID, nil
	}
	return 0, nil
}
