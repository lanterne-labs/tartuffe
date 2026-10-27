// Anything that is not the vote lives on the site: a link to a candidate's
// page, the dashboard, the feed.
import { redirect } from '@sveltejs/kit';

export const load = ({ url }: { url: URL }) => {
	redirect(302, 'https://tartuffe.me' + url.pathname + url.search);
};
