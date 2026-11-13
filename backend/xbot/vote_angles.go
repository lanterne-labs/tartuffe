package xbot

import (
	"sort"
	"time"
)

// The tweets on the vote take an angle each, in turn: a reason to use the
// site or to vote, or an occasion (one of our tweets on a contradiction to
// quote, what a candidate said about the vote or democracy, a Sunday). The
// writer gets the angle with what may be said of the vote and of the site
// (voteTexts, siteTexts) and the tweets proposed before, so no tweet comes
// twice. The more angles, the longer before the account sounds the same.

// VoteAngle is a way to call to the vote.
type VoteAngle struct {
	Key string
	// Label names the angle in the Telegram draft.
	Label string
	// Brief tells the writer what the tweet is about.
	Brief string
	// Quote: the tweet is posted as a quote of one of our own tweets on a
	// contradiction. X lets an app quote its own posts.
	Quote bool
	// Said: the tweet starts from what a candidate said about the vote or
	// democracy (storage.PickVoteFact) and names them.
	Said bool
	// When limits the angle to an occasion (nil: any time).
	When func(v *VoteState, at time.Time) bool
}

// VoteQuoteWindow is how old a tweet of ours a vote tweet may quote;
// VoteSaidWindow how old a statement a vote tweet may start from.
const (
	VoteQuoteWindow = 14 * 24 * time.Hour
	VoteSaidWindow  = 365 * 24 * time.Hour
)

const quotedBrief = " Your tweet comments the tweet of ours quoted under it, which tells a contradiction of a candidate: the quoted tweet already tells it, do not tell it again."

// VoteAngles are the angles, in the order they come when none was used yet:
// the playful ones first.
var VoteAngles = []VoteAngle{
	{Key: "tartuffe", Label: "le tartuffe préféré",
		Brief: "Play on the word tartuffe (Molière's hypocrite, the name of the site): in the spirit of « Et vous, c'est qui votre tartuffe préféré ? » or « Classez-les, du moins tartuffe au plus tartuffe ». The reader ranks the candidates as they wish; the Tartuffe-Score beside each name helps them."},
	{Key: "quote-tartuffe", Label: "citation : votez pour votre tartuffe préféré", Quote: true,
		Brief: "A playful line in the spirit of « Votez pour votre tartuffe préféré » or « Un de plus pour le Tartuffe-Score » that sends the reader to the vote, where the Tartuffe-Score of each candidate is shown beside their name." + quotedBrief},
	{Key: "said", Label: "un candidat pris au mot", Said: true,
		Brief: "A candidate spoke of the vote, of democracy or of the constitution (their words are below). Take them at their word, in the spirit of « Chiche » or « Ça tombe bien » : quote a short piece of their words between « », name them, then turn to the reader: here the vote is already open. Judge neither the person nor what they said: the tweet takes no side, it only seizes the occasion."},
	{Key: "moliere", Label: "Molière",
		Brief: "Molière wrote one Tartuffe; a presidential campaign offers a whole cast. A theatre image (the cast, the curtain, the audience who decides) that leads the reader to rank the candidates. Name nobody."},
	{Key: "memory", Label: "la mémoire longue",
		Brief: "Why use the site: politicians count on short memories, the site keeps what they said and sets it beside what they said or did later. Then the vote: with that in mind, rank them."},
	{Key: "quote-choice", Label: "citation : est-ce que ça compte dans votre choix ?", Quote: true,
		Brief: "Ask the reader whether that weighs in their choice for 2027, and invite them to vote." + quotedBrief},
	{Key: "promises", Label: "les promesses",
		Brief: "A campaign is a season of promises. The site lets the reader compare each candidate's words with their later words and acts before ranking them. Light irony on promises in general, about nobody in particular."},
	{Key: "sunday", Label: "dimanche, jour de vote",
		Brief: "Today is a Sunday, the day the French vote. This vote is already open, without waiting for 2027.",
		When:  func(_ *VoteState, at time.Time) bool { return at.Weekday() == time.Sunday }},
	{Key: "quote-change", Label: "citation : vous pouvez changer d'avis", Quote: true,
		Brief: "Candidates change their mind; the voter may too: a vote can be changed until the close. Invite those who voted to look again, and the others to vote." + quotedBrief},
	{Key: "no-poll", Label: "personne ne vous a sondé ?",
		Brief: "Nobody ever called the reader for a poll? Here nobody is picked: any French elector with a passport gives their opinion. It is neither a poll nor an official ballot."},
	{Key: "secret", Label: "le secret du vote",
		Brief: "The secret: nobody, not us, not the State, can know who voted what, and the passport proves the voter is an elector without saying who they are."},
	{Key: "blank", Label: "le vote blanc",
		Brief: "For the reader no candidate suits: the blank vote is counted apart. It is a way to say so."},
	{Key: "sources", Label: "vérifiez vous-même",
		Brief: "Why trust the site: do not. Each contradiction shows the two statements, their dates and their sources, so anyone can check; anyone can report an error and the corrections are public. Then the vote: judge for yourself and rank them."},
	{Key: "simple", Label: "un passeport et un téléphone",
		Brief: "How little it takes: a passport and a phone, no account, no e-mail."},
	{Key: "score", Label: "le Tartuffe-Score au moment de voter",
		Brief: "Before choosing, look at the Tartuffe-Score: it is shown beside each name at the moment of voting."},
	{Key: "same-rule", Label: "la même règle pour tous",
		Brief: "The site applies the same method to every candidate, whatever their side: it compares their own words and acts. The vote takes no side either; the reader does."},
	{Key: "change", Label: "changer d'avis jusqu'à la clôture",
		Brief: "No need to wait for 2027 to have an opinion: vote now, and change your vote until the close if you change your mind."},
	{Key: "verify", Label: "chacun peut recompter",
		Brief: "What this vote is: an opinion vote that anyone can recount, where the count does not rest on trusting us."},
}

// freeAngle is the angle of a draft that has none (a draft from before the
// angles).
var freeAngle = VoteAngle{Label: "appel au vote", Brief: "A call to vote, on the reason of your choice among the lines above."}

// VoteAngleByKey finds an angle; a key that is not one gives the free angle.
func VoteAngleByKey(key string) VoteAngle {
	for _, a := range VoteAngles {
		if a.Key == key {
			return a
		}
	}
	return freeAngle
}

// PickVoteAngles returns the angles a tweet drafted for `at` may take, the
// least recently used first (used: when each was last drafted; an angle never
// used goes first, in the order of VoteAngles). An angle that quotes needs a
// tweet of ours to quote (canQuote), one that takes a candidate at their word
// needs their words (hasSaid).
func PickVoteAngles(used map[string]time.Time, v *VoteState, at time.Time, canQuote, hasSaid bool) []VoteAngle {
	var out []VoteAngle
	for _, a := range VoteAngles {
		if a.Quote && !canQuote || a.Said && !hasSaid {
			continue
		}
		if a.When != nil && !a.When(v, at) {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return used[out[i].Key].Before(used[out[j].Key]) })
	return out
}
