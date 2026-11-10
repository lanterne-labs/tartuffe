// The pages of a poll event, the lab's temporary experiment: its
// presentation, the vote, and how it works. They are the event's own and
// never mix with the contradiction checker's pages. The rest of the site
// (the home page, the banner, the lab, the nav) links to `intro`, never
// to `vote`: a voter reads what the vote is before the ballot. Only the
// event's own pages and a voter's profile link to `vote`.

export const FEATURED_SLUG = "presidentielle-2027";

export function eventPaths(slug: string = FEATURED_SLUG) {
  const base = `/election/${encodeURIComponent(slug)}`;
  return { intro: base, vote: `${base}/voter`, how: `${base}/fonctionnement`, verify: `${base}/verifier`, complaint: `${base}/complaint` };
}

// The count of voters shows by hundreds, rounded down ("300+"), and not at
// all under the first hundred: the results show from the first tally, but a
// handful of voters says nothing of the experiment, and an exact count beside
// two tallies tells how many ballots came in between.
export const VOTERS_STEP = 100;

export function votersShown(n: number | null | undefined, lang: string): string | null {
  const floor = Math.floor((n ?? 0) / VOTERS_STEP) * VOTERS_STEP;
  return floor > 0 ? `${floor.toLocaleString(lang === "en" ? "en-GB" : "fr-FR")}+` : null;
}

// The shares of the results (percentages) show from the same floor: under it
// the ranking shows alone, since "50 %" of two voters is the count again.
export const sharesShown = (n: number | null | undefined) => (n ?? 0) >= VOTERS_STEP;
