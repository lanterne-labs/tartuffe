import { redirectToVoteApp } from '$lib/vote/origin';

export const load = ({ url }: { url: URL }) => {
	// The audit reads the mirror and the relays from the vote app's origin,
	// as the vote does.
	redirectToVoteApp(url);
	return {
		meta: {
			title: 'Vérifier le vote — Présidentielle 2027 — tartuffe.me',
			description: 'Votre navigateur recompte tout le vote à partir du registre public : chaque bulletin, chaque preuve, chaque résultat. Sans nous faire confiance.'
		}
	};
};
