// The root of the vote app: the featured event's vote page. The slug is
// known to the app itself (lib/event.ts). The app never calls the site's
// API: a visit to the vote would give our server a visitor's address and
// time, which the public log could match with a ballot. It works with the
// site down, too.
import { redirect } from '@sveltejs/kit';
import { FEATURED_SLUG } from '$lib/event';

export const load = () => {
	redirect(302, `/election/${encodeURIComponent(FEATURED_SLUG)}/voter`);
};
