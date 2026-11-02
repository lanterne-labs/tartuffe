// The site's contact and publisher, in one place: the contact page, the legal
// notice and the footer read them. CONTACT_EMAIL is empty until the address is
// created; the pages then say it is coming.
export const CONTACT_EMAIL = '';

// The publisher (directeur de la publication). Null: the site is published by
// a private individual who, under article 6-III-2 of the LCEN (loi n° 2004-575),
// gave their identity to the host and publishes only the host's details.
export const PUBLISHER: { name: string; address?: string } | null = null;

export function mailto(subject: string): string {
	return `mailto:${CONTACT_EMAIL}?subject=${encodeURIComponent(subject)}`;
}
