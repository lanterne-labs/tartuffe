import adapterNode from '@sveltejs/adapter-node';
import adapterStatic from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// VOTE_APP=1 builds the vote app alone (scripts/vote-app.mjs): the vote
// routes, as static files for https://lanterne-labs.github.io, every page
// rendered in the browser (the fallback), nothing of ours serving it.
const voteApp = process.env.VOTE_APP === '1';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	preprocess: vitePreprocess(),
	kit: {
		adapter: voteApp ? adapterStatic({ pages: 'build-vote', assets: 'build-vote', fallback: '404.html', strict: false }) : adapterNode(),
		files: voteApp ? { routes: '.vote-routes' } : undefined
	}
};

export default config;
