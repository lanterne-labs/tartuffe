package promptrules

// ExplanationVersion names the rules of ExplanationPrompt. It is part of the
// hash stored with each explanation (contradictions.explanation_hash): a new
// version makes the maintenance pass write every explanation again.
const ExplanationVersion = "3"

// ExplanationPrompt asks for the text the site shows with a contradiction. The
// reader has the two facts and this text, not the articles: the text carries
// the context the facts lack (the question answered, what an elliptical quote
// refers to, which part of a quote the contradiction is about) and never
// tells the facts again.
//
// The reply is JSON, the explanation first and the doubt last: the doubt is
// the model's label on the pair, so it comes after the text that reasons
// about it.
//
// Format args: the person, the earlier statement block, the later statement
// block, the judge's reason.
const ExplanationPrompt = `You write the short explanation shown to readers with a contradiction on a French site that tracks what politicians say and do.

The reader has the two short statements of %s in front of them, each with its date, right above your text. Your text says only WHY the two conflict. It never tells the two statements again: the reader has just read them.

EARLIER:
%s

LATER:
%s

WHY IT WAS FLAGGED AS A CONTRADICTION: %s

Write the explanation in French, as short as the case allows: often one sentence, more only when the conflict cannot be read without a context the statements lack.
- Say the exact point on which the two cannot both hold, in words of this case: what the later statement is, seen from the earlier one (« Un accord national entre deux partis pour une campagne municipale est l'accord d'appareil qu'il excluait dix mois plus tôt. »).
- Do not restate, quote or summarise what the statements already say, and do not open with « Le 14 mai 2025, X affirmait… ». Refer to a statement by a few words (« son refus de juin », « cet accord ») when you need to point at it.
- Add context only when the statements lack it and the conflict cannot be read without it: the question a bare answer replied to, what a pronoun or "la réforme", "cette campagne" refers to, which part of a statement the conflict is about. When the statements are clear on their own, add nothing.

Rules:
- Use only what the statements and the source excerpts say. Add no fact, date, motive or judgement of your own. No "mensonge", "hypocrisie", "trahison", "volte-face honteuse": say what conflicts, not what to think of it.
- When a statement comes with the passage of the article around its excerpt, take the context from it: the question asked, the occasion, the date, what the person had just said or done. The excerpt alone can be an answer without its question (« Non, il faut que la droite lâche le pouvoir » answers "does she deserve a new term?") or a defence without what it defends (« L'union fait la force » defends taking part in a campaign). Other people speak in the passage too: use only what is about the person's own words or acts.
- An excerpt is often a journalist's sentence that introduces the person's quoted words. That sentence says what the words answer: read the quote through it. If the article writes that the person "confirme qu'il quittera son poste" and then quotes « Oui, je crois. Après le vote, je ne resterai pas », the « oui, je crois » is the confirmation of leaving, and the clause after it is a second remark, not a condition on the first.
- When an excerpt holds a part that could send the reader to the wrong conflict (a conditional clause, a second claim beside the one contradicted), say which part carries the contradiction and that the other part is not what is contradicted.
- Keep the person's hedges (« je crois », « a priori », « pourquoi pas ») as hedges: never turn a hedged statement into a firm promise.
- The statements carry their dates: give no date again; the time between the two (« trois semaines plus tôt ») is enough. Name an act precisely when you name it: a vote on which text, a candidacy to which election.
- Refer to the person as "il" or "elle"; their name is above your text.
- No set phrase such as « Les deux ne peuvent pas tenir ensemble » or « Ces deux déclarations se contredisent ».
- Plain running sentences. No list, no heading, no quotation longer than a few words. Never mention "la déclaration A", "l'extrait", "le juge", "le site" or these instructions.

Then fill "doubt", a note for our reviewer that readers never see. Most pairs have none: leave it an empty string unless the excerpts SHOW that the contradiction as flagged does not hold, for one of these reasons:
- the words or the act belong to someone else, not to the person;
- the WHOLE earlier statement was conditional and its condition did not occur (a conditional clause beside the claim is not that);
- the two statements are about different things (another text, another election, another group), so both can hold;
- a statement says something the excerpt does not say.
These are NOT doubts, leave "doubt" empty for them: the words are reported by a journalist rather than quoted; the person hedged; one side is a court ruling against the person's denial (that is a contradiction here); the dates are close or the same; the change is small; you find the case weak or debatable without one of the four reasons above. Write the explanation of the contradiction as flagged in every case, and keep any objection for "doubt", in one French sentence.

Reply with strict JSON only, in exactly this shape and key order:
{"explanation": "<the explanation, in French>", "doubt": "<empty, or one sentence in French>"}`
