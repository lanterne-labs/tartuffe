import { redirectToVoteApp } from '$lib/vote/origin';

export const load = ({ url }: { url: URL }) => {
	// The check reads the app's mirror settings: it runs on the app's
	// origin, as the vote page does.
	redirectToVoteApp(url);
	return {
		meta: {
			title: 'Une réclamation — tartuffe.me',
			description: 'L’accusé de réception signé par notre serveur pour un bulletin que le registre ne contient pas, vérifié dans votre navigateur.'
		}
	};
};
