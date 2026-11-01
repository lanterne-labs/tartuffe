// The link preview of a person's page, and of the stance, contradiction or
// fact the link points at (?stance=, ?c=, ?fact=; lib/share.ts). The page
// itself loads its data in the browser.
import type { PageServerLoad } from './$types';
import { serverFetchJSON } from '$lib/server/api';
import { clip, displayName } from '$lib/share';
import type { ShareKind, ShareTarget } from '$lib/types';

function pointed(url: URL): { kind: ShareKind; id: string } | null {
	for (const [param, kind] of [['c', 'contradiction'], ['fact', 'fact'], ['stance', 'stance']] as const) {
		const id = url.searchParams.get(param);
		if (id && /^\d+$/.test(id)) return { kind, id };
	}
	return null;
}

function describe(target: ShareTarget): { title: string; description: string } {
	const t = { ...target, person_name: displayName(target.person_name) };
	const counts = `${t.contradiction_count} contradiction${t.contradiction_count === 1 ? '' : 's'} sur ${t.fact_count} fait${t.fact_count === 1 ? '' : 's'}`;
	switch (t.kind) {
		case 'stance':
			return { title: `${t.person_name} — ${t.stance_label ?? ''}`, description: clip(t.text || `Ce que ${t.person_name} dit et vote.`, 200) };
		case 'contradiction':
			return {
				title: `${t.person_name} se contredit${t.stance_label ? ` : ${t.stance_label}` : ''}`,
				description: clip(`« ${clip(t.text, 110)} » puis « ${clip(t.other ?? '', 110)} »${t.quote ? ` — ${t.quote}` : ''}`, 280)
			};
		case 'fact':
			return { title: `${t.person_name}${t.stance_label ? ` — ${t.stance_label}` : ''}`, description: clip(t.quote || t.text, 240) };
		default:
			return { title: `Le Tartuffe-Score de ${t.person_name}`, description: `${counts}, avec leurs sources, sur tartuffe.me.` };
	}
}

export const load: PageServerLoad = async ({ params, url, fetch, setHeaders }) => {
	if (!/^\d+$/.test(params.id)) return {};
	const p = pointed(url) ?? { kind: 'person' as const, id: params.id };
	const q = p.kind === 'fact' ? `?person=${params.id}` : '';
	let t = await serverFetchJSON<ShareTarget>(fetch, `/api/share/${p.kind}/${p.id}${q}`);
	if (t && t.person_id !== Number(params.id)) t = null; // a link that mixes two persons
	if (!t && p.kind !== 'person') t = await serverFetchJSON<ShareTarget>(fetch, `/api/share/person/${params.id}`);
	if (!t) return {};
	setHeaders({ 'cache-control': 'public, max-age=300' });
	return { meta: { ...describe(t), image: t.image_url ?? undefined } };
};
