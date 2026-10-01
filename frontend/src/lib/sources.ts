// sourceDomain is the label shown for a fact's source link: the outlet's
// domain without "www." ("lefigaro.fr"), or the account for an X post
// ("x.com/@jlmelenchon"), so a reader sees where a fact comes from at a glance.
export function sourceDomain(url: string): string {
	let u: URL;
	try {
		u = new URL(url);
	} catch {
		return url;
	}
	if (u.protocol !== 'http:' && u.protocol !== 'https:') return url;
	const host = u.hostname.replace(/^www\./, '');
	if (host === 'x.com' || host === 'twitter.com') {
		const account = u.pathname.split('/').filter(Boolean)[0];
		return account ? `x.com/@${account}` : host;
	}
	return host;
}
