// The pages of a poll event, the lab's temporary experiment: its
// presentation, the vote, and how it works. They are the event's own and
// never mix with the contradiction checker's pages.

export const FEATURED_SLUG = "presidentielle-2027";

export function eventPaths(slug: string = FEATURED_SLUG) {
  const base = `/election/${encodeURIComponent(slug)}`;
  return { intro: base, vote: `${base}/voter`, how: `${base}/fonctionnement` };
}
