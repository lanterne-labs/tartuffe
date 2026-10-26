// The data mirror (SPEC §8.3): the files the browser reads instead of
// asking our server anything. An ordered list of mirrors; a fetch tries
// them in turn. A mirror is a directory served by a static host (the dev
// server, GitHub Pages) or a git repository read through a CDN by the
// commit the checkpoint names.

export interface MirrorSource {
	// url builds the address of a path for a commit token ('' for the
	// latest files).
	url(path: string, token: string): string;
}

// dirSource serves a directory: tokens are ignored, the files are the
// latest.
export function dirSource(base: string): MirrorSource {
	const b = base.replace(/\/$/, '');
	return { url: (path) => `${b}/${path}` };
}

// jsDelivrSource reads a GitHub repository by commit.
export function jsDelivrSource(repo: string, branch = 'main'): MirrorSource {
	return { url: (path, token) => `https://cdn.jsdelivr.net/gh/${repo}@${token || branch}/${path}` };
}

export class Sources {
	constructor(private readonly mirrors: MirrorSource[]) {}

	// text fetches a file, trying each mirror; null when no mirror has it.
	async text(path: string, token = ''): Promise<string | null> {
		let lastErr: unknown = null;
		for (const m of this.mirrors) {
			try {
				const res = await fetch(m.url(path, token), { credentials: 'omit', cache: token ? 'force-cache' : 'no-cache' });
				if (res.status === 404) return null;
				if (!res.ok) throw new Error(`HTTP ${res.status}`);
				return await res.text();
			} catch (e) {
				lastErr = e;
			}
		}
		if (lastErr) throw lastErr;
		return null;
	}

	async json<T>(path: string, token = ''): Promise<T | null> {
		const t = await this.text(path, token);
		return t === null ? null : (JSON.parse(t) as T);
	}

	async bytes(path: string, token = ''): Promise<Uint8Array | null> {
		for (const m of this.mirrors) {
			try {
				const res = await fetch(m.url(path, token), { credentials: 'omit' });
				if (res.status === 404) return null;
				if (!res.ok) throw new Error(`HTTP ${res.status}`);
				return new Uint8Array(await res.arrayBuffer());
			} catch {}
		}
		return null;
	}
}

// The settings: VITE_VOTE_MIRROR (a directory URL) and/or
// VITE_VOTE_MIRROR_REPO (owner/repo on GitHub).
export function defaultSources(): Sources {
	const list: MirrorSource[] = [];
	const dir = import.meta.env.VITE_VOTE_MIRROR as string | undefined;
	if (dir) list.push(dirSource(dir));
	const repo = import.meta.env.VITE_VOTE_MIRROR_REPO as string | undefined;
	if (repo) list.push(jsDelivrSource(repo));
	return new Sources(list);
}
