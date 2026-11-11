// The link preview of a person's page, and of the stance, contradiction or
// fact the link points at (?stance=, ?c=, ?fact=; lib/share.ts). A page
// named by id (/person/42, the links shared before the slugs) is redirected
// to the page named by slug. The page itself loads its data in the browser.
import { redirect } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';
import { serverFetchJSON } from '$lib/server/api';
import { clip, displayName, personPath } from '$lib/share';
import type { ShareKind, ShareTarget } from '$lib/types';

function pointed(url: URL): { kind: ShareKind; id: string } | null {
	for (const [param, kind] of [['c', 'contradiction'], ['fact', 'fact'], ['stance', 'stance']] as const) {
		const id = url.searchParams.get(param);
		if (id && /^\d+$/.test(id)) return { kind, id };
	}
	return null;
}

// isPerson says whether the target belongs to the person the page names,
// by slug or by id.
function isPerson(t: ShareTarget, ref: string): boolean {
	return t.person_slug === ref || String(t.person_id) === ref;
}

function describe(target: ShareTarget): { title: string; description: string } {
	const t = { ...target, person_name: displayName(target.person_name) };
	const counts = `${t.contradiction_count} contradiction${t.contradiction_count === 1 ? '' : 's'} sur ${t.fact_count} fait${t.fact_count === 1 ? '' : 's'}`;
	// The +1 of the users on a fact or a contradiction, after the text.
	const likes = t.likes > 0 ? ` · ${t.likes} +1` : '';
	switch (t.kind) {
		case 'stance':
			return { title: `${t.person_name} — ${t.stance_label ?? ''}`, description: clip(t.text || `Ce que ${t.person_name} dit et vote.`, 200) };
		case 'contradiction':
			return {
				title: `${t.person_name} se contredit${t.stance_label ? ` : ${t.stance_label}` : ''}`,
				description: clip(`« ${clip(t.text, 110)} » puis « ${clip(t.other ?? '', 110)} »${t.quote ? ` — ${t.quote}` : ''}`, 280) + likes
			};
		case 'fact':
			return { title: `${t.person_name}${t.stance_label ? ` — ${t.stance_label}` : ''}`, description: clip(t.quote || t.text, 240) + likes };
		default:
			return { title: `Le Tartuffe-Score de ${t.person_name}`, description: `${counts}, avec leurs sources, sur tartuffe.me.` };
	}
}

export const load: PageServerLoad = async ({ params, url, fetch, setHeaders }) => {
	const ref = params.slug;
	if (!/^[a-z0-9-]+$/.test(ref)) return {};
	const p = pointed(url) ?? { kind: 'person' as const, id: ref };
	const q = p.kind === 'fact' ? `?person=${ref}` : '';
	let t = await serverFetchJSON<ShareTarget>(fetch, `/api/share/${p.kind}/${p.id}${q}`);
	if (t && !isPerson(t, ref)) t = null; // a link that mixes two persons
	if (!t && p.kind !== 'person') t = await serverFetchJSON<ShareTarget>(fetch, `/api/share/person/${ref}`);
	if (!t) return {};
	// One address per person: a link by id goes to the page by slug, for good.
	if (t.person_slug && ref !== t.person_slug) redirect(301, personPath(t.person_slug) + url.search);
	setHeaders({ 'cache-control': 'public, max-age=300' });
	return { meta: { ...describe(t), image: t.image_url ?? undefined } };
};
