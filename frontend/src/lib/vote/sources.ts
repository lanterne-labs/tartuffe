// The data mirror (SPEC §8.3): the files the browser reads instead of
// asking our server anything. An ordered list of mirrors; a fetch tries
// them in turn. A mirror is a directory served by a static host (the dev
// server, GitHub Pages) or a git repository read through a CDN by the
// commit the checkpoint names.

export interface MirrorSource {
	// url builds the address of a path for a commit token ('' for the
	// latest files).
	url(path: string, token: string): string;
	// byCommit: the source honours the token (a read at a commit goes to
	// such sources first; a directory serves the latest files only).
	byCommit: boolean;
}

// Read tells how a file was read: exact when it came from the commit
// asked for, or the latest files were asked for, or no source reads by
// commit (the directory is then the mirror itself). A read that fell back
// to the latest files may be older than the checkpoint, so a miss in it
// proves nothing.
export interface Read {
	exact: boolean;
}

interface Attempt {
	source: MirrorSource;
	token: string;
	exact: boolean;
}

// dirSource serves a directory: tokens are ignored, the files are the
// latest.
export function dirSource(base: string): MirrorSource {
	const b = base.replace(/\/$/, '');
	return { url: (path) => `${b}/${path}`, byCommit: false };
}

// jsDelivrSource reads a GitHub repository by commit.
export function jsDelivrSource(repo: string, branch = 'main'): MirrorSource {
	return { url: (path, token) => `https://cdn.jsdelivr.net/gh/${repo}@${token || branch}/${path}`, byCommit: true };
}

export class Sources {
	private readonly byCommit: boolean;

	constructor(private readonly mirrors: MirrorSource[]) {
		this.byCommit = mirrors.some((m) => m.byCommit);
	}

	// attempts lists what to try for a path: for a commit, the sources that
	// read by commit at that commit, then every source's latest files (a
	// CDN that does not serve the commit yet answers 404: the search goes
	// on to the next source, then to the latest files, SPEC §8.3); without
	// a token, each source's latest files.
	private attempts(token: string): Attempt[] {
		const out: Attempt[] = [];
		if (token) for (const source of this.mirrors) if (source.byCommit) out.push({ source, token, exact: true });
		for (const source of this.mirrors) out.push({ source, token: '', exact: !token || !this.byCommit });
		return out;
	}

	// text fetches a file, trying each attempt in turn; null when the last
	// one has no such file either. A failure (a 5xx, the network) moves to
	// the next attempt; when every attempt failed, the last error is
	// thrown. read, when given, tells whether the answer (or the miss) came
	// from the commit asked for.
	async text(path: string, token = '', read?: Read): Promise<string | null> {
		const list = this.attempts(token);
		let lastErr: unknown = null;
		for (const a of list) {
			try {
				const res = await fetch(a.source.url(path, a.token), { credentials: 'omit', cache: a.token ? 'force-cache' : 'no-cache' });
				if (res.status === 404) {
					if (a === list[list.length - 1]) {
						if (read) read.exact = a.exact;
						return null;
					}
					continue;
				}
				if (!res.ok) throw new Error(`HTTP ${res.status}`);
				if (read) read.exact = a.exact;
				return await res.text();
			} catch (e) {
				lastErr = e;
			}
		}
		if (lastErr) throw lastErr;
		if (read) read.exact = false;
		return null;
	}

	async json<T>(path: string, token = '', read?: Read): Promise<T | null> {
		const t = await this.text(path, token, read);
		return t === null ? null : (JSON.parse(t) as T);
	}

	async bytes(path: string, token = ''): Promise<Uint8Array | null> {
		for (const a of this.attempts(token)) {
			try {
				const res = await fetch(a.source.url(path, a.token), { credentials: 'omit' });
				if (res.status === 404) continue;
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
