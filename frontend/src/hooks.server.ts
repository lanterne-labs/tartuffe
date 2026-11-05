import type { Handle } from '@sveltejs/kit';
import { umamiTag } from '$lib/server/umami';

export const handle: Handle = async ({ event, resolve }) => {
	const tag = umamiTag();
	if (!tag) return resolve(event);
	return resolve(event, { transformPageChunk: ({ html }) => html.replace('</head>', `${tag}</head>`) });
};
