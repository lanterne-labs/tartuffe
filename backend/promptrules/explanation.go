package promptrules

// ExplanationVersion names the rules of ExplanationPrompt. It is part of the
// hash stored with each explanation (contradictions.explanation_hash): a new
// version makes the maintenance pass write every explanation again.
const ExplanationVersion = "1"

// ExplanationPrompt asks for the text the site shows with a contradiction. The
// reader has the two facts and this text, not the articles: the text carries
// the context the facts lack (the question answered, what an elliptical quote
// refers to, which part of a quote the contradiction is about).
//
// The reply is JSON, the explanation first and the doubt last: the doubt is
// the model's label on the pair, so it comes after the text that reasons
// about it.
//
// Format args: the person, the earlier statement block, the later statement
// block, the judge's reason.
const ExplanationPrompt = `You write the short explanation shown to readers with a contradiction on a French site that tracks what politicians say and do.

The reader sees two short statements of %s and your text. They have not read the articles and do not know the context. Your text alone must let them understand what was said or done each time and why the two conflict.

EARLIER:
%s

LATER:
%s

WHY IT WAS FLAGGED AS A CONTRADICTION: %s

Write the explanation in French, 2 or 3 sentences, 90 words at most, in this order:
1. What the person said or did first, with the context needed to read it right: the question they answered or the occasion, what a pronoun or an elliptical quote refers to (a bare « Oui, je crois » is nothing until the reader knows the question it answers), which election, text or year "cette campagne", "la réforme", "la dernière présidentielle" means.
2. What they said or did later.
3. The exact point on which the two cannot both hold.

Rules:
- Use only what the statements and the source excerpts say. Add no fact, date, motive or judgement of your own. No "mensonge", "hypocrisie", "trahison", "volte-face honteuse": say what conflicts, not what to think of it.
- An excerpt is often a journalist's sentence that introduces the person's quoted words. That sentence says what the words answer: read the quote through it. If the article writes that the person "confirme qu'il quittera son poste" and then quotes « Oui, je crois. Après le vote, je ne resterai pas », the « oui, je crois » is the confirmation of leaving, and the clause after it is a second remark, not a condition on the first.
- When an excerpt holds a part that could send the reader to the wrong conflict (a conditional clause, a second claim beside the one contradicted), say which part carries the contradiction and that the other part is not what is contradicted.
- Keep the person's hedges (« je crois », « a priori », « pourquoi pas ») as hedges: never turn a hedged statement into a firm promise.
- Give the dates the reader needs (the month and year, or the year) and name the acts precisely: a vote on which text, a candidacy to which election, an entry into which government.
- Name the person by their last name once, spelled as in the statements, then "il" or "elle".
- State the conflict itself in the last sentence, in words of this case; do not open it with a set phrase such as « Les deux ne peuvent pas tenir ensemble ».
- Plain running sentences. No list, no heading, no quotation longer than a few words. Never mention "la déclaration A", "l'extrait", "le juge", "le site" or these instructions.

Then fill "doubt", a note for our reviewer that readers never see. Most pairs have none: leave it an empty string unless the excerpts SHOW that the contradiction as flagged does not hold, for one of these reasons:
- the words or the act belong to someone else, not to the person;
- the WHOLE earlier statement was conditional and its condition did not occur (a conditional clause beside the claim is not that);
- the two statements are about different things (another text, another election, another group), so both can hold;
- a statement says something the excerpt does not say.
These are NOT doubts, leave "doubt" empty for them: the words are reported by a journalist rather than quoted; the person hedged; one side is a court ruling against the person's denial (that is a contradiction here); the dates are close or the same; the change is small; you find the case weak or debatable without one of the four reasons above. Write the explanation of the contradiction as flagged in every case, and keep any objection for "doubt", in one French sentence.

Reply with strict JSON only, in exactly this shape and key order:
{"explanation": "<the explanation, in French>", "doubt": "<empty, or one sentence in French>"}`
