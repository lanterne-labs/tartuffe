import { redirectToVoteApp } from '$lib/vote/origin';

export const load = ({ url }: { url: URL }) => {
	// The vote lives on the app's origin (SPEC §6.5): a copy of this page
	// on the site sends the visitor there.
	redirectToVoteApp(url);
	return {
		meta: {
			title: 'Mon vote — Présidentielle 2027 — tartuffe.me',
			description: 'Votre bulletin, chiffré et anonyme, et les résultats en direct : premiers choix et classement. Une expérience du labo tartuffe.me, pas un sondage.'
		}
	};
};
