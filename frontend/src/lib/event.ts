// The pages of a poll event, the lab's temporary experiment: its
// presentation, the vote, and how it works. They are the event's own and
// never mix with the contradiction checker's pages.

export const FEATURED_SLUG = "presidentielle-2027";

export function eventPaths(slug: string = FEATURED_SLUG) {
  const base = `/election/${encodeURIComponent(slug)}`;
  return { intro: base, vote: `${base}/voter`, how: `${base}/fonctionnement`, complaint: `${base}/complaint` };
}

// The count of voters shows from SHOW_VOTERS_FROM on: the results show from
// the first vote, but a handful of voters says nothing of the experiment.
export const SHOW_VOTERS_FROM = 1000;

export const showVoters = (n: number | null | undefined) => (n ?? 0) >= SHOW_VOTERS_FROM;
